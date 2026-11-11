package collector

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestIsTranscript(t *testing.T) {
	cases := map[string]bool{
		"/h/.claude/projects/p/abc.jsonl":                                   true,
		"/h/.claude/projects/p/abc/subagents/agent-a1.jsonl":                true,
		"/h/.claude/projects/p/abc/subagents/workflows/wf_1/agent-a2.jsonl": true,
		"/h/.claude/projects/p/abc/subagents/workflows/wf_1/journal.jsonl":  false,
		"/h/.claude/projects/p/abc.json":                                    false,
	}
	for p, want := range cases {
		if got := isTranscript(p); got != want {
			t.Errorf("isTranscript(%q) = %v, want %v", p, got, want)
		}
	}
}

// conflictUp refuses every registration the way the server answers a
// session_id_collision.
type conflictUp struct {
	fakeUp
	upserts int
}

func (c *conflictUp) UpsertSession(store.Session) (int64, error) {
	c.upserts++
	return 0, fmt.Errorf("register: %w", statusErr{http.StatusConflict})
}

func TestRegistrationConflictWaitsForBackoff(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	writeLines(t, fp, 2, "hi")
	now := time.Unix(1000, 0)
	backoffNow = func() time.Time { return now }
	defer func() { backoffNow = time.Now }()
	up := &conflictUp{}
	st := newTestState(dir)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err == nil {
		t.Fatal("want the conflict back")
	}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err == nil || up.upserts != 1 {
		t.Fatalf("registered again during backoff: upserts=%d err=%v", up.upserts, err)
	}
	now = now.Add(time.Minute)
	SyncFile(fp, "claude-code", up, st, "m")
	if up.upserts != 2 {
		t.Fatalf("after backoff: upserts=%d", up.upserts)
	}
}
