package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// hiddenRequest is a request distilled from a private session of mia: only she
// may read it.
func (w *painWorld) hiddenRequest(t *testing.T) int64 {
	t.Helper()
	hidden, err := w.st.CreateRequest(requestdomain.CreateInput{
		Request: requestdomain.Request{Type: "feature", Title: "geheim", Scope: scope.Axes{Project: roleProject}, Origin: "distilled", SessionRef: "session:777"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "priv", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	w.st.DB().Exec(`UPDATE requests SET session_ref=? WHERE id=?`, "session:"+idRef("", sid), hidden.Request.ID)
	return hidden.Request.ID
}

func TestMovingARequestTakesItsTasksAndDropsForeignEdges(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "wandert")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "entscheiden"})
	d := w.newPain(t, "mia", pain.Target{RequestID: rid})
	if err := w.st.MoveRequest(rid, "github.com/other/place", "mia", "falsches Projekt"); err != nil {
		t.Fatal(err)
	}
	var project string
	w.st.DB().QueryRow(`SELECT project FROM human_tasks WHERE id=?`, task.ID).Scan(&project)
	if project != "github.com/other/place" {
		t.Errorf("the task stayed in %q", project)
	}
	var edges int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM entity_links WHERE src_id=?`, d.Pain.ID).Scan(&edges)
	if edges != 0 {
		t.Errorf("the pain keeps %d edge(s) across the project border", edges)
	}
	if rows, _ := w.st.painsLinkedTo("request", rid); len(rows) != 0 {
		t.Errorf("the request page would still list the pain: %+v", rows)
	}
}

func TestCanonicalizeKeepsDuplicateRequestsThatCarryATask(t *testing.T) {
	w := newPainWorld(t)
	first := w.request(t, "doppelt")
	second := w.request(t, "doppelt")
	w.task(t, "mia", humantask.CreateInput{RequestID: second, Kind: "input", Title: "hängt dran"})
	report, err := w.st.CanonicalizeScopes(nil)
	if err != nil {
		t.Fatalf("canonicalize failed on a duplicate with a task: %v", err)
	}
	if len(report.MergeSkipped) != 1 || report.MergeSkipped[0] != second {
		t.Errorf("skipped %v, want [%d] (first %d)", report.MergeSkipped, second, first)
	}
}

func TestCanonicalizeRescopesPainsAndTasks(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "alias")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "t"})
	d := w.newPain(t, "mia", pain.Target{RequestID: rid})
	w.st.DB().Exec(`UPDATE pains SET project=?`, "https://"+roleProject+".git")
	if _, err := w.st.CanonicalizeScopes(nil); err != nil {
		t.Fatal(err)
	}
	var project string
	w.st.DB().QueryRow(`SELECT project FROM pains WHERE id=?`, d.Pain.ID).Scan(&project)
	if project != roleProject {
		t.Errorf("pain project %q, want %q", project, roleProject)
	}
	_ = task
}

func TestMembersDoNotSeeTasksOfHiddenRequestsAndFiltersDoNotProbe(t *testing.T) {
	w := newPainWorld(t)
	hidden := w.hiddenRequest(t)
	task := w.task(t, "mia", humantask.CreateInput{RequestID: hidden, Kind: "input", Title: "unsichtbar"})
	d := w.newPain(t, "mia", pain.Target{RequestID: hidden})
	rex := w.pa["rex"]
	if list, _ := w.st.ListHumanTasks(rex, TaskFilter{Project: roleProject, State: "all"}); len(list) != 0 {
		t.Errorf("a member lists a task of a hidden request: %+v", list)
	}
	if waiting, _ := w.st.WaitingTasksFor(rex, roleProject, 5); len(waiting.Open) != 0 {
		t.Errorf("the bootstrap shows a task of a hidden request: %+v", waiting)
	}
	if list, _ := w.st.ListHumanTasks(w.pa["mia"], TaskFilter{Project: roleProject, State: "all"}); len(list) != 1 || list[0].ID != task.ID {
		t.Errorf("the author lost her task: %+v", list)
	}
	for _, who := range []string{"rex", "gus"} {
		got, err := w.pa[who].SearchPains(pain.Filter{Project: roleProject, State: "all", RequestID: hidden})
		if err != nil || len(got) != 0 {
			t.Errorf("%s probes the edge to a hidden request: %v %v", who, got, err)
		}
	}
	if got, _ := w.pa["mia"].SearchPains(pain.Filter{Project: roleProject, State: "all", RequestID: hidden}); len(got) != 1 || got[0].ID != d.Pain.ID {
		t.Errorf("the author cannot filter by her request: %+v", got)
	}
}

func TestWorkIdOfAHiddenRequestAnswersLikeAnUnknownOne(t *testing.T) {
	w := newPainWorld(t)
	hidden := w.hiddenRequest(t)
	sid, _ := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "other", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	work, _, err := w.st.StartRequestWork(hidden, sid, "primary", "mia")
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := accountNumericID(w.pa["rex"].principal.ID)
	_, got := w.pa["rex"].CreateHumanTask(humantask.CreateInput{WorkID: work.ID, Kind: "input", Title: "x"}, humantask.Actor{AccountID: acct})
	_, unknown := w.pa["rex"].CreateHumanTask(humantask.CreateInput{WorkID: 99999, Kind: "input", Title: "x"}, humantask.Actor{AccountID: acct})
	if ruleCode(got) != "work_invalid" || ruleCode(unknown) != "work_invalid" {
		t.Errorf("hidden work %v, unknown work %v: the answers differ", got, unknown)
	}
	if errors.Is(got, ErrAccessForbidden) {
		t.Error("forbidden leaks existence")
	}
}

func TestConfirmPromptOnlyForThoseWhoMayConfirm(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "bestätigen")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "wähle"})
	acct, _ := accountNumericID(w.pa["rex"].principal.ID)
	if _, err := w.pa["rex"].MarkTasksDone([]int64{task.ID}, "ok", humantask.Actor{AccountID: acct, Person: "rex"}); err != nil {
		t.Fatal(err)
	}
	for who, want := range map[string]bool{"mia": true, "rex": false} {
		waiting, _ := w.st.WaitingTasksFor(w.pa[who], roleProject, 5)
		if len(waiting.Done) != 1 || waiting.Done[0].CanConfirm != want {
			t.Errorf("%s: %+v, want CanConfirm=%v", who, waiting.Done, want)
		}
	}
}

func TestSessionEvidenceOfAPainStaysWithThoseWhoMayReadIt(t *testing.T) {
	w := newPainWorld(t)
	sid, _ := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "evid", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	d := w.newPain(t, "mia")
	ref := idRef("", sid)
	if _, err := w.pa["mia"].SetPainState(d.Pain.ID, pain.Resolved, "", pain.Evidence{Kind: "session", Ref: ref}, "", pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	evidence := func(who string) string {
		got, err := w.pa[who].PainDetailView(d.Pain.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range got.Events {
			if e.EvidenceKind == "session" {
				return e.EvidenceRef
			}
		}
		return ""
	}
	if evidence("gus") != "" {
		t.Error("a guest reads the session reference")
	}
	if evidence("rex") != "" {
		t.Error("a member without transcript access reads the session reference")
	}
}
