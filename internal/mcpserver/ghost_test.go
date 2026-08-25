package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestNormalizeGhostPathAcceptsRepoRelativeAndRejectsEscapes(t *testing.T) {
	ok := map[string]string{
		".":                             "",
		"internal/store":                "internal/store",
		"internal/store/knowledge.go":   "internal/store/knowledge.go",
		"./internal/store/knowledge.go": "internal/store/knowledge.go",
	}
	for in, want := range ok {
		got, err := normalizeGhostPath(in)
		if err != nil {
			t.Fatalf("normalizeGhostPath(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("normalizeGhostPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"/etc/passwd", "../ausserhalb", "a\\b", ".ghosttree/tree/x.md"} {
		if _, err := normalizeGhostPath(bad); err == nil {
			t.Fatalf("normalizeGhostPath(%q) must be rejected", bad)
		}
	}
}

// Der Baum hat die Form von `git ls-files`. Eine Beschreibung fuer etwas, das
// dort nicht vorkommt, wuerde geschrieben, ausgeliefert — und waere im Baum
// nirgends zu finden. Lieber beim Schreiben nein sagen als still verlieren.
func TestTrackedInRepoRejectsWhatTheTreeCannotShow(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	for _, name := range []string{"versioniert.go", "ignoriert.log"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "versioniert.go")
	run("commit", "-m", "i")
	if err := os.WriteFile(filepath.Join(repo, "neu.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := trackedInRepo(repo, "versioniert.go"); err != nil {
		t.Fatalf("eine versionierte Datei muss beschreibbar sein: %v", err)
	}
	for _, bad := range []string{"neu.go", "ignoriert.log"} {
		err := trackedInRepo(repo, bad)
		if err == nil {
			t.Fatalf("%q ist nicht versioniert und muss abgewiesen werden", bad)
		}
		if !strings.Contains(err.Error(), "versioniert") {
			t.Fatalf("die Meldung muss den Grund nennen, got: %v", err)
		}
	}
}

// Fuenfzehn Beschreibungen hintereinander beantworteten fuenfzehnmal mit
// demselben Satz. Genau das Rauschen, gegen das die Auslieferung mit ihrer
// Einmal-je-Sitzung-Regel antritt — der Schreibpfad hatte sie nicht.
func TestTheSameNudgeIsNotRepeatedWithinASession(t *testing.T) {
	s := &Server{}
	if !s.firstMentionOf("internal") {
		t.Fatal("beim ersten Mal wird genannt")
	}
	if s.firstMentionOf("internal") {
		t.Fatal("beim zweiten Mal nicht mehr")
	}
	if !s.firstMentionOf("cmd") {
		t.Fatal("ein anderer Pfad ist ein anderer Hinweis")
	}
}

// Das go-sdk behandelt Werkzeugaufrufe asynchron (jsonrpc2.Async in
// server.go), zwei handleDescribe koennen also gleichzeitig laufen. Eine
// ungeschuetzte Map darunter ist kein theoretisches Rennen, sondern ein
// "concurrent map writes"-Absturz mitten in einer Sitzung. Mit -race gefunden.
func TestFirstMentionOfSurvivesConcurrentCalls(t *testing.T) {
	s := &Server{}
	var wg sync.WaitGroup
	var first int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.firstMentionOf("internal") {
				atomic.AddInt64(&first, 1)
			}
		}()
	}
	wg.Wait()
	if first != 1 {
		t.Fatalf("genau ein Aufrufer darf den Zuschlag bekommen, waren %d", first)
	}
}

func TestRenderGhostHitIsOneLineAndNamesThePath(t *testing.T) {
	g := store.GhostFile{Path: "internal/store/knowledge.go", Kind: "file",
		Description: "Rangfolge nach Vertrauen.\nZweite Zeile, die nicht umbrechen darf."}
	got := renderGhostHit(g)
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("a hit is exactly one line: %q", got)
	}
	if !strings.Contains(got, "internal/store/knowledge.go") {
		t.Fatalf("the path is the point of the hit: %q", got)
	}
	if !strings.Contains(got, "Zweite Zeile") {
		t.Fatalf("the snippet must survive the newline: %q", got)
	}
}
