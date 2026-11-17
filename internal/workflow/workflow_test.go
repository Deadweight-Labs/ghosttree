package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/testtmp"
)

func TestMain(m *testing.M) { os.Exit(testtmp.Run(m)) }

const project = "github.com/alpha/app"

func engineStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenRuntime(filepath.Join(t.TempDir(), "wf.db"), store.DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", project, "alpha"); err != nil {
		t.Fatal(err)
	}
	return st
}

func merged(t *testing.T, st *store.Store, pr string) {
	t.Helper()
	if _, _, err := st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventPRMerged, Project: project, SubjectKind: "pr", SubjectID: pr,
		Data: map[string]string{"base": "main", "default_branch": "1"}, Origin: store.EventOrigin{Kind: store.OriginSystem, Person: "github"},
		DedupeKey: store.EventPRMerged + ":" + project + "#" + pr}); err != nil {
		t.Fatal(err)
	}
}

// A merge is a finished echo run after the next tick (AC-693.1: the server
// ticks every 15 seconds, far inside five minutes), and the same merge
// delivered again adds nothing.
func TestOneTickTurnsAMergeIntoAFinishedEchoRun(t *testing.T) {
	st := engineStore(t)
	if _, err := st.SetWorkflowTrigger("person:1", store.WorkflowTrigger{Project: project, Workflow: EchoName, EventKind: store.EventPRMerged, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st, Resolver: DefaultBuiltins()}
	merged(t, st, "12")
	merged(t, st, "12")
	rep, err := e.Tick(context.Background())
	if err != nil || rep.Events != 1 || rep.Queued != 1 || rep.Executed != 1 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	runs, err := st.WorkflowRunsIn(project, store.WorkflowRunFilter{SubjectKind: "pr", SubjectID: "12"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	r := runs[0]
	if r.State != store.RunDone || r.Result != "pr.merged PR #12" || r.WorkflowVersion != "builtin" || r.StartedAt == "" || r.FinishedAt == "" || r.CostUSD != 0 {
		t.Fatalf("run: %+v", r)
	}
	_, text, err := st.WorkflowRunByID(r.ID)
	if err != nil || text == nil || !strings.Contains(text.Inputs, `"subject_id": "12"`) || text.Output != r.Result {
		t.Fatalf("text: %+v %v", text, err)
	}
	merged(t, st, "12")
	if rep, err := e.Tick(context.Background()); err != nil || rep.Queued+rep.Executed != 0 {
		t.Fatalf("a third delivery: %+v %v", rep, err)
	}
}

func TestUnknownFailingAndPanickingBlocksEndTheirRun(t *testing.T) {
	st := engineStore(t)
	for _, name := range []string{"ghosttree/missing", "test/fails", "test/panics", "test/nostate"} {
		if _, err := st.SetWorkflowTrigger("person:1", store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventPRMerged, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	blocks := Builtins{
		"test/fails": BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) {
			return store.WorkflowRunOutcome{}, errors.New("model said no")
		}),
		"test/panics": BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) { panic("boom") }),
		"test/nostate": BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) {
			return store.WorkflowRunOutcome{Result: "?"}, nil
		}),
	}
	e := &Engine{Store: st, Resolver: blocks}
	merged(t, st, "3")
	if rep, err := e.Tick(context.Background()); err != nil || rep.Executed != 4 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	runs, _ := st.WorkflowRunsIn(project, store.WorkflowRunFilter{})
	got := map[string]string{}
	for _, r := range runs {
		got[r.Workflow] = r.State + " " + r.Reason
	}
	want := map[string]string{"ghosttree/missing": "skipped unknown_workflow", "test/fails": "failed model said no", "test/panics": "failed panic: boom", "test/nostate": "failed block ended without a state"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}

func TestTickPurgesTextsPastTheRetentionHourly(t *testing.T) {
	st := engineStore(t)
	if _, err := st.SetWorkflowTrigger("person:1", store.WorkflowTrigger{Project: project, Workflow: EchoName, EventKind: store.EventPRMerged, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	e := &Engine{Store: st, Resolver: DefaultBuiltins(), Now: func() time.Time { return clock }}
	merged(t, st, "5")
	if rep, err := e.Tick(context.Background()); err != nil || rep.Executed != 1 || rep.Purged != 0 {
		t.Fatalf("first tick: %+v %v", rep, err)
	}
	clock = clock.AddDate(0, 0, store.WorkflowRetentionDefault+1)
	if rep, err := e.Tick(context.Background()); err != nil || rep.Purged != 1 {
		t.Fatalf("tick after the retention: %+v %v", rep, err)
	}
}
