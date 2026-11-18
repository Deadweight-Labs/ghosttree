package serverinstall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSystem stands in for systemd and the server. The "binaries" it runs are
// files whose content is "v:<version>"; a flag "broken" after the version never
// answers on the health check, "badschema" fails upgrade-schema. It records every command.
type fakeSystem struct {
	t      *testing.T
	prefix string

	mu       sync.Mutex
	cmds     []string
	accounts string // output of account list
	running  bool
	// failBackup makes db backup fail.
	failBackup bool
}

func newFakeSystem(t *testing.T, prefix string) *fakeSystem {
	return &fakeSystem{t: t, prefix: prefix}
}

func (f *fakeSystem) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, s)
}

func (f *fakeSystem) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeSystem) saw(sub string) bool {
	for _, c := range f.commands() {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func (f *fakeSystem) index(sub string) int {
	for i, c := range f.commands() {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func (f *fakeSystem) binVersion() string {
	v, _ := versionOf(filepath.Join(f.prefix, BinPath))
	return v
}

// versionOf reads a fake binary: "v:<version> [flags]".
func versionOf(path string) (string, bool) {
	v, _, ok := fakeBinary(path)
	return v, ok
}

func fakeBinary(path string) (version string, flags []string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(b), "v:") {
		return "", nil, false
	}
	fields := strings.Fields(strings.TrimPrefix(string(b), "v:"))
	if len(fields) == 0 {
		return "", nil, false
	}
	return fields[0], fields[1:], true
}

func hasFlag(path, flag string) bool {
	_, flags, _ := fakeBinary(path)
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}

func (f *fakeSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	f.log(name + " " + strings.Join(args, " "))
	switch name {
	case "systemctl":
		switch args[0] {
		case "start", "restart":
			if args[1] == ServiceID {
				f.running = true
			}
			if args[1] == BackupID { // the unit runs ctx db backup
				dir := filepath.Join(f.prefix, BackupDir)
				os.MkdirAll(dir, 0o755)
				os.WriteFile(filepath.Join(dir, "ghosttree-20260101-030000.db"), []byte("db"), 0o600)
			}
		case "stop":
			if args[1] == ServiceID {
				f.running = false
			}
		case "is-active":
			if f.running || args[1] != ServiceID {
				return "active\n", nil
			}
			return "inactive\n", fmt.Errorf("exit status 3")
		}
		return "", nil
	case "systemd-run":
		// systemd-run <options> <bin> <args...>: the options end at the first
		// argument that is not an option or the value of -p.
		i := 0
		for i < len(args) {
			if args[i] == "-p" {
				i += 2
			} else if strings.HasPrefix(args[i], "--") {
				i++
			} else {
				break
			}
		}
		bin, rest := args[i], args[i+1:]
		return f.ctxCommand(bin, rest)
	default:
		if v, ok := versionOf(name); ok && len(args) == 1 && args[0] == "version" {
			return "ctx " + v + "\n", nil
		}
	}
	return "", fmt.Errorf("fake: unexpected command %s", name)
}

func (f *fakeSystem) ctxCommand(bin string, args []string) (string, error) {
	ver, _ := versionOf(bin)
	badSchema := hasFlag(bin, "badschema")
	switch strings.Join(args[:min(len(args), 2)], " ") {
	case "account list":
		return f.accounts, nil
	case "account add":
		name := args[2]
		kind := "person"
		f.accounts += fmt.Sprintf("person:%d\t%s\tkind=%s state=active admin=%t email=\n", len(strings.Split(f.accounts, "\n")), name, kind, name != "gardener")
		return "account created\n", nil
	case "account set-kind":
		return "ok\n", nil
	case "account set-gardener":
		lines := strings.Split(strings.TrimRight(f.accounts, "\n"), "\n")
		for i, l := range lines {
			if strings.Contains(l, "\t"+args[2]+"\t") {
				lines[i] = l + " gardener=true"
			}
		}
		f.accounts = strings.Join(lines, "\n") + "\n"
		return "ok\n", nil
	case "account login-link":
		return "https://login.example/ui/login/code?code=ABCD\nvalid 15m, single use\n", nil
	case "db check":
		return "ok\n", nil
	case "db backup":
		if f.failBackup {
			return "disk full\n", fmt.Errorf("exit status 1")
		}
		out := args[indexOf(args, "--out")+1]
		full := filepath.Join(f.prefix, out)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("db before "+ver), 0o600)
		return "backup " + out + "\n", nil
	case "db restore":
		return "restored\n", nil
	case "upgrade-schema --db":
		if badSchema {
			return "upgrade failed\n", fmt.Errorf("exit status 1")
		}
		return "schema already current, nothing to do\n", nil
	}
	return "", fmt.Errorf("fake: unexpected ctx command %v", args)
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func (f *fakeSystem) Get(ctx context.Context, url string) ([]byte, error) {
	f.log("GET " + url)
	if strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("no route to host")
	}
	if !f.running {
		return nil, fmt.Errorf("connection refused")
	}
	v := f.binVersion()
	if hasFlag(filepath.Join(f.prefix, BinPath), "broken") {
		return nil, fmt.Errorf("HTTP 500")
	}
	if strings.HasSuffix(url, "/api/version") {
		return []byte(fmt.Sprintf(`{"version":%q}`, v)), nil
	}
	return []byte(`{"ok":true}`), nil
}

func testEnv(sys System) (Env, *bytes.Buffer) {
	var out bytes.Buffer
	return Env{Sys: sys, Out: &out, HealthTimeout: 400 * time.Millisecond, Poll: 20 * time.Millisecond}, &out
}
