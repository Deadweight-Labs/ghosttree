// Package aiproxy is the model proxy of the server (REQ-694, DEC-43). It
// listens on loopback only. A run calls it with its run token instead of a
// provider key; the proxy resolves the model the run asks for, puts in the key
// of the connection, passes the request on in the provider's own format (also
// streaming), books tokens and cost of every call on the run and refuses as
// soon as a cap is reached. Neither key nor token is ever logged or answered.
package aiproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	// PathAnthropic and PathOpenAI are the provider paths the proxy serves.
	PathAnthropic = "/v1/messages"
	PathOpenAI    = "/v1/chat/completions"
	// PathResolve tells a run which format and model an alias stands for.
	PathResolve = "/ghosttree/v1/resolve"

	maxRequestBody  = 16 << 20
	maxResponseBody = 32 << 20
	// defaultMaxOut is the output assumed for the estimate when a request sets
	// no limit.
	defaultMaxOut = 8192
)

// Proxy serves the model calls of runs.
type Proxy struct {
	Store *store.Store
	// Box opens the sealed keys of connections; nil means only connections
	// from llm.json work.
	Box *secretbox.Box
	// FileKeys are the keys of connections from llm.json, by connection id.
	// They are read from the file at start and never stored.
	FileKeys map[int64]string
	Log      *slog.Logger
	// Upstream makes the HTTP client for a connection; nil uses the safe
	// default (UpstreamClient). Tests point it at a fake provider.
	Upstream func(c store.AIConnection, u *url.URL) (*http.Client, error)
}

// Listen opens the proxy's listener. Only loopback addresses are accepted:
// the proxy answers anyone who holds a run token, so it must not be
// reachable from another machine.
func Listen(addr string) (net.Listener, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the model proxy listens on loopback only, not %q", host)
	}
	return net.Listen("tcp", addr)
}

// Serve runs the proxy on ln until ctx ends. It returns the base URL runs use.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener) (string, func()) {
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return "http://" + ln.Addr().String(), func() { <-done }
}

type apiError struct {
	status int
	kind   string
	msg    string
}

func (p *Proxy) fail(w http.ResponseWriter, e apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": e.kind, "message": e.msg}})
}

// runToken reads the token from x-api-key (Anthropic clients) or the bearer
// header (OpenAI clients).
func runToken(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		return k
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var format string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == PathAnthropic:
		format = store.AIFormatAnthropic
	case r.Method == http.MethodPost && r.URL.Path == PathOpenAI:
		format = store.AIFormatOpenAI
	case r.Method == http.MethodGet && r.URL.Path == PathResolve:
	default:
		p.fail(w, apiError{http.StatusNotFound, "not_found_error", "unknown path"})
		return
	}
	run, err := p.Store.AIRunOfToken(runToken(r))
	if err != nil {
		p.fail(w, apiError{http.StatusUnauthorized, "authentication_error", "run token is not valid"})
		return
	}
	if format == "" {
		p.resolve(w, run, r.URL.Query().Get("model"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		p.fail(w, apiError{http.StatusRequestEntityTooLarge, "invalid_request_error", "request too large"})
		return
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		p.fail(w, apiError{http.StatusBadRequest, "invalid_request_error", "request is not a JSON object"})
		return
	}
	var model string
	_ = json.Unmarshal(req["model"], &model)
	route, err := p.Store.AIRouteFor(run, model)
	if aerr, ok := p.routeError(run, err); !ok {
		p.fail(w, aerr)
		return
	}
	if route.Connection.Format != format {
		p.fail(w, apiError{http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("%s speaks the %s format", route.Connection.Name, route.Connection.Format)})
		return
	}
	key, err := p.keyOf(route)
	if err != nil {
		p.logf(slog.LevelError, "ai_key", run.ID, route, "error", err.Error())
		p.fail(w, apiError{http.StatusServiceUnavailable, "api_error", "the key of " + route.Connection.Name + " cannot be used"})
		return
	}
	price := llm.Price{InUSD: route.Model.InUSD, OutUSD: route.Model.OutUSD}
	if est := price.Cost(estimate(req, len(body))); est > route.Remaining {
		_ = p.Store.MarkAIRunCap(run.ID, route.TightestCap)
		p.logf(slog.LevelInfo, "ai_budget", run.ID, route, "cap", route.TightestCap, "estimate_usd", est)
		p.fail(w, budgetError(route.TightestCap))
		return
	}
	stream := false
	_ = json.Unmarshal(req["stream"], &stream)
	req["model"], _ = json.Marshal(route.Model.Model)
	if format == store.AIFormatOpenAI && stream {
		opts := map[string]any{}
		_ = json.Unmarshal(req["stream_options"], &opts)
		opts["include_usage"] = true
		req["stream_options"], _ = json.Marshal(opts)
	}
	out, _ := json.Marshal(req)
	p.forward(w, r, run, route, format, key, out, stream, price)
}

func budgetError(capName string) apiError {
	return apiError{http.StatusPaymentRequired, "budget_error", "budget reached: " + capName}
}

// routeError maps a refusal of the store to an answer; ok is true when there
// is none.
func (p *Proxy) routeError(run store.WorkflowRun, err error) (apiError, bool) {
	var be *store.AIBudgetError
	switch {
	case err == nil:
		return apiError{}, true
	case errors.As(err, &be):
		_ = p.Store.MarkAIRunCap(run.ID, be.Cap)
		return budgetError(be.Cap), false
	case errors.Is(err, store.ErrAIUnavailable):
		return apiError{http.StatusForbidden, "permission_error", "model is not available to this run"}, false
	}
	return apiError{http.StatusInternalServerError, "api_error", "model cannot be resolved"}, false
}

func (p *Proxy) resolve(w http.ResponseWriter, run store.WorkflowRun, model string) {
	route, err := p.Store.AIRouteFor(run, model)
	if aerr, ok := p.routeError(run, err); !ok {
		p.fail(w, aerr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"format": route.Connection.Format, "connection": route.Connection.Name, "model": route.Model.Model})
}

func (p *Proxy) keyOf(route store.AIRoute) (string, error) {
	if route.Connection.FromFile {
		if k := p.FileKeys[route.Connection.ID]; k != "" {
			return k, nil
		}
		return "", errors.New("the key from llm.json is not loaded")
	}
	return p.Box.Open(route.KeySealed, store.AIKeyPurpose)
}

// estimate is the most a request can cost: the body as input (a token is
// at least about three bytes of JSON) and the full output limit.
func estimate(req map[string]json.RawMessage, bodyLen int) llm.Usage {
	maxOut := 0
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if v, ok := req[k]; ok {
			_ = json.Unmarshal(v, &maxOut)
			if maxOut > 0 {
				break
			}
		}
	}
	if maxOut <= 0 {
		maxOut = defaultMaxOut
	}
	return llm.Usage{InputTokens: bodyLen / 3, OutputTokens: maxOut}
}

func (p *Proxy) client(c store.AIConnection, u *url.URL) (*http.Client, error) {
	if p.Upstream != nil {
		return p.Upstream(c, u)
	}
	return UpstreamClient(c, u)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, run store.WorkflowRun, route store.AIRoute, format, key string, body []byte, stream bool, price llm.Price) {
	target, err := url.Parse(strings.TrimRight(route.Connection.BaseURL, "/") + r.URL.Path)
	if err != nil {
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "connection address is not valid"})
		return
	}
	client, err := p.client(route.Connection, target)
	if err != nil {
		p.logf(slog.LevelWarn, "ai_upstream", run.ID, route, "error", err.Error())
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "connection address is not allowed"})
		return
	}
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "request cannot be made"})
		return
	}
	up.Header.Set("Content-Type", "application/json")
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	if format == store.AIFormatAnthropic {
		up.Header.Set("x-api-key", key)
		v := r.Header.Get("anthropic-version")
		if v == "" {
			v = "2023-06-01"
		}
		up.Header.Set("anthropic-version", v)
		if b := r.Header.Get("anthropic-beta"); b != "" {
			up.Header.Set("anthropic-beta", b)
		}
	} else {
		up.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(up)
	if err != nil {
		// The error of a transport names the address, never the key; it is
		// scrubbed all the same.
		p.logf(slog.LevelWarn, "ai_upstream", run.ID, route, "error", scrub(err.Error(), key))
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "provider did not answer"})
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	var usage llm.Usage
	if ok && stream {
		w.WriteHeader(resp.StatusCode)
		usage = relayStream(w, resp.Body, format)
	} else {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		if ok {
			usage = usageOf(raw, format)
		} else {
			// An error body can quote what it was sent; the key does not go back.
			raw = []byte(scrub(string(raw), key))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
	}
	if !ok {
		p.logf(slog.LevelWarn, "ai_call", run.ID, route, "status", resp.StatusCode)
		return
	}
	cost := price.Cost(usage)
	reached, err := p.Store.RecordAICall(store.AICall{RunID: run.ID, Route: route, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, CostUSD: cost, Status: resp.StatusCode})
	if err != nil {
		p.logf(slog.LevelError, "ai_call", run.ID, route, "error", err.Error())
		return
	}
	p.logf(slog.LevelInfo, "ai_call", run.ID, route, "status", resp.StatusCode, "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cache_read_tokens", usage.CacheReadTokens, "cache_write_tokens", usage.CacheWriteTokens, "cost_usd", cost, "cap", reached)
}

// logf writes one line about a call: run, connection and model, never a key
// or a token.
func (p *Proxy) logf(level slog.Level, msg string, runID int64, route store.AIRoute, args ...any) {
	if p.Log == nil {
		return
	}
	base := []any{"run", runID, "connection", route.Connection.Name, "model", route.Model.Model}
	p.Log.Log(context.Background(), level, msg, append(base, args...)...)
}

func scrub(s, key string) string {
	if key == "" {
		return s
	}
	s = strings.ReplaceAll(s, key, "[key]")
	if len(key) > 12 {
		// Providers quote keys shortened ("sk-abc...wxyz"); the inner part goes too.
		s = strings.ReplaceAll(s, key[:len(key)-4], "[key]")
		s = strings.ReplaceAll(s, key[4:], "[key]")
	}
	return s
}

// relayStream passes server-sent events through line by line, flushing each
// event, and reads the usage the provider reports in them.
func relayStream(w http.ResponseWriter, body io.Reader, format string) llm.Usage {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64<<10)
	var u llm.Usage
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return u
			}
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				mergeUsage(&u, usageOf(bytes.TrimSpace(data), format))
			}
			if flusher != nil && len(bytes.TrimSpace(line)) == 0 {
				flusher.Flush()
			}
		}
		if err != nil {
			if flusher != nil {
				flusher.Flush()
			}
			return u
		}
	}
}

func mergeUsage(u *llm.Usage, v llm.Usage) {
	u.InputTokens = max(u.InputTokens, v.InputTokens)
	u.OutputTokens = max(u.OutputTokens, v.OutputTokens)
	u.CacheReadTokens = max(u.CacheReadTokens, v.CacheReadTokens)
	u.CacheWriteTokens = max(u.CacheWriteTokens, v.CacheWriteTokens)
}

type anthropicUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	Details          struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// usageOf reads the usage of a reply or of one streamed event.
func usageOf(raw []byte, format string) llm.Usage {
	if format == store.AIFormatAnthropic {
		var v struct {
			Usage   *anthropicUsage `json:"usage"`
			Message struct {
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return llm.Usage{}
		}
		var u llm.Usage
		for _, a := range []*anthropicUsage{v.Usage, v.Message.Usage} {
			if a != nil {
				mergeUsage(&u, llm.Usage{InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CacheReadTokens: a.CacheReadTokens, CacheWriteTokens: a.CacheWriteTokens})
			}
		}
		return u
	}
	var v struct {
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Usage == nil {
		return llm.Usage{}
	}
	cached := min(v.Usage.Details.CachedTokens, v.Usage.PromptTokens)
	return llm.Usage{InputTokens: v.Usage.PromptTokens - cached, OutputTokens: v.Usage.CompletionTokens, CacheReadTokens: cached}
}
