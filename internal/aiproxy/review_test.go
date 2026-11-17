package aiproxy

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// Tests for the findings of the security review of #283 (KN-4801).

const bigStream = `{"model":"klein","max_tokens":100000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// streamCall posts a stream and reads it; stopAfter > 0 closes the answer
// after that many data lines, as a client that gives up.
func streamCall(ctx context.Context, in workflow.Input, body string, stopAfter int) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, in.AI.BaseURL+PathAnthropic, strings.NewReader(body))
	req.Header.Set("x-api-key", in.AI.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	n := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			n++
			if stopAfter > 0 && n == stopAfter {
				return nil
			}
		}
	}
	return nil
}

// M1: a stream that ends before its final usage, because the provider cut it
// or the client stopped reading, is booked at the estimate, not at the few
// tokens of its first event.
func TestAStreamThatBreaksOffIsBookedAtTheEstimate(t *testing.T) {
	for _, tc := range []struct{ mode string }{{"cut"}, {"hang"}} {
		t.Run(tc.mode, func(t *testing.T) {
			e := newEnv(t)
			e.provider.mode = tc.mode
			e.provider.hung = make(chan struct{})
			e.blocks["test/abort"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
				stop := 0
				if tc.mode == "hang" {
					stop = 3
				}
				return store.WorkflowRunOutcome{State: store.RunDone}, streamCall(ctx, in, bigStream, stop)
			})
			run := e.run(t, "test/abort", nil)
			// 100000 output tokens at 5 USD per million.
			if run.CostUSD < 0.5 {
				t.Fatalf("a stream broken off is booked at %v USD, want the estimate of at least 0.5", run.CostUSD)
			}
		})
	}
}

// m1: an answer without usage is booked at the estimate, so the caps hold.
func TestAnAnswerWithoutUsageIsBookedAtTheEstimate(t *testing.T) {
	e := newEnv(t)
	e.provider.mode = "nousage"
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, RunUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	var statuses []int
	e.blocks["test/nousage"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		for i := 0; i < 5; i++ {
			s, _ := post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, `{"model":"klein","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`)
			statuses = append(statuses, s)
		}
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	run := e.run(t, "test/nousage", nil)
	if fmt.Sprint(statuses) != "[200 402 402 402 402]" || run.CostUSD < 0.005 || run.State != store.RunBudget {
		t.Fatalf("statuses %v, run %+v", statuses, run)
	}
}

// m2: the largest of the limit fields counts, and a request without one gets
// one, so that the provider cannot write more than was estimated.
func TestTheEstimateTakesTheLargestLimitAndSetsOneWhenNoneIsNamed(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, RunUSD: 0.1}); err != nil {
		t.Fatal(err)
	}
	status := 0
	e.blocks["test/limits"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		status, _ = post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, `{"model":"klein","max_tokens":1,"max_completion_tokens":100000,"messages":[]}`)
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	e.run(t, "test/limits", nil)
	// Estimated at 100000 output tokens (0.5 USD), over the cap of 0.1.
	if status != http.StatusPaymentRequired || e.provider.calls != 0 {
		t.Fatalf("max_tokens 1 next to max_completion_tokens 100000: %d, %d calls", status, e.provider.calls)
	}
	// A request without a limit gets one.
	e.blocks["test/nolimit"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, `{"model":"klein","messages":[]}`)
		post(t, in.AI.BaseURL, in.AI.Token, PathOpenAI, `{"model":"openai-a/gpt-a","messages":[]}`)
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	e.run(t, "test/nolimit", nil)
	if len(e.provider.bodies) != 2 || e.provider.bodies[0]["max_tokens"] != float64(defaultMaxOut) || e.provider.bodies[1]["max_completion_tokens"] != float64(defaultMaxOut) {
		t.Fatalf("limits put in: %v", e.provider.bodies)
	}
}

// flat is a connection whose calls cost exactly their estimate: input is
// free, output is what max_tokens allows and the provider writes.
func (e *env) flat(t *testing.T) {
	t.Helper()
	c, err := e.st.SaveAIConnection("person:1", store.AIConnectionInput{OrgID: e.org.ID, Name: "flat", Format: store.AIFormatAnthropic, BaseURL: e.provider.srv.URL,
		KeyLast4: secretbox.Last4(anthropicKey), Models: []store.AIModel{{Model: "flat-1", InUSD: 0, OutUSD: 5}}},
		func(purpose string) ([]byte, error) { return e.box.Seal(anthropicKey, purpose) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetAIAlias("person:1", store.AIAlias{Level: store.AILevelOwner, OrgID: e.org.ID, Alias: "flat", ConnectionID: c.ID, Model: "flat-1"}); err != nil {
		t.Fatal(err)
	}
}

const flatCall = `{"model":"flat","max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`

func parallel(n int, call func() int) []int {
	var wg sync.WaitGroup
	out := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = call()
		}(i)
	}
	wg.Wait()
	return out
}

func count(statuses []int, want int) int {
	n := 0
	for _, s := range statuses {
		if s == want {
			n++
		}
	}
	return n
}

// M2 in one run: calls in parallel do not pass the run's cap together.
func TestParallelCallsOfARunStayUnderItsCap(t *testing.T) {
	e := newEnv(t)
	e.flat(t)
	e.provider.delay = 30 * time.Millisecond
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, RunUSD: 0.003}); err != nil {
		t.Fatal(err)
	}
	var statuses []int
	e.blocks["test/parallel"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		statuses = parallel(20, func() int { s, _ := post(t, in.AI.BaseURL, in.AI.Token, PathAnthropic, flatCall); return s })
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	run := e.run(t, "test/parallel", nil)
	if ok := count(statuses, http.StatusOK); ok != 3 || e.provider.calls != 3 {
		t.Fatalf("%d of 20 calls passed a cap for 3 (provider saw %d)", ok, e.provider.calls)
	}
	if run.CostUSD > 0.003+1e-9 || run.State != store.RunBudget {
		t.Fatalf("run: %+v", run)
	}
}

// running makes a run of workflow that is running, with its token, without
// the engine, so that two runs can call at the same time.
func (e *env) running(t *testing.T, name string) (store.WorkflowRun, string) {
	t.Helper()
	if _, err := e.st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.seq++
	if _, _, err := e.st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventIdeaCreated, Project: project, SubjectKind: "idea", SubjectID: strconv.Itoa(e.seq),
		Origin: store.EventOrigin{Kind: store.OriginHuman}, DedupeKey: "r" + strconv.Itoa(e.seq)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DispatchWorkflowEvents(0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	run, _, ok, err := e.st.ClaimWorkflowRun()
	if err != nil || !ok || run.Workflow != name {
		t.Fatalf("claim %s: %+v %v %v", name, run, ok, err)
	}
	token, err := e.st.IssueAIRunToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, token
}

// M2 across runs: two runs calling at the same time do not pass the owner's
// monthly cap together.
func TestParallelCallsOfTwoRunsStayUnderTheOwnersCap(t *testing.T) {
	e := newEnv(t)
	e.flat(t)
	e.provider.delay = 30 * time.Millisecond
	if err := e.st.SetAICap("person:1", store.AICap{OrgID: e.org.ID, MonthUSD: 0.004}); err != nil {
		t.Fatal(err)
	}
	_, ta := e.running(t, "test/a")
	_, tb := e.running(t, "test/b")
	statuses := parallel(20, func() int {
		token := ta
		if time.Now().UnixNano()%2 == 0 {
			token = tb
		}
		s, _ := post(t, e.proxy.URL, token, PathAnthropic, flatCall)
		return s
	})
	rep, _ := e.st.AICostReportOf(e.org.ID, store.AIMonthStart())
	if ok := count(statuses, http.StatusOK); ok != 4 || rep.TotalUSD > 0.004+1e-9 {
		t.Fatalf("%d calls passed, %v USD booked under a cap of 0.004", ok, rep.TotalUSD)
	}
}

// m7: when a run ends, a call the block left running is stopped and booked
// before the run's cost is read.
func TestTheEndOfARunStopsItsCallsAndBooksThem(t *testing.T) {
	e := newEnv(t)
	e.provider.mode = "hang"
	e.provider.hung = make(chan struct{})
	started := make(chan struct{})
	// Without the fix the stream would stay open past the test; the client
	// lets go when the test ends, so that the servers can close.
	clientCtx, letGo := context.WithCancel(context.Background())
	t.Cleanup(letGo)
	e.blocks["test/leave"] = workflow.BlockFunc(func(ctx context.Context, in workflow.Input) (store.WorkflowRunOutcome, error) {
		go func() {
			req, _ := http.NewRequestWithContext(clientCtx, http.MethodPost, in.AI.BaseURL+PathAnthropic, strings.NewReader(bigStream))
			req.Header.Set("x-api-key", in.AI.Token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				close(started)
				return
			}
			defer resp.Body.Close()
			sc := bufio.NewScanner(resp.Body)
			first := true
			for sc.Scan() {
				if first && strings.HasPrefix(sc.Text(), "data:") {
					first = false
					close(started)
				}
			}
		}()
		<-started
		// The block returns with its stream still open.
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	run := e.run(t, "test/leave", nil)
	select {
	case <-e.provider.hung:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream goes on after the run ended")
	}
	if run.CostUSD < 0.5 {
		t.Fatalf("the run shows %v USD, without the call it left running", run.CostUSD)
	}
}

// m4: addresses the standard library calls global unicast but that are
// reserved, internal or embed an IPv4 address are no way out for an owner.
func TestOwnerConnectionsRefuseReservedAndEmbeddedAddresses(t *testing.T) {
	for _, ip := range []string{"64:ff9b:1::a00:1", "2002:7f00:1::", "fec0::1", "::7f00:1", "::a00:1", "240.0.0.1", "255.255.255.255",
		"192.0.2.10", "198.51.100.10", "203.0.113.10", "2001:0:4136:e378::1", "100::1", "192.88.99.1"} {
		if PublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s counts as public", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111", "2a00:1450:4001::200e"} {
		if !PublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s counts as internal", ip)
		}
	}
}
