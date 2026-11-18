package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// REQ-690 AC-1: a claude -p agent calls start_work in its first seconds, the
// collector has not uploaded the session yet (it sweeps every 30 s). The MCP
// process knows the harness session id; start_work registers the session itself
// instead of failing with "no recently active session", and the collector's later
// upload lands on the same row.
func TestStartWorkRegistersTheSessionTheCollectorHasNotUploadedYet(t *testing.T) {
	s, st := pkgServer(t, "main")
	s.sessionRef = "8a133919-7088-4cc8-997c-42cf07d36340"
	s.SetSessionHarness("claude-code")
	req := pkgRequests(t, st, "r")[0].Request.ID

	_, out, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req})
	if err != nil {
		t.Fatalf("start_work before the collector uploaded: %v", err)
	}
	if out.Work.SessionID == 0 || out.Work.State != "active" {
		t.Fatalf("work %+v", out.Work)
	}
	// Ohne beanspruchtes Repo führt der Server die Session projektlos; sie steht
	// trotzdem in der Liste der Maschine.
	got, err := st.ListSessions(scope.Axes{}, 20)
	if err != nil || len(got) != 1 || got[0].ExternalID != s.sessionRef || got[0].Harness != "claude-code" || got[0].Scope.Machine != "workstation-a" {
		t.Fatalf("sessions %+v err %v", got, err)
	}
	// The collector's own registration finds the same row.
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: s.sessionRef, CWD: "/work/y",
		Scope: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}})
	if err != nil || id != out.Work.SessionID {
		t.Fatalf("collector upsert gave %d (err %v), work is on %d", id, err, out.Work.SessionID)
	}
	// A second start_work from the same process stays on that session.
	req2 := pkgRequests(t, st, "second")[0].Request.ID
	_, out2, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req2, Role: "related"})
	if err != nil || out2.Work.SessionID != out.Work.SessionID {
		t.Fatalf("second start_work: %+v %v", out2.Work, err)
	}
}

// Without a harness session id (a harness that does not set one) the newest
// recent session of the machine is the last resort, and the answer says so.
func TestStartWorkFallsBackToTheNewestSessionOfTheMachine(t *testing.T) {
	s, st := pkgServer(t, "main")
	s.SetLive(func() Live { return Live{Branch: "feat/x", CWD: "/nowhere"} })
	req := pkgRequests(t, st, "r")[0].Request.ID
	if _, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "other-project", CWD: "/work/other",
		Scope: scope.Axes{Project: "github.com/acme/elsewhere", Branch: "dev", Machine: "workstation-a"}}); err != nil {
		t.Fatal(err)
	}
	_, out, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req})
	if err != nil {
		t.Fatalf("start_work: %v", err)
	}
	if out.Work.SessionID == 0 || len(out.Warnings) == 0 || !strings.Contains(strings.Join(out.Warnings, " "), "newest session of machine workstation-a") {
		t.Fatalf("work %+v warnings %v", out.Work, out.Warnings)
	}
}

// With nothing to go on the failure is an instruction, not a riddle.
func TestStartWorkWithoutAnySessionSaysWhatToDo(t *testing.T) {
	s, st := pkgServer(t, "main")
	req := pkgRequests(t, st, "r")[0].Request.ID
	_, _, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req})
	if err == nil {
		t.Fatal("start_work without a session succeeded")
	}
	for _, want := range []string{"session_id", "context_search action=sessions", "ctx watch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// slowAnswer forwards every request to the real server and holds back the answer
// of the first PATCH on a criterion past the client's timeout: the change is
// saved, the caller sees a timeout (Lauf 3, jonas: criterion_met on a loaded server).
func slowAnswer(t *testing.T) (*Server, *store.Store, *atomic.Int32) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	real := httptest.NewServer(server.New(st))
	t.Cleanup(real.Close)
	target, _ := url.Parse(real.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var patches atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/criteria/") && patches.Add(1) == 1 {
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			time.Sleep(400 * time.Millisecond)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	c := client.NewWithTimeout(config.Config{ServerURL: front.URL, Token: token, Machine: "workstation-a"}, 150*time.Millisecond)
	return &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}}, st, &patches
}

func TestCriterionMetSurvivesATimeoutThatSavedTheChange(t *testing.T) {
	old := criterionRetryPause
	criterionRetryPause = 0
	t.Cleanup(func() { criterionRetryPause = old })
	s, st, patches := slowAnswer(t)
	d := pkgRequests(t, st, "r")[0]
	crit := d.Criteria[0].ID

	res, out, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: d.Request.ID, Action: "criterion_met", CriterionID: crit, EvidenceKind: "test", EvidenceRef: "TestX",
	})
	if err != nil {
		t.Fatalf("criterion_met after a timeout: %v", err)
	}
	if out.Criterion == nil || out.Criterion.State != "met" {
		t.Fatalf("output %+v", out)
	}
	if patches.Load() < 1 {
		t.Fatal("the slow answer was never exercised")
	}
	var said bool
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, "answered slowly") {
			said = true
		}
	}
	if !said {
		t.Errorf("the reply does not say that the server was slow: %+v", res.Content)
	}
	got, err := st.RequestByID(d.Request.ID)
	if err != nil || got.Criteria[0].State != "met" {
		t.Fatalf("criterion not met: %+v %v", got.Criteria, err)
	}
}
