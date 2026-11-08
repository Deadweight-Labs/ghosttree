package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
)

func TestFeatureCLI(t *testing.T) {
	st := moveCLIFixture(t)
	dir := t.TempDir()
	run := func(args ...string) (int, string) {
		t.Helper()
		return painTaskRun(t, append(args, "--project", cliHere)...)
	}
	code, out := run("feature", "add", "Arbeitspakete", "--summary", "Ein Branch trägt seine Requests.", "--behavior", "Pakete binden Requests an Branches.",
		"--limits", "Kein Abgleich mit GitHub.", "--cli", "ctx package", "--area", "internal/request", "--area", "cmd/ctx/package.go", "--message", "erste Fassung")
	if code != 0 || !strings.Contains(out, "FEAT-1 [active, version 1 of 1] Arbeitspakete") || !strings.Contains(out, "Paths: cmd/ctx/package.go, internal/request") || !strings.Contains(out, "link: ") {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := run("feature", "add", "arbeitspakete"); code == 0 || !strings.Contains(out, "exists") {
		t.Errorf("the same name twice: %d %s", code, out)
	}
	if code, out := run("feature", "add"); code != 2 {
		t.Errorf("add without a name: %d %s", code, out)
	}
	if code, out := run("feature", "add", "Schlecht", "--area", "../etc"); code == 0 {
		t.Errorf("an area outside the repository: %d %s", code, out)
	}
	if code, out := run("feature", "list"); code != 0 || !strings.Contains(out, "FEAT-1 [active] v1 Arbeitspakete: Ein Branch trägt seine Requests.") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := run("feature", "list", "--path", "cmd/ctx/package.go"); code != 0 || !strings.Contains(out, "FEAT-1") {
		t.Errorf("list by path: %d %s", code, out)
	}
	if code, out := run("feature", "list", "--path", "internal/web"); code != 0 || !strings.Contains(out, "no features") {
		t.Errorf("list by another path: %d %s", code, out)
	}

	// Fortschreiben mit Idee und PR; die Idee betrifft das Feature.
	if code, out := painTaskRun(t, "idea", "add", "Pakete pro PR", "--project", cliHere); code != 0 {
		t.Fatalf("idea: %d %s", code, out)
	}
	if code, out := run("feature", "link", "FEAT-1", "idea", "IDEA-1"); code != 0 || !strings.Contains(out, "changed by IDEA-1 Pakete pro PR (description not carried forward yet)") {
		t.Fatalf("link: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "show", "1"); code != 0 || !strings.Contains(out, "changes FEAT-1 Arbeitspakete (description not carried forward yet)") {
		t.Errorf("idea show: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--behavior", "Pakete binden Requests an Branches und PRs.", "--idea", "IDEA-1", "--pr", "9", "--message", "PR dazu"); code != 0 || !strings.Contains(out, "FEAT-1 is at version 2") {
		t.Fatalf("update: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--behavior", "Pakete binden Requests an Branches und PRs."); code != 0 || !strings.Contains(out, "unchanged") {
		t.Errorf("update with the same text: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--idea", "1", "--idea", "2"); code == 0 || !strings.Contains(out, "one idea") {
		t.Errorf("two ideas: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--name", "Anders"); code != 2 {
		t.Errorf("rename via update: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--summary", "neu", "--base", "1"); code == 0 || !strings.Contains(out, "revision 2") {
		t.Errorf("stale base: %d %s", code, out)
	}
	bf := filepath.Join(dir, "behavior.txt")
	if err := os.WriteFile(bf, []byte("Aus einer Datei,\nmehrzeilig."), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := run("feature", "update", "FEAT-1", "--behavior-file", bf, "--behavior", "x"); code == 0 {
		t.Errorf("both --behavior and --behavior-file: %d %s", code, out)
	}
	if code, out := run("feature", "update", "FEAT-1", "--behavior-file", bf); code != 0 {
		t.Fatalf("update from a file: %d %s", code, out)
	}
	code, out = run("feature", "show", "FEAT-1", "--versions")
	for _, want := range []string{"[active, version 3 of 3]", "Aus einer Datei,\nmehrzeilig.", "v1 ", "erste Fassung", "v2 ", "PR dazu (IDEA-1, PR #9)", "changed by IDEA-1 Pakete pro PR\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("show --versions lacks %q:\n%s", want, out)
		}
	}
	if code, out := run("feature", "show", "1", "--rev", "1"); code != 0 || !strings.Contains(out, "version 1 of 3") || !strings.Contains(out, "Pakete binden Requests an Branches.") {
		t.Errorf("show --rev: %d %s", code, out)
	}
	if code, out := run("feature", "show", "FEAT-9"); code == 0 || !strings.Contains(out, "FEAT-9") {
		t.Errorf("a missing feature: %d %s", code, out)
	}
	if code, out := run("feature", "show", "x"); code == 0 {
		t.Errorf("a bad number: %d %s", code, out)
	}

	// Die Checkliste eines PRs aus gemeldeten Pfaden.
	if code, out := run("feature", "paths", "--pr", "12", "internal/request/package.go", "README.md"); code != 0 || !strings.Contains(out, "stored 2 paths for PR #12") || !strings.Contains(out, "[ ] Feature FEAT-1 Arbeitspakete: Beschreibung angepasst?") {
		t.Fatalf("paths: %d %s", code, out)
	}
	if code, out := run("feature", "paths"); code == 0 || !strings.Contains(out, "--pr") {
		t.Errorf("paths without --pr: %d %s", code, out)
	}
	if code, out := run("feature", "check", "FEAT-1", "--pr", "12"); code == 0 || !strings.Contains(out, "note") {
		t.Errorf("check without a note: %d %s", code, out)
	}
	if code, out := run("feature", "check", "FEAT-1", "--pr", "12", "--note", "nur umbenannt"); code != 0 {
		t.Fatalf("check: %d %s", code, out)
	}
	if code, out := run("feature", "checklist", "--pr", "12"); code != 0 || !strings.Contains(out, "[x] Feature FEAT-1") || !strings.Contains(out, "checked, still right: nur umbenannt") {
		t.Errorf("checklist: %d %s", code, out)
	}
	if code, out := run("feature", "checklist", "--pr", "13"); code != 0 || !strings.Contains(out, "no feature description is touched by PR #13") {
		t.Errorf("another PR: %d %s", code, out)
	}
	if code, out := run("feature", "context", "internal/request/a.go"); code != 0 || !strings.Contains(out, "FEAT-1 Arbeitspakete (version 3)") {
		t.Errorf("context: %d %s", code, out)
	}
	if code, out := run("feature", "context", "internal/web/a.go"); code != 0 || !strings.Contains(out, "no feature has these paths") {
		t.Errorf("context of another area: %d %s", code, out)
	}

	// Die Seite aus den Features und zurück.
	page := filepath.Join(dir, "features.md")
	if code, out := run("feature", "export", "--out", page); code != 0 || !strings.Contains(out, "wrote ") {
		t.Fatalf("export: %d %s", code, out)
	}
	b, _ := os.ReadFile(page)
	if !strings.Contains(string(b), "- **Arbeitspakete**: Ein Branch trägt seine Requests.") || !strings.Contains(string(b), "Limits: Kein Abgleich mit GitHub.") {
		t.Fatalf("exported page:\n%s", b)
	}
	if code, out := run("feature", "retire", "FEAT-1", "--reason", "weg"); code != 0 || !strings.Contains(out, "retired: weg") {
		// Der Token der Konfiguration ist der Admin dieses Fixtures.
		t.Fatalf("retire: %d %s", code, out)
	}
	if code, out := run("feature", "list"); code != 0 || !strings.Contains(out, "no features") {
		t.Errorf("a retired feature is listed: %d %s", code, out)
	}
	if code, out := run("feature", "reopen", "FEAT-1"); code != 0 || !strings.Contains(out, "[active") {
		t.Errorf("reopen: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "feature"); code != 2 {
		t.Errorf("bare feature: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "feature", "bogus"); code != 2 {
		t.Errorf("unknown subcommand: %d %s", code, out)
	}
	_ = st
}

// Die vorhandenen Abschnitte von docs/features.md werden Features: erst ein
// Probelauf, dann --apply; ein zweiter Lauf schreibt Fassungen nur, wo sich etwas
// ändert, und die erzeugte Seite enthält jeden Namen.
func TestFeatureImportOfThisRepositorysFeaturesPage(t *testing.T) {
	moveCLIFixture(t)
	run := func(args ...string) (int, string) {
		t.Helper()
		return painTaskRun(t, append(args, "--project", cliHere)...)
	}
	md, mp := "../../docs/features.md", "../../docs/features-map.json"
	code, out := run("feature", "import", md, "--map", mp)
	if code != 0 || !strings.Contains(out, "dry run: 15 items") || !strings.Contains(out, "create  Work packages, 4 paths") || strings.Contains(out, "no entry in the map") {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if code, out := run("feature", "list"); !strings.Contains(out, "no features") {
		t.Fatalf("a dry run wrote: %d %s", code, out)
	}
	if code, out := run("feature", "import", md, "--map", mp, "--apply"); code != 0 || strings.Contains(out, "dry run") || strings.Count(out, "create FEAT-") != 15 {
		t.Fatalf("apply: %d %s", code, out)
	}
	if code, out := run("feature", "list"); code != 0 || strings.Count(out, "[active]") != 15 || !strings.Contains(out, "Runbooks") {
		t.Fatalf("list after the import: %d %s", code, out)
	}
	// Zweiter Lauf: aktualisiert über den Namen, legt nichts doppelt an.
	code, out = run("feature", "import", md, "--map", mp, "--apply")
	if code != 0 || strings.Contains(out, "create") || strings.Count(out, "update FEAT-") != 15 {
		t.Fatalf("second run: %d %s", code, out)
	}
	if code, out := run("feature", "show", "FEAT-8", "--versions"); code != 0 || strings.Contains(out, "v2 ") {
		t.Errorf("an unchanged item made a version: %d %s", code, out)
	}
	// Eine Seite aus den importierten Features nennt jeden Namen.
	page := filepath.Join(t.TempDir(), "features.md")
	if code, out := run("feature", "export", "--out", page); code != 0 {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(page)
	for _, name := range []string{"Scoped context", "Search", "MCP tools", "Runbooks", "Coordination"} {
		if !strings.Contains(string(b), "- **"+name+"**") {
			t.Errorf("the generated page lacks %q", name)
		}
	}
	// Eine Zuordnung, die einen Punkt der Seite nicht trifft, ist ein Fehler.
	bad := filepath.Join(t.TempDir(), "map.json")
	os.WriteFile(bad, []byte(`[{"match":"Gibt es nicht","name":"X","areas":["a"]}]`), 0o600)
	if code, out := run("feature", "import", md, "--map", bad); code == 0 || !strings.Contains(out, "matches no item") {
		t.Errorf("a map entry without an item: %d %s", code, out)
	}
	if code, out := run("feature", "import", "/nonexistent/features.md"); code == 0 {
		t.Errorf("a missing file: %d %s", code, out)
	}
	// Ohne Zuordnung sind Name und Bereiche geraten und leer; der Probelauf sagt es.
	plain := filepath.Join(t.TempDir(), "p.md")
	os.WriteFile(plain, []byte("# T\n\n- Etwas ohne Zuordnung: tut was.\n"), 0o600)
	if code, out := run("feature", "import", plain); code != 0 || !strings.Contains(out, "no entry in the map") || !strings.Contains(out, "Etwas ohne Zuordnung") {
		t.Errorf("without a map: %d %s", code, out)
	}
	_ = feature.Active
}

// ctx feature paths ohne Pfade fragt git: die Pfade, die der Branch gegenüber dem
// Stand ändert. Die Referenz darf keine Option sein.
func TestGitChangedPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, text string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "a")
	write("internal/x/old.go", "package x")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feat")
	write("internal/x/new.go", "package x")
	write("a.txt", "b")
	git("mv", "internal/x/old.go", "internal/x/renamed.go")
	git("add", ".")
	git("commit", "-q", "-m", "change")
	got, err := gitChangedPaths(dir, "main")
	if err != nil || strings.Join(got, ",") != "a.txt,internal/x/new.go,internal/x/old.go,internal/x/renamed.go" {
		t.Fatalf("paths: %v %v", got, err)
	}
	for _, ref := range []string{"--output=/tmp/x", "-p", "main..evil", "a b", ""} {
		if _, err := gitChangedPaths(dir, ref); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}
	if _, err := gitChangedPaths(dir, "does-not-exist"); err == nil || !strings.Contains(err.Error(), "--ref") {
		t.Errorf("an unknown ref: %v", err)
	}
	if _, err := gitChangedPaths("", "main"); err == nil {
		t.Error("no checkout")
	}
}

// Der Hook legt die Features des Pfades neben die Beschreibung der Datei: kurz,
// einmal je Session und Fassung, und schweigt, wo es nichts gibt.
func TestHookDeliversTheFeaturesOfAPath(t *testing.T) {
	moveCLIFixture(t)
	if code, out := painTaskRun(t, "feature", "add", "Ghost-Beschreibungen", "--summary", "Beschreibung je Datei.", "--area", "internal/ghost", "--project", cliHere); code != 0 {
		t.Fatalf("add: %d %s", code, out)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	gc := collector.GitContext{Project: cliHere}
	first := featureContextForPath(cfg, gc, "claude:s1", "internal/ghost/build.go")
	if !strings.Contains(first, "## Features in this area") || !strings.Contains(first, "FEAT-1 Ghost-Beschreibungen (version 1): Beschreibung je Datei.") {
		t.Fatalf("first delivery:\n%s", first)
	}
	if again := featureContextForPath(cfg, gc, "claude:s1", "internal/ghost/moves.go"); again != "" {
		t.Errorf("the same session got it twice:\n%s", again)
	}
	if other := featureContextForPath(cfg, gc, "claude:s2", "internal/ghost/moves.go"); other == "" {
		t.Error("another session got nothing")
	}
	if none := featureContextForPath(cfg, gc, "claude:s3", "internal/web/a.go"); none != "" {
		t.Errorf("another area:\n%s", none)
	}
	if none := featureContextForPath(cfg, collector.GitContext{}, "claude:s4", "internal/ghost/build.go"); none != "" {
		t.Errorf("without a project:\n%s", none)
	}
	// Ein Server, der nicht antwortet, kostet nur die Auskunft.
	down := cfg
	down.ServerURL = "http://127.0.0.1:1"
	if got := featureContextForPath(down, gc, "claude:s5", "internal/ghost/build.go"); got != "" {
		t.Errorf("a dead server: %q", got)
	}
}
