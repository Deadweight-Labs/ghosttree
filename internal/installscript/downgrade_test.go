package installscript

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installedCtx puts a ctx that reports version into home's default directory.
func installedCtx(t *testing.T, home, versionLine string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "ctx")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+versionLine+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// runInHome is runWith for a home that already has a ctx in it.
func runInHome(t *testing.T, k key, home string, args ...string) run {
	t.Helper()
	srv := setupServer(t, k)
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "install.sh")
	body := strings.Replace(string(src), "__GHOSTTREE_MINISIGN_PUBKEY__", k.pub.String(), 1)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{script, "--no-modify-path"}, args...)...)
	cmd.Env = []string{"PATH=" + toolbox(t, true, false), "HOME=" + home, "TMPDIR=" + t.TempDir(), "SHELL=/bin/sh",
		"GHOSTTREE_RELEASE_BASE=" + srv + "/dl", "GHOSTTREE_API_URL=" + srv + "/api"}
	out, err := cmd.CombinedOutput()
	r := run{dir: home, out: string(out)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

// setupServer serves releases v0.1.0, v0.1.0-rc.2, v0.2.0, v0.2.0-rc.2 and v0.2.0-rc.10,
// all signed by k, and returns its address.
func setupServer(t *testing.T, k key) string {
	t.Helper()
	rel := &release{files: map[string][]byte{}}
	for _, tag := range []string{"v0.1.0", "v0.1.0-rc.2", "v0.2.0", "v0.2.0-rc.2", "v0.2.0-rc.10"} {
		rel.publish(t, k, tag, fakeCtx)
	}
	rel.latest = "v0.1.0"
	srv := httptest.NewServer(rel)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOlderReleaseThanTheInstalledOneIsRefusedWithoutTheFlag(t *testing.T) {
	k := newKey(t)
	for _, c := range []struct{ installed, tag string }{
		{"ctx 0.2.0", "v0.1.0"},
		{"ctx 0.2.0-rc.10", "v0.2.0-rc.2"}, // numeric, not text: rc.10 is newer than rc.2
		{"ctx 0.1.0", "v0.1.0-rc.2"},       // the release is newer than its candidates
		{"ctx 1.0.0", "v0.2.0"},
		{"ctx 0.2.0+abc1234", "v0.1.0"},
	} {
		t.Run(c.installed+" -> "+c.tag, func(t *testing.T) {
			home := t.TempDir()
			p := installedCtx(t, home, c.installed)
			before, _ := os.ReadFile(p)
			r := runInHome(t, k, home, "--version", c.tag)
			if r.code == 0 || !strings.Contains(r.out, "downgrade") || !strings.Contains(r.out, "--allow-downgrade") {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if after, _ := os.ReadFile(p); string(after) != string(before) {
				t.Fatalf("the installed ctx was replaced:\n%s", r.out)
			}
			r = runInHome(t, k, home, "--version", c.tag, "--allow-downgrade")
			if r.code != 0 {
				t.Fatalf("with the flag: exit %d:\n%s", r.code, r.out)
			}
			if after, _ := os.ReadFile(p); string(after) == string(before) {
				t.Fatalf("with the flag the ctx was not replaced:\n%s", r.out)
			}
		})
	}
}

func TestNewerSameOrUnknownInstalledVersionIsNoDowngrade(t *testing.T) {
	k := newKey(t)
	for _, c := range []struct{ installed, tag string }{
		{"ctx 0.1.0", "v0.2.0"},
		{"ctx 0.1.0", "v0.1.0"},             // the same release again
		{"ctx 0.1.0-dev+abc1234", "v0.1.0"}, // a development build is older than its release
		{"ctx 0.1.0-dev+abc1234", "v0.1.0-rc.2"},
		{"ctx 0.2.0-rc.2", "v0.2.0-rc.10"},
		{"ctx 0.1.0-rc.2", "v0.1.0"},
		{"something else entirely", "v0.1.0"}, // not a version: nothing to compare with
		{"", "v0.1.0"},
	} {
		t.Run(c.installed+" -> "+c.tag, func(t *testing.T) {
			home := t.TempDir()
			installedCtx(t, home, c.installed)
			if r := runInHome(t, k, home, "--version", c.tag); r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
		})
	}
}

// Without a ctx there is nothing to go back from.
func TestFirstInstallNeverCountsAsADowngrade(t *testing.T) {
	k := newKey(t)
	if r := runInHome(t, k, t.TempDir(), "--version", "v0.1.0"); r.code != 0 || !r.installed() {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
}

// The newest release comes from an answer nobody signed; what counts is the
// version the signature names, which the check compares.
func TestTheVersionCheckedIsTheSignedOne(t *testing.T) {
	k := newKey(t)
	home := t.TempDir()
	installedCtx(t, home, "ctx 0.2.0")
	// latest says v0.1.0 (an old release that is genuinely signed).
	r := runInHome(t, k, home)
	if r.code == 0 || !strings.Contains(r.out, "downgrade") || !strings.Contains(r.out, fmt.Sprintf("ghosttree %s", "v0.1.0")) {
		t.Fatalf("an old release named as the newest was installed: exit %d:\n%s", r.code, r.out)
	}
}
