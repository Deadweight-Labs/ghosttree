package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func (w *painWorld) task(t *testing.T, who string, in humantask.CreateInput) humantask.Task {
	t.Helper()
	acct, _ := accountNumericID(w.pa[who].principal.ID)
	task, err := w.pa[who].CreateHumanTask(in, humantask.Actor{AccountID: acct, Person: who})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func TestHumanTaskCreateRules(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Zahlung")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "secret", Title: "Key anlegen", Location: "Agent Vault: stripe/prod", CheckHint: "Eintrag existiert"})
	if task.State != humantask.Open || task.RequestID != rid || task.Project != roleProject {
		t.Fatalf("task: %+v", task)
	}
	pa := w.pa["mia"]
	cases := []struct {
		name string
		in   humantask.CreateInput
		want string
	}{
		{"secret without location", humantask.CreateInput{RequestID: rid, Kind: "secret", Title: "x"}, "location_required"},
		{"file without location", humantask.CreateInput{RequestID: rid, Kind: "file", Title: "x", Location: "  "}, "location_required"},
		{"bad kind", humantask.CreateInput{RequestID: rid, Kind: "wish", Title: "x"}, "invalid_kind"},
		{"no title", humantask.CreateInput{RequestID: rid, Kind: "input", Title: " "}, "title_required"},
		{"no request", humantask.CreateInput{Kind: "input", Title: "x"}, "request_required"},
		{"unknown request", humantask.CreateInput{RequestID: 99999, Kind: "input", Title: "x"}, "link_target_not_found"},
		{"foreign work", humantask.CreateInput{RequestID: rid, WorkID: 99999, Kind: "input", Title: "x"}, "work_invalid"},
		{"assignee outside the project", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "x", Assignee: "nora"}, "assignee_not_member"},
		{"assignee unknown", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "x", Assignee: "niemand"}, "assignee_not_member"},
		{"other project", humantask.CreateInput{RequestID: rid, Project: "github.com/dw/q", Kind: "input", Title: "x"}, "link_target_not_found"},
	}
	for _, c := range cases {
		if _, err := pa.CreateHumanTask(c.in, humantask.Actor{Person: "mia"}); ruleCode(err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	if _, err := w.pa["gus"].CreateHumanTask(humantask.CreateInput{RequestID: rid, Kind: "input", Title: "x"}, humantask.Actor{}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("guest create: %v", err)
	}
	// Ein Gast als Zugewiesener ist erlaubt.
	if _, err := pa.CreateHumanTask(humantask.CreateInput{RequestID: rid, Kind: "input", Title: "für Gus", Assignee: "gus"}, humantask.Actor{Person: "mia"}); err != nil {
		t.Errorf("guest as assignee: %v", err)
	}
	// Ein abgeschlossener Auftrag nimmt keine Aufgabe mehr.
	w.st.DB().Exec(`UPDATE requests SET state='done' WHERE id=?`, rid)
	if _, err := pa.CreateHumanTask(humantask.CreateInput{RequestID: rid, Kind: "input", Title: "x"}, humantask.Actor{}); ruleCode(err) != "request_not_open" {
		t.Errorf("closed request: %v", err)
	}
}

func TestHumanTaskBindsToWorkAndToTheSessionsActiveWork(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Migration")
	sid, err := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s-mia", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := w.st.StartRequestWork(rid, sid, "primary", "mia")
	if err != nil {
		t.Fatal(err)
	}
	byWork := w.task(t, "mia", humantask.CreateInput{WorkID: work.ID, Kind: "action", Title: "per Work"})
	bySession := w.task(t, "mia", humantask.CreateInput{SessionID: sid, Kind: "action", Title: "per Session"})
	if byWork.RequestID != rid || bySession.RequestID != rid || bySession.WorkID != work.ID {
		t.Errorf("bindings: %+v %+v", byWork, bySession)
	}
	if _, err := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: rid + 1, WorkID: work.ID, Kind: "action", Title: "x"}, humantask.Actor{}); ruleCode(err) != "work_invalid" {
		t.Errorf("mismatched work: %v", err)
	}
	if _, err := w.st.FinishRequestWork(work.ID, "paused", "Schritt 4 fehlt", "mia"); err != nil {
		t.Fatal(err)
	}
	waiting, err := w.st.WaitingTasksFor(w.pa["mia"], roleProject, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range waiting.Open {
		found = found || task.Handoff == "Schritt 4 fehlt"
	}
	if !found {
		t.Errorf("handoff missing in %+v", waiting.Open)
	}
}

func TestHumanTaskLifecycleAndActivityOrder(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Ablauf")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "file", Title: "Dump", Location: "./db/schema.sql", CheckHint: "Datei existiert"})
	id := task.ID
	human := humantask.Actor{AccountID: 3, Person: "mia", Web: true}
	agent := humantask.Actor{AccountID: 3, Person: "mia", SessionID: 11}
	mia, lena := w.pa["mia"], w.pa["lena"]

	if _, err := mia.ConfirmHumanTask(id, agent); ruleCode(err) != "invalid_transition" {
		t.Errorf("confirm an open task: %v", err)
	}
	if done, err := mia.MarkTasksDone([]int64{id}, "liegt da", human); err != nil || done[0].DoneVia != "web" || done[0].DoneNote != "liegt da" {
		t.Fatalf("done: %+v %v", done, err)
	}
	if _, err := mia.MarkTasksDone([]int64{id}, "", human); ruleCode(err) != "invalid_transition" {
		t.Errorf("done twice: %v", err)
	}
	if _, err := mia.ReopenHumanTask(id, "  ", agent); ruleCode(err) != "reason_required" {
		t.Errorf("reopen without reason: %v", err)
	}
	if reopened, err := mia.ReopenHumanTask(id, "Datei ist leer", agent); err != nil || reopened.State != humantask.Open || reopened.DoneNote != "" {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	if done, err := mia.MarkTasksDone([]int64{id}, "jetzt vollständig", humantask.Actor{AccountID: 3, Person: "mia"}); err != nil || done[0].DoneVia != "token" {
		t.Fatalf("done via token: %+v %v", done, err)
	}
	if c, err := lena.ConfirmHumanTask(id, humantask.Actor{AccountID: 2, Person: "lena", SessionID: 12}); err != nil || c.State != humantask.Confirmed {
		t.Fatalf("confirm: %+v %v", c, err)
	}
	var kinds []string
	rows, _ := w.st.DB().Query(`SELECT kind FROM request_activity WHERE request_id=? AND kind LIKE 'task.%' ORDER BY id`, rid)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	want := "task.created task.done task.reopened task.done task.confirmed"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("activity %q, want %q", got, want)
	}
	if _, err := mia.DropHumanTask(id, "egal", agent); ruleCode(err) != "invalid_transition" {
		t.Errorf("drop a confirmed task: %v", err)
	}
	dropped := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "weg"})
	if _, err := mia.DropHumanTask(dropped.ID, "", agent); ruleCode(err) != "reason_required" {
		t.Errorf("drop without reason: %v", err)
	}
	if d, err := mia.DropHumanTask(dropped.ID, "nicht mehr nötig", agent); err != nil || d.State != humantask.Dropped {
		t.Fatalf("drop: %+v %v", d, err)
	}
	if r, err := mia.ReopenHumanTask(dropped.ID, "doch", agent); err != nil || r.State != humantask.Open {
		t.Errorf("reopen dropped: %+v %v", r, err)
	}
}

func TestMarkTasksDoneIsAllOrNothing(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Mehrere")
	a := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "a"})
	b := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "b", Assignee: "lena"})
	gus := w.pa["gus"]
	forGus := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "c", Assignee: "gus"})
	// Gus darf nur die ihm zugewiesene; die erste verbotene Id bricht alles ab und
	// antwortet wie eine unbekannte.
	if _, err := gus.MarkTasksDone([]int64{forGus.ID, a.ID}, "", humantask.Actor{AccountID: 5, Person: "gus", Web: true}); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("hidden id: %v", err)
	}
	if got, _ := w.st.TaskByID(forGus.ID); got.State != humantask.Open {
		t.Errorf("the allowed id was written despite the abort: %s", got.State)
	}
	if _, err := gus.MarkTasksDone([]int64{forGus.ID}, "erledigt", humantask.Actor{AccountID: 5, Person: "gus", Web: true}); err != nil {
		t.Errorf("assigned guest marks done: %v", err)
	}
	if _, err := gus.ConfirmHumanTask(forGus.ID, humantask.Actor{AccountID: 5}); err == nil {
		t.Errorf("a guest confirmed")
	}
	// Eine zugewiesene Aufgabe eines anderen Mitglieds darf ein Mitglied nicht erledigen.
	if _, err := w.pa["rex"].MarkTasksDone([]int64{a.ID, b.ID}, "", humantask.Actor{AccountID: 4, Web: true}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("member on a task assigned to someone else: %v", err)
	}
	if got, _ := w.st.TaskByID(a.ID); got.State != humantask.Open {
		t.Errorf("task a changed: %s", got.State)
	}
	if done, err := w.pa["mia"].MarkTasksDone([]int64{a.ID}, "", humantask.Actor{AccountID: 3, Web: true}); err != nil || len(done) != 1 {
		t.Errorf("unassigned task: %v", err)
	}
}

func TestHumanTaskVisibilityEverywhere(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Sicht")
	open := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "nur Mitglieder"})
	mine := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "für Gus", Assignee: "gus"})
	ids := func(list []humantask.Task) string {
		var out []string
		for _, t := range list {
			out = append(out, idRef("", t.ID))
		}
		return strings.Join(out, ",")
	}
	gus := w.pa["gus"]
	list, _ := w.st.ListHumanTasks(gus, TaskFilter{Project: roleProject, State: "all"})
	if ids(list) != idRef("", mine.ID) {
		t.Errorf("guest list: %s", ids(list))
	}
	if _, err := gus.GetHumanTask(open.ID); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("guest get unassigned: %v", err)
	}
	rd, _ := w.st.RequestByID(rid)
	view := gus.RequestDetailView(rd)
	if len(view.Tasks) != 1 || view.Tasks[0].ID != mine.ID {
		t.Errorf("guest request view tasks: %+v", view.Tasks)
	}
	for _, a := range view.Activity {
		if strings.HasPrefix(a.Kind, "task.") {
			t.Errorf("guest sees task activity: %+v", a)
		}
	}
	waiting, _ := w.st.WaitingTasksFor(gus, roleProject, 5)
	if len(waiting.Open) != 1 || waiting.Open[0].ID != mine.ID {
		t.Errorf("guest bootstrap: %+v", waiting)
	}
	if list, _ := w.st.ListHumanTasks(w.pa["mia"], TaskFilter{Project: roleProject, State: "all"}); len(list) != 2 {
		t.Errorf("member list: %d", len(list))
	}
	if list, _ := w.st.ListHumanTasks(w.pa["nora"], TaskFilter{Project: roleProject, State: "all"}); len(list) != 0 {
		t.Errorf("stranger list: %d", len(list))
	}
	mineOnly, _ := w.st.ListHumanTasks(w.pa["mia"], TaskFilter{Project: roleProject, Mine: true})
	if ids(mineOnly) != idRef("", open.ID) {
		t.Errorf("mine for a member: %s", ids(mineOnly))
	}
}

func TestHumanTaskRejectsSecretsAndWritesNothing(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Geheim")
	secret := "sk-" + strings.Repeat("a1", 12)
	count := func() (tasks, activity int) {
		w.st.DB().QueryRow(`SELECT COUNT(*) FROM human_tasks`).Scan(&tasks)
		w.st.DB().QueryRow(`SELECT COUNT(*) FROM request_activity WHERE kind LIKE 'task.%'`).Scan(&activity)
		return
	}
	for field, in := range map[string]humantask.CreateInput{
		"title":       {Kind: "input", Title: "key " + secret},
		"description": {Kind: "input", Title: "t", Description: secret},
		"location":    {Kind: "secret", Title: "t", Location: ".env OPENAI_KEY=" + secret},
		"check_hint":  {Kind: "input", Title: "t", CheckHint: secret},
	} {
		in.RequestID = rid
		_, err := w.pa["mia"].CreateHumanTask(in, humantask.Actor{Person: "mia"})
		if ruleCode(err) != "secret_detected" || !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %v", field, err)
		}
	}
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "input", Title: "ok"})
	tasks0, act0 := count()
	if _, err := w.pa["mia"].MarkTasksDone([]int64{task.ID}, "hier: "+secret, humantask.Actor{}); ruleCode(err) != "secret_detected" {
		t.Errorf("done note: %v", err)
	}
	if _, err := w.pa["mia"].DropHumanTask(task.ID, secret, humantask.Actor{}); ruleCode(err) != "secret_detected" {
		t.Errorf("reason: %v", err)
	}
	if tasks, act := count(); tasks != tasks0 || act != act0 {
		t.Errorf("a rejected write left rows behind: %d/%d -> %d/%d", tasks0, act0, tasks, act)
	}
	var leaked int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM human_tasks WHERE title||description||location||check_hint||done_note||state_note LIKE ?`, "%"+secret+"%").Scan(&leaked)
	if leaked != 0 {
		t.Error("the secret reached the database")
	}
}

func TestMatrixPainAndTask(t *testing.T) {
	owner, lead, member, guest, none := RoleInfo{Role: RoleOwner}, RoleInfo{Role: RoleLead}, RoleInfo{Role: RoleMember}, RoleInfo{Role: RoleGuest}, RoleInfo{}
	own, assigned, unassigned, other := Object{Own: true}, Object{Assigned: true}, Object{Unassigned: true}, Object{}
	rows := []struct {
		name string
		role RoleInfo
		res  Resource
		act  Action
		obj  Object
		want bool
	}{
		{"guest reads pain", guest, ResPain, ActRead, other, true},
		{"stranger reads pain", none, ResPain, ActRead, other, false},
		{"guest creates pain", guest, ResPain, ActCreate, other, false},
		{"member creates pain", member, ResPain, ActCreate, other, true},
		{"member sets pain state", member, ResPain, ActWork, other, true},
		{"guest sets pain state", guest, ResPain, ActWork, own, false},
		{"member edits own pain", member, ResPain, ActEdit, own, true},
		{"member edits foreign pain", member, ResPain, ActEdit, other, false},
		{"lead edits foreign pain", lead, ResPain, ActEdit, other, true},
		{"guest reads unassigned task", guest, ResTask, ActRead, unassigned, false},
		{"guest reads assigned task", guest, ResTask, ActRead, assigned, true},
		{"member reads task", member, ResTask, ActRead, other, true},
		{"stranger reads task", none, ResTask, ActRead, assigned, false},
		{"member creates task", member, ResTask, ActCreate, other, true},
		{"guest creates task", guest, ResTask, ActCreate, other, false},
		{"member marks an unassigned task", member, ResTask, ActWork, unassigned, true},
		{"member marks his own assignment", member, ResTask, ActWork, assigned, true},
		{"member marks a task assigned to another", member, ResTask, ActWork, other, false},
		{"lead marks any task", lead, ResTask, ActWork, other, true},
		{"owner marks any task", owner, ResTask, ActWork, other, true},
		{"guest marks an assigned task", guest, ResTask, ActWork, assigned, true},
		{"guest marks an unassigned task", guest, ResTask, ActWork, unassigned, false},
		{"guest confirms", guest, ResTask, ActEdit, assigned, false},
		{"member confirms own", member, ResTask, ActEdit, own, true},
		{"member confirms foreign", member, ResTask, ActEdit, other, false},
		{"lead confirms foreign", lead, ResTask, ActEdit, other, true},
	}
	for _, r := range rows {
		if got := MatrixAllows(r.role, r.res, r.act, r.obj); got != r.want {
			t.Errorf("%s: got %v, want %v", r.name, got, r.want)
		}
	}
}

// The same flows through the runtime writer: payloads are cloned and replayed on
// the writer, so a payload the cloner cannot own, or a read that sneaks into the
// writer path, only shows here.
func TestPainAndTasksThroughTheRuntimeWriter(t *testing.T) {
	s, err := OpenRuntime(t.TempDir()+"/rt.db", DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	org := mustOrg(t, s, "person:1", "Alpha", "alpha")
	code, _, _ := s.CreateInvitation("person:1", org.ID, "", OrgMember, 0)
	if _, err := s.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureProject("person:1", roleProject); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectRole("person:1", roleProject, "person:2", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	s.SetAccessMode(AccessMode{Enforce: true})
	robin := s.Access(Principal{ID: "person:1", Label: "robin"})
	lena := s.Access(Principal{ID: "person:2", Label: "lena"})
	rd, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "r", Scope: scope.Axes{Project: roleProject}, Person: "robin"}, Criteria: []string{"c"}, AuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	d, err := robin.CreatePain(CreatePainInput{Project: roleProject, Title: "p", Motivates: []pain.Target{{RequestID: rd.Request.ID}, {ExternalRef: "x"}}, Actor: pain.Actor{Person: "robin"}, AuthorID: 1})
	if err != nil || len(d.Motivates) != 2 {
		t.Fatalf("create pain: %+v %v", d, err)
	}
	if _, err := lena.SetPainState(d.Pain.ID, pain.Mitigated, "Rest", pain.Evidence{Kind: "commit", Ref: "abc"}, "", pain.Actor{AccountID: 2, Person: "lena"}); err != nil {
		t.Fatalf("state: %v", err)
	}
	title := "neu"
	if _, err := robin.CorrectPain(d.Pain.ID, &title, nil, "r", pain.Actor{Person: "robin"}); err != nil {
		t.Fatalf("correct: %v", err)
	}
	if err := robin.RemovePainLink(d.Motivates[1].ID, "weg", pain.Actor{Person: "robin"}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, err := robin.AddPainLink(d.Pain.ID, pain.Target{ExternalRef: "y"}, pain.Actor{}); err != nil {
		t.Fatalf("link: %v", err)
	}
	task, err := robin.CreateHumanTask(humantask.CreateInput{RequestID: rd.Request.ID, Kind: "file", Title: "t", Location: "./x", Assignee: "lena"}, humantask.Actor{AccountID: 1, Person: "robin"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := lena.MarkTasksDone([]int64{task.ID}, "ok", humantask.Actor{AccountID: 2, Person: "lena", Web: true}); err != nil {
		t.Fatalf("done: %v", err)
	}
	if _, err := robin.ReopenHumanTask(task.ID, "nochmal", humantask.Actor{AccountID: 1}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := lena.MarkTasksDone([]int64{task.ID}, "ok", humantask.Actor{AccountID: 2, Web: true}); err != nil {
		t.Fatalf("done again: %v", err)
	}
	if _, err := robin.ConfirmHumanTask(task.ID, humantask.Actor{AccountID: 1, SessionID: 3}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if w, err := s.WaitingTasksFor(robin, roleProject, 5); err != nil || len(w.Open)+len(w.Done) != 0 {
		t.Errorf("waiting after confirm: %+v %v", w, err)
	}
	if got, err := s.ListHumanTasks(robin, TaskFilter{Project: roleProject, State: "all"}); err != nil || len(got) != 1 || got[0].State != humantask.Confirmed {
		t.Errorf("list: %+v %v", got, err)
	}
}
