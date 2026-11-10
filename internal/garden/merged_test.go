package garden

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// mergeFixture is a default branch with a start commit, a pull request merged
// with a merge commit (a new file, a rename, a change) and a squashed one (a new
// file, a deletion).
type mergeFixture struct {
	repo                 *Repo
	dir                  string
	start, merge, squash string
}

func newMergeFixture(t *testing.T) mergeFixture {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeIn(t, dir, "a.go", numbered("a", 3))
	writeIn(t, dir, "old.go", numbered("alt", 12))
	writeIn(t, dir, "keep.go", numbered("bleibt", 6))
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "start")
	fx := mergeFixture{dir: dir, start: gitIn(t, dir, "rev-parse", "HEAD")}

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeIn(t, dir, "neu/b.go", numbered("b", 4))
	gitIn(t, dir, "mv", "old.go", "renamed.go")
	writeIn(t, dir, "keep.go", numbered("bleibt", 6)+"// mehr\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature")
	gitIn(t, dir, "checkout", "-q", "main")
	gitIn(t, dir, "merge", "-q", "--no-ff", "feature", "-m", "Merge PR #5: neue Datei b\n\nWarum: b hält den Zähler.")
	fx.merge = gitIn(t, dir, "rev-parse", "HEAD")

	writeIn(t, dir, "c.go", numbered("c", 4))
	gitIn(t, dir, "rm", "-q", "a.go")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feat: c statt a (#6)\n\nc ersetzt a.")
	fx.squash = gitIn(t, dir, "rev-parse", "HEAD")
	repo, err := OpenRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	fx.repo = repo
	return fx
}

func TestMergedFindsWhatTheMergesSinceTheMarkBrought(t *testing.T) {
	fx := newMergeFixture(t)
	got, err := fx.repo.Merged(fx.start)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		commit string
		isNew  bool
	}{
		"neu/b.go":   {fx.merge, true},
		"renamed.go": {fx.merge, true},
		"keep.go":    {fx.merge, false},
		"c.go":       {fx.squash, true},
	}
	if len(got) != len(want) {
		t.Fatalf("Merged = %+v, want %d paths", got, len(want))
	}
	for p, w := range want {
		m, ok := got[p]
		if !ok || m.Commit != w.commit || m.New != w.isNew {
			t.Errorf("%s = %+v (found %v), want commit %s new %v", p, m, ok, w.commit[:7], w.isNew)
		}
	}
	if !strings.Contains(got["neu/b.go"].Text, "Warum: b hält den Zähler.") || !strings.Contains(got["c.go"].Text, "c ersetzt a.") {
		t.Errorf("the texts of the merges are missing: %q / %q", got["neu/b.go"].Text, got["c.go"].Text)
	}
	// Nothing new since HEAD; without a mark (or with one that is not in the
	// history) the last commits are looked at, the start commit included.
	if now, err := fx.repo.Merged(fx.squash); err != nil || len(now) != 0 {
		t.Errorf("Merged(HEAD) = %v, %v", now, err)
	}
	for _, since := range []string{"", strings.Repeat("0", 40), "nicht-hex"} {
		all, err := fx.repo.Merged(since)
		if err != nil || !all["keep.go"].New || all["neu/b.go"].Commit != fx.merge || len(all) != 4 {
			t.Errorf("Merged(%q) = %+v, %v", since, all, err)
		}
	}
}

func TestSelectGhostsTakesTheMergesFirstPastTheLimitAndTheirNewDirectories(t *testing.T) {
	fx := newMergeFixture(t)
	merged, err := fx.repo.Merged(fx.start)
	if err != nil {
		t.Fatal(err)
	}
	// keep.go has a description from before its change (one line more of seven).
	before := t.TempDir()
	writeIn(t, before, "keep.go", numbered("bleibt", 6))
	described := map[string]store.GhostFile{"keep.go": describeAs(t, before, "keep.go", oldText)}
	got, rot := SelectGhosts(fx.repo, described, nil, nil, nil, merged, 0)
	// keep.go changed by one line in fifteen: a slight change is no reason to
	// rewrite, so it waits in the backlog like any drifted file, without its merge.
	if p := paths(got); p != "c.go neu/b.go renamed.go keep.go neu" {
		t.Fatalf("selected %q", p)
	}
	for _, g := range got {
		if (g.Merge == nil) != (g.Path == "keep.go") {
			t.Errorf("%s: merge %+v", g.Path, g.Merge)
		}
	}
	if k := got[3]; k.State != GhostDrifted {
		t.Errorf("keep.go = %+v", k)
	}
	if d := got[4]; !d.Dir || d.SHA == "" || d.Blob != "" || d.Merge.Commit != fx.merge || !strings.Contains(GhostStateLabel(d), "neues Verzeichnis durch Merge "+fx.merge[:7]) {
		t.Errorf("directory = %+v (%s)", d, GhostStateLabel(d))
	}
	if rot.Merged != 3 || rot.MergedLeft != 0 || rot.Due != 4 {
		t.Errorf("rotation = %+v", rot)
	}
	// The files of the merges do not count against the limit; the backlog does.
	writeIn(t, fx.dir, "z.go", numbered("z", 3))
	writeIn(t, fx.dir, "y.go", numbered("y", 3))
	gitIn(t, fx.dir, "add", ".")
	gitIn(t, fx.dir, "commit", "-q", "-m", "ohne Merge-Text")
	repo, _ := OpenRepo(fx.dir)
	got, _ = SelectGhosts(repo, described, nil, nil, nil, merged, 1)
	if p := paths(got); p != "c.go neu/b.go renamed.go y.go neu" {
		t.Errorf("with limit 1: %q", p)
	}
	// A directory the gardener looked at in this state is not taken again.
	seen := map[int64]Seen{GhostKey("neu"): {EntryHash: got[len(got)-1].Stamp()}}
	if again, _ := SelectGhosts(repo, described, nil, seen, nil, merged, 1); strings.HasSuffix(paths(again), " neu") {
		t.Errorf("a directory looked at is taken again: %q", paths(again))
	}
}

func TestGhostPromptShowsTheMergeOnceAndMarksTheParagraphsOfOthers(t *testing.T) {
	m := &Merge{Commit: strings.Repeat("c", 40), Text: "Merge PR #5: Zähler\n\nWarum: b hält den Zähler.", New: true}
	ps := []GhostPath{
		{Path: "neu/a.go", State: GhostUndescribed, Merge: m, Lines: 3},
		{Path: "neu/b.go", State: GhostStale, Percent: 60, Merge: m, Lines: 9, Old: "Agent sagt.\n\nGärtner sagte.",
			Prior: []ghost.Paragraph{{Text: "Agent sagt.", Gardener: false}, {Text: "Gärtner sagte.", Gardener: true}}},
		{Path: "neu", Dir: true, State: GhostUndescribed, Merge: m},
	}
	p := GhostPrompt("p", "Beschreibungen 1/1", ps, nil, 40)
	if strings.Count(p, "Warum: b hält den Zähler.") != 1 || !strings.Contains(p, "--- Merge ccccccc ---") {
		t.Errorf("the text of the merge is not there once:\n%s", p)
	}
	for _, want := range []string{"=== PFAD neu/a.go (neu durch Merge ccccccc, 3 Zeilen)", "veraltet durch Merge ccccccc (60 %",
		"[FEST (von Agent oder Mensch, bleibt wörtlich)]\nAgent sagt.", "[DEIN (darfst du neu schreiben)]\nGärtner sagte.",
		"=== VERZEICHNIS neu (neues Verzeichnis durch Merge ccccccc)"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	if !strings.Contains(GhostSystemPrompt(), "FEST") || strings.Contains(GhostSystemPrompt(), "ein Mensch prüft ihn") {
		t.Error("the standing instruction does not say how the paragraphs of others are kept")
	}
}

// What the gardener writes keeps every paragraph of others word for word, in its
// place, and replaces only its own; a session that tries to drop them changes
// nothing about that.
func TestValidateGhostsPutsTheParagraphsOfOthersBack(t *testing.T) {
	fx := newMergeFixture(t)
	sha, blob, lines, err := ghost.HashFile(filepath.Join(fx.dir, "keep.go"))
	if err != nil {
		t.Fatal(err)
	}
	human := "Ein Mensch: hier lief einmal der Export doppelt, deshalb das Flag."
	old := "Gärtner: alte Fassung über den Zweck der Datei.\n\n" + human
	g := GhostPath{Path: "keep.go", State: GhostStale, SHA: sha, Blob: blob, Lines: lines, Old: old,
		Prior: ghost.Attribute(old, []store.GhostVersion{{Description: old, Harness: "mcp"}, {Description: "Gärtner: alte Fassung über den Zweck der Datei.", Harness: store.GardenerHarness}})}
	v, err := validateGhosts(fx.repo, ghostJSON(ghostItem("keep.go", "describe", goodText)), []GhostPath{g}, nil)
	if err != nil || len(v.Proposals) != 1 {
		t.Fatalf("validate = %+v, %v", v, err)
	}
	if got := v.Proposals[0].Description; got != goodText+"\n\n"+human {
		t.Errorf("description = %q", got)
	}
	// The same text as before is no change.
	v, _ = validateGhosts(fx.repo, ghostJSON(ghostItem("keep.go", "describe", "Gärtner: alte Fassung über den Zweck der Datei.")), []GhostPath{g}, nil)
	if len(v.Proposals) != 0 || len(v.Kept) != 1 {
		t.Errorf("an unchanged text = %+v", v)
	}
	// A directory needs a file of it read, and its listing unchanged.
	names, _ := ghost.ChildNames(filepath.Join(fx.dir, "neu"))
	d := GhostPath{Path: "neu", Dir: true, State: GhostUndescribed, SHA: ghost.HashDir(names)}
	looked := &Looked{Read: map[string]bool{}}
	v, _ = validateGhosts(fx.repo, ghostJSON(ghostItem("neu/", "describe", goodText)), []GhostPath{d}, looked)
	if len(v.Proposals) != 0 || len(v.Rejected) != 1 || !strings.Contains(v.Rejected[0], "keine Datei des Verzeichnisses") {
		t.Errorf("a directory nobody looked into = %+v", v)
	}
	looked.Read[filepath.Join(fx.dir, "neu/b.go")] = true
	v, _ = validateGhosts(fx.repo, ghostJSON(ghostItem("neu", "describe", goodText)), []GhostPath{d}, looked)
	if len(v.Proposals) != 1 || !v.Proposals[0].Dir {
		t.Errorf("a directory read = %+v", v)
	}
	writeIn(t, fx.dir, "neu/c.go", "package neu\n")
	v, _ = validateGhosts(fx.repo, ghostJSON(ghostItem("neu", "describe", goodText)), []GhostPath{d}, looked)
	if len(v.Proposals) != 0 || !strings.Contains(strings.Join(v.Rejected, " "), "seit dem Lesen verändert") {
		t.Errorf("a directory that changed = %+v", v)
	}
}

type recordingGhostWriter struct {
	files   []store.GhostFile
	anchors []gitanchor.Anchor
}

func (w *recordingGhostWriter) PutGhostAnchored(g store.GhostFile, a gitanchor.Anchor) (int64, error) {
	w.files = append(w.files, g)
	w.anchors = append(w.anchors, a)
	return int64(len(w.files)), nil
}

func TestWriteGhostsMarksTheGardenerAndAnchorsOnTheMerge(t *testing.T) {
	head, merge := strings.Repeat("a", 40), strings.Repeat("b", 40)
	w := &recordingGhostWriter{}
	n, err := WriteGhosts(w, "github.com/o/r", head, "main", []GhostProposalData{
		{GhostPath: GhostPath{Path: "neu/b.go", SHA: "s", Blob: "bl", Lines: 4, Merge: &Merge{Commit: merge}}, Description: goodText},
		{GhostPath: GhostPath{Path: "neu", Dir: true, SHA: "d", Merge: &Merge{Commit: merge}}, Description: goodText},
		{GhostPath: GhostPath{Path: "alt.go", SHA: "s2", Blob: "b2", Lines: 9}, Description: goodText},
	})
	if err != nil || n != 3 {
		t.Fatalf("WriteGhosts = %d, %v", n, err)
	}
	for i, want := range []struct{ ref, commit, kind string }{
		{"gardener:merge:" + merge, merge, "file"}, {"gardener:merge:" + merge, merge, "dir"}, {"gardener:run:" + head, head, "file"},
	} {
		f, a := w.files[i], w.anchors[i]
		if f.Harness != store.GardenerHarness || f.SessionRef != want.ref || f.Kind != want.kind || a.Commit != want.commit || a.Branch != "main" || !ghost.IsGardenerWriteRef(f.SessionRef) {
			t.Errorf("write %d = %+v at %+v", i, f, a)
		}
	}
	if w.files[1].GitBlob != "" || w.files[1].LineCount != 0 || w.files[1].ContentSHA != "d" {
		t.Errorf("a directory carries file facts: %+v", w.files[1])
	}
}

// The description sessions run on their own model and are priced with it; a dry
// run says how many paths and what they would cost.
func TestGhostSessionsUseTheirModelAndADryRunPricesThem(t *testing.T) {
	fx := newMergeFixture(t)
	merged, _ := fx.repo.Merged(fx.start)
	gp, _ := SelectGhosts(fx.repo, nil, nil, nil, nil, merged, 0)
	haiku := Pricing{InUSDPerMTok: 0.1, OutUSDPerMTok: 0.5}
	opt := AgentOptions{Project: "p", Pricing: price, MaxUSD: 4, Sessions: 1, PerSession: 4, Turns: 40, Ghosts: gp,
		GhostModel: DefaultGhostModel, GhostPricing: &haiku, DryRun: true}
	res := CheckAgent(context.Background(), fx.repo, nil, opt)
	e := res.GhostEstimate
	if e.Files != 4 || e.Dirs != 1 || e.Merged != 5 || e.Sessions != 1 || !(e.USD > 0) {
		t.Fatalf("estimate = %+v", e)
	}
	var b strings.Builder
	WriteReport(&b, "p", res, time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC))
	if !strings.Contains(b.String(), "Der Lauf würde 4 Dateien und 1 Verzeichnisse beschreiben (5 davon aus Merges), in 1 Sessions; geschätzt $") {
		t.Errorf("the dry run does not say what it would do:\n%s", b.String())
	}
	// The same paths priced at the gardener's model cost more.
	opt.GhostPricing = nil
	if dear := CheckAgent(context.Background(), fx.repo, nil, opt).GhostEstimate.USD; dear <= e.USD {
		t.Errorf("estimate at the dearer model $%.4f, at the description model $%.4f", dear, e.USD)
	}

	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){func(SessionSpec) (SessionResult, error) {
		return SessionResult{Text: ghostJSON(), Turns: 2, Usage: llm.Usage{InputTokens: 1_000_000}}, nil
	}}}
	opt.DryRun, opt.Runner, opt.GhostPricing = false, fr, &haiku
	res = CheckAgent(context.Background(), fx.repo, nil, opt)
	if len(fr.specs) != 1 || fr.specs[0].Model != DefaultGhostModel {
		t.Fatalf("sessions = %+v", fr.specs)
	}
	if got := res.Sessions[0].CostUSD; got < 0.099 || got > 0.101 {
		t.Errorf("a million input tokens of the description model cost $%.4f, want $0.10", got)
	}
	args, err := (ClaudeConfig{Model: "claude-sonnet-5-5", RepoRoot: "/r"}).Args(fr.specs[0])
	if err != nil || !strings.Contains(strings.Join(args, " "), "--model "+DefaultGhostModel) || strings.Contains(strings.Join(args, " "), "sonnet") {
		t.Errorf("args = %v, %v", args, err)
	}
}
