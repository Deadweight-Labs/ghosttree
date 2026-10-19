package garden

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// coverRepo is a checkout with a Go test, a Go file that is no test, and a test
// of another language.
func coverRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for name, body := range map[string]string{
		"internal/store/x_test.go": "package store\n\nimport \"testing\"\n\nfunc TestHoldsX(t *testing.T) {}\n\nfunc helperOnly() {}\n",
		"internal/store/x.go":      "package store\n\nfunc TestNotATest() {}\n",
		"web/app.test.js":          "test('x', () => {})\nfunction checksLogin() {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	repo, err := OpenRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func coverEntry(id int64) Entry {
	return Entry{ID: id, Type: "pitfall", Title: fmt.Sprintf("pitfall %d", id), Body: "b", AskCover: true}
}

func covers(t *testing.T, repo *Repo, answer string, looked *Looked, entries ...Entry) Validated {
	t.Helper()
	text := `{"verdicts":[],"regression":[` + answer + `]}`
	v, err := validateAnswer(repo, text, Package{Name: "p", Entries: entries}, nil, looked)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRegressionAnswerNamesATestThatExistsOrIsDropped(t *testing.T) {
	repo := coverRepo(t)
	seen := &Looked{Read: map[string]bool{"/work/internal/store/x_test.go": true}}
	cases := []struct {
		name, answer string
		ok           bool
	}{
		{"defined test", `{"id":1,"kind":"existing","file":"internal/store/x_test.go","test":"TestHoldsX","reason":"prüft es"}`, true},
		{"leading ./", `{"id":1,"kind":"existing","file":"./internal/store/x_test.go","test":"TestHoldsX"}`, true},
		{"not in the file", `{"id":1,"kind":"existing","file":"internal/store/x_test.go","test":"TestInvented"}`, false},
		{"a helper is no test of that name", `{"id":1,"kind":"existing","file":"internal/store/x_test.go","test":"TestHold"}`, false},
		{"file does not exist", `{"id":1,"kind":"existing","file":"internal/store/nope_test.go","test":"TestHoldsX"}`, false},
		{"go file that is no test file", `{"id":1,"kind":"existing","file":"internal/store/x.go","test":"TestNotATest"}`, false},
		{"file the session never looked at", `{"id":1,"kind":"existing","file":"web/app.test.js","test":"checksLogin"}`, false},
		{"outside the checkout", `{"id":1,"kind":"existing","file":"../x_test.go","test":"TestHoldsX"}`, false},
		{"absolute path", `{"id":1,"kind":"existing","file":"/etc/passwd","test":"TestHoldsX"}`, false},
		{"name that is no identifier", `{"id":1,"kind":"existing","file":"internal/store/x_test.go","test":"Test Holds X"}`, false},
		{"missing name", `{"id":1,"kind":"existing","file":"internal/store/x_test.go"}`, false},
	}
	for _, c := range cases {
		v := covers(t, repo, c.answer, seen, coverEntry(1))
		if got := len(v.Covers) == 1; got != c.ok {
			t.Errorf("%s: accepted=%v, want %v (rejected: %v)", c.name, got, c.ok, v.Rejected)
		}
		if !c.ok && len(v.Rejected) != 1 {
			t.Errorf("%s: a dropped answer must be named in the report: %v", c.name, v.Rejected)
		}
	}
	// Another language: the name as a word in a file the session read.
	other := &Looked{Read: map[string]bool{"/work/web/app.test.js": true}}
	if v := covers(t, repo, `{"id":1,"kind":"existing","file":"web/app.test.js","test":"checksLogin"}`, other, coverEntry(1)); len(v.Covers) != 1 {
		t.Errorf("a test of another language: %v", v.Rejected)
	}
	v := covers(t, repo, `{"id":1,"kind":"existing","file":"internal/store/x_test.go","test":"TestHoldsX","reason":"r"}`, seen, coverEntry(1))
	if c := v.Covers[0]; c.TestID() != "internal/store/x_test.go:TestHoldsX" || c.Kind != CoverExisting || c.Entry.ID != 1 {
		t.Errorf("cover = %+v", c)
	}
}

func TestRegressionSketchNeedsAPlaceAStepListAndAFreeName(t *testing.T) {
	repo := coverRepo(t)
	steps := "lege einen Eintrag an\nrufe die Funktion auf\nprüfe das Ergebnis"
	ok := func(file, name, steps string) string {
		return fmt.Sprintf(`{"id":1,"kind":"sketch","file":%q,"test":%q,"steps":%q}`, file, name, steps)
	}
	for _, c := range []struct {
		name, answer string
		ok           bool
	}{
		{"a sketch", ok("internal/store/y_test.go", "TestNewY", steps), true},
		{"in a file that exists, under a new name", ok("internal/store/x_test.go", "TestNewY", steps), true},
		{"the test exists already: say so as an existing test", ok("internal/store/x_test.go", "TestHoldsX", steps), false},
		{"a directory the checkout does not have", ok("nowhere/y_test.go", "TestNewY", steps), false},
		{"no steps", ok("internal/store/y_test.go", "TestNewY", ""), false},
		{"one line", ok("internal/store/y_test.go", "TestNewY", "alles prüfen"), false},
		{"far too long", ok("internal/store/y_test.go", "TestNewY", strings.Repeat("schritt\n", 12)), false},
		{"inside .git", ok(".git/hooks/y_test.go", "TestNewY", steps), false},
		{"no name", ok("internal/store/y_test.go", "", steps), false},
	} {
		v := covers(t, repo, c.answer, nil, coverEntry(1))
		if got := len(v.Covers) == 1; got != c.ok {
			t.Errorf("%s: accepted=%v, want %v (%v)", c.name, got, c.ok, v.Rejected)
		}
	}
}

func TestRegressionUntestableNeedsAReason(t *testing.T) {
	repo := coverRepo(t)
	if v := covers(t, repo, `{"id":1,"kind":"untestable","reason":"Eine Eigenschaft des Werkzeugs, nichts im Code."}`, nil, coverEntry(1)); len(v.Covers) != 1 {
		t.Errorf("untestable with a reason: %v", v.Rejected)
	}
	for _, a := range []string{`{"id":1,"kind":"untestable"}`, `{"id":1,"kind":"untestable","reason":"nein"}`, `{"id":1,"kind":"whatever","reason":"ein langer Grund hier"}`} {
		if v := covers(t, repo, a, nil, coverEntry(1)); len(v.Covers) != 0 || len(v.Rejected) != 1 {
			t.Errorf("%s accepted: %+v", a, v)
		}
	}
}

// Only a question that was asked is answered: the entry of the package that did not
// ask, another entry, a doubled answer and an eleventh pitfall are dropped.
func TestRegressionAnswersAreTakenOnlyForQuestionsThatWereAsked(t *testing.T) {
	repo := coverRepo(t)
	one := func(id int64) string {
		return fmt.Sprintf(`{"id":%d,"kind":"untestable","reason":"nichts zu testen, ein Werkzeug"}`, id)
	}
	var es []Entry
	for i := int64(1); i <= 10; i++ {
		es = append(es, coverEntry(i))
	}
	es[1].AskCover = false                                                       // judged already
	es = append(es, Entry{ID: 20, Type: "decision", Title: "d", AskCover: true}) // a decision has no regression question
	var answers []string
	for _, id := range []int64{1, 2, 20, 99, 1, 10} {
		answers = append(answers, one(id))
	}
	v := covers(t, repo, strings.Join(answers, ","), nil, es...)
	got := map[int64]bool{}
	for _, c := range v.Covers {
		got[c.Entry.ID] = true
	}
	// 1 is asked and answered once (its double is dropped); 10 is the ninth pitfall
	// that asks, past the limit of eight a session; 2, 20 and 99 were not asked.
	if len(got) != 1 || !got[1] || len(v.Rejected) != 5 {
		t.Errorf("covers %v, rejected %v", got, v.Rejected)
	}
}

func TestPromptAsksTheRegressionQuestionOnlyWhereItIsAsked(t *testing.T) {
	plain := Package{Name: "p", Entries: []Entry{{ID: 1, Type: "pitfall", Title: "a", Body: "b"}, {ID: 2, Type: "decision", Title: "c", Body: "d"}}}
	if p := PackagePrompt(plain, "o/r", 30); strings.Contains(p, "Regressionsfrage") || strings.Contains(p, "regression") {
		t.Errorf("a package that asks nothing is charged for the question:\n%s", p)
	}
	var es []Entry
	for i := int64(1); i <= 12; i++ {
		es = append(es, coverEntry(i))
	}
	asked := Package{Name: "p", Entries: es}
	p := PackagePrompt(asked, "o/r", 30)
	if n := strings.Count(p, "[Regressionsfrage], beobachtet"); n != maxCoverAsks {
		t.Errorf("%d entries tagged, want %d", n, maxCoverAsks)
	}
	if !strings.Contains(p, `"regression"`) || strings.Count(p, "Regressionsfrage: ") != 1 {
		t.Errorf("the question is not put exactly once:\n%s", p)
	}
	// The standing instruction and the answer of a package that never asks are the
	// old ones: the schema grew by an optional field only.
	if strings.Contains(AgentSystemPrompt(false), "regression") {
		t.Error("the system prompt carries the regression question for every package")
	}
	if _, err := extractAnswer(`{"verdicts":[{"id":1,"verdict":"holds"}]}`); err != nil {
		t.Errorf("an old-style answer is not read: %v", err)
	}
}

func TestPromptGrowthOfTheRegressionQuestionIsSmall(t *testing.T) {
	var base, asked []Entry
	for i := int64(1); i <= 30; i++ {
		e := Entry{ID: i, Type: "pitfall", Title: "Titel eines Pitfalls mit einiger Länge", Body: strings.Repeat("Ein Satz über den Code, der etwas Konkretes behauptet. ", 40), ObservedAt: "2026-10-01T00:00:00Z"}
		base = append(base, e)
		e.AskCover = true
		asked = append(asked, e)
	}
	plain := len(PackagePrompt(Package{Name: "p", Entries: base}, "o/r", 60)) + len(AgentSystemPrompt(false))
	with := len(PackagePrompt(Package{Name: "p", Entries: asked}, "o/r", 60)) + len(AgentSystemPrompt(false))
	t.Logf("package of 30 pitfalls with 2000 characters each: %d -> %d characters (+%d, %.1f%%)", plain, with, with-plain, 100*float64(with-plain)/float64(plain))
	if growth := float64(with-plain) / float64(plain); growth > 0.03 {
		t.Errorf("the question makes the prompt %.1f%% longer", growth*100)
	}
}

func TestCoverProposalIsStagedGardenerMarkedAndAsksOnce(t *testing.T) {
	c := Cover{Entry: Entry{ID: 12, Type: "pitfall", Title: "Pitfall"}, Kind: CoverSketch, File: "a/b_test.go", Test: "TestB", Steps: "eins\nzwei\ndrei", Reason: "weil"}
	k := CoverProposal("github.com/o/r", c)
	if k.Harness != Harness || k.Confidence != "staged" || k.Status != "active" || k.Type != "note" || k.Origin == "human" || k.RegressionState != "" {
		t.Errorf("proposal = %+v", k)
	}
	if k.SessionRef != "gardener:regression:12:sketch" || !strings.HasPrefix(k.Title, "[Gärtner] Test fehlt? #12: ") {
		t.Errorf("ref %q title %q", k.SessionRef, k.Title)
	}
	for _, want := range []string{"Herkunft gardener, nicht bestätigt", "Test: a/b_test.go:TestB", "Schritte:", "  zwei", "Begründung: weil"} {
		if !strings.Contains(k.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, k.Body)
		}
	}
	sink := &recordingSink{}
	if n, err := ProposeCovers(sink, "github.com/o/r", nil, []Cover{c}); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	existing := []store.Knowledge{sink.written[0]}
	existing[0].CreatedAt = "2026-10-01T00:00:00Z"
	// The same pitfall, unchanged: not again, in whatever state the note is in.
	if n, _ := ProposeCovers(sink, "github.com/o/r", existing, []Cover{c}); n != 0 {
		t.Errorf("proposed again: %d", n)
	}
	existing[0].Status = "deprecated"
	c.Entry.UpdatedAt = "2026-09-01T00:00:00Z"
	if !CoverProposed(existing, c.Entry) {
		t.Error("a rejected proposal is forgotten")
	}
	// Changed after the proposal: the question may be asked again.
	c.Entry.UpdatedAt = "2026-10-02T00:00:00Z"
	if CoverProposed(existing, c.Entry) {
		t.Error("a changed pitfall stays unasked")
	}
	if n, _ := ProposeCovers(sink, "github.com/o/r", existing, []Cover{c}); n != 1 {
		t.Errorf("a changed pitfall was not proposed again: %d", n)
	}
}

func TestReportGivesTestSketchesASectionOfTheirOwn(t *testing.T) {
	var b strings.Builder
	WriteReport(&b, "o/r", Result{Agent: true, Covers: []Cover{
		{Entry: Entry{ID: 12, Title: "Pitfall A"}, Kind: CoverSketch, File: "a/b_test.go", Test: "TestB", Steps: "lege an\nrufe auf\nprüfe", Reason: "weil"},
		{Entry: Entry{ID: 13, Title: "Pitfall B"}, Kind: CoverExisting, File: "c/d_test.go", Test: "TestD"},
		{Entry: Entry{ID: 14, Title: "Pitfall C"}, Kind: CoverUntestable, Reason: "ein Werkzeug"},
	}}, mustTime("2026-10-19T03:00:00Z"))
	out := b.String()
	sketch := out[strings.Index(out, "## Testskizzen"):]
	sketch = sketch[:strings.Index(sketch, "\n\n")]
	for _, want := range []string{"#12 Pitfall A", "a/b_test.go:TestB", "rufe auf"} {
		if !strings.Contains(sketch, want) {
			t.Errorf("the sketch section lacks %q:\n%s", want, sketch)
		}
	}
	if strings.Contains(sketch, "Pitfall B") || strings.Contains(sketch, "Pitfall C") {
		t.Errorf("the sketch section holds more than sketches:\n%s", sketch)
	}
	rest := out[strings.Index(out, "## Regressionsschutz"):]
	if !strings.Contains(rest, "c/d_test.go:TestD deckt ihn ab") || !strings.Contains(rest, "nicht testbar (ein Werkzeug)") {
		t.Errorf("the other answers are missing:\n%s", rest)
	}
}

// ---------------------------------------------------------------- threads

// fakeThreads is a thread store with the behaviour the gardener depends on: a
// thread is found through the object it is linked to, and a person can close it.
type fakeThreads struct {
	threads []store.Thread
	links   map[int64][]store.ThreadLink
	posts   []store.CoordMessage
	agents  []store.CoordAgent
	calls   []string
	failFor string
}

func (f *fakeThreads) log(s string) { f.calls = append(f.calls, s) }

func (f *fakeThreads) RegisterCoordAgent(a store.CoordAgent) (int64, error) {
	f.log("register")
	f.agents = append(f.agents, a)
	return 1, nil
}

func (f *fakeThreads) ThreadsForObject(kind, id, _ string) ([]store.Thread, error) {
	f.log("find")
	if f.failFor == "find" {
		return nil, errors.New("server down")
	}
	var out []store.Thread
	for _, t := range f.threads {
		for _, l := range f.links[t.ID] {
			if l.Kind == kind && l.ID == id {
				out = append(out, t)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeThreads) ThreadLinks(id int64, _ string) ([]store.ThreadLink, error) {
	f.log("links")
	return f.links[id], nil
}

func (f *fakeThreads) CreateThread(t store.Thread, _ string) (int64, error) {
	f.log("create")
	if f.failFor == "create" {
		return 0, errors.New("refused")
	}
	t.ID = int64(len(f.threads) + 1)
	t.State = store.ThreadOpen
	if f.links == nil {
		f.links = map[int64][]store.ThreadLink{}
	}
	if t.Link != nil {
		l := *t.Link
		l.ThreadID = t.ID
		f.links[t.ID] = append(f.links[t.ID], l)
	}
	f.threads = append(f.threads, t)
	return t.ID, nil
}

func (f *fakeThreads) LinkThread(l store.ThreadLink, _ string) error {
	f.log("link")
	f.links[l.ThreadID] = append(f.links[l.ThreadID], l)
	return nil
}

func (f *fakeThreads) SendCoordMessage(m store.CoordMessage) (int64, error) {
	f.log("post")
	f.posts = append(f.posts, m)
	return int64(len(f.posts)), nil
}

func (f *fakeThreads) setState(id int64, state string, archived bool) {
	for i := range f.threads {
		if f.threads[i].ID == id {
			f.threads[i].State, f.threads[i].Archived = state, archived
		}
	}
}

func contradiction() (Relation, map[int64]Entry) {
	known := map[int64]Entry{
		12: {ID: 12, Type: "decision", Title: "Dienst läuft auf server-a", Body: "Der Dienst läuft auf server-a und nirgends sonst."},
		15: {ID: 15, Type: "pitfall", Title: "Dienst läuft auf server-b", Body: "Der Dienst läuft auf server-b."},
	}
	return Relation{Kind: Contradiction, IDs: []int64{12, 15}, Reason: "Zwei Orte.",
		Quotes: map[int64]string{12: "läuft auf server-a und nirgends sonst", 15: "läuft auf server-b"}}, known
}

const testProject = "github.com/o/r"

func TestContradictionOpensOneThreadLinkedToBothWithTheQuotes(t *testing.T) {
	rel, known := contradiction()
	f := &fakeThreads{}
	out := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)
	if len(out) != 1 || out[0].State != ThreadOpened || out[0].ThreadID != 1 || out[0].Err != nil || out[0].Warn != "" {
		t.Fatalf("outcome = %+v", out)
	}
	th := f.threads[0]
	if th.Project != testProject || !strings.HasPrefix(th.Title, "[Gärtner] Widersprechen sich #12 „Dienst läuft auf server-a“ und #15") || !strings.HasSuffix(th.Title, "?") {
		t.Errorf("title = %q", th.Title)
	}
	if th.Link == nil || th.Link.Kind != "knowledge" || th.Link.ID != "12" {
		t.Errorf("link on creation = %+v", th.Link)
	}
	got := map[string]bool{}
	for _, l := range f.links[1] {
		got[l.Kind+":"+l.ID] = true
	}
	if !got["knowledge:12"] || !got["knowledge:15"] {
		t.Errorf("linked to %v, want both entries", got)
	}
	if len(f.posts) != 1 {
		t.Fatalf("%d posts, want the first post", len(f.posts))
	}
	p := f.posts[0]
	if p.DestinationKind != store.DestinationDiscussion || p.DestinationID != store.ThreadDestinationID(1) || p.SenderExternalID != ThreadAgentID(testProject) {
		t.Errorf("post = %+v", p)
	}
	for _, want := range []string{"Herkunft gardener, nicht bestätigt", `Zitat: "läuft auf server-a und nirgends sonst"`, `Zitat: "läuft auf server-b"`, "Begründung: Zwei Orte."} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("first post lacks %q:\n%s", want, p.Body)
		}
	}
	if len(f.agents) != 1 || f.agents[0].Provider != Harness || f.agents[0].RoomKey != store.RoomKeyForProject(testProject) || f.agents[0].ExternalID != ThreadAgentID(testProject) {
		t.Errorf("registered as %+v", f.agents)
	}
	for _, c := range f.calls {
		if c != "register" && c != "find" && c != "links" && c != "create" && c != "link" && c != "post" {
			t.Errorf("unexpected call %q", c)
		}
	}
}

func TestContradictionThreadIsIdempotentAndNeverReopened(t *testing.T) {
	rel, known := contradiction()
	f := &fakeThreads{}
	open := func() ThreadOutcome { return OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)[0] }
	open()

	// Open: no second one.
	if o := open(); o.State != ThreadExists || o.ThreadID != 1 || len(f.threads) != 1 {
		t.Fatalf("second run: %+v, %d threads", o, len(f.threads))
	}
	// Parked or archived is not answered either, and the same entries do not ask again.
	for name, set := range map[string]func(){
		"resolved": func() { f.setState(1, store.ThreadResolved, false) },
		"deferred": func() { f.setState(1, store.ThreadDeferred, false) },
		"archived": func() { f.setState(1, store.ThreadOpen, true) },
	} {
		set()
		callsBefore := len(f.calls)
		if o := open(); len(f.threads) != 1 || (o.State != ThreadDecided && o.State != ThreadExists) {
			t.Errorf("%s: %+v, %d threads", name, o, len(f.threads))
		}
		for _, c := range f.calls[callsBefore:] {
			if c == "create" || c == "post" {
				t.Errorf("%s: %s after a person's decision", name, c)
			}
		}
	}
	f.setState(1, store.ThreadResolved, false)
	// The gardener has no call that reopens or closes: nothing but these ever ran.
	for _, c := range f.calls {
		if strings.Contains(c, "state") || strings.Contains(c, "archive") {
			t.Errorf("a thread was touched: %s", c)
		}
	}
	// One of the entries changed: the old thread stays closed as it is, a new one asks again.
	changed := map[int64]Entry{12: known[12], 15: known[15]}
	e := changed[15]
	e.Body += " Seit dem Umzug."
	changed[15] = e
	if o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, changed)[0]; o.State != ThreadOpened || o.ThreadID != 2 {
		t.Fatalf("a changed entry: %+v", o)
	}
	if f.threads[0].State != store.ThreadResolved {
		t.Error("the closed thread was reopened")
	}
	// And that one is open now, so again no second one.
	if o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, changed)[0]; o.State != ThreadExists || o.ThreadID != 2 || len(f.threads) != 2 {
		t.Errorf("after the new thread: %+v", o)
	}
}

func TestContradictionIsLeftToPeopleWhereTheyAlreadyActed(t *testing.T) {
	rel, known := contradiction()
	// A thread people opened on both entries counts in every state, with or without our stand line.
	f := &fakeThreads{
		threads: []store.Thread{{ID: 1, Project: testProject, Title: "Welcher Ort gilt?", State: store.ThreadResolved}},
		links:   map[int64][]store.ThreadLink{1: {{ThreadID: 1, Kind: "knowledge", ID: "12"}, {ThreadID: 1, Kind: "knowledge", ID: "15"}}},
	}
	if o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)[0]; o.State != ThreadDecided || len(f.threads) != 1 {
		t.Errorf("a closed thread of people: %+v", o)
	}
	// A thread on only one of the entries is another question.
	f = &fakeThreads{
		threads: []store.Thread{{ID: 1, Project: testProject, Title: "Nur zu zwölf", State: store.ThreadOpen}},
		links:   map[int64][]store.ThreadLink{1: {{ThreadID: 1, Kind: "knowledge", ID: "12"}}},
	}
	if o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)[0]; o.State != ThreadOpened {
		t.Errorf("a thread on one entry blocked the pair: %+v", o)
	}
	// A note of the gardener on the same relation, in any state, means somebody has seen it.
	note := RelationProposal(testProject, rel, map[int64]string{12: "a", 15: "b"})
	note.Status = "deprecated"
	f = &fakeThreads{}
	if o := OpenContradictionThreads(f, testProject, []store.Knowledge{note}, []Relation{rel}, known)[0]; o.State != ThreadDecided || len(f.calls) != 0 {
		t.Errorf("after a rejected note: %+v, calls %v", o, f.calls)
	}
}

func TestContradictionThreadIsNotOpenedWhenThreadsCannotBeLookedUp(t *testing.T) {
	rel, known := contradiction()
	f := &fakeThreads{failFor: "find"}
	o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)[0]
	if o.State != ThreadFailed || o.Err == nil || len(f.threads) != 0 {
		t.Errorf("without a look-up: %+v, %d threads", o, len(f.threads))
	}
	f = &fakeThreads{failFor: "create"}
	if o := OpenContradictionThreads(f, testProject, nil, []Relation{rel}, known)[0]; o.State != ThreadFailed || o.Err == nil {
		t.Errorf("refused: %+v", o)
	}
	// Duplicates are notes, not threads, and a relation reported twice is one thread.
	dup := Relation{Kind: Duplicate, IDs: []int64{12, 15}, Reason: "r", Quotes: rel.Quotes}
	f = &fakeThreads{}
	if out := OpenContradictionThreads(f, testProject, nil, []Relation{dup, rel, rel}, known); len(out) != 1 || len(f.threads) != 1 {
		t.Errorf("outcomes %+v, threads %d", out, len(f.threads))
	}
}

// What the gardener may do on the server is a short list, and it holds no
// call that changes, closes, supersedes, retires, judges, or answers somebody
// else's thread. A new method on these interfaces has to be added here, which is
// the moment somebody asks whether it belongs there.
func TestWhatTheGardenerMayCallIsAShortListWithoutAnythingThatDecides(t *testing.T) {
	names := func(i any) []string {
		ty := reflect.TypeOf(i).Elem()
		var out []string
		for n := 0; n < ty.NumMethod(); n++ {
			out = append(out, ty.Method(n).Name)
		}
		sort.Strings(out)
		return out
	}
	if got, want := names((*Sink)(nil)), []string{"Remember"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Sink = %v, want %v", got, want)
	}
	want := []string{"CreateThread", "LinkThread", "RegisterCoordAgent", "SendCoordMessage", "ThreadLinks", "ThreadsForObject"}
	if got := names((*ThreadSink)(nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("ThreadSink = %v, want %v", got, want)
	}
	for _, n := range append(names((*Sink)(nil)), names((*ThreadSink)(nil))...) {
		for _, banned := range []string{"Patch", "Update", "Delete", "Supersede", "Retire", "Regression", "State", "Archive", "Criterion", "Complete", "Drop", "Verify", "Outcome"} {
			if strings.Contains(n, banned) {
				t.Errorf("%s can decide something (%s)", n, banned)
			}
		}
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The server reserves harness "gardener" and "gardener:" references to the
// gardener's account (store.ReservedForGardener). Every note the gardener files has
// to be one of those, or it would be refused, and the constants have to agree.
func TestEveryNoteTheGardenerFilesIsReservedToItsAccount(t *testing.T) {
	if Harness != store.GardenerHarness {
		t.Fatalf("the gardener writes harness %q, the server reserves %q", Harness, store.GardenerHarness)
	}
	rel := Relation{Kind: Duplicate, IDs: []int64{1, 2}, Reason: "r", Quotes: map[int64]string{1: "a", 2: "b"}}
	stale, _ := Proposal(testProject, Verdict{Entry: Entry{ID: 1, Title: "t"}, Kind: Stale, Reason: "r"})
	for name, k := range map[string]store.Knowledge{
		"stale":      stale,
		"duplicate":  RelationProposal(testProject, rel, map[int64]string{1: "a", 2: "b"}),
		"regression": CoverProposal(testProject, Cover{Entry: Entry{ID: 1, Title: "t"}, Kind: CoverUntestable, Reason: "ein Grund hier"}),
	} {
		if !store.ReservedForGardener(k) || !strings.HasPrefix(k.SessionRef, store.GardenerRefPrefix) {
			t.Errorf("%s: harness %q reference %q are not reserved to the gardener", name, k.Harness, k.SessionRef)
		}
	}
}
