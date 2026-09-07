package hookbudget

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSessionBudgetPersistsAndCountsUnicodeIncludingNotice(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	total, notices := 0, 0
	for range 308 {
		if err := Deliver("one", strings.Repeat("界", 390), func(text string) error {
			total += utf8.RuneCountInString(text)
			notices += strings.Count(text, "Session hook context budget reached")
			if !utf8.ValidString(text) || total > Limit {
				t.Fatalf("invalid or excessive output: %d", total)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if total != Limit || notices != 1 {
		t.Fatalf("total=%d notices=%d", total, notices)
	}
	receipts, err := Recent()
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts=%v err=%v", receipts, err)
	}
	r := receipts[0]
	if r.ReservedChars != total || r.EmittedChars != total || !r.Exhausted {
		t.Fatalf("receipt=%+v", r)
	}
	if err := Deliver("other", "independent", func(text string) error {
		if text != "independent" {
			t.Fatalf("new session got %q", text)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedOutputRemainsReservedAndPrivate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	want := errors.New("closed stdout")
	if err := Deliver("private-session", "secret prompt text", func(string) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	receipts, err := Recent()
	if err != nil || len(receipts) != 1 {
		t.Fatalf("%v %v", receipts, err)
	}
	if r := receipts[0]; r.EmittedChars != 0 || r.ReservedChars != 18 {
		t.Fatalf("%+v", r)
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "ghosttree", "context-budget", "*.json"))
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-session") || strings.Contains(string(raw), "secret") {
		t.Fatalf("sensitive receipt: %s", raw)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	if err := os.WriteFile(files[0], []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := Deliver("private-session", "more", func(string) error { called = true; return nil }); err == nil || called {
		t.Fatalf("corruption reset budget: %v %v", err, called)
	}
}

func TestMissingIdentityAndReadOnlyDiagnostics(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	for _, session := range []string{"", "  ", strings.Repeat("s", 4097)} {
		if err := Deliver(session, "text", func(string) error { t.Fatal("emitted without identity"); return nil }); err == nil {
			t.Fatal("missing error")
		}
	}
	if got, err := Recent(); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("doctor created state")
	}
}

func TestConcurrentProcessBudget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	outputs := make(chan string, 24)
	for range 24 {
		wg.Go(func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBudgetHelperProcess$")
			cmd.Env = append(os.Environ(), "GHOSTTREE_BUDGET_HELPER=1")
			output, err := cmd.CombinedOutput()
			if err != nil {
				errs <- errors.New(string(output) + err.Error())
				return
			}
			var text string
			if err := json.Unmarshal([]byte(strings.SplitN(string(output), "\n", 2)[0]), &text); err != nil {
				errs <- err
				return
			}
			outputs <- text
		})
	}
	wg.Wait()
	close(errs)
	close(outputs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	total, notices := 0, 0
	for text := range outputs {
		total += utf8.RuneCountInString(text)
		notices += strings.Count(text, "Session hook context budget reached")
	}
	if total != Limit || notices != 1 {
		t.Fatalf("subprocess output: %d characters, %d notices", total, notices)
	}
	rs, err := Recent()
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %v", rs, err)
	}
	if r := rs[0]; r.EmittedChars != Limit || r.ReservedChars != Limit || !r.Exhausted {
		t.Fatalf("%+v", r)
	}
}

func TestBudgetHelperProcess(t *testing.T) {
	if os.Getenv("GHOSTTREE_BUDGET_HELPER") != "1" {
		return
	}
	if err := Deliver("concurrent", strings.Repeat("x", 2000), func(text string) error { return json.NewEncoder(os.Stdout).Encode(text) }); err != nil {
		t.Fatal(err)
	}
}

func TestContendedBudgetHasBoundedWait(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Deliver("locked", "hello", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	locks, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "ghosttree", "context-budget", "*.lock"))
	f, err := os.OpenFile(locks[0], os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	start := time.Now()
	if err := Deliver("locked", "more", func(string) error { t.Fatal("ignored lock"); return nil }); err == nil {
		t.Fatal("missing timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("budget blocked hook")
	}
}

func TestIncompleteReceiptCannotResetCounters(t *testing.T) {
	for _, field := range []string{"reserved_chars", "emitted_chars", "exhausted", "reserved_chars,emitted_chars"} {
		for _, value := range []string{"missing", "null"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				if err := Deliver("incomplete", "already sent", func(string) error { return nil }); err != nil {
					t.Fatal(err)
				}
				files, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "ghosttree", "context-budget", "*.json"))
				raw, _ := os.ReadFile(files[0])
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				for _, key := range strings.Split(field, ",") {
					delete(fields, key)
					if value == "null" {
						fields[key] = json.RawMessage("null")
					}
				}
				raw, _ = json.Marshal(fields)
				if err := os.WriteFile(files[0], raw, 0o600); err != nil {
					t.Fatal(err)
				}
				called := false
				if err := Deliver("incomplete", "new", func(string) error { called = true; return nil }); err == nil || called {
					t.Fatalf("incomplete state accepted: %v %v", err, called)
				}
			})
		}
	}
}

func TestRelativeStateHomeCannotSplitSessionBudgetAcrossRepos(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_STATE_HOME", "relative-state")
	called := false
	if err := Deliver("same-session", "text", func(string) error { called = true; return nil }); err == nil || called {
		t.Fatalf("relative state accepted: %v %v", err, called)
	}
	if _, err := Recent(); err == nil {
		t.Fatal("doctor did not report invalid state root")
	}
}
