package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
	"github.com/Deadweight-Labs/ghosttree/internal/selfupdate"
	ver "github.com/Deadweight-Labs/ghosttree/internal/version"
)

// updateEnv points ctx update at temp files and fake servers; it records the
// systemctl calls and restores every hook.
type updateEnv struct {
	target    string
	systemctl [][]string
	pk        minisign.PublicKey
	priv      ed25519.PrivateKey
	foreign   bool // sign with a key the release key does not match
}

// updEnv is the environment of the running test; releaseServer and devServerFor
// sign with it.
var updEnv *updateEnv

func (e *updateEnv) sign(sums []byte, comment string) []byte {
	pk, priv := e.pk, e.priv
	if e.foreign {
		var err error
		if pk, priv, err = minisign.GenerateKey(rand.Reader); err != nil {
			panic(err)
		}
	}
	return minisign.Sign(pk, priv, sums, comment)
}

func setupUpdate(t *testing.T, clientVersion string) *updateEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	e := &updateEnv{target: filepath.Join(t.TempDir(), "ctx")}
	var err error
	if e.pk, e.priv, err = minisign.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	updEnv = e
	os.WriteFile(e.target, []byte("OLD"), 0o755)
	oldV, oldTarget, oldVerifier, oldSys, oldBase, oldGOOS, oldGOARCH, oldHTTP := version, updateTargetPath, updateVerifier, systemctlUser, updateAPIBase, updateGOOS, updateGOARCH, updateHTTP
	t.Cleanup(func() {
		version, updateTargetPath, updateVerifier, systemctlUser, updateAPIBase, updateGOOS, updateGOARCH, updateHTTP = oldV, oldTarget, oldVerifier, oldSys, oldBase, oldGOOS, oldGOARCH, oldHTTP
	})
	version = clientVersion
	updateGOOS, updateGOARCH = "linux", "amd64"
	updateTargetPath = func() (string, error) { return e.target, nil }
	updateVerifier = func() selfupdate.Verifier {
		return selfupdate.MinisignVerifier(func() (minisign.PublicKey, error) { return e.pk, nil })
	}
	systemctlUser = func(args ...string) error {
		e.systemctl = append(e.systemctl, args)
		return nil
	}
	// Kein echtes systemd: ohne Unit-Angabe gilt das aktualisierte ctx als das
	// des Dienstes.
	oldUnit := watchUnitState
	t.Cleanup(func() { watchUnitState = oldUnit })
	watchUnitState = func() unitState { return unitState{} }
	return e
}

func tgz(content string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "ctx", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releaseServer fakes the GitHub release API and the asset downloads.
func releaseServer(t *testing.T, tag string, signed bool, badSum bool) {
	t.Helper()
	name := fmt.Sprintf("ctx_%s_linux_amd64.tar.gz", strings.TrimPrefix(tag, "v"))
	archive := tgz("NEWSTABLE")
	h := sha256.Sum256(archive)
	sum := hex.EncodeToString(h[:])
	if badSum {
		sum = strings.Repeat("0", 64)
	}
	sums := []byte(fmt.Sprintf("%s  %s\n", sum, name))
	sig := updEnv.sign(sums, "ghosttree "+tag)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/Deadweight-Labs/ghosttree/releases", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]string{
			{"name": name, "browser_download_url": base + "/a/" + name},
			{"name": "checksums.txt", "browser_download_url": base + "/a/checksums.txt"},
		}
		if signed {
			assets = append(assets, map[string]string{"name": "checksums.txt.minisig", "browser_download_url": base + "/a/sig"})
		}
		json.NewEncoder(w).Encode([]any{map[string]any{"tag_name": tag, "prerelease": strings.Contains(tag, "-"), "assets": assets}})
	})
	mux.HandleFunc("/a/checksums.txt", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux.HandleFunc("/a/sig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig) })
	mux.HandleFunc("/a/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	updateAPIBase, updateHTTP = srv.URL, srv.Client()
}

func runUpd(args ...string) (string, int) {
	var out bytes.Buffer
	code := run(args, &out)
	return out.String(), code
}

func readTarget(t *testing.T, e *updateEnv) string {
	t.Helper()
	b, _ := os.ReadFile(e.target)
	return string(b)
}

func TestUpdateStableInstallsASignedRelease(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	releaseServer(t, "v0.2.0", true, false)
	out, code := runUpd("update")
	if code != 0 || readTarget(t, e) != "NEWSTABLE" {
		t.Fatalf("code %d target %q\n%s", code, readTarget(t, e), out)
	}
	if _, err := os.Stat(e.target + ".prev-0.1.0-dev+1c48c74"); err != nil {
		t.Fatalf("no backup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "v0.2.0") || !strings.Contains(out, "MCP") {
		t.Fatalf("output lacks version or the MCP note:\n%s", out)
	}
	if len(e.systemctl) != 2 || e.systemctl[1][0] != "restart" || e.systemctl[1][1] != watchUnit {
		t.Fatalf("systemctl calls %v", e.systemctl)
	}
}

func TestUpdateStableWithBadSignatureChecksumOrNoSignatureChangesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		signed, badSum bool
		foreign        bool
	}{
		"signature rejected": {signed: true, foreign: true},
		"wrong checksum":     {signed: true, badSum: true},
		"no signature file":  {signed: false},
	} {
		t.Run(name, func(t *testing.T) {
			e := setupUpdate(t, "0.1.0-dev+1c48c74")
			e.foreign = c.foreign
			releaseServer(t, "v0.2.0", c.signed, c.badSum)
			out, code := runUpd("update")
			if code == 0 || !strings.Contains(out, "nothing changed") && !strings.Contains(out, "no update") {
				t.Fatalf("code %d\n%s", code, out)
			}
			entries, _ := os.ReadDir(filepath.Dir(e.target))
			if readTarget(t, e) != "OLD" || len(entries) != 1 || len(e.systemctl) != 0 {
				t.Fatalf("state changed: target %q entries %d systemctl %v", readTarget(t, e), len(entries), e.systemctl)
			}
		})
	}
}

func TestUpdateCheckOnlyShowsAndTheSameVersionIsCurrent(t *testing.T) {
	e := setupUpdate(t, "0.1.0")
	releaseServer(t, "v0.2.0", true, false)
	out, code := runUpd("update", "--check")
	if code != 0 || !strings.Contains(out, "v0.2.0") || !strings.Contains(out, "changed nothing") || readTarget(t, e) != "OLD" {
		t.Fatalf("code %d\n%s", code, out)
	}
	version = "0.2.0"
	if out, _ := runUpd("update"); !strings.Contains(out, "already up to date") || readTarget(t, e) != "OLD" {
		t.Fatalf("expected up to date:\n%s", out)
	}
	version = "0.3.0"
	if out, code := runUpd("update"); code == 0 || !strings.Contains(out, "refusing to go back") || readTarget(t, e) != "OLD" {
		t.Fatalf("downgrade not refused:\n%s", out)
	}
}

// devServerFor fakes the server: /dist/ for the dev channel, without a signature.
func devServerFor(t *testing.T, distVersion string) *httptest.Server {
	return devServerSigned(t, distVersion, "")
}

// devServerSigned is devServerFor with a checksums.txt.minisig carrying
// signedComment (empty: none).
func devServerSigned(t *testing.T, distVersion, signedComment string) *httptest.Server {
	t.Helper()
	name := fmt.Sprintf("ctx_%s_linux_amd64.tar.gz", distVersion)
	archive := tgz("NEWDEV")
	h := sha256.Sum256(archive)
	sums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(h[:]), name))
	mux := http.NewServeMux()
	mux.HandleFunc("/dist/checksums.txt", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	if signedComment != "" {
		sig := updEnv.sign(sums, signedComment)
		mux.HandleFunc("/dist/checksums.txt.minisig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig) })
	}
	mux.HandleFunc("/dist/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateDevTakesTheServersBuild(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	srv := devServerFor(t, "0.1.0-dev+b0cd1b9")
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m", Channel: "dev"}); err != nil {
		t.Fatal(err)
	}
	out, code := runUpd("update", "--trust-server") // channel comes from the config
	if code != 0 || readTarget(t, e) != "NEWDEV" || !strings.Contains(out, "0.1.0-dev+b0cd1b9") || !strings.Contains(out, "MCP") {
		t.Fatalf("code %d target %q\n%s", code, readTarget(t, e), out)
	}
	if !strings.Contains(out, "only the server") {
		t.Fatalf("the output must say that only the server vouches for the binary:\n%s", out)
	}
	if fi, _ := os.Stat(e.target); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestUpdateDevWithoutSignatureNeedsTheUsersAgreement(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	srv := devServerFor(t, "0.1.0-dev+b0cd1b9")
	config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m", Channel: "dev"})
	out, code := runUpd("update")
	if code == 0 || readTarget(t, e) != "OLD" || !strings.Contains(out, "--trust-server") || !strings.Contains(out, "server alone is trusted") {
		t.Fatalf("an unsigned server build must not be installed on its own: code %d\n%s", code, out)
	}
	if entries, _ := os.ReadDir(filepath.Dir(e.target)); len(entries) != 1 {
		t.Fatalf("directory changed: %v", entries)
	}
}

func TestUpdateDevTakesAServerBuildTheReleaseKeySigned(t *testing.T) {
	e := setupUpdate(t, "0.1.0")
	srv := devServerSigned(t, "0.2.0", "ghosttree v0.2.0")
	config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m", Channel: "dev"})
	out, code := runUpd("update")
	if code != 0 || readTarget(t, e) != "NEWDEV" || !strings.Contains(out, "signed with the release key") || strings.Contains(out, "only the server") {
		t.Fatalf("code %d target %q\n%s", code, readTarget(t, e), out)
	}
}

func TestUpdateDevSignatureThatDoesNotHoldIsNeverOverriddenByTrustServer(t *testing.T) {
	for name, c := range map[string]struct {
		foreign bool
		comment string
	}{
		"other key":        {foreign: true, comment: "ghosttree v0.2.0"},
		"another release":  {comment: "ghosttree v0.1.5"},
		"no trusted label": {comment: "something"},
	} {
		t.Run(name, func(t *testing.T) {
			e := setupUpdate(t, "0.1.0")
			e.foreign = c.foreign
			srv := devServerSigned(t, "0.2.0", c.comment)
			config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m", Channel: "dev"})
			out, code := runUpd("update", "--trust-server")
			if code == 0 || readTarget(t, e) != "OLD" {
				t.Fatalf("code %d\n%s", code, out)
			}
		})
	}
}

func TestUpdateTrustServerIsOnlyForTheDevChannel(t *testing.T) {
	setupUpdate(t, "0.1.0")
	for _, args := range [][]string{{"update", "--trust-server"}, {"update", "--trust-server", "--channel", "stable"}, {"update", "--trust-server", "--source"}} {
		if out, code := runUpd(args...); code != 2 || !strings.Contains(out, "--trust-server") {
			t.Fatalf("%v: code %d\n%s", args, code, out)
		}
	}
}

func TestUpdateDevRefusesAnOlderServerBuildAndNeedsAServer(t *testing.T) {
	e := setupUpdate(t, "0.2.0-dev+aaaaaaa")
	srv := devServerFor(t, "0.1.0-dev+b0cd1b9")
	if out, code := runUpd("update", "--channel", "dev"); code == 0 || !strings.Contains(out, "run 'ctx setup'") {
		t.Fatalf("no config must be explained:\n%s", out)
	}
	config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"})
	if out, code := runUpd("update", "--channel", "dev", "--trust-server"); code == 0 || !strings.Contains(out, "refusing to go back") || readTarget(t, e) != "OLD" {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestUpdateRestartHintWithoutAWatchUnit(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev")
	systemctlUser = func(...string) error { return errors.New("inactive") }
	releaseServer(t, "v0.2.0", true, false)
	out, _ := runUpd("update")
	if readTarget(t, e) != "NEWSTABLE" || !strings.Contains(out, "no "+watchUnit+" running") {
		t.Fatalf("\n%s", out)
	}
}

func TestUpdateRejectsBadChannelAndSourceWithStable(t *testing.T) {
	setupUpdate(t, "0.1.0")
	if _, code := runUpd("update", "--channel", "nightly"); code != 2 {
		t.Fatal("unknown channel accepted")
	}
	if _, code := runUpd("update", "--source", "--channel", "stable"); code != 2 {
		t.Fatal("--source with stable accepted")
	}
}

func TestConfigChannelDefaultsToStableAndPersists(t *testing.T) {
	setupUpdate(t, "0.1.0")
	// An existing config.json without a channel stays valid.
	os.MkdirAll(filepath.Dir(config.Path()), 0o755)
	os.WriteFile(config.Path(), []byte(`{"server_url":"https://x.example.invalid","token":"t","machine":"m"}`), 0o600)
	if out, code := runUpd("config", "get", "channel"); code != 0 || strings.TrimSpace(out) != "stable" {
		t.Fatalf("%d %q", code, out)
	}
	if _, code := runUpd("config", "set", "channel", "dev"); code != 0 {
		t.Fatal("set failed")
	}
	if out, _ := runUpd("config", "get", "channel"); strings.TrimSpace(out) != "dev" {
		t.Fatalf("%q", out)
	}
	if out, code := runUpd("config", "set", "channel", "beta"); code != 2 || !strings.Contains(out, "unknown channel") {
		t.Fatalf("%d %q", code, out)
	}
	cfg, _ := config.Load()
	if cfg.Token != "t" || cfg.ServerURL != "https://x.example.invalid" || cfg.Channel != "dev" {
		t.Fatalf("config %+v", cfg)
	}
	if out, _ := runUpd("config", "get"); strings.Contains(out, "token") || strings.Contains(out, " t\n") {
		t.Fatalf("token printed: %q", out)
	}
}

func TestSetupKeepsTheChannel(t *testing.T) {
	setupUpdate(t, "0.1.0")
	config.Save(config.Config{ServerURL: "http://127.0.0.1:1", Token: "t", Machine: "m", Channel: "dev"})
	runUpd("setup", "--server", "http://127.0.0.1:1", "--token", "n")
	if cfg, _ := config.Load(); cfg.Channel != "dev" || cfg.Token != "n" {
		t.Fatalf("config %+v", cfg)
	}
}

func TestStatusShowsClientServerVersionAndWarnsWhenOlder(t *testing.T) {
	setupUpdate(t, "0.1.0-dev+1c48c74")
	for _, c := range []struct {
		server  ver.Info
		want    []string
		notWant string
	}{
		{ver.NewInfo("0.1.0-dev+b0cd1b9", ver.Capabilities), []string{"client 0.1.0-dev+1c48c74 (dev, protocol 1)", "server 0.1.0-dev+b0cd1b9 (dev, protocol 1)", "channel stable", "warning"}, ""},
		{ver.NewInfo("0.3.0", ver.Capabilities), []string{"server v0.3.0 (stable", "older than the server"}, ""},
		{ver.NewInfo("0.1.0-dev+1c48c74", ver.Capabilities), []string{"client 0.1.0-dev+1c48c74"}, "warning"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/version" {
				json.NewEncoder(w).Encode(c.server)
				return
			}
			w.Write([]byte(`{"ok":true}`))
		}))
		config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"})
		out, _ := runUpd("status")
		srv.Close()
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("status lacks %q:\n%s", w, out)
			}
		}
		if c.notWant != "" && strings.Contains(out, c.notWant) {
			t.Errorf("status has %q:\n%s", c.notWant, out)
		}
	}
}

func TestStatusWarnsWhenTheServerNeedsANewerProtocol(t *testing.T) {
	setupUpdate(t, "0.9.0")
	info := ver.NewInfo("1.0.0", nil)
	info.MinClient = ver.Protocol + 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			json.NewEncoder(w).Encode(info)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"})
	out, code := runUpd("status")
	if !strings.Contains(out, "needs at least") || code == 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestStatusWithAnOlderServerWithoutVersionRoute(t *testing.T) {
	setupUpdate(t, "0.1.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"})
	if out, _ := runUpd("status"); !strings.Contains(out, "server unknown") {
		t.Fatalf("\n%s", out)
	}
}

func TestUpdatePreInstallsAReleaseCandidateAfterTheUsualChecks(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	releaseServer(t, "v0.2.0-rc.1", true, false)

	out, code := runUpd("update")
	if code == 0 || readTarget(t, e) != "OLD" || !strings.Contains(out, "--pre") {
		t.Fatalf("without --pre the candidate must stay out and the output must name --pre: code %d target %q\n%s", code, readTarget(t, e), out)
	}

	out, code = runUpd("update", "--pre")
	if code != 0 || readTarget(t, e) != "NEWSTABLE" || !strings.Contains(out, "v0.2.0-rc.1") {
		t.Fatalf("code %d target %q\n%s", code, readTarget(t, e), out)
	}
}

func TestUpdatePreKeepsSignatureAndChecksumChecks(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	e.foreign = true
	releaseServer(t, "v0.2.0-rc.1", true, false)
	if out, code := runUpd("update", "--pre"); code == 0 || readTarget(t, e) != "OLD" {
		t.Fatalf("rejected signature: code %d target %q\n%s", code, readTarget(t, e), out)
	}
	e.foreign = false
	releaseServer(t, "v0.2.0-rc.1", true, true)
	if out, code := runUpd("update", "--pre"); code == 0 || readTarget(t, e) != "OLD" {
		t.Fatalf("wrong checksum: code %d target %q\n%s", code, readTarget(t, e), out)
	}
	releaseServer(t, "v0.2.0-rc.1", false, false)
	if out, code := runUpd("update", "--pre"); code == 0 || readTarget(t, e) != "OLD" {
		t.Fatalf("no signature: code %d target %q\n%s", code, readTarget(t, e), out)
	}
}

func TestUpdatePreIsOnlyForTheStableChannel(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev+1c48c74")
	for _, args := range [][]string{{"update", "--pre", "--channel", "dev"}, {"update", "--pre", "--source"}} {
		if out, code := runUpd(args...); code != 2 || readTarget(t, e) != "OLD" {
			t.Fatalf("%v: code %d\n%s", args, code, out)
		}
	}
}

// Startet der Dienst ein anderes ctx als das gerade aktualisierte, bleibt er in
// Ruhe: ein Neustart brächte das neue Binary nicht in Gang.
func TestUpdateLeavesAWatchUnitOfAnotherCtxAlone(t *testing.T) {
	e := setupUpdate(t, "0.1.0-dev")
	other := filepath.Join(t.TempDir(), "ctx")
	if err := os.WriteFile(other, []byte("OTHER"), 0o755); err != nil {
		t.Fatal(err)
	}
	watchUnitState = func() unitState { return unitState{Active: true, ExecPath: other} }
	releaseServer(t, "v0.2.0", true, false)
	out, _ := runUpd("update")
	if readTarget(t, e) != "NEWSTABLE" || !strings.Contains(out, "not this ctx; left alone") {
		t.Fatalf("\n%s", out)
	}
	for _, call := range e.systemctl {
		if len(call) > 0 && call[0] == "restart" {
			t.Fatalf("restarted the unit of another ctx: %v", e.systemctl)
		}
	}
}
