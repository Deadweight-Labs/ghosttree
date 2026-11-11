package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	ver "github.com/Deadweight-Labs/ghosttree/internal/version"
)

func TestDoctorJudgesTheServerVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	all := ver.Capabilities
	for _, tc := range []struct {
		name       string
		client     string
		info       ver.Info
		ok, unver  bool
		detailHas  []string
		fixHas     string
		fixMissing bool
	}{
		{name: "same release", client: "0.2.0", info: ver.NewInfo("0.2.0", all), ok: true, detailHas: []string{"this ctx v0.2.0", "server v0.2.0"}, fixMissing: true},
		{name: "ctx older than the server", client: "0.1.0", info: ver.NewInfo("0.2.0", all),
			detailHas: []string{"this ctx is older than the server", "v0.1.0", "v0.2.0", "coord"}, fixHas: "ctx update"},
		{name: "server older than ctx", client: "0.3.0", info: ver.NewInfo("0.2.0", []string{"anchor", "origin"}), unver: true,
			detailHas: []string{"the server is older than this ctx", "does not offer", "runbooks"}, fixHas: "rolls out"},
		{name: "two dev builds", client: "0.2.0-dev+aaaaaaa", info: ver.NewInfo("0.2.0-dev+bbbbbbb", all), unver: true,
			detailHas: []string{"different builds"}, fixHas: "ctx update"},
		{name: "same version, server lacks a capability", client: "0.2.0", info: ver.NewInfo("0.2.0", []string{"anchor"}), unver: true,
			detailHas: []string{"does not offer", "packages"}, fixHas: "rolls out"},
		{name: "a protocol the server no longer serves", client: "0.2.0", info: ver.Info{Version: "0.9.0", Protocol: 5, MinClient: 4, Capabilities: all},
			detailHas: []string{"too old for the server", "protocol 1", "at least 4"}, fixHas: "ctx update"},
		{name: "versions that cannot be compared", client: "unknown", info: ver.NewInfo("0.2.0", all), unver: true,
			detailHas: []string{"cannot be compared"}, fixMissing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := judgeServerVersion(tc.client, tc.info)
			if c.OK != tc.ok || c.Unverified != tc.unver {
				t.Fatalf("OK %v unverified %v, want %v %v: %+v", c.OK, c.Unverified, tc.ok, tc.unver, c)
			}
			for _, want := range tc.detailHas {
				if !strings.Contains(c.Detail, want) {
					t.Errorf("detail %q misses %q", c.Detail, want)
				}
			}
			if tc.fixMissing && c.Fix != "" || !tc.fixMissing && !strings.Contains(c.Fix, tc.fixHas) {
				t.Errorf("fix %q, want it to contain %q (none: %v)", c.Fix, tc.fixHas, tc.fixMissing)
			}
		})
	}
}

func TestDoctorNamesTheChannelWhenTheServerRunsAnotherOne(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := config.Save(config.Config{ServerURL: "https://ghosttree.example.invalid", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	c := judgeServerVersion("0.1.0", ver.NewInfo("0.2.0-dev+abcdef1", ver.Capabilities))
	if !strings.Contains(c.Fix, "dev channel") || !strings.Contains(c.Fix, "ctx config set channel dev") {
		t.Fatalf("fix %q", c.Fix)
	}
}

// A server's version text comes over the wire and is shown to a person.
func TestDoctorServerVersionLineCarriesNoControlCharacters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := judgeServerVersion("0.1.0", ver.NewInfo("0.2.0\x1b[2J", ver.Capabilities))
	if strings.ContainsRune(c.Detail+c.Fix, '\x1b') {
		t.Fatalf("escape in %q / %q", c.Detail, c.Fix)
	}
}

func versionServer(t *testing.T, info *ver.Info) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/version":
			if info == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(info)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// ctx doctor reports the version of the server it is set up for, and tells a
// ctx that is older than its server what to do (REQ-436 AC-8).
func TestDoctorReportsAnOlderCtxAgainstTheServer(t *testing.T) {
	harnessesPresent(t)
	oldVersion := version
	t.Cleanup(func() { version = oldVersion })
	version = "0.1.0"
	info := ver.NewInfo("0.2.0", ver.Capabilities)
	if err := config.Save(config.Config{ServerURL: versionServer(t, &info), Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := cmdDoctor([]string{"--only", "server"}, &out)
	text := out.String()
	if code != 1 || !strings.Contains(text, "server version") || !strings.Contains(text, "FAIL") ||
		!strings.Contains(text, "this ctx is older than the server") || !strings.Contains(text, "fix: run 'ctx update'") {
		t.Fatalf("exit %d:\n%s", code, text)
	}
	if !strings.Contains(text, "server reachable  OK") {
		t.Fatalf("the reachable line is gone:\n%s", text)
	}

	// The same ctx and server fit: the line passes and the exit code is 0.
	version = "0.2.0"
	out.Reset()
	if code := cmdDoctor([]string{"--only", "server"}, &out); code != 0 || !strings.Contains(out.String(), "server version") || strings.Contains(out.String(), "FAIL") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
}

func TestDoctorSaysSoWhenTheServerHasNoVersionRoute(t *testing.T) {
	harnessesPresent(t)
	if err := config.Save(config.Config{ServerURL: versionServer(t, nil), Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmdDoctor([]string{"--only", "server"}, &out)
	if text := out.String(); !strings.Contains(text, "server version") || !strings.Contains(text, "UNVERIFIED") || !strings.Contains(text, "older server") {
		t.Fatalf("no version route:\n%s", text)
	}
}
