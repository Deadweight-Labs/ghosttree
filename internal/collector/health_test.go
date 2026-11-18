package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// slowUp lets the first failFor chunk uploads time out and then takes them.
type slowUp struct {
	fakeUp
	failFor int
	calls   int
}

func (s *slowUp) AppendChunks(id int64, cs []store.Chunk) error {
	s.calls++
	if s.calls <= s.failFor {
		return os.ErrDeadlineExceeded
	}
	return s.fakeUp.AppendChunks(id, cs)
}

func noRetryPause(t *testing.T) {
	t.Helper()
	old := syncRetryPause
	syncRetryPause = 0
	t.Cleanup(func() { syncRetryPause = old })
}

// REQ-690 AC-1 (Lauf 3, pia/051): the server did not answer an upload in time.
// The sweep repeats it right away instead of leaving the session without text
// for another half minute, and the report is clean afterwards.
func TestSweepRepeatsAnUploadThatTimedOut(t *testing.T) {
	noRetryPause(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(root, "abc.jsonl")
	writeLines(t, fp, 3, "hi")
	st := newTestState(dir)
	up := &slowUp{failFor: 2}
	if err := Sweep(map[string]string{root: "claude-code"}, up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if up.calls != 3 || len(up.chunks[1]) != 3 {
		t.Fatalf("uploads %d, chunks %d; want taken on the third try", up.calls, len(up.chunks[1]))
	}
	h, ok := ReadHealth(st.path)
	if !ok || len(h.Failing) != 0 || h.Describe() != "uploading normally" {
		t.Fatalf("health %+v ok=%v", h, ok)
	}
}

// What still fails after the repetitions is reported: which transcript, since
// when, why. The next sweep tries again, and a success clears the entry.
func TestSweepReportsWhatTheServerDidNotTake(t *testing.T) {
	noRetryPause(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "projects")
	os.MkdirAll(root, 0o755)
	fp := filepath.Join(root, "abc.jsonl")
	writeLines(t, fp, 2, "hi")
	st := newTestState(dir)
	up := &slowUp{failFor: 100}
	if err := Sweep(map[string]string{root: "claude-code"}, up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if up.calls != 1+syncRetries {
		t.Fatalf("uploads %d, want the first try and %d repetitions", up.calls, syncRetries)
	}
	h, ok := ReadHealth(st.path)
	if !ok || len(h.Failing) != 1 || h.Failing[0].File != fp || !strings.Contains(h.Failing[0].Error, "timeout") {
		t.Fatalf("health %+v ok=%v", h, ok)
	}
	if d := h.Describe(); !strings.Contains(d, "1 transcript(s) could not be uploaded") || !strings.Contains(d, "tries again") {
		t.Errorf("description %q", d)
	}
	up.failFor = 0
	if err := Sweep(map[string]string{root: "claude-code"}, up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if h, ok := ReadHealth(st.path); !ok || len(h.Failing) != 0 {
		t.Fatalf("health after the server took it: %+v", h)
	}
}

// A report from a collector that no longer runs is not shown as current.
func TestStaleHealthIsNotShown(t *testing.T) {
	dir := t.TempDir()
	st := newTestState(dir)
	if err := st.WriteHealth(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadHealth(st.path); !ok {
		t.Fatal("fresh report not read")
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(HealthPath(st.path), old, old); err != nil {
		t.Fatal(err)
	}
	// The age comes from the content, not from the file time.
	b, _ := os.ReadFile(HealthPath(st.path))
	b = []byte(strings.Replace(string(b), time.Now().UTC().Format("2006-01-02"), "2020-01-01", 1))
	os.WriteFile(HealthPath(st.path), b, 0o600)
	if _, ok := ReadHealth(st.path); ok {
		t.Fatal("stale report shown as current")
	}
}
