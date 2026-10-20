package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func openRequest(id int64, title, createdAt, lastActivity string, criteria ...string) requestdomain.Detail {
	d := requestdomain.Detail{Request: requestdomain.Request{ID: id, Type: "feature", Title: title, State: "open", Scope: scope.Axes{Project: gardenProject}, CreatedAt: createdAt, UpdatedAt: lastActivity}}
	for i, c := range criteria {
		d.Criteria = append(d.Criteria, requestdomain.Criterion{ID: id*10 + int64(i), RequestID: id, Number: i + 1, Description: c, State: "open", CreatedAt: createdAt, UpdatedAt: createdAt})
	}
	return d
}

var entryIDRe = regexp.MustCompile(`=== EINTRAG #(\d+)`)

// jobsRunner answers every kind of session as a good session would: it judges the
// entries as holding, and for the ledger and the description package it proves one
// criterion with a real commit, tries to prove another with invented evidence, and
// describes the one file.
func jobsRunner(head string) *gardenFakeRunner {
	return &gardenFakeRunner{text: func(prompt string) string {
		switch {
		case strings.Contains(prompt, "=== AUFTRAG #"):
			return fmt.Sprintf(`{"checked":[1],"criteria":[`+
				`{"request":1,"criterion":10,"reason":"Der Commit führt es ein.","evidence":[{"kind":"commit","ref":%q}]},`+
				`{"request":1,"criterion":11,"reason":"Das steht nirgends im Code.","evidence":[{"kind":"commit","ref":"deadbeefdeadbeef"},{"kind":"test","file":"cmd/x_test.go","test":"TestInvented"},{"kind":"file","file":"cmd/nope.go"}]}]}`, head[:12])
		case strings.Contains(prompt, "=== PFAD "):
			return `{"ghosts":[{"path":"cmd/x.go","verdict":"describe","description":"Die Datei hält den Einstieg des Befehls. Invariante: sie hat keine Seiteneffekte beim Laden.","reason":"noch nie beschrieben"}]}`
		}
		var vs []string
		for _, m := range entryIDRe.FindAllStringSubmatch(prompt, -1) {
			vs = append(vs, fmt.Sprintf(`{"id":%s,"verdict":"holds","reason":"ok"}`, m[1]))
		}
		return `{"verdicts":[` + strings.Join(vs, ",") + `]}`
	}}
}

func jobsOpts(fr *gardenFakeRunner) gardenOpts {
	return gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 3, Propose: true, Sessions: 4, PerSession: 30, Turns: 30,
		Runner: fr, Requests: 8, Ghosts: 8, IdleDays: 30, MinAge: 14 * 24 * time.Hour}
}

func jobsStore() *gardenFakeStore {
	// Request 1 has the same number as the knowledge entry 1: the two must not meet.
	return &gardenFakeStore{entries: gardenEntries(), requests: []requestdomain.Detail{
		openRequest(1, "Auftrag eins", "2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z", "Erstes Kriterium", "Zweites Kriterium"),
	}}
}

func TestGardenProposesCriteriaAndDescriptionsAsStagedNotesAfterCheckingThem(t *testing.T) {
	repo := gardenRepo(t)
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	st := jobsStore()
	fr := jobsRunner(head)
	state, err := openFileState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	opts := jobsOpts(fr)
	opts.State = state
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	// One session each for the ledger and the description, one for the knowledge entries.
	if fr.calls != 3 {
		t.Fatalf("%d sessions, want 3\n%s", fr.calls, out.String())
	}
	byRef := map[string]store.Knowledge{}
	for _, k := range st.written {
		if k.Harness != "gardener" || k.Confidence != "staged" || k.Status != "active" || k.Type != "note" || k.Origin == "human" {
			t.Errorf("a note is not marked as the gardener's and staged: %+v", k)
		}
		byRef[k.SessionRef] = k
	}
	if len(st.written) != 2 || byRef["gardener:criterion:1:10"].Title == "" || byRef["gardener:ghost:cmd/x.go"].Title == "" {
		t.Fatalf("wrote %d notes: %+v", len(st.written), st.written)
	}
	if k := byRef["gardener:criterion:1:10"]; !strings.Contains(k.Body, "Beleg: commit "+head[:12]) || k.Title != "[Gärtner] Erfüllt? #1 Kriterium 1: Erstes Kriterium" {
		t.Errorf("criterion note:\n%s\n%s", k.Title, k.Body)
	}
	if k := byRef["gardener:ghost:cmd/x.go"]; !strings.Contains(k.Body, "Invariante: sie hat keine Seiteneffekte") || !strings.Contains(k.Body, "Bisher: keine") {
		t.Errorf("description note:\n%s", k.Body)
	}
	// The invented evidence proposes nothing, and the report names it.
	for _, want := range []string{"Kriterium 11 von #1: kein gültiger Beleg, nichts vorgeschlagen", "definiert keinen Test", "keine versionierte Datei", "kein Commit",
		"## Ledger: erfüllte Kriterien (Vorschläge)", "## Beschreibungen (Ghosts)", "1 criteria proposed as met, 1 descriptions proposed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	// Nothing was settled, closed or described by the run.
	d, _ := st.GetRequest(1)
	if d.Request.State != "open" || d.Criteria[0].State != "open" || d.Criteria[1].State != "open" {
		t.Errorf("the request was touched: %+v", d)
	}

	// The memory keeps the three jobs apart, although request 1 and entry 1 share a number.
	for name, want := range map[string]int{gardenProject: 2, garden.LedgerState(gardenProject): 1, garden.GhostState(gardenProject): 1} {
		got, err := state.GardenChecks(name)
		if err != nil || len(got) != want {
			t.Errorf("memory %q holds %d looks (%v), want %d", name, len(got), err, want)
		}
	}
	if got, _ := state.GardenChecks(garden.LedgerState(gardenProject)); got[1].EntryHash == "" || got[1].Commit != head {
		t.Errorf("look at the request = %+v", got[1])
	}

	// A second run asks nothing again and files nothing again.
	st.entries = append(st.entries, st.written...)
	st.written = nil
	before := fr.calls
	out.Reset()
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("second run exit %d:\n%s", code, out.String())
	}
	if fr.calls != before || len(st.written) != 0 {
		t.Errorf("the second run made %d sessions and wrote %d notes:\n%s", fr.calls-before, len(st.written), out.String())
	}
}

// A person said no to a proposal: the same evidence for the same criterion, and the
// same version of the file, are not asked again, even without the memory of earlier runs.
func TestGardenDoesNotProposeAgainWhatAPersonRejected(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	opts := jobsOpts(jobsRunner(head))
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 || len(st.written) != 2 {
		t.Fatalf("first run: exit %d, %d notes\n%s", code, len(st.written), out.String())
	}
	for _, k := range st.written {
		k.Status = "deprecated"
		st.entries = append(st.entries, k)
	}
	st.written = nil
	fr := jobsRunner(head)
	opts.Runner = fr
	out.Reset()
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if len(st.written) != 0 {
		t.Errorf("rejected proposals were made again: %+v", st.written)
	}
}

func TestGardenListsIdleRequestsFromDataAloneInADryRunAndInApiMode(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	st := jobsStore()
	st.requests = append(st.requests,
		openRequest(5, "Seit Wochen still", "2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z", "Ein Kriterium"),
		openRequest(6, "Gerade bearbeitet", "2026-07-01T00:00:00Z", "2026-10-19T00:00:00Z", "Ein Kriterium"),
	)
	// A dry run in agent mode: no session, no model call, and the list is there.
	var out bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 3, DryRun: true, Sessions: 4, PerSession: 30, Turns: 30,
		Requests: 8, Ghosts: 8, IdleDays: 30, Now: func() time.Time { return now }}
	if code := runGardenAgent(st, gardenRepo(t), gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	for _, want := range []string{"## Liegengeblieben", "- #5 Seit Wochen still: seit 111 Tagen, 1 offene Kriterien"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Gerade bearbeitet") {
		t.Error("a request with recent activity is listed")
	}
	// The sessions of the new jobs are in the estimate with the prompt they would get.
	for _, want := range []string{"Ledger 1/1 (3 Aufträge)", "Beschreibungen 1/1 (1 Pfade, cmd)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the estimate lacks %q:\n%s", want, out.String())
		}
	}

	// The api mode has no sessions for the new jobs, and the list all the same.
	out.Reset()
	m := &gardenFakeModel{}
	opts = gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 1, IdleDays: 30, Now: func() time.Time { return now }}
	if code := runGardenCheck(st, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out); code != 0 {
		t.Fatalf("api mode exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "- #5 Seit Wochen still") {
		t.Errorf("api mode has no list of idle requests:\n%s", out.String())
	}
	// Off by --idle-days 0: no section, and the requests are not even read.
	st.requestsErr = fmt.Errorf("must not be read")
	out.Reset()
	opts.IdleDays = 0
	if code := runGardenCheck(st, gardenRepo(t), gardenProject, opts, &gardenFakeModel{}, gardenPrice, "", &out); code != 0 || strings.Contains(out.String(), "Liegengeblieben") || strings.Contains(out.String(), "must not") {
		t.Errorf("--idle-days 0: exit %d\n%s", code, out.String())
	}
}

// What cannot be read costs the job, not the run: the knowledge sessions go on.
func TestGardenGoesOnWithTheKnowledgeWhenTheRequestsCannotBeRead(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	st.requestsErr = fmt.Errorf("503 unavailable")
	fr := jobsRunner(head)
	opts := jobsOpts(fr)
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "cannot read requests: 503 unavailable") {
		t.Errorf("the failure is not said:\n%s", out.String())
	}
	// The description session and the knowledge session ran; no ledger session.
	if fr.calls != 2 {
		t.Errorf("%d sessions, want the description and the knowledge session", fr.calls)
	}
	for _, k := range st.written {
		if strings.HasPrefix(k.SessionRef, "gardener:criterion:") {
			t.Errorf("a criterion note without requests: %+v", k)
		}
	}
}

func TestGardenTurnsTheNewJobsOffAndRejectsNonsense(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	fr := jobsRunner(head)
	opts := jobsOpts(fr)
	opts.Requests, opts.Ghosts, opts.IdleDays = 0, 0, 0
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 || fr.calls != 1 || len(st.written) != 0 {
		t.Errorf("with both jobs off: exit %d, %d sessions, %d notes\n%s", code, fr.calls, len(st.written), out.String())
	}
	for _, args := range [][]string{{"--requests", "-1"}, {"--requests", "41"}, {"--ghosts", "-1"}, {"--ghosts", "41"}, {"--idle-days", "-3"}, {"--requests", "x"}} {
		out.Reset()
		if code := cmdGarden(append([]string{"check-knowledge", "--dry-run"}, args...), &out); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
	for _, want := range []string{"--requests 8", "--ghosts 8", "--idle-days 30", "Liegengeblieben"} {
		if !strings.Contains(gardenUsage, want) {
			t.Errorf("the help of ctx garden lacks %q", want)
		}
	}
}

// With no knowledge entries at all the other jobs still run: a project that only
// has requests and files gets its ledger and its descriptions looked after.
func TestGardenRunsTheNewJobsWithoutAnyKnowledgeEntries(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	st.entries = nil
	fr := jobsRunner(head)
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, jobsOpts(fr), gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if fr.calls != 2 || len(st.written) != 2 {
		t.Errorf("%d sessions, %d notes, want the ledger and the description session and their two notes\n%s", fr.calls, len(st.written), out.String())
	}
}

// The jobs read through the real client of the server: nothing here may need a
// method it does not have, and nothing in it writes a request or a ghost.
var _ gardenReader = (*client.Client)(nil)
