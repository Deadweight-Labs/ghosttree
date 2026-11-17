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
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	mu     sync.Mutex
	active map[int64]*runCalls
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
	est := price.Cost(limitOutput(req, format, len(body)))
	if est > route.Remaining {
		_ = p.Store.MarkAIRunCap(run.ID, route.TightestCap)
		p.logf(slog.LevelInfo, "ai_budget", run.ID, route, "cap", route.TightestCap, "estimate_usd", est)
		p.fail(w, budgetError(route.TightestCap))
		return
	}
	// The estimate is booked before the call goes out, in the writer, so that
	// calls in parallel cannot all pass the same remaining amount.
	callID, err := p.Store.ReserveAICall(route, est)
	if err != nil {
		var be *store.AIBudgetError
		if errors.As(err, &be) {
			p.logf(slog.LevelInfo, "ai_budget", run.ID, route, "cap", be.Cap, "estimate_usd", est)
			p.fail(w, budgetError(be.Cap))
			return
		}
		p.logf(slog.LevelError, "ai_call", run.ID, route, "error", err.Error())
		p.fail(w, apiError{http.StatusServiceUnavailable, "api_error", "the call cannot be booked"})
		return
	}
	ctx, done := p.track(r.Context(), run.ID)
	defer done()
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
	b := p.forward(ctx, w, r, run, route, format, key, out, stream)
	p.settle(run, route, callID, b, price, est)
}

// statusNoAnswer is booked for a call that was sent and got no answer.
const statusNoAnswer = 499

// booking is what came back from the provider for one call.
type booking struct {
	// called: the provider answered with success, or the request was sent and
	// no answer came back; otherwise the reservation is dropped (nothing was
	// billed: the request never left, or the provider refused it).
	called bool
	status int
	usage  llm.Usage
	// complete: the answer carried its final usage. A stream that broke off
	// before its last event, or an answer without usage, is booked at the
	// estimate: the provider bills what it generated, which nobody saw.
	complete bool
}

func (p *Proxy) settle(run store.WorkflowRun, route store.AIRoute, callID int64, b booking, price llm.Price, est float64) {
	if !b.called {
		if err := p.Store.ReleaseAICall(callID); err != nil {
			p.logf(slog.LevelError, "ai_call", run.ID, route, "error", err.Error())
		}
		return
	}
	cost := price.Cost(b.usage)
	if !b.complete {
		cost = max(cost, est)
	}
	reached, err := p.Store.SettleAICall(callID, store.AICall{InputTokens: b.usage.InputTokens, OutputTokens: b.usage.OutputTokens,
		CacheReadTokens: b.usage.CacheReadTokens, CacheWriteTokens: b.usage.CacheWriteTokens, CostUSD: cost, Status: b.status})
	if err != nil {
		p.logf(slog.LevelError, "ai_call", run.ID, route, "error", err.Error())
		return
	}
	p.logf(slog.LevelInfo, "ai_call", run.ID, route, "status", b.status, "input_tokens", b.usage.InputTokens, "output_tokens", b.usage.OutputTokens,
		"cache_read_tokens", b.usage.CacheReadTokens, "cache_write_tokens", b.usage.CacheWriteTokens, "cost_usd", cost, "complete", b.complete, "cap", reached)
}

// track registers a call of a run so that EndRun can stop it.
func (p *Proxy) track(parent context.Context, runID int64) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	p.mu.Lock()
	if p.active == nil {
		p.active = map[int64]*runCalls{}
	}
	rc := p.active[runID]
	if rc == nil {
		rc = &runCalls{cancels: map[*context.CancelFunc]struct{}{}}
		p.active[runID] = rc
	}
	key := &cancel
	rc.cancels[key] = struct{}{}
	rc.wg.Add(1)
	p.mu.Unlock()
	return ctx, func() {
		cancel()
		p.mu.Lock()
		delete(rc.cancels, key)
		if len(rc.cancels) == 0 && p.active[runID] == rc {
			delete(p.active, runID)
		}
		p.mu.Unlock()
		rc.wg.Done()
	}
}

type runCalls struct {
	cancels map[*context.CancelFunc]struct{}
	wg      sync.WaitGroup
}

// endRunWait is how long EndRun waits for the stopped calls to be booked.
var endRunWait = 10 * time.Second

// EndRun stops the calls of a run that are still under way and waits until
// they are booked. The engine calls it when a run ends, after the run's
// token is revoked and before the run's cost is read, so that a stream left
// running by the block neither goes on nor is missing from the run.
func (p *Proxy) EndRun(runID int64) {
	p.mu.Lock()
	rc := p.active[runID]
	if rc != nil {
		for c := range rc.cancels {
			(*c)()
		}
	}
	p.mu.Unlock()
	if rc == nil {
		return
	}
	finished := make(chan struct{})
	go func() { rc.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(endRunWait):
	}
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
	return p.Box.Open(route.KeySealed, store.AIKeyPurposeFor(route.Connection.OrgID, route.Connection.ID))
}

// outputLimitKeys are the fields a request may name its output limit in.
var outputLimitKeys = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}

// limitOutput is the most a request can cost: the body as input (a token is
// at least about three bytes of JSON) and the output limit. A provider may
// honour any of the limit fields, so the largest one counts. A request that
// names none gets one (defaultMaxOut) put in, so that the provider cannot
// write more than was estimated.
func limitOutput(req map[string]json.RawMessage, format string, bodyLen int) llm.Usage {
	maxOut, named := 0, false
	for _, k := range outputLimitKeys {
		v, ok := req[k]
		if !ok {
			continue
		}
		var n float64
		if json.Unmarshal(v, &n) != nil || n <= 0 {
			continue
		}
		named = true
		maxOut = max(maxOut, int(min(n, 1<<30)))
	}
	if !named {
		maxOut = defaultMaxOut
		k := "max_tokens"
		if format == store.AIFormatOpenAI {
			k = "max_completion_tokens"
		}
		req[k], _ = json.Marshal(maxOut)
	}
	return llm.Usage{InputTokens: bodyLen / 3, OutputTokens: maxOut}
}

func (p *Proxy) client(c store.AIConnection, u *url.URL) (*http.Client, error) {
	if p.Upstream != nil {
		return p.Upstream(c, u)
	}
	return UpstreamClient(c, u)
}

// forward makes the call and relays the answer. ctx ends with the client's
// request or with EndRun.
func (p *Proxy) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, run store.WorkflowRun, route store.AIRoute, format, key string, body []byte, stream bool) booking {
	target, err := url.Parse(strings.TrimRight(route.Connection.BaseURL, "/") + r.URL.Path)
	if err != nil {
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "connection address is not valid"})
		return booking{}
	}
	client, err := p.client(route.Connection, target)
	if err != nil {
		p.logf(slog.LevelWarn, "ai_upstream", run.ID, route, "error", err.Error())
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "connection address is not allowed"})
		return booking{}
	}
	// Once the request is out, the provider may bill it, whatever happens to
	// the answer; only a request that never left is free.
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) { sent.Store(info.Err == nil) }}
	up, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		p.fail(w, apiError{http.StatusBadGateway, "api_error", "request cannot be made"})
		return booking{}
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
		if sent.Load() {
			// Sent, but no answer came back (the run or the client gave up, or
			// the connection broke): booked at the estimate.
			return booking{called: true, status: statusNoAnswer}
		}
		return booking{}
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	b := booking{called: ok, status: resp.StatusCode}
	if ok && stream {
		w.WriteHeader(resp.StatusCode)
		b.usage, b.complete = relayStream(w, resp.Body, format)
	} else {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		if ok {
			b.usage, b.complete = usageOf(raw, format)
			b.complete = b.complete && err == nil
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
	}
	return b
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
// event, and reads the usage the provider reports in them. complete says the
// stream reached its end with the final usage: for Anthropic the event
// message_stop after a message_delta with usage, for OpenAI the chunk with
// usage (the proxy asks for it) and [DONE]. A stream that breaks off earlier,
// because the run, the client or the provider ended it, is not complete.
func relayStream(w http.ResponseWriter, body io.Reader, format string) (u llm.Usage, complete bool) {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64<<10)
	finalUsage, end := false, false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return u, false
			}
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				data = bytes.TrimSpace(data)
				if format == store.AIFormatOpenAI && string(data) == "[DONE]" {
					end = true
				} else {
					v, has := usageOf(data, format)
					mergeUsage(&u, v)
					if format == store.AIFormatAnthropic {
						var ev struct {
							Type string `json:"type"`
						}
						_ = json.Unmarshal(data, &ev)
						finalUsage = finalUsage || (has && ev.Type == "message_delta")
						end = end || ev.Type == "message_stop"
					} else {
						finalUsage = finalUsage || has
					}
				}
			}
			if flusher != nil && len(bytes.TrimSpace(line)) == 0 {
				flusher.Flush()
			}
		}
		if err != nil {
			if flusher != nil {
				flusher.Flush()
			}
			return u, finalUsage && end && errors.Is(err, io.EOF)
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

// usageOf reads the usage of a reply or of one streamed event; has says the
// reply carried one at all. A reply without usage is booked at the estimate.
func usageOf(raw []byte, format string) (u llm.Usage, has bool) {
	if format == store.AIFormatAnthropic {
		var v struct {
			Usage   *anthropicUsage `json:"usage"`
			Message struct {
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return llm.Usage{}, false
		}
		for _, a := range []*anthropicUsage{v.Usage, v.Message.Usage} {
			if a != nil {
				has = true
				mergeUsage(&u, llm.Usage{InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CacheReadTokens: a.CacheReadTokens, CacheWriteTokens: a.CacheWriteTokens})
			}
		}
		return u, has
	}
	var v struct {
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Usage == nil {
		return llm.Usage{}, false
	}
	cached := min(v.Usage.Details.CachedTokens, v.Usage.PromptTokens)
	return llm.Usage{InputTokens: v.Usage.PromptTokens - cached, OutputTokens: v.Usage.CompletionTokens, CacheReadTokens: cached}, true
}
