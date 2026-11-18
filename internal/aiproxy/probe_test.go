package aiproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// testCalls reads what the tests of connections booked this month.
func (e *env) testCalls(t *testing.T) (calls int, cost float64) {
	t.Helper()
	rep, err := e.st.AICostReportOf(e.org.ID, store.AIMonthStart())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range rep.ByWorkflow {
		if w.Key == store.AITestWorkflow {
			calls, cost = w.Calls, w.CostUSD
		}
	}
	return calls, cost
}

// connTo saves an own connection of the organization that sends its calls to
// base.
func (e *env) connTo(t *testing.T, name, base string, capUSD float64) store.AIConnection {
	t.Helper()
	in := store.AIConnectionInput{OrgID: e.org.ID, Name: name, Format: store.AIFormatOpenAI, BaseURL: base, KeyLast4: "1111", MonthlyCapUSD: capUSD,
		Models: []store.AIModel{{Model: "gpt-expensive", InUSD: 10, OutUSD: 30}, {Model: "gpt-cheap", InUSD: 1, OutUSD: 5}}}
	c, err := e.st.SaveAIConnection("person:1", in, func(purpose string) ([]byte, error) { return e.box.Seal(openaiKey, purpose) })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// REQ-707 / AC-707.3: a test sends one minimal call the way a run's calls go,
// with the key put in by the proxy and the cheapest model, books it as a cost
// and says it worked.
func TestTestConnectionSendsOneMinimalCallAndBooksItsCost(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	for _, c := range []struct {
		conn         store.AIConnection
		path, header string
		limitKey     string
	}{{e.team, "anthropic", anthropicKey, "max_tokens"}, {e.openai, "openai", "Bearer " + openaiKey, "max_completion_tokens"}} {
		before := e.provider.calls
		res, err := e.p.TestConnection(context.Background(), "person:1", c.conn.ID)
		if err != nil || !res.OK || res.Kind != TestOK || res.Status != 200 || res.Connection != c.conn.Name || res.Model == "" {
			t.Fatalf("%s: %+v %v", c.path, res, err)
		}
		if e.provider.calls != before+1 {
			t.Fatalf("%s: the provider got %d calls", c.path, e.provider.calls-before)
		}
		body := e.provider.bodies[len(e.provider.bodies)-1]
		if v, _ := body[c.limitKey].(float64); v != float64(testMaxOut) {
			t.Fatalf("%s: the call asks for %v tokens at most: %v", c.path, v, body)
		}
		got := e.provider.keys[len(e.provider.keys)-1] + e.provider.auths[len(e.provider.auths)-1]
		if !strings.Contains(got, strings.TrimPrefix(c.header, "Bearer ")) {
			t.Fatalf("%s: the provider did not get the key", c.path)
		}
	}
	calls, cost := e.testCalls(t)
	if calls != 2 || cost <= 0 {
		t.Fatalf("the tests are booked as calls with their cost: %d calls, %v", calls, cost)
	}
	e.mustNotLeak(t, "the server log", e.logs.String())
}

// The test takes the cheapest model of the connection.
func TestTestConnectionTakesTheCheapestModel(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	c := e.connTo(t, "two-models", e.provider.srv.URL, 0)
	res, err := e.p.TestConnection(context.Background(), "person:1", c.ID)
	if err != nil || res.Model != "gpt-cheap" {
		t.Fatalf("%+v %v", res, err)
	}
}

// REQ-707: the provider's refusals come back worded by kind, the provider's
// own message shortened, a quoted key scrubbed; nothing is booked for a call
// the provider refused, and a call that got no answer is booked.
func TestTestConnectionNamesWhatWentWrong(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	for _, c := range []struct {
		name   string
		status int
		body   string
		kind   string
		detail string
	}{
		{"unauthorized", 401, `{"error":{"message":"Incorrect API key provided: ` + openaiKey + `"}}`, TestKey, "Incorrect API key provided: [key]"},
		{"not-found", 404, "404 page not found", TestNotFound, "404 page not found"},
		{"bad-model", 400, `{"error":{"message":"model gpt-cheap does not exist"}}`, TestRequest, "model gpt-cheap does not exist"},
		{"payment", 402, `{"error":"credit exhausted"}`, TestPayment, "credit exhausted"},
		{"limited", 429, `{"error":{"message":"slow down"}}`, TestRateLimited, "slow down"},
		{"broken", 503, "upstream down", TestServer, "upstream down"},
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			fmt.Fprint(w, c.body)
		}))
		defer srv.Close()
		e.p.Upstream = func(store.AIConnection, *url.URL) (*http.Client, error) { return srv.Client(), nil }
		conn := e.connTo(t, c.name, srv.URL, 0)
		res, err := e.p.TestConnection(context.Background(), "person:1", conn.ID)
		if err != nil || res.OK || res.Kind != c.kind || res.Status != c.status || res.Detail != c.detail {
			t.Fatalf("%s: %+v %v", c.name, res, err)
		}
		e.mustNotLeak(t, c.name, res.Detail)
	}
	if calls, _ := e.testCalls(t); calls != 0 {
		t.Fatalf("a call the provider refused costs nothing, yet %d are booked", calls)
	}
}

func TestTestConnectionTimesOutAndBooksTheCallThatGotNoAnswer(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	e.p.TestTimeout = 80 * time.Millisecond
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer slow.Close()
	e.p.Upstream = func(store.AIConnection, *url.URL) (*http.Client, error) { return slow.Client(), nil }
	conn := e.connTo(t, "slow", slow.URL, 0)
	res, err := e.p.TestConnection(context.Background(), "person:1", conn.ID)
	if err != nil || res.Kind != TestTimeout || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if calls, _ := e.testCalls(t); calls != 1 {
		t.Fatalf("a sent call without an answer is booked at the estimate: %d", calls)
	}
}

func TestTestConnectionToAClosedPortIsUnreachable(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	gone := httptest.NewTLSServer(http.NotFoundHandler())
	base := gone.URL
	client := gone.Client()
	gone.Close()
	e.p.Upstream = func(store.AIConnection, *url.URL) (*http.Client, error) { return client, nil }
	conn := e.connTo(t, "gone", base, 0)
	res, err := e.p.TestConnection(context.Background(), "person:1", conn.ID)
	if err != nil || res.Kind != TestUnreachable {
		t.Fatalf("%+v %v", res, err)
	}
	if calls, _ := e.testCalls(t); calls != 0 {
		t.Fatalf("a call that never left is free: %d", calls)
	}
}

// The test goes the way a run's calls go: an organization's connection must
// use a public address, so a loopback address is refused before anything is
// sent.
func TestTestConnectionRefusesAddressesARunWouldRefuse(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	e.p.Upstream = nil
	before := e.provider.calls
	conn := e.connTo(t, "inside", e.provider.srv.URL, 0)
	res, err := e.p.TestConnection(context.Background(), "person:1", conn.ID)
	if err != nil || res.Kind != TestAddress || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if e.provider.calls != before {
		t.Fatal("the request reached an address the proxy refuses")
	}
	if calls, _ := e.testCalls(t); calls != 0 {
		t.Fatalf("nothing was sent, nothing is booked: %d", calls)
	}
}

// A cap that the test would pass stops it, and the test says which.
func TestTestConnectionStopsAtACap(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	conn := e.connTo(t, "tiny-cap", e.provider.srv.URL, 0.0000001)
	res, err := e.p.TestConnection(context.Background(), "person:1", conn.ID)
	if err != nil || res.Kind != TestCap || res.Detail != store.AICapConnection {
		t.Fatalf("%+v %v", res, err)
	}
	if e.provider.calls != 0 {
		t.Fatal("a cap stops the call before it goes out")
	}
}

// Only who keeps the connection tests it: the owner of the namespace, the
// admin for the server's. The key of llm.json, which only the proxy holds,
// works the same.
func TestTestConnectionIsForTheKeepersAlone(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	if _, err := e.p.TestConnection(context.Background(), "person:2", e.team.ID); !errors.Is(err, store.ErrNotOrgOwner) {
		t.Fatalf("a person outside the organization tests its connection: %v", err)
	}
	if _, err := e.p.TestConnection(context.Background(), "person:1", 99999); !errors.Is(err, store.ErrAIUnavailable) {
		t.Fatalf("a connection that does not exist: %v", err)
	}
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-api-key")
		fmt.Fprint(w, `{"type":"message","usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()
	file, err := e.st.ImportFileAIConnection(store.AIFileConnection{Name: "standard", Format: store.AIFormatAnthropic, BaseURL: srv.URL, Model: "claude-haiku-5-5", InUSD: 1, OutUSD: 5, KeyLast4: "9999"})
	if err != nil {
		t.Fatal(err)
	}
	e.p.FileKeys = map[int64]string{file.ID: "sk-file-KEYfromLLMjson-9999"}
	e.p.Upstream = func(store.AIConnection, *url.URL) (*http.Client, error) { return srv.Client(), nil }
	if _, err := e.p.TestConnection(context.Background(), "person:2", file.ID); !errors.Is(err, store.ErrNotServerAdmin) {
		t.Fatalf("a person who is no admin tests the server's connection: %v", err)
	}
	res, err := e.p.TestConnection(context.Background(), "person:1", file.ID)
	if err != nil || !res.OK || got != "sk-file-KEYfromLLMjson-9999" {
		t.Fatalf("the server's connection from llm.json: %+v %v, key %q", res, err, got)
	}
}

// Against abuse: one test at a time and a gap per connection, a number per
// person and window.
func TestTestConnectionIsRateLimited(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = 0
	oldGap, oldPer := testGap, testPerWindow
	testGap, testPerWindow = time.Hour, 3
	t.Cleanup(func() { testGap, testPerWindow = oldGap, oldPer })
	if res, err := e.p.TestConnection(context.Background(), "person:1", e.team.ID); err != nil || !res.OK {
		t.Fatalf("first: %+v %v", res, err)
	}
	var rate *TestRateError
	if _, err := e.p.TestConnection(context.Background(), "person:1", e.team.ID); !errors.As(err, &rate) || rate.Wait <= 0 {
		t.Fatalf("the same connection again at once: %v", err)
	}
	// Other connections go on until the person's window is full.
	e.p.TestGap = -1
	for i, id := range []int64{e.openai.ID, e.team.ID} {
		if _, err := e.p.TestConnection(context.Background(), "person:1", id); err != nil {
			t.Fatalf("test %d: %v", i+2, err)
		}
	}
	if _, err := e.p.TestConnection(context.Background(), "person:1", e.openai.ID); !errors.As(err, &rate) {
		t.Fatalf("the fourth test in the window: %v", err)
	}
}

// A test has no run to mark when it uses a cap up: raising the cap lets the
// next test through (a mark on "run 0" would stop every later test for good).
func TestTestConnectionAfterAReachedCapWorksAgainWhenTheCapIsRaised(t *testing.T) {
	e := newEnv(t)
	e.p.TestGap = -1
	// The fake provider reports 1000 tokens in and 200 out: 0.002 USD at 1 and 5.
	c := e.conn(t, e.org.ID, "limited", store.AIFormatAnthropic, anthropicKey, "claude-haiku-5-5")
	raise := func(capUSD float64) {
		t.Helper()
		_, err := e.st.SaveAIConnection("person:1", store.AIConnectionInput{ID: c.ID, OrgID: e.org.ID, Name: "limited", Format: store.AIFormatAnthropic, BaseURL: c.BaseURL,
			MonthlyCapUSD: capUSD, Models: []store.AIModel{{Model: "claude-haiku-5-5", InUSD: 1, OutUSD: 5}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	raise(0.002)
	if res, err := e.p.TestConnection(context.Background(), "person:1", c.ID); err != nil || !res.OK {
		t.Fatalf("first: %+v %v", res, err)
	}
	if res, _ := e.p.TestConnection(context.Background(), "person:1", c.ID); res.Kind != TestCap {
		t.Fatalf("second, the cap is nearly used: %+v", res)
	}
	raise(10)
	if res, err := e.p.TestConnection(context.Background(), "person:1", c.ID); err != nil || !res.OK {
		t.Fatalf("after the cap was raised: %+v %v", res, err)
	}
}
