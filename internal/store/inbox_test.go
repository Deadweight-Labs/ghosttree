package store

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

type inboxWorld struct {
	*painWorld
	room                             string
	author, recipient                CoordAccess
	open, done, confirmed, dropped   humantask.Task
	decOpen, decDecided, decDeferred idea.Decision
}

func newInboxWorld(t *testing.T) *inboxWorld {
	t.Helper()
	w := &inboxWorld{painWorld: newPainWorld(t)}
	req := w.request(t, "Auftrag mit Aufgaben")
	mia := humantask.Actor{AccountID: 3, Person: "mia"}
	mk := func(title string) humantask.Task {
		task, err := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: req, Kind: "action", Title: title, Everyone: true}, mia)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	w.open, w.done, w.confirmed, w.dropped = mk("offen"), mk("erledigt"), mk("bestätigt"), mk("verworfen")
	if _, err := w.pa["mia"].MarkTasksDone([]int64{w.done.ID, w.confirmed.ID}, "gemacht", mia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].ConfirmHumanTask(w.confirmed.ID, mia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].DropHumanTask(w.dropped.ID, "nicht mehr nötig", mia); err != nil {
		t.Fatal(err)
	}
	i := w.workshopIdea(t, "Idee mit Fragen")
	human := func(q string) idea.Decision {
		return w.openOn(t, "mia", idea.SubjectIdea, i.ID, q, w.actor("mia"), withAspects(idea.AspectCost))
	}
	w.decOpen, w.decDecided, w.decDeferred = human("offene Frage"), human("beantwortete Frage"), human("vertagte Frage")
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: w.decDecided.ID, Outcome: idea.DecisionDecided, Answer: "ja", Reason: "weil", Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: w.decDeferred.ID, Outcome: idea.DecisionDeferred, Trigger: "nach dem Release", Reason: "später", Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	group, err := w.st.CreateCoordGroup(GroupInput{Label: "release", Creator: "person:1", Members: []string{"person:1", "person:3", "person:4"}})
	if err != nil {
		t.Fatal(err)
	}
	w.room = group.Key
	w.author = w.st.CoordinationFor(Principal{ID: "person:1", Label: "robin"}, "")
	w.recipient = w.st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "")
	for i, intent := range []string{IntentQuestion, IntentBlocker} {
		if _, err := w.author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: w.room, ClientID: []string{"q", "b"}[i],
			Body: []string{"Können wir ausliefern?", "Der Rollout hängt"}[i], Intent: intent, Mentions: []string{"person:3"}}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := w.recipient.Attention()
	if err != nil || len(items) != 2 {
		t.Fatalf("attention: %+v %v", items, err)
	}
	for _, item := range items {
		if item.Reason == AttentionQuestion {
			if err := w.recipient.ResolveAttention(item.ID, AttentionActionAnswer); err != nil {
				t.Fatal(err)
			}
		}
	}
	return w
}

func TestInboxShowsOpenAndFinishedEntriesOfAllThreeSources(t *testing.T) {
	w := newInboxWorld(t)
	mia := w.pa["mia"]

	open, err := mia.Inbox(w.recipient, InboxFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 3 {
		t.Fatalf("open entries (%d): %+v", len(open), open)
	}
	kinds := map[string]bool{}
	for _, e := range open {
		if e.State != InboxOpen || e.How != "" || e.At == "" {
			t.Errorf("an open entry carries a finish: %+v", e)
		}
		kinds[e.Kind+":"+e.Ref] = true
		if e.Kind == InboxAttention {
			kinds["attention"] = e.Reason == AttentionBlocker && strings.HasPrefix(e.Ref, "attention:")
		}
	}
	if !kinds["task:"+w.open.HumanID()] || !kinds["decision:DEC-"+strconv.FormatInt(w.decOpen.ID, 10)] || !kinds["attention"] {
		t.Fatalf("open entries: %v", kinds)
	}

	done, err := mia.Inbox(w.recipient, InboxFilter{State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	hows := map[string]string{}
	for _, e := range done {
		if e.State != InboxDone || e.At == "" {
			t.Errorf("finished entry: %+v", e)
		}
		hows[e.Ref] = e.How
	}
	wantHows := map[string]string{
		w.done.HumanID(): "done", w.confirmed.HumanID(): "confirmed", w.dropped.HumanID(): "dropped",
		"DEC-" + strconv.FormatInt(w.decDecided.ID, 10): "decided", "DEC-" + strconv.FormatInt(w.decDeferred.ID, 10): "deferred",
	}
	for ref, how := range wantHows {
		if hows[ref] != how {
			t.Errorf("%s finished as %q, want %q (all: %v)", ref, hows[ref], how, hows)
		}
	}
	var resolved int
	for _, e := range done {
		if e.Kind == InboxAttention && e.How == AttentionResolved && e.Title == "Können wir ausliefern?" {
			resolved++
		}
	}
	if resolved != 1 || len(done) != len(wantHows)+1 {
		t.Errorf("done entries (%d): %+v", len(done), done)
	}
	// Nichts davon ist offen.
	for _, e := range open {
		if e.Ref == w.done.HumanID() || e.Ref == w.dropped.HumanID() {
			t.Errorf("a finished task is still open: %+v", e)
		}
	}

	all, err := mia.Inbox(w.recipient, InboxFilter{State: "all"})
	if err != nil || len(all) != len(open)+len(done) {
		t.Fatalf("all: %d %v", len(all), err)
	}
	for i, e := range all[:len(open)] {
		if e.State != InboxOpen {
			t.Errorf("entry %d of all is not open: %+v", i, e)
		}
	}
	if _, err := mia.Inbox(w.recipient, InboxFilter{State: "weg"}); err == nil {
		t.Error("an unknown state was accepted")
	}
	// Die Fassung ohne Fragen (ein Konto ohne Räume) trägt Aufgaben und Entscheidungen bei.
	if noRooms, err := mia.Inbox(CoordAccess{}, InboxFilter{State: "all"}); err != nil || len(noRooms) != len(all)-2 {
		t.Errorf("without rooms: %d entries, want %d (%v)", len(noRooms), len(all)-2, err)
	}
}

// Erledigtes bleibt in der Datenbank; die Liste zeigt ein Fenster davon.
func TestInboxDoneWindowLimitsTheViewNotTheData(t *testing.T) {
	w := newInboxWorld(t)
	mia := w.pa["mia"]
	later := time.Now().Add(45 * 24 * time.Hour)
	done, err := mia.Inbox(w.recipient, InboxFilter{State: "done", Now: later})
	if err != nil || len(done) != 0 {
		t.Fatalf("done after 45 days with the default window: %d %v", len(done), err)
	}
	if done, err = mia.Inbox(w.recipient, InboxFilter{State: "done", Now: later, Days: 90}); err != nil || len(done) != 6 {
		t.Fatalf("done after 45 days with 90 days: %d %v", len(done), err)
	}
	if done, err = mia.Inbox(w.recipient, InboxFilter{State: "done", Now: later, Days: 100000}); err != nil || len(done) != 6 {
		t.Fatalf("a window beyond the maximum: %d %v", len(done), err)
	}
	// Offene kennen kein Fenster.
	if open, err := mia.Inbox(w.recipient, InboxFilter{Now: later}); err != nil || len(open) != 3 {
		t.Fatalf("open after 45 days: %d %v", len(open), err)
	}
	// Die Zeilen sind noch da.
	var tasks, decisions, attention int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM human_tasks WHERE state IN ('done','confirmed','dropped')`).Scan(&tasks)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM open_decisions WHERE state<>'open'`).Scan(&decisions)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM coord_attention WHERE state<>'open'`).Scan(&attention)
	if tasks != 3 || decisions != 2 || attention != 1 {
		t.Errorf("rows kept: tasks %d decisions %d attention %d", tasks, decisions, attention)
	}
}

func TestInboxMineAndRights(t *testing.T) {
	w := newInboxWorld(t)
	// Mine: rex (Member) hat nichts erledigt; die offene Aufgabe gehört niemandem, also auch ihm.
	rexDone, err := w.pa["rex"].Inbox(CoordAccess{}, InboxFilter{State: "done", Mine: true})
	if err != nil || len(rexDone) != 2 { // nur die beiden erledigten Entscheidungen; Aufgaben hat er nicht erledigt
		t.Fatalf("rex done mine: %+v %v", rexDone, err)
	}
	for _, e := range rexDone {
		if e.Kind == InboxTask {
			t.Errorf("rex finished no task: %+v", e)
		}
	}
	if rexOpen, _ := w.pa["rex"].Inbox(CoordAccess{}, InboxFilter{Mine: true}); len(rexOpen) != 2 {
		t.Errorf("rex open mine: %+v", rexOpen)
	}
	miaDone, _ := w.pa["mia"].Inbox(CoordAccess{}, InboxFilter{State: "done", Mine: true})
	tasks := 0
	for _, e := range miaDone {
		if e.Kind == InboxTask {
			tasks++
		}
	}
	if tasks != 2 { // erledigt und bestätigt; die verworfene hat niemand erledigt
		t.Errorf("mia done mine tasks: %d (%+v)", tasks, miaDone)
	}

	// Fremder und Gast: der Fremde sieht nichts, auch nicht mit Projekt; für ein
	// Projekt, das es nicht gibt, kommt dieselbe Antwort.
	for _, f := range []InboxFilter{{State: "all"}, {State: "all", Project: roleProject}, {State: "all", Project: "github.com/nobody/nothing"}} {
		got, err := w.pa["nora"].Inbox(CoordAccess{}, f)
		if err != nil || len(got) != 0 {
			t.Errorf("stranger with %+v: %+v %v", f, got, err)
		}
	}
	guest, err := w.pa["gus"].Inbox(CoordAccess{}, InboxFilter{State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range guest {
		if e.Kind == InboxTask {
			t.Errorf("a guest sees a task assigned to nobody: %+v", e)
		}
	}
	// Ehemaliges Mitglied: ohne Rolle bleibt nichts außer dem, was in seinen eigenen Räumen steht.
	if err := w.st.RemoveProjectRole("person:1", roleProject, "person:3", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := w.st.RemoveOrgMember("person:1", w.orgID(t), "person:3"); err != nil {
		t.Fatal(err)
	}
	former, err := w.st.Access(Principal{ID: "person:3", Label: "mia"}).Inbox(CoordAccess{}, InboxFilter{State: "all", Project: roleProject})
	if err != nil || len(former) != 0 {
		t.Errorf("former member: %+v %v", former, err)
	}
}

func (w *painWorld) orgID(t *testing.T) int64 {
	t.Helper()
	p, _ := w.st.ProjectByRemote(roleProject)
	return p.OrgID
}

// Eine Frage, die nicht an den Betrachter ging, kommt nicht in seinen
// Posteingang, auch nicht als Erledigte; der Autor sieht seine ausgehende Frage nicht als Eingang.
func TestInboxOnlyHoldsWhatWasAddressedToTheViewer(t *testing.T) {
	w := newInboxWorld(t)
	robin, err := w.pa["robin"].Inbox(w.author, InboxFilter{State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range robin {
		if e.Kind == InboxAttention {
			t.Errorf("the author sees his own outgoing question as inbox: %+v", e)
		}
	}
	// rex ist Mitglied der Gruppe, aber nicht angesprochen worden.
	rex := w.st.CoordinationFor(Principal{ID: "person:4", Label: "rex"}, "")
	got, err := w.pa["rex"].Inbox(rex, InboxFilter{State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Kind == InboxAttention {
			t.Errorf("a member that was not addressed sees %+v", e)
		}
	}
	// Mit Projektfilter: der Gruppenraum gehört zu keinem Projekt.
	if got, _ := w.pa["mia"].Inbox(w.recipient, InboxFilter{State: "all", Project: roleProject}); func() bool {
		for _, e := range got {
			if e.Kind == InboxAttention {
				return true
			}
		}
		return false
	}() {
		t.Error("a group room question shows under a project filter")
	}
}
