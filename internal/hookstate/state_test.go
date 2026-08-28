package hookstate

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentProcessesDoNotLoseReceipts(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	const count = 24
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecordHelperProcess$")
			cmd.Env = append(os.Environ(),
				"GHOSTTREE_HOOKSTATE_HELPER="+strconv.Itoa(i),
				"XDG_STATE_HOME="+stateHome,
			)
			errs <- cmd.Run()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < count; i++ {
		if _, ok, err := Latest("codex", "event-"+strconv.Itoa(i)); err != nil || !ok {
			t.Errorf("receipt %d: found=%v err=%v", i, ok, err)
		}
	}
}

func TestRecordHelperProcess(t *testing.T) {
	value := os.Getenv("GHOSTTREE_HOOKSTATE_HELPER")
	if value == "" {
		return
	}
	if err := RecordAt("codex", "event-"+value, time.Now().UTC()); err != nil {
		os.Exit(1)
	}
}

func TestRecordStoresOnlyHarnessEventAndTimestamp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	if err := RecordAt("codex", "PreToolUse", now); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"codex", "PreToolUse", "2026-08-28"} {
		if !strings.Contains(text, want) {
			t.Errorf("receipt missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"prompt", "command", "path", "session", "tool_input"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("receipt leaks %q: %s", forbidden, text)
		}
	}
	info, err := os.Stat(DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o", got)
	}
}

func TestRecordRateLimitsSameHarnessEvent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	if err := RecordAt("codex", "PreToolUse", first); err != nil {
		t.Fatal(err)
	}
	if err := RecordAt("codex", "PreToolUse", first.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	receipt, ok, err := Latest("codex", "PreToolUse")
	if err != nil || !ok {
		t.Fatalf("latest = %#v, %v, %v", receipt, ok, err)
	}
	if !receipt.SeenAt.Equal(first) {
		t.Fatalf("rate-limited timestamp = %s, want %s", receipt.SeenAt, first)
	}
}
