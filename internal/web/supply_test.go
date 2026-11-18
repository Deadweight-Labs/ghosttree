package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
)

// Was die Seite zum Kopieren zeigt, geht in eine Shell: ein Host aus der Anfrage
// darf nur aus Hostname-Zeichen bestehen, sonst gibt es keinen Befehl.
func TestJoinCommandNeverPutsAnUnsafeHostIntoShellText(t *testing.T) {
	trusted, _ := proxytrust.Parse("127.0.0.1")
	a := &app{proxies: trusted}
	for _, host := range []string{"a$(id)", "evil.example`id`", "x';touch${IFS}/tmp/pwn;'", "a;b", "a&b", "a|b", "a*b", "a!b", "a(b)", "a,b", "a=b", "gt.example:80x"} {
		req := httptest.NewRequest("GET", "/join/pair", nil)
		req.Host = host
		req.TLS = &tls.ConnectionState{}
		if c := a.joinCommand(req, "ABCD-EFGH"); c != "" {
			t.Errorf("Host %q gave a command: %s", host, c)
		}
		fwd := httptest.NewRequest("GET", "/join/pair", nil)
		fwd.RemoteAddr = "127.0.0.1:5555"
		fwd.Header.Set("X-Forwarded-Proto", "https")
		fwd.Header.Set("X-Forwarded-Host", host)
		if c := a.joinCommand(fwd, "ABCD-EFGH"); c != "" {
			t.Errorf("X-Forwarded-Host %q gave a command: %s", host, c)
		}
		if b := a.joinBase(req); strings.ContainsAny(b, "$`';&|*!(),=") {
			t.Errorf("Host %q reached joinBase: %s", host, b)
		}
	}
	for host, want := range map[string]string{
		"gt.example.com":      "curl -fsSL https://gt.example.com/install.sh | sh -s -- --pair ABCD-EFGH",
		"gt.example.com:8443": "curl -fsSL https://gt.example.com:8443/install.sh | sh -s -- --pair ABCD-EFGH",
		"[::1]:8474":          "curl -fsSL https://[::1]:8474/install.sh | sh -s -- --pair ABCD-EFGH",
	} {
		req := httptest.NewRequest("GET", "/join/pair", nil)
		req.Host = host
		req.TLS = &tls.ConnectionState{}
		if c := a.joinCommand(req, "ABCD-EFGH"); c != want {
			t.Errorf("Host %q: %q, want %q", host, c, want)
		}
	}
}

// Mit GHOSTTREE_PUBLIC_URL entscheidet der Host der Anfrage nichts.
func TestJoinCommandUsesThePublicURLWhenSet(t *testing.T) {
	a := &app{}
	WithPublicURL("https://gt.example.com")(a)
	req := httptest.NewRequest("GET", "/join/pair", nil)
	req.Host = "evil.example$(id)"
	if c := a.joinCommand(req, "ABCD-EFGH"); c != "curl -fsSL https://gt.example.com/install.sh | sh -s -- --pair ABCD-EFGH" {
		t.Fatalf("command %q", c)
	}
}

// Eine öffentliche Adresse, die nicht Hostname[:Port] ist, kommt nie in den
// Befehl, auch wenn sie ohne WithPublicURL gesetzt wurde.
func TestJoinCommandRefusesAnUnsafePublicOrigin(t *testing.T) {
	for _, origin := range []string{"https://a$(id)", "https://user@gt.example.com", "https://gt.example.com/x", "https://gt.example.com'", "gt.example.com"} {
		a := &app{publicOrigin: origin}
		if c := a.joinCommand(httptest.NewRequest("GET", "/join/pair", nil), "ABCD-EFGH"); c != "" {
			t.Errorf("public origin %q gave a command: %s", origin, c)
		}
		rec := serveRawApp(a, "gt.example.com")
		if strings.Contains(rec, "$(id)") {
			t.Errorf("public origin %q reached install.sh: %s", origin, rec)
		}
	}
}

func serveRawApp(a *app, host string) string {
	req := httptest.NewRequest("GET", "/install.sh", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	a.distDir = "unused"
	a.installSh(rec, req)
	return rec.Body.String()
}

// Die Signatur der Prüfsummen steht neben ihnen, auch wenn checksums.txt sie nicht
// aufführt (sie kann sich nicht selbst enthalten); andere nicht gelistete Dateien
// bleiben unter 404.
func TestDistServesTheSignatureOfTheChecksums(t *testing.T) {
	dir := distDir(t, fakeCtx)
	srv := distServer(t, dir)
	if resp, _ := get(t, srv.URL+"/dist/checksums.txt.minisig"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no signature file: %d", resp.StatusCode)
	}
	sig := "untrusted comment: signature\nAAAA\ntrusted comment: ghosttree v1.2.3\nBBBB\n"
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt.minisig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, srv.URL+"/dist/checksums.txt.minisig")
	if resp.StatusCode != http.StatusOK || body != sig || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("signature: %d %q cache %q", resp.StatusCode, body, resp.Header.Get("Cache-Control"))
	}
	if resp, _ := get(t, srv.URL+"/dist/unlisted.txt"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unlisted file was served: %d", resp.StatusCode)
	}
	// Auch die Signatur ist nur eine reguläre Datei im Verzeichnis, kein Symlink.
	if err := os.Remove(filepath.Join(dir, "checksums.txt.minisig")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "unlisted.txt"), filepath.Join(dir, "checksums.txt.minisig")); err != nil {
		t.Skip("no symlinks")
	}
	if resp, _ := get(t, srv.URL+"/dist/checksums.txt.minisig"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a symlinked signature was served: %d", resp.StatusCode)
	}
}

// "http://127.0.0.1:1@evil.example/" geht an evil.example: ein "@" im Hostteil
// hebt die Ausnahme für diese Maschine auf, und ein Schalter, der Klartext zu
// jedem Host erlaubt, gibt es nicht.
func TestInstallScriptPlainHTTPIsForThisMachineOnly(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	_, sh := get(t, srv.URL+"/install.sh")
	for name, env := range map[string][]string{
		"userinfo":        {"GHOSTTREE_DOWNLOAD_BASE=http://127.0.0.1:1@evil.example/x"},
		"userinfo ipv6":   {"GHOSTTREE_DOWNLOAD_BASE=http://[::1]:1@evil.example/x"},
		"userinfo name":   {"GHOSTTREE_DOWNLOAD_BASE=http://localhost:1@evil.example/x"},
		"backslash":       {"GHOSTTREE_DOWNLOAD_BASE=http://127.0.0.1:1\\@evil.example/x"},
		"lookalike host":  {"GHOSTTREE_DOWNLOAD_BASE=http://127.0.0.1.evil.example/x"},
		"lookalike local": {"GHOSTTREE_DOWNLOAD_BASE=http://localhost.evil.example/x"},
		"allow switch":    {"GHOSTTREE_DOWNLOAD_BASE=http://gt.example.invalid/x", "GHOSTTREE_ALLOW_HTTP=1"},
	} {
		r := runInstall(t, srv.URL, env, sh)
		if r.err == nil || r.installed() || !strings.Contains(r.out, "refusing") {
			t.Errorf("%s: err=%v\n%s", name, r.err, r.out)
		}
	}
	// Die Ausnahme für diese Maschine bleibt.
	r := runInstall(t, srv.URL, []string{"GHOSTTREE_DOWNLOAD_BASE=" + srv.URL + "/dist"}, sh)
	if r.err != nil || !r.installed() {
		t.Fatalf("loopback download: %v\n%s", r.err, r.out)
	}
}

// Das Skript eines Servers prüft nur dessen eigene Prüfsummen. Es sagt das, bevor
// es etwas installiert, und nennt den Weg zu einem signierten Release.
func TestInstallScriptSaysThatOnlyTheServerIsTrusted(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	r := runInstall(t, srv.URL, nil, "")
	if r.err != nil || !r.installed() {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "no ghosttree release signature") || !strings.Contains(r.out, srv.URL) || !strings.Contains(r.out, "downloads.deadweightlabs.com/ghosttree/install.sh") {
		t.Fatalf("the output does not say whom the download trusts:\n%s", r.out)
	}
	if strings.Index(r.out, "no ghosttree release signature") > strings.Index(r.out, "Checksum OK") {
		t.Fatalf("the notice has to come before the install:\n%s", r.out)
	}
}
