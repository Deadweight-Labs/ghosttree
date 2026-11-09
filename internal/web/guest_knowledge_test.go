package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// projectOnlyEnv is the shell world (gina is a guest, anna a member without
// review right, both in the organisation, like Gus and Lena on the demo) with
// knowledge of three kinds: the project's own, another project's, and a
// machine entry without project that belongs to alice's machine.
type projectOnlyEnv struct {
	shellEnv
	ownTrusted, ownStaged, other, mch int64
}

func newProjectOnlyEnv(t *testing.T) projectOnlyEnv {
	t.Helper()
	e := ovEnv(t)
	if err := e.St.ClaimMachine("alice-box", "person:1"); err != nil {
		t.Fatal(err)
	}
	env := projectOnlyEnv{shellEnv: e}
	env.ownTrusted = knInsert(t, e.St, store.Knowledge{Title: "Zebra trusted in shell", Confidence: "trusted"})
	env.ownStaged = knInsert(t, e.St, store.Knowledge{Title: "Zebra staged in shell", Confidence: "staged"})
	env.other = knInsert(t, e.St, store.Knowledge{Title: "Zebra in hidden", Confidence: "trusted", Scope: scope.Axes{Project: shellHiddenProject}})
	id, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Zebra on alice-box", Body: "disk", Person: "alice",
		Confidence: "trusted", Scope: scope.Axes{Machine: "alice-box"}})
	if err != nil {
		t.Fatal(err)
	}
	env.mch = id
	return env
}

// Befund 1: die Wissen-Liste eines Projekts zeigt Gast und Mitglied, die nur im
// Projekt eingeladen sind, genau das Wissen des Projekts, das sie lesen dürfen:
// nie fremde Maschinen-Einträge oder andere Projekte.
func TestKnowledgeListOfAProjectForProjectOnlyGuestAndMember(t *testing.T) {
	e := newProjectOnlyEnv(t)
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		_, page := fetchPage(t, c, e.Base+"/ui/x/shell/knowledge")
		if !strings.Contains(page, "Zebra trusted in shell") {
			t.Errorf("%s: the project's own entry is missing", name)
		}
		for _, foreign := range []string{"Zebra on alice-box", "Zebra in hidden"} {
			if strings.Contains(page, foreign) {
				t.Errorf("%s sees %q in the project's list", name, foreign)
			}
		}
		staged := strings.Contains(page, "Zebra staged in shell")
		if name == "guest" && staged {
			t.Error("a guest sees an entry waiting for review")
		}
		if name == "member" && !staged {
			t.Error("a member does not see the entry waiting for review")
		}
		// Detail: der Maschinen-Eintrag ist für beide unbekannt.
		if code, _ := fetchPage(t, c, e.Base+"/ui/knowledge/"+strconv.FormatInt(e.mch, 10)); code != http.StatusNotFound {
			t.Errorf("%s reads alice's machine entry: %d", name, code)
		}
	}
	// Die Seite ohne Projekt (alle Projekte) zeigt ihnen ebenfalls keine fremde Maschine.
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		_, all := fetchPage(t, c, e.Base+"/ui/knowledge?q=zebra")
		if strings.Contains(all, "Zebra on alice-box") || strings.Contains(all, "Zebra in hidden") {
			t.Errorf("%s sees foreign knowledge on the global page", name)
		}
	}
}

// Befund 3: Übernehmen und Ablehnen braucht das Prüfrecht. Ein Gast und ein
// Mitglied ohne Prüfrecht bekommen die Seite nicht und können nichts freigeben.
func TestReviewNeedsTheReviewRight(t *testing.T) {
	e := newProjectOnlyEnv(t)
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		_, page := fetchPage(t, c, e.Base+"/ui/x/shell/review")
		if strings.Contains(page, "/ui/review/"+strconv.FormatInt(e.ownStaged, 10)+"/approve") {
			t.Errorf("%s gets the approve button", name)
		}
		for _, verdict := range []string{"approve", "reject"} {
			for _, id := range []int64{e.ownStaged, e.ownTrusted} {
				resp := postForm(t, e.shellEnv, c, "/ui/review/"+strconv.FormatInt(id, 10)+"/"+verdict, url.Values{})
				if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
					t.Errorf("%s %s #%d = %d", name, verdict, id, resp.StatusCode)
				}
			}
		}
	}
	for _, id := range []int64{e.ownStaged, e.ownTrusted} {
		k, err := e.St.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if k.ConfirmedBy != "" || k.Status != "active" || (id == e.ownStaged && k.Confidence != "staged") {
			t.Errorf("#%d was changed: %+v", id, k)
		}
	}
}

// Robin, 2026-11-09: Gäste lesen, sie schreiben nicht; Wissen freigeben braucht
// das Prüfrecht. Beides gilt auch ohne GHOSTTREE_ENFORCE_ACCESS (Log-Modus),
// denn auf so einer Instanz fand die UI-Durchsicht die Lücken.
func TestGuestWritesAndUnreviewedApprovalsAreRefusedInLogMode(t *testing.T) {
	e := newProjectOnlyEnv(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: false})
	// Wissen: weder Gast noch Mitglied ohne Prüfrecht geben frei, der Gast lehnt auch nicht ab.
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		resp := postForm(t, e.shellEnv, c, "/ui/review/"+strconv.FormatInt(e.ownStaged, 10)+"/approve", url.Values{})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s approves in log mode: %d", name, resp.StatusCode)
		}
		_, review := fetchPage(t, c, e.Base+"/ui/x/shell/review")
		if approveEnabled(review, e.ownStaged) {
			t.Errorf("%s gets an active approve button in log mode", name)
		}
	}
	if resp := postForm(t, e.shellEnv, e.Guest, "/ui/review/"+strconv.FormatInt(e.ownTrusted, 10)+"/reject", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest rejects in log mode: %d", resp.StatusCode)
	}
	for _, id := range []int64{e.ownStaged, e.ownTrusted} {
		if k, _ := e.St.KnowledgeByID(id); k.ConfirmedBy != "" || k.Status != "active" || (id == e.ownStaged && k.Confidence != "staged") {
			t.Errorf("#%d was changed in log mode: %+v", id, k)
		}
	}
	_, item := fetchPage(t, e.Guest, e.Base+"/ui/knowledge/"+strconv.FormatInt(e.ownTrusted, 10))
	if strings.Contains(item, "?edit=1") {
		t.Error("a guest gets the edit button in log mode")
	}
	// Ein Prüfer gibt im Log-Modus weiter frei.
	if resp := postForm(t, e.shellEnv, e.Reviewer, "/ui/review/"+strconv.FormatInt(e.ownStaged, 10)+"/approve", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("reviewer approves in log mode: %d", resp.StatusCode)
	}

	// Aufgaben: auch die eigene meldet ein Gast nicht, und er bekommt keinen Knopf dafür.
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	own := tpTask(t, e.shellEnv, shellProject, req.Request.ID, humantask.CreateInput{Title: "For gina", Assignee: "gina"})
	if resp := tpPost(t, e.shellEnv, e.Guest, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(own.ID, 10)}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest marks a task done in log mode: %d", resp.StatusCode)
	}
	if got, _ := e.St.TaskByID(own.ID); got.State != humantask.Open {
		t.Errorf("task changed: %s", got.State)
	}
	_, task := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/tasks/"+strconv.FormatInt(own.ID, 10))
	if strings.Contains(task, `action="/ui/tasks/done"`) {
		t.Error("a guest gets the done form in log mode")
	}
	// Ein Mitglied meldet seine Aufgabe weiter.
	forAnna := tpTask(t, e.shellEnv, shellProject, req.Request.ID, humantask.CreateInput{Title: "For anna", Assignee: "anna"})
	if resp := tpPost(t, e.shellEnv, e.Member, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(forAnna.ID, 10)}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("member marks own task: %d", resp.StatusCode)
	}

	// Abläufe: kein Anlegen, kein Knopf.
	if resp := postForm(t, e.shellEnv, e.Guest, "/ui/runbooks/write", rbForm("guest-thing")); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Errorf("guest creates a runbook in log mode: %d", resp.StatusCode)
	}
	if _, err := e.St.RunbookRefByScope(shellProject, "", "guest-thing", 1); err == nil {
		t.Error("the guest's runbook exists")
	}
	if _, list := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/runbooks"); strings.Contains(list, "/runbooks/new") {
		t.Error("a guest gets the button for a new runbook in log mode")
	}
}

// approveEnabled says whether a page offers an active approve button for the
// entry (a disabled one next to "reject" does not count).
func approveEnabled(page string, id int64) bool {
	action := `action="/ui/review/` + strconv.FormatInt(id, 10) + `/approve"`
	for rest := page; ; {
		i := strings.Index(rest, action)
		if i < 0 {
			return false
		}
		rest = rest[i+len(action):]
		end := strings.Index(rest, "</button>")
		if end < 0 {
			return false
		}
		if !strings.Contains(rest[:end], "disabled") {
			return true
		}
	}
}
