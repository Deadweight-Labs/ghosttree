package aiproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

const (
	anthropicKey = "sk-ant-api03-SECRETanthropicKEY-1234"
	openaiKey    = "sk-proj-SECREToPENAIkey-5678"
	project      = "alpha/app"
)

// fakeProvider answers like Anthropic and OpenAI and remembers what reached it.
type fakeProvider struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls int
	keys  []string
	auths []string
	fail  bool
	// mode changes the answer: "cut" ends a stream before its final usage,
	// "hang" holds it open there until the request ends, "nousage" answers
	// without usage.
	mode   string
	delay  time.Duration
	bodies []map[string]any
	hung   chan struct{}
}

func (f *fakeProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	f.calls++
	f.keys = append(f.keys, r.Header.Get("x-api-key"))
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.bodies = append(f.bodies, req)
	fail, mode, delay := f.fail, f.mode, f.delay
	f.mu.Unlock()
	time.Sleep(delay)
	if fail {
		// A provider that quotes the key it got, as some error pages do.
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s%s"}}`, r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		return
	}
	stream, _ := req["stream"].(bool)
	switch r.URL.Path {
	case "/v1/messages":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":2000,"output_tokens":1}}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hallo "}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Welt"}}`,
				`{"type":"message_delta","usage":{"output_tokens":200}}`,
				`{"type":"message_stop"}`,
			} {
				if strings.Contains(ev, "message_delta") {
					switch mode {
					case "cut":
						return
					case "hang":
						<-r.Context().Done()
						close(f.hung)
						return
					}
				}
				fmt.Fprintf(w, "event: x\ndata: %s\n\n", ev)
				w.(http.Flusher).Flush()
			}
			return
		}
		if mode == "hang" {
			<-r.Context().Done()
			close(f.hung)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "nousage" {
			fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
			return
		}
		fmt.Fprintf(w, `{"type":"message","content":[{"type":"text","text":"Ein PR wurde gemergt (%v)."}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":200}}`, req["model"])
	case "/v1/chat/completions":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"o\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"k\"}}]}\n\n")
			if opts, _ := req["stream_options"].(map[string]any); opts["include_usage"] == true {
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":200,\"prompt_tokens_details\":{\"cached_tokens\":400}}}\n\n")
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok %v"}}],"usage":{"prompt_tokens":1000,"completion_tokens":200}}`, req["model"])
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	st       *store.Store
	org      store.Org
	box      *secretbox.Box
	provider *fakeProvider
	proxy    *httptest.Server
	p        *Proxy
	logs     *bytes.Buffer
	team     store.AIConnection
	openai   store.AIConnection
	blocks   workflow.Builtins
	seq      int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/ai.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"robin", "anna"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamedProject("person:1", "app", "alpha"); err != nil {
		t.Fatal(err)
	}
	box, _ := secretbox.New(bytes.Repeat([]byte{9}, 32))
	f := &fakeProvider{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	e := &env{st: st, org: org, box: box, provider: f, logs: &bytes.Buffer{}}
	e.team = e.conn(t, org.ID, "anthropic-team", store.AIFormatAnthropic, anthropicKey, "claude-haiku-5-5")
	e.openai = e.conn(t, org.ID, "openai-a", store.AIFormatOpenAI, openaiKey, "gpt-a")
	if _, err := st.SetAIAlias("person:1", store.AIAlias{Level: store.AILevelOwner, OrgID: org.ID, Alias: "klein", ConnectionID: e.team.ID, Model: "claude-haiku-5-5"}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Store: st, Box: box, Log: slog.New(slog.NewJSONHandler(e.logs, nil)), FileKeys: map[int64]string{},
		Upstream: func(store.AIConnection, *url.URL) (*http.Client, error) { return f.srv.Client(), nil }}
	e.p = p
	e.proxy = httptest.NewServer(p)
	t.Cleanup(e.proxy.Close)
	e.blocks = workflow.Builtins{workflow.AskName: workflow.Ask}
	return e
}

func (e *env) conn(t *testing.T, orgID int64, name, format, key string, models ...string) store.AIConnection {
	t.Helper()
	in := store.AIConnectionInput{OrgID: orgID, Name: name, Format: format, BaseURL: e.provider.srv.URL, KeyLast4: secretbox.Last4(key)}
	for _, m := range models {
		in.Models = append(in.Models, store.AIModel{Model: m, InUSD: 1, OutUSD: 5})
	}
	c, err := e.st.SaveAIConnection("person:1", in, func(purpose string) ([]byte, error) { return e.box.Seal(key, purpose) })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// run starts workflow for one event with data and executes it.
func (e *env) run(t *testing.T, name string, data map[string]string) store.WorkflowRun {
	t.Helper()
	if _, err := e.st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.seq++
	if _, _, err := e.st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventIdeaCreated, Project: project, SubjectKind: "idea", SubjectID: strconv.Itoa(e.seq),
		Data: data, Origin: store.EventOrigin{Kind: store.OriginHuman}, DedupeKey: "e" + strconv.Itoa(e.seq)}); err != nil {
		t.Fatal(err)
	}
	eng := &workflow.Engine{Store: e.st, Resolver: e.blocks, AIBaseURL: e.proxy.URL, AIEndRun: e.p.EndRun}
	if _, err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One run per call: the trigger stops listening to later events.
	if _, err := e.st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.st.WorkflowRunsIn(project, store.WorkflowRunFilter{SubjectKind: "idea", SubjectID: strconv.Itoa(e.seq)})
	for _, r := range runs {
		if r.Workflow == name {
			return r
		}
	}
	t.Fatalf("no run of %s", name)
	return store.WorkflowRun{}
}

func post(t *testing.T, base, token, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	req.Header.Set("x-api-key", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (e *env) mustNotLeak(t *testing.T, where, text string, extra ...string) {
	t.Helper()
	for _, secret := range append([]string{anthropicKey, openaiKey, anthropicKey[4 : len(anthropicKey)-4], openaiKey[4 : len(openaiKey)-4]}, extra...) {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s carries a secret (%q…)", where, secret[:8])
		}
	}
}

// AC-694.6 and AC-694.4: a run calls the model through the proxy with its
// token, the provider gets the key, the block never holds it, the run names
// connection and model, and the token is refused once the run ended.
func TestRunCallsAModelOnlyWithItsTokenAndTheTokenEndsWithTheRun(t *testing.T) {
	e := newEnv(t)
	var seen workflow.ModelAccess
	e.blocks["test/spy"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		seen = *in.AI
		return workflow.Ask.Run(ctx, in)
	})
	run := e.run(t, "test/spy", nil)
	if run.State != store.RunDone || run.Connection != "anthropic-team" || run.Model != "claude-haiku-5-5" || run.CostUSD != 0.002 {
		t.Fatalf("run: %+v", run)
	}
	_, text, _ := e.st.WorkflowRunByID(run.ID)
	if !strings.Contains(text.Output, "claude-haiku-5-5") || strings.Contains(run.Result, "gemergt") {
		t.Fatalf("the proxy put in the real model, and the answer stays out of the result: %q / %q", text.Output, run.Result)
	}
	if e.provider.keys[0] != anthropicKey {
		t.Fatalf("the provider did not get the key of the connection")
	}
	if !strings.HasPrefix(seen.Token, store.AIRunTokenPrefix) || seen.BaseURL != e.proxy.URL {
		t.Fatalf("model access: %+v", seen)
	}
	e.mustNotLeak(t, "the block's access", seen.Token+seen.BaseURL)
	// Outside the run the token is worth nothing.
	status, body := post(t, e.proxy.URL, seen.Token, PathAnthropic, `{"model":"klein","max_tokens":10,"messages":[]}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("token after the run: %d %s", status, body)
	}
	if status, _ := post(t, e.proxy.URL, "gtr_made-up", PathAnthropic, `{"model":"klein"}`); status != http.StatusUnauthorized {
		t.Fatalf("made-up token: %d", status)
	}
	if status, _ := post(t, e.proxy.URL, anthropicKey, PathAnthropic, `{"model":"klein"}`); status != http.StatusUnauthorized {
		t.Fatalf("a provider key is no run token: %d", status)
	}
	if e.provider.calls != 1 {
		t.Fatalf("calls that reached the provider: %d", e.provider.calls)
	}
	e.mustNotLeak(t, "the run's text", text.Inputs+text.Output, seen.Token)
	e.mustNotLeak(t, "the proxy's log", e.logs.String(), seen.Token)
	if !strings.Contains(e.logs.String(), `"connection":"anthropic-team"`) {
		t.Fatalf("log: %s", e.logs.String())
	}
}

// Streaming passes through event by event in both formats, and the tokens
// the provider reports in the stream are booked.
func TestStreamingPassesThroughAndIsCounted(t *testing.T) {
	e := newEnv(t)
	var got []string
	e.blocks["test/stream"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		for _, c := range []struct{ path, body string }{
			{PathAnthropic, `{"model":"klein","max_tokens":300,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
			{PathOpenAI, `{"model":"openai-a/gpt-a","max_completion_tokens":300,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		} {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, in.AI.BaseURL+c.path, strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+in.AI.Token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return store.WorkflowRunOutcome{}, err
			}
			sc := bufio.NewScanner(resp.Body)
			n := 0
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "data:") {
					n++
				}
			}
			resp.Body.Close()
			got = append(got, resp.Header.Get("Content-Type")+" "+strconv.Itoa(n))
		}
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	run := e.run(t, "test/stream", nil)
	if strings.Join(got, ",") != "text/event-stream 5,text/event-stream 4" {
		t.Fatalf("events through the proxy: %v", got)
	}
	if run.State != store.RunDone || run.Connection != "anthropic-team, openai-a" || run.Model != "claude-haiku-5-5, gpt-a" {
		t.Fatalf("run: %+v", run)
	}
	// Anthropic: 1000 in, 2000 cache reads (a tenth of the price), 200 out;
	// OpenAI: 600 in, 400 cached, 200 out.
	want := (1000*1+2000*0.1+200*5)/1e6 + (600*1+400*0.1+200*5)/1e6
	if d := run.CostUSD - want; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost %v, want %v", run.CostUSD, want)
	}
	if e.provider.auths[1] != "Bearer "+openaiKey {
		t.Fatal("the OpenAI provider did not get the key")
	}
}

// AC-694.7: at the cap of the run the next call is refused before it reaches
// the provider, and the run ends in the state budget naming the cap.
func TestRunAtItsCapEndsInBudgetAndNoFurtherCallGoesThrough(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, RunUSD: 0.003}); err != nil {
		t.Fatal(err)
	}
	var statuses []int
	e.blocks["test/greedy"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		for i := 0; i < 4; i++ {
			s, _ := post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, `{"model":"klein","max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)
			statuses = append(statuses, s)
		}
		// The block itself would call it a success.
		return store.WorkflowRunOutcome{State: store.RunDone, Result: "done"}, nil
	})
	run := e.run(t, "test/greedy", nil)
	if fmt.Sprint(statuses) != "[200 402 402 402]" {
		t.Fatalf("statuses: %v", statuses)
	}
	if e.provider.calls != 1 {
		t.Fatalf("calls that reached the provider: %d", e.provider.calls)
	}
	if run.State != store.RunBudget || run.Reason != store.AICapRun || run.CostUSD != 0.002 {
		t.Fatalf("run: %+v", run)
	}
	// The workflow's monthly cap ends the next run before its first call.
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, Workflow: "test/greedy", MonthUSD: 0.002}); err != nil {
		t.Fatal(err)
	}
	statuses = nil
	next := e.run(t, "test/greedy", nil)
	if next.State != store.RunBudget || next.Reason != store.AICapWorkflow || e.provider.calls != 1 {
		t.Fatalf("next run: %+v, calls %d, statuses %v", next, e.provider.calls, statuses)
	}
}

// An error page of the provider that quotes the key reaches the run without
// it, and so does the log.
func TestProviderErrorsAreScrubbedOfTheKey(t *testing.T) {
	e := newEnv(t)
	e.provider.fail = true
	var answers []string
	e.blocks["test/fail"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		for _, c := range []struct{ path, model string }{{PathAnthropic, "klein"}, {PathOpenAI, "gpt-a"}} {
			_, body := post(t, in.AI.BaseURL, in.AI.Token, c.path, `{"model":"`+c.model+`","max_tokens":10,"messages":[]}`)
			answers = append(answers, body)
		}
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	e.run(t, "test/fail", nil)
	if len(answers) != 2 || !strings.Contains(answers[0], "Incorrect API key") {
		t.Fatalf("answers: %v", answers)
	}
	e.mustNotLeak(t, "the error answer", strings.Join(answers, "\n"))
	e.mustNotLeak(t, "the log", e.logs.String())
}

// AC-694.3 at the proxy: a loan taken back stops the very next call of a
// running run; AC-694.2: without a release the server's connection is no
// way out either.
func TestRevokedLoanAndUnreleasedServerStopTheNextCall(t *testing.T) {
	e := newEnv(t)
	ns, err := e.st.EnsureUserNamespace("person:1")
	if err != nil {
		t.Fatal(err)
	}
	mine := e.conn(t, ns.ID, "robin-own", store.AIFormatOpenAI, "sk-robin-PRIVATEkey-0000", "gpt-own")
	loan, err := e.st.LendAIConnection("person:1", mine.ID, e.org.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetAIAlias("person:1", store.AIAlias{Level: store.AILevelOwner, OrgID: e.org.ID, Alias: "privat", ConnectionID: mine.ID, Model: "gpt-own"}); err != nil {
		t.Fatal(err)
	}
	e.conn(t, 0, "standard", store.AIFormatAnthropic, "sk-ant-SERVER-key-9999", "claude-opus-5-5")
	var statuses []int
	e.blocks["test/loan"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		body := `{"model":"privat","max_tokens":10,"messages":[]}`
		s, _ := post(t, in.AI.BaseURL, in.AI.Token, PathOpenAI, body)
		statuses = append(statuses, s)
		if err := e.st.RevokeAILoan("person:1", loan.ID); err != nil {
			return store.WorkflowRunOutcome{}, err
		}
		s, _ = post(t, in.AI.BaseURL, in.AI.Token, PathOpenAI, body)
		statuses = append(statuses, s)
		s, _ = post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, `{"model":"standard/claude-opus-5-5","max_tokens":10,"messages":[]}`)
		statuses = append(statuses, s)
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	run := e.run(t, "test/loan", nil)
	if fmt.Sprint(statuses) != "[200 403 403]" || e.provider.calls != 1 || run.Connection != "robin-own" {
		t.Fatalf("statuses %v, calls %d, run %+v", statuses, e.provider.calls, run)
	}
	rep, _ := e.st.AICostReportOf(e.org.ID, store.AIMonthStart())
	if rep.Calls != 1 || rep.ByConnection[0].Key != "robin-own" {
		t.Fatalf("the org's report: %+v", rep)
	}
}

// AC-694.5 end to end: two workflows of one org over two connections and
// models; every amount is assigned to workflow, run, connection and model.
func TestTwoWorkflowsOverTwoConnectionsAreAccountedApart(t *testing.T) {
	e := newEnv(t)
	a := e.run(t, workflow.AskName, nil)
	b := e.run(t, "test/ask-openai", map[string]string{"model": "openai-a/gpt-a"})
	if b.State != store.RunSkipped {
		// The block is not registered: the run is skipped. Register it and run again.
		t.Fatalf("unregistered: %+v", b)
	}
	e.blocks["test/ask-openai"] = workflow.Ask
	b = e.run(t, "test/ask-openai", map[string]string{"model": "openai-a/gpt-a"})
	if a.Connection != "anthropic-team" || b.Connection != "openai-a" || b.Model != "gpt-a" || b.State != store.RunDone {
		t.Fatalf("runs: %+v %+v", a, b)
	}
	rep, _ := e.st.AICostReportOf(e.org.ID, store.AIMonthStart())
	keys := func(rows []store.AICostRow) string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return strings.Join(out, ",")
	}
	if rep.Calls != 2 || keys(rep.ByWorkflow) != "ghosttree/ask,test/ask-openai" && keys(rep.ByWorkflow) != "test/ask-openai,ghosttree/ask" ||
		len(rep.ByRun) != 2 || len(rep.ByConnection) != 2 || len(rep.ByModel) != 2 {
		t.Fatalf("report: %+v", rep)
	}
}

func TestOwnerConnectionsReachOnlyPublicAddressesOverHTTPS(t *testing.T) {
	owner := store.AIConnection{OrgID: 7, Name: "x"}
	for _, raw := range []string{"http://api.example.com/v1/messages", "https://127.0.0.1:443/v1/messages", "https://10.0.0.5/v1/messages", "https://[::1]/v1/messages", "https://169.254.169.254/v1/messages", "https://100.100.1.1/v1/messages"} {
		u, _ := url.Parse(raw)
		c, err := UpstreamClient(owner, u)
		if err == nil {
			_, err = c.Post(raw, "application/json", strings.NewReader("{}"))
		}
		if err == nil || !(strings.Contains(err.Error(), "not a public address") || strings.Contains(err.Error(), "https")) {
			t.Errorf("%s: %v", raw, err)
		}
	}
	server := store.AIConnection{OrgID: 0, Name: "standard"}
	u, _ := url.Parse("http://127.0.0.1:1/v1/messages")
	if _, err := UpstreamClient(server, u); err != nil {
		t.Fatalf("the server's connection to a local proxy: %v", err)
	}
	for ip, want := range map[string]bool{"8.8.8.8": true, "2606:4700::1": true, "192.168.1.1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "0.0.0.0": false} {
		if got := PublicAddress(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s public: %v", ip, got)
		}
	}
}

// A provider that answers with a redirect does not get the key carried to
// the address it names.
func TestRedirectOfTheProviderIsNotFollowed(t *testing.T) {
	var leaked []string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = append(leaked, r.Header.Get("x-api-key")+r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	u, _ := url.Parse(provider.URL + "/v1/messages")
	c, err := UpstreamClient(store.AIConnection{Name: "standard"}, u)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, u.String(), strings.NewReader("{}"))
	req.Header.Set("x-api-key", anthropicKey)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || len(leaked) != 0 {
		t.Fatalf("redirect followed: %d, %d requests elsewhere", resp.StatusCode, len(leaked))
	}
	if owner, _ := UpstreamClient(store.AIConnection{OrgID: 7, Name: "x"}, &url.URL{Scheme: "https", Host: "api.example.com"}); owner.CheckRedirect == nil {
		t.Fatal("an owner's connection follows redirects")
	}
}

func TestProxyListensOnLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0", "192.0.2.1:0"} {
		if ln, err := Listen(addr); err == nil {
			ln.Close()
			t.Errorf("%s accepted", addr)
		}
	}
	ln, err := Listen("")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatalf("default address: %s", ln.Addr())
	}
}
