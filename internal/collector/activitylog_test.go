package collector

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type refusingUp struct {
	fakeUp
	calls int
}

func (u *refusingUp) RecordPathActivity([]store.PathActivity) error {
	u.calls++
	return errors.New("POST /api/activity: 403: cannot record activity for another person's session")
}

func TestRefusedActivityIsLoggedNotDropped(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	prev := activityWarn
	activityWarn = &limitedLog{interval: time.Minute, now: func() time.Time { return clock }, out: func(s string) { lines = append(lines, s) }}
	t.Cleanup(func() { activityWarn = prev })

	dir := t.TempDir()
	fp := filepath.Join(dir, "abc.jsonl")
	touch := `{"type":"assistant","cwd":"/tmp","gitBranch":"","sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/tmp/a.go"}}]}}`
	st := newTestState(dir)
	up := &refusingUp{}
	write := func(n int) {
		f, _ := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		for i := 0; i < n; i++ {
			f.WriteString(touch + "\n")
		}
		f.Close()
		if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
			t.Fatalf("a refused activity must not fail the archive: %v", err)
		}
	}
	write(1)
	if len(lines) != 1 || !strings.Contains(lines[0], "abc") || !strings.Contains(lines[0], "403") {
		t.Fatalf("a 403 must be logged with session and reason: %v", lines)
	}
	// Within the interval: rate limited. After it: logged again, with the count.
	write(1)
	write(1)
	if len(lines) != 1 {
		t.Fatalf("log must be rate limited: %v", lines)
	}
	clock = clock.Add(2 * time.Minute)
	write(1)
	if len(lines) != 2 || !strings.Contains(lines[1], "2 similar messages suppressed") {
		t.Fatalf("after the interval the log reports what it suppressed: %v", lines)
	}
	if up.calls != 4 {
		t.Fatalf("every batch still tries to record: %d", up.calls)
	}
}
