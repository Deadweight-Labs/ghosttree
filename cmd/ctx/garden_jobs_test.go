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
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
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
// describes the one file and its directory.
func jobsRunner(head string) *gardenFakeRunner {
	return &gardenFakeRunner{text: func(prompt string) string {
		switch {
		case strings.Contains(prompt, "=== AUFTRAG #"):
			return fmt.Sprintf(`{"checked":[1],"criteria":[`+
				`{"request":1,"criterion":10,"reason":"Der Commit führt es ein.","evidence":[{"kind":"commit","ref":%q}]},`+
				`{"request":1,"criterion":11,"reason":"Das steht nirgends im Code.","evidence":[{"kind":"commit","ref":"deadbeefdeadbeef"},{"kind":"test","file":"cmd/x_test.go","test":"TestInvented"},{"kind":"file","file":"cmd/nope.go"}]}]}`, head[:12])
		case strings.Contains(prompt, "=== PFAD "):
			return `{"ghosts":[{"path":"cmd/x.go","verdict":"describe","description":"Die Datei hält den Einstieg des Befehls. Invariante: sie hat keine Seiteneffekte beim Laden.","reason":"noch nie beschrieben"},` +
				`{"path":"cmd","verdict":"describe","description":"Das Verzeichnis hält die Befehle, je einer in einer Datei, ohne gemeinsamen Zustand.","reason":"neues Verzeichnis"}]}`
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

func TestGardenProposesCriteriaAndWritesDescriptionsAfterCheckingThem(t *testing.T) {
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
	if len(st.written) != 1 || byRef["gardener:criterion:1:10"].Title == "" {
		t.Fatalf("wrote %d notes: %+v", len(st.written), st.written)
	}
	if k := byRef["gardener:criterion:1:10"]; !strings.Contains(k.Body, "Beleg: commit "+head[:12]) || k.Title != "[Gärtner] Erfüllt? #1 Kriterium 1: Erstes Kriterium" {
		t.Errorf("criterion note:\n%s\n%s", k.Title, k.Body)
	}
	// The file the (root) merge brought and its directory are described by the
	// gardener itself, anchored on that commit.
	if len(st.described) != 2 {
		t.Fatalf("wrote %d descriptions: %+v", len(st.described), st.described)
	}
	for _, g := range st.described {
		if g.Harness != "gardener" || g.SessionRef != "gardener:merge:"+head || g.GitAnchor == nil || g.GitAnchor.Commit != head || g.Project != gardenProject {
			t.Errorf("description not marked as the gardener's on the merge commit: %+v", g)
		}
	}
	byPath := map[string]store.GhostFile{}
	for _, g := range st.described {
		byPath[g.Path] = g
	}
	if g := byPath["cmd"]; g.Kind != "dir" || g.ContentSHA == "" || g.GitBlob != "" {
		t.Errorf("directory description = %+v", g)
	}
	if g := byPath["cmd/x.go"]; g.Kind != "file" || g.GitBlob == "" || !strings.Contains(g.Description, "Invariante: sie hat keine Seiteneffekte") {
		t.Errorf("file description = %+v", g)
	}
	// The invented evidence proposes nothing, and the report names it.
	for _, want := range []string{"Kriterium 11 von #1: kein gültiger Beleg, nichts vorgeschlagen", "definiert keinen Test", "keine versionierte Datei", "kein Commit",
		"## Ledger: erfüllte Kriterien (Vorschläge)", "## Beschreibungen (Ghosts)", "1 criteria proposed as met", "2 descriptions written by the gardener"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	// Nothing was settled, closed or described by the run.
	d, _ := st.GetRequest(1)
	if d.Request.State != "open" || d.Criteria[0].State != "open" || d.Criteria[1].State != "open" {
		t.Errorf("the request was touched: %+v", d)
	}

	// The memory keeps the jobs apart, although request 1 and entry 1 share a
	// number, and the merge mark moved to HEAD: every path of the merges is done.
	for name, want := range map[string]int{gardenProject: 2, garden.LedgerState(gardenProject): 1, garden.GhostState(gardenProject): 2, garden.GhostMergeState(gardenProject): 1} {
		got, err := state.GardenChecks(name)
		if err != nil || len(got) != want {
			t.Errorf("memory %q holds %d looks (%v), want %d", name, len(got), err, want)
		}
	}
	if got, _ := state.GardenChecks(garden.LedgerState(gardenProject)); got[1].EntryHash == "" || got[1].Commit != head {
		t.Errorf("look at the request = %+v", got[1])
	}
	if got, _ := state.GardenChecks(garden.GhostMergeState(gardenProject)); got[1].Commit != head {
		t.Errorf("merge mark = %+v, want HEAD %s", got[1], head)
	}
	// What the description session used is the gardener's spending like the rest:
	// run-due measures each run from it against the budget and reports it to the
	// Gardener tab. Three sessions of three turns each.
	u, err := state.LLMUsageMonth(llmusage.JobGarden, time.Now().UTC().Format("2006-01"))
	if err != nil || u.Calls != 9 || u.CostMicroUSD <= 0 {
		t.Errorf("metered usage = %+v (%v), want the three sessions", u, err)
	}

	// A second run asks nothing again and files nothing again.
	st.entries = append(st.entries, st.written...)
	st.written, st.described = nil, nil
	before := fr.calls
	out.Reset()
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("second run exit %d:\n%s", code, out.String())
	}
	if fr.calls != before || len(st.written) != 0 || len(st.described) != 0 {
		t.Errorf("the second run made %d sessions, wrote %d notes and %d descriptions:\n%s", fr.calls-before, len(st.written), len(st.described), out.String())
	}
}

// A person said no to a proposal: the same evidence for the same criterion is not
// asked again, even without the memory of earlier runs; and a file the gardener
// described is not described again.
func TestGardenDoesNotProposeAgainWhatAPersonRejected(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	opts := jobsOpts(jobsRunner(head))
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 || len(st.written) != 1 || len(st.described) != 2 {
		t.Fatalf("first run: exit %d, %d notes, %d descriptions\n%s", code, len(st.written), len(st.described), out.String())
	}
	st.described = nil
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
	if len(st.written) != 0 || len(st.described) != 0 {
		t.Errorf("rejected proposals were made again: %+v %+v", st.written, st.described)
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
	for _, want := range []string{"Ledger 1/1 (3 Aufträge)", "Beschreibungen 1/1 (2 Pfade, ",
		"Der Lauf würde 1 Dateien und 1 Verzeichnisse beschreiben (2 davon aus Merges), in 1 Sessions; geschätzt $"} {
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
	if fr.calls != 2 || len(st.written) != 1 || len(st.described) != 2 {
		t.Errorf("%d sessions, %d notes, %d descriptions, want the ledger and the description session, a note and two descriptions\n%s", fr.calls, len(st.written), len(st.described), out.String())
	}
}

// The jobs read through the real client of the server: nothing here may need a
// method it does not have, and nothing in it writes a request.
var _ gardenReader = (*client.Client)(nil)

var _ garden.GhostWriter = (*client.Client)(nil)

// A description the server refuses is not remembered as written, and the merge
// mark stays: the next run takes the path first again.
func TestGardenRemembersADescriptionOnlyOnceItIsWritten(t *testing.T) {
	repo := gardenRepo(t)
	head, _ := repo.Head()
	st := jobsStore()
	st.putErr = fmt.Errorf("HTTP 409: kept_paragraphs")
	state, err := openFileState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	opts := jobsOpts(jobsRunner(head))
	opts.State = state
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 1 || !strings.Contains(out.String(), "kept_paragraphs") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	for _, name := range []string{garden.GhostState(gardenProject), garden.GhostMergeState(gardenProject)} {
		if got, _ := state.GardenChecks(name); len(got) != 0 {
			t.Errorf("%s remembers %+v after a refused write", name, got)
		}
	}
	st.putErr = nil
	fr := jobsRunner(head)
	opts.Runner = fr
	out.Reset()
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 || len(st.described) != 2 {
		t.Fatalf("the next run: exit %d, %d descriptions\n%s", code, len(st.described), out.String())
	}
}
