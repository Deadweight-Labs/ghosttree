package aiproxy

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/netguard"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The kinds of outcome of a test call. The page words them; they carry no
// text of their own.
const (
	TestOK          = "ok"
	TestKey         = "key"         // 401 or 403: the provider does not take the key
	TestNotFound    = "not_found"   // 404: address or model not known there
	TestRequest     = "request"     // another 4xx: the provider refused the request
	TestPayment     = "payment"     // 402
	TestRateLimited = "rate"        // 429
	TestServer      = "server"      // 5xx
	TestTimeout     = "timeout"     // no answer in time
	TestUnreachable = "unreachable" // name does not resolve, connection refused or broken
	TestTLS         = "tls"         // the certificate does not fit
	TestAddress     = "address"     // the address is not one a connection may use
	TestNoKey       = "no_key"      // the stored key cannot be opened here
	TestCap         = "cap"         // a cap stops the call
	TestOther       = "other"
)

// TestResult is what one test call found.
type TestResult struct {
	OK         bool
	Kind       string
	Status     int // HTTP status of the answer; 0 without one
	Connection string
	Model      string
	Host       string
	Millis     int
	// Detail is what the provider said about it (its error message), shortened;
	// for a cap the name of the cap.
	Detail  string
	CostUSD float64
}

// TestRateError: tests come too fast; Wait is how long until the next one.
type TestRateError struct{ Wait time.Duration }

func (e *TestRateError) Error() string { return "tests of connections are rate limited" }

var (
	// testTimeout is how long a test waits for the provider.
	testTimeout = 20 * time.Second
	// testGap is the least time between two tests of one connection;
	// testWindow and testPerWindow cap the tests one person makes.
	testGap       = 3 * time.Second
	testWindow    = 10 * time.Minute
	testPerWindow = 20
	// testMaxOut is the output a test asks for: one short word.
	testMaxOut = 16
	// testDetailMax is how much of the provider's message is kept.
	testDetailMax = 200
)

// testGate lets a test through: one at a time per connection, a gap between
// them, and a number per person and window. The count is in memory: it holds
// against a person who clicks, which is all it is for.
func (p *Proxy) testGate(actor string, connID int64) (release func(), err error) {
	gap := testGap
	if p.TestGap != 0 {
		gap = max(p.TestGap, 0)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.testBusy == nil {
		p.testBusy, p.testLast, p.testBy = map[int64]bool{}, map[int64]time.Time{}, map[string][]time.Time{}
	}
	if p.testBusy[connID] {
		return nil, &TestRateError{Wait: gap}
	}
	if last, ok := p.testLast[connID]; ok && now.Sub(last) < gap {
		return nil, &TestRateError{Wait: gap - now.Sub(last)}
	}
	recent := p.testBy[actor][:0:0]
	for _, t := range p.testBy[actor] {
		if now.Sub(t) < testWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= testPerWindow {
		p.testBy[actor] = recent
		return nil, &TestRateError{Wait: testWindow - now.Sub(recent[0])}
	}
	p.testBy[actor] = append(recent, now)
	p.testBusy[connID] = true
	return func() {
		p.mu.Lock()
		delete(p.testBusy, connID)
		p.testLast[connID] = time.Now()
		p.mu.Unlock()
	}, nil
}

// TestConnection sends one minimal call through the connection and says what
// came of it. The call takes the same way as a run's: the key of the
// connection is put in here, the address is checked as the proxy checks it
// (UpstreamClient: https to public addresses for an organization's or a
// profile's connection), and the call is booked like any other, with the
// caps that apply. Only who keeps the connection may test it (the admin for
// the server's, the owner for a namespace's); the store decides. A *TestRateError
// comes back when the person tests too often.
func (p *Proxy) TestConnection(ctx context.Context, actor string, connID int64) (TestResult, error) {
	route, err := p.Store.AITestRoute(actor, connID)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{Connection: route.Connection.Name, Model: route.Model.Model}
	if u, err := url.Parse(route.Connection.BaseURL); err == nil {
		res.Host = u.Host
	}
	release, err := p.testGate(actor, connID)
	if err != nil {
		return res, err
	}
	defer release()
	key, err := p.keyOf(route)
	if err != nil {
		p.logf(slog.LevelWarn, "ai_test", 0, route, "error", err.Error())
		res.Kind = TestNoKey
		return res, nil
	}
	format, path := route.Connection.Format, PathOpenAI
	body := map[string]any{"model": route.Model.Model, "messages": []map[string]string{{"role": "user", "content": "Reply with one word: OK"}}}
	if format == store.AIFormatAnthropic {
		path = PathAnthropic
		body["max_tokens"] = testMaxOut
	} else {
		body["max_completion_tokens"] = testMaxOut
	}
	raw, _ := json.Marshal(body)
	price := llm.Price{InUSD: route.Model.InUSD, OutUSD: route.Model.OutUSD}
	est := price.Cost(llm.Usage{InputTokens: len(raw) / 3, OutputTokens: testMaxOut})
	callID, err := p.Store.ReserveAICall(route, est)
	if err != nil {
		var be *store.AIBudgetError
		if errors.As(err, &be) {
			res.Kind, res.Detail = TestCap, be.Cap
			return res, nil
		}
		return res, err
	}
	target, err := url.Parse(llm.Endpoint(route.Connection.BaseURL, path))
	var client *http.Client
	if err == nil {
		client, err = p.client(route.Connection, target)
	}
	if err != nil {
		// Never sent: nothing was billed.
		_ = p.Store.ReleaseAICall(callID)
		res.Kind = TestAddress
		return res, nil
	}
	timeout := testTimeout
	if p.TestTimeout > 0 {
		timeout = p.TestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) { sent.Store(info.Err == nil) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, target.String(), bytes.NewReader(raw))
	if err != nil {
		_ = p.Store.ReleaseAICall(callID)
		res.Kind = TestAddress
		return res, nil
	}
	req.Header.Set("Content-Type", "application/json")
	if format == store.AIFormatAnthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	start := time.Now()
	resp, err := client.Do(req)
	res.Millis = int(time.Since(start).Milliseconds())
	if err != nil {
		res.Kind = classifyTransport(err, ctx)
		p.logf(slog.LevelInfo, "ai_test", 0, route, "kind", res.Kind, "error", scrub(err.Error(), key))
		b := booking{}
		if sent.Load() {
			b = booking{called: true, status: statusNoAnswer}
		}
		p.settle(store.WorkflowRun{}, route, callID, b, price, est)
		return res, nil
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	res.Status = resp.StatusCode
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	b := booking{called: ok, status: resp.StatusCode}
	if ok {
		b.usage, b.complete = usageOf(answer, format)
		res.Kind, res.OK = TestOK, true
	} else {
		res.Kind = classifyStatus(resp.StatusCode)
		res.Detail = providerMessage(scrub(string(answer), key))
	}
	p.logf(slog.LevelInfo, "ai_test", 0, route, "kind", res.Kind, "status", resp.StatusCode)
	p.settle(store.WorkflowRun{}, route, callID, b, price, est)
	if ok {
		res.CostUSD = price.Cost(b.usage)
	}
	return res, nil
}

func classifyStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return TestKey
	case status == http.StatusNotFound:
		return TestNotFound
	case status == http.StatusPaymentRequired:
		return TestPayment
	case status == http.StatusTooManyRequests:
		return TestRateLimited
	case status >= 500:
		return TestServer
	case status >= 400:
		return TestRequest
	}
	return TestOther
}

func classifyTransport(err error, ctx context.Context) string {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.Is(err, netguard.ErrNotPublic):
		return TestAddress
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || isTimeout(err):
		return TestTimeout
	case errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) || strings.Contains(err.Error(), "tls:"):
		return TestTLS
	case errors.As(err, &dns):
		return TestUnreachable
	}
	return TestUnreachable
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// providerMessage reads what a provider says about a refusal: the message of
// its JSON error, else the start of the text, on one line and short.
func providerMessage(raw string) string {
	var v struct {
		Error json.RawMessage `json:"error"`
		Msg   string          `json:"message"`
	}
	text := strings.TrimSpace(raw)
	if json.Unmarshal([]byte(raw), &v) == nil {
		var e struct {
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(v.Error, &e) == nil && e.Message != "":
			text = e.Message
		case json.Unmarshal(v.Error, &s) == nil && s != "":
			text = s
		case v.Msg != "":
			text = v.Msg
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > testDetailMax {
		text = string([]rune(text)[:testDetailMax]) + "…"
	}
	return text
}
