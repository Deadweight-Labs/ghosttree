package web

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func distServer(t *testing.T, dir string, opts ...Option) *httptest.Server {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if dir != "" {
		opts = append(opts, WithDistDir(dir))
	}
	srv := httptest.NewServer(New(st, opts...))
	t.Cleanup(srv.Close)
	return srv
}

// fakeCtx is the "binary" inside the test archives: a shell script.
const fakeCtx = `#!/bin/sh
case "$1" in
version) echo "ctx 9.9.9-test" ;;
*) echo "unknown command $1"; echo "usage: ctx <command>"; echo; echo "  version  print version"; exit 2 ;;
esac
`

const fakeCtxWithJoin = `#!/bin/sh
case "$1" in
version) echo "ctx 9.9.9-test" ;;
join) if [ "$2" = --help ]; then exit 0; fi; shift; echo "$*" > "$HOME/join-args" ;;
*) echo "unknown command $1"; echo "usage: ctx <command>"; echo; echo "  join     join a server"; echo "  version  print version"; exit 2 ;;
esac
`

func makeArchive(t *testing.T, dir, name, script string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ctx", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()
}

func sumOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// distDir builds archives for all four platforms plus checksums.txt.
func distDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, osn := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := fmt.Sprintf("ctx_1.2.3_%s_%s.tar.gz", osn, arch)
			makeArchive(t, dir, name, script)
			fmt.Fprintf(&sums, "%s  %s\n", sumOf(t, filepath.Join(dir, name)), name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unlisted.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body(t, resp)
}

func TestDistRoutesAre404WithoutDirectory(t *testing.T) {
	srv := distServer(t, "")
	for _, p := range []string{"/install.sh", "/dist/checksums.txt", "/dist/ctx_1.2.3_linux_amd64.tar.gz"} {
		resp, _ := get(t, srv.URL+p)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestDistServesOnlyListedFiles(t *testing.T) {
	dir := distDir(t, fakeCtx)
	srv := distServer(t, dir)

	resp, txt := get(t, srv.URL+"/dist/checksums.txt")
	if resp.StatusCode != 200 || !strings.Contains(txt, "ctx_1.2.3_linux_amd64.tar.gz") {
		t.Fatalf("checksums: %d %q", resp.StatusCode, txt)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("checksums content type %q", ct)
	}
	if resp.Header.Get("Cache-Control") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing cache/nosniff headers: %v", resp.Header)
	}

	resp, _ = get(t, srv.URL+"/dist/ctx_1.2.3_linux_amd64.tar.gz")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Errorf("archive: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") == "" {
		t.Error("archive without Cache-Control")
	}

	for _, p := range []string{
		"/dist/unlisted.txt", "/dist/nope.tar.gz", "/dist/..%2Fchecksums.txt", "/dist/%2e%2e/unlisted.txt",
		"/dist/sub/ctx_1.2.3_linux_amd64.tar.gz", "/dist/ctx_1.2.3_linux_amd64.tar.gz/", "/dist/",
	} {
		resp, b := get(t, srv.URL+p)
		if resp.StatusCode == 200 || strings.Contains(b, "secret") {
			t.Errorf("%s = %d", p, resp.StatusCode)
		}
	}
	// POST is not allowed.
	r, err := http.Post(srv.URL+"/dist/checksums.txt", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode == 200 {
		t.Error("POST served")
	}
}

func TestDistChecksumListCannotEscapeDirectory(t *testing.T) {
	dir := distDir(t, fakeCtx)
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	os.WriteFile(outside, []byte("outside"), 0o644)
	t.Cleanup(func() { os.Remove(outside) })
	sums := "deadbeef  ../outside.txt\n" + "deadbeef  ctx_1.2.3_linux_amd64.tar.gz\n"
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums), 0o644)
	srv := distServer(t, dir)
	resp, b := get(t, srv.URL+"/dist/..%2Foutside.txt")
	if resp.StatusCode == 200 || strings.Contains(b, "outside") {
		t.Errorf("escaped: %d", resp.StatusCode)
	}
}

func TestInstallScriptUsesPublicURL(t *testing.T) {
	dir := distDir(t, fakeCtx)
	srv := distServer(t, dir, WithPublicURL("https://gt.example.com"))
	resp, sh := get(t, srv.URL+"/install.sh")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if !strings.Contains(sh, "'https://gt.example.com'") || strings.Contains(sh, "__GHOSTTREE_SERVER__") {
		t.Errorf("server URL not substituted")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("install.sh Cache-Control %q", cc)
	}
}

func TestInstallScriptUsesRequestOriginWithoutPublicURL(t *testing.T) {
	dir := distDir(t, fakeCtx)
	srv := distServer(t, dir)
	_, sh := get(t, srv.URL+"/install.sh")
	if !strings.Contains(sh, "'"+srv.URL+"'") {
		t.Errorf("request origin %s not in script", srv.URL)
	}
}

func serveRaw(t *testing.T, h http.Handler, host, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/install.sh", nil)
	req.Host = host
	if remote != "" {
		req.RemoteAddr = remote
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func distHandler(t *testing.T, opts ...Option) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, append(opts, WithDistDir(distDir(t, fakeCtx)))...)
}

func TestInstallScriptRejectsHostThatCouldInjectShell(t *testing.T) {
	h := distHandler(t)
	for _, host := range []string{"x';touch /tmp/pwn;'", "a b", "a$(id)", "a`id`", "a\"b", "a;b", "127.0.0.1'; id; '"} {
		rec := serveRaw(t, h, host, "", nil)
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "touch") {
			t.Errorf("host %q: %d", host, rec.Code)
		}
	}
}

func TestInstallScriptRefusesPlainHTTPForRemoteHosts(t *testing.T) {
	h := distHandler(t)
	// TLS terminated by a proxy that sends no forwarded headers: no TLS seen.
	if rec := serveRaw(t, h, "gt.example.com", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("plain http remote host: %d", rec.Code)
	}
	for _, host := range []string{"localhost:8474", "127.0.0.1:8474", "[::1]:8474"} {
		if rec := serveRaw(t, h, host, "", nil); rec.Code != http.StatusOK {
			t.Errorf("loopback %s: %d", host, rec.Code)
		}
	}
	// http public URL on a remote host is refused as well.
	hp := distHandler(t, WithPublicURL("http://gt.example.com"))
	if rec := serveRaw(t, hp, "gt.example.com", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("http public url: %d", rec.Code)
	}
}

func TestInstallScriptForwardedHeaders(t *testing.T) {
	h := distHandler(t)
	ok := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "gt.example.com"}
	rec := serveRaw(t, h, "internal:8474", "127.0.0.1:5555", ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "'https://gt.example.com'") {
		t.Errorf("trusted proxy: %d", rec.Code)
	}
	// Untrusted peer must not steer the address.
	if rec := serveRaw(t, h, "internal:8474", "203.0.113.5:5555", ok); rec.Code == 200 {
		t.Error("untrusted peer's forwarded headers accepted")
	}
	for name, hdr := range map[string]map[string]string{
		"injected host": {"X-Forwarded-Proto": "https", "X-Forwarded-Host": "x';id;'"},
		"proto only":    {"X-Forwarded-Proto": "https"},
		"host only":     {"X-Forwarded-Host": "gt.example.com"},
		"http remote":   {"X-Forwarded-Proto": "http", "X-Forwarded-Host": "gt.example.com"},
		"odd proto":     {"X-Forwarded-Proto": "javascript", "X-Forwarded-Host": "gt.example.com"},
		"list of hosts": {"X-Forwarded-Proto": "https", "X-Forwarded-Host": "a.example,b.example"},
	} {
		if rec := serveRaw(t, h, "internal:8474", "127.0.0.1:5555", hdr); rec.Code == 200 {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPublicURLWithShellCharactersIsRejected(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WithPublicURL accepted a host with quotes")
		}
	}()
	WithPublicURL("https://x';id;'")(&app{})
}

func TestInstallScriptRefusesPlainHTTPBase(t *testing.T) {
	for _, base := range []string{"http://gt.example.com", "ftp://x"} {
		script := strings.ReplaceAll(installScript, installServerPlaceholder, base)
		r := runInstall(t, "", nil, script)
		if r.err == nil || r.installed() || !strings.Contains(r.out, "refusing") && !strings.Contains(r.out, "unsupported") {
			t.Errorf("%s: err=%v\n%s", base, r.err, r.out)
		}
	}
}

func TestInstallScriptRefusesAmbiguousChecksums(t *testing.T) {
	dir := distDir(t, fakeCtx)
	sum := sumOf(t, filepath.Join(dir, "ctx_1.2.3_linux_amd64.tar.gz"))
	var b strings.Builder
	for _, osn := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			fmt.Fprintf(&b, "%s  ctx_1.2.3_%s_%s.tar.gz\n%s  ctx_1.2.4_%s_%s.tar.gz\n", sum, osn, arch, sum, osn, arch)
		}
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(b.String()), 0o644)
	srv := distServer(t, dir)
	r := runInstall(t, srv.URL, nil, "")
	if r.err == nil || r.installed() || !strings.Contains(r.out, "ambiguous") {
		t.Errorf("err=%v\n%s", r.err, r.out)
	}
}

func TestInstallScriptAcceptsCRLFAndStarChecksums(t *testing.T) {
	dir := distDir(t, fakeCtx)
	b, _ := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(b), "  ctx_", " *ctx_"), "\n", "\r\n")
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(crlf), 0o644)
	srv := distServer(t, dir)
	r := runInstall(t, srv.URL, nil, "")
	if r.err != nil || !r.installed() {
		t.Errorf("%v\n%s", r.err, r.out)
	}
	if resp, _ := get(t, srv.URL+"/dist/ctx_1.2.3_linux_amd64.tar.gz"); resp.StatusCode != 200 {
		t.Errorf("server did not list starred name: %d", resp.StatusCode)
	}
}

func TestInstallScriptRejectsSymlinkedCtxInArchive(t *testing.T) {
	dir := distDir(t, fakeCtx)
	var sums strings.Builder
	for _, osn := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := fmt.Sprintf("ctx_1.2.3_%s_%s.tar.gz", osn, arch)
			f, _ := os.Create(filepath.Join(dir, name))
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: "ctx", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh", Mode: 0o755})
			tw.Close()
			gz.Close()
			f.Close()
			fmt.Fprintf(&sums, "%s  %s\n", sumOf(t, filepath.Join(dir, name)), name)
		}
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644)
	srv := distServer(t, dir)
	r := runInstall(t, srv.URL, nil, "")
	if r.err == nil || r.installed() {
		t.Errorf("symlink ctx installed\n%s", r.out)
	}
}

func TestDistRefusesSymlinks(t *testing.T) {
	dir := distDir(t, fakeCtx)
	outside := filepath.Join(t.TempDir(), "outside.tar.gz")
	os.WriteFile(outside, []byte("outside-data"), 0o644)
	link := filepath.Join(dir, "ctx_1.2.3_linux_amd64.tar.gz")
	os.Remove(link)
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("no symlinks")
	}
	srv := distServer(t, dir)
	resp, b := get(t, srv.URL+"/dist/ctx_1.2.3_linux_amd64.tar.gz")
	if resp.StatusCode == 200 || strings.Contains(b, "outside-data") {
		t.Errorf("symlinked archive served: %d", resp.StatusCode)
	}
	// A symlinked checksums.txt disqualifies the directory at startup.
	other := t.TempDir()
	real := filepath.Join(other, "real.txt")
	os.WriteFile(real, []byte(""), 0o644)
	os.Symlink(real, filepath.Join(other, "checksums.txt"))
	if _, err := CheckDistDir(other); err == nil {
		t.Error("CheckDistDir accepted a symlinked checksums.txt")
	}
}

func TestCheckDistDirReportsVersions(t *testing.T) {
	dir := distDir(t, fakeCtx)
	v, err := CheckDistDir(dir)
	if err != nil || len(v) != 1 || v[0] != "1.2.3" {
		t.Fatalf("%v %v", v, err)
	}
	sum := sumOf(t, filepath.Join(dir, "ctx_1.2.3_linux_amd64.tar.gz"))
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sum+"  ctx_1.0.0_linux_amd64.tar.gz\n"+sum+"  ctx_2.0.0_linux_amd64.tar.gz\n"), 0o644)
	if v, _ := CheckDistDir(dir); len(v) != 2 {
		t.Errorf("versions %v", v)
	}
}

func TestDistChecksumsCacheFollowsChanges(t *testing.T) {
	dir := distDir(t, fakeCtx)
	srv := distServer(t, dir)
	if resp, _ := get(t, srv.URL+"/dist/ctx_1.2.3_linux_amd64.tar.gz"); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("short\n"), 0o644)
	if resp, _ := get(t, srv.URL+"/dist/ctx_1.2.3_linux_amd64.tar.gz"); resp.StatusCode == 200 {
		t.Error("stale checksums cache")
	}
}

func TestInstallScriptShellcheckClean(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed")
	}
	out, err := exec.Command(sc, "-s", "sh", "install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck:\n%s", out)
	}
}

// ---- end-to-end runs of the script against the test server ----

type installRun struct {
	home, out string
	err       error
}

func platformArchiveOK() bool {
	return (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}

// runInstall pipes the served script into sh like the documented one-liner.
func runInstall(t *testing.T, srvURL string, extraEnv []string, script string, args ...string) installRun {
	t.Helper()
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s missing", tool)
		}
	}
	if !platformArchiveOK() {
		t.Skip("unsupported test platform")
	}
	home := t.TempDir()
	if script == "" {
		_, script = get(t, srvURL+"/install.sh")
	}
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_BIN_HOME="}, extraEnv...)
	out, err := cmd.CombinedOutput()
	return installRun{home: home, out: string(out), err: err}
}

func (r installRun) installed() bool {
	_, err := os.Stat(filepath.Join(r.home, ".local", "bin", "ctx"))
	return err == nil
}

func TestInstallScriptInstallsVerifiedBinary(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	r := runInstall(t, srv.URL, nil, "")
	if r.err != nil || !r.installed() {
		t.Fatalf("install failed: %v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "9.9.9-test") {
		t.Errorf("version not shown:\n%s", r.out)
	}
	if !strings.Contains(r.out, "PATH") {
		t.Errorf("no PATH hint although ~/.local/bin is not on PATH:\n%s", r.out)
	}
	if strings.Contains(strings.ToLower(r.out), "sudo") && !strings.Contains(r.out, "no sudo") {
		t.Errorf("mentions sudo:\n%s", r.out)
	}
}

func TestInstallScriptNoPathHintWhenOnPath(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	r := runInstall(t, srv.URL, nil, "")
	if !r.installed() {
		t.Fatal(r.out)
	}
	// second run with the install dir on PATH
	script := func() string { _, s := get(t, srv.URL+"/install.sh"); return s }()
	cmd := exec.Command("sh", "-s", "--")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"HOME=" + r.home, "PATH=" + filepath.Join(r.home, ".local", "bin") + ":" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(string(out), "add ") {
		t.Errorf("PATH hint although dir is on PATH:\n%s", out)
	}
}

func TestInstallScriptHonoursXDGBinHome(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	xdg := t.TempDir()
	r := runInstall(t, srv.URL, []string{"XDG_BIN_HOME=" + xdg}, "")
	if r.err != nil {
		t.Fatal(r.out)
	}
	if _, err := os.Stat(filepath.Join(xdg, "ctx")); err != nil {
		t.Errorf("not installed to XDG_BIN_HOME: %v\n%s", err, r.out)
	}
}

func TestInstallScriptRejectsTamperedArchive(t *testing.T) {
	dir := distDir(t, fakeCtx)
	for _, osn := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			makeArchive(t, dir, fmt.Sprintf("ctx_1.2.3_%s_%s.tar.gz", osn, arch), "#!/bin/sh\necho evil\n")
		}
	}
	srv := distServer(t, dir)
	r := runInstall(t, srv.URL, nil, "")
	if r.err == nil || r.installed() {
		t.Fatalf("tampered archive was installed (err=%v)\n%s", r.err, r.out)
	}
	if !strings.Contains(strings.ToLower(r.out), "checksum") {
		t.Errorf("no checksum message:\n%s", r.out)
	}
}

func TestInstallScriptFailsWhenArchiveMissingFromChecksums(t *testing.T) {
	dir := distDir(t, fakeCtx)
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("abc  ctx_1.2.3_plan9_mips.tar.gz\n"), 0o644)
	srv := distServer(t, dir)
	r := runInstall(t, srv.URL, nil, "")
	if r.err == nil || r.installed() {
		t.Fatalf("installed without a checksum entry\n%s", r.out)
	}
}

func TestInstallScriptTruncatedDownloadRunsNothing(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	_, sh := get(t, srv.URL+"/install.sh")
	for _, frac := range []int{2, 3, 4} {
		cut := sh[:len(sh)/frac]
		if i := strings.LastIndex(cut, "\n"); i > 0 {
			cut = cut[:i+1]
		}
		r := runInstall(t, srv.URL, nil, cut)
		if r.installed() {
			t.Errorf("truncated script (1/%d) installed something\n%s", frac, r.out)
		}
	}
}

func TestInstallScriptUnknownPlatform(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "uname"), []byte("#!/bin/sh\necho Plan9\n"), 0o755)
	r := runInstall(t, srv.URL, []string{"PATH=" + fake + ":" + os.Getenv("PATH")}, "")
	if r.err == nil || r.installed() || !strings.Contains(r.out, "Plan9") {
		t.Fatalf("expected clear refusal, err=%v\n%s", r.err, r.out)
	}
}

func TestInstallScriptWindowsPointsToWSL(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "uname"), []byte("#!/bin/sh\necho MINGW64_NT-10.0\n"), 0o755)
	r := runInstall(t, srv.URL, []string{"PATH=" + fake + ":" + os.Getenv("PATH")}, "")
	if r.err == nil || r.installed() || !strings.Contains(r.out, "WSL") {
		t.Fatalf("expected WSL hint, err=%v\n%s", r.err, r.out)
	}
	if n := strings.Count(strings.TrimSpace(r.out), "\n"); n != 0 {
		t.Errorf("WSL message should be one line, got:\n%s", r.out)
	}
}

func TestInstallScriptPairWithoutJoinCommand(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	r := runInstall(t, srv.URL, nil, "", "--pair", "ABCD-1234")
	if r.err != nil || !r.installed() {
		t.Fatalf("install must still finish: %v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "ctx join") {
		t.Errorf("no one-line join hint:\n%s", r.out)
	}
	if _, err := os.Stat(filepath.Join(r.home, "join-args")); err == nil {
		t.Error("join ran although ctx has no join")
	}
}

func TestInstallScriptPassesArgumentsToJoin(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtxWithJoin))
	r := runInstall(t, srv.URL, nil, "", "--pair", "ABCD-1234")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	b, err := os.ReadFile(filepath.Join(r.home, "join-args"))
	if err != nil {
		t.Fatalf("join not called: %v\n%s", err, r.out)
	}
	if got := strings.TrimSpace(string(b)); got != "--server "+srv.URL+" --pair ABCD-1234" {
		t.Errorf("join args %q", got)
	}
}

func TestInstallScriptDownloadBaseOverride(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	other := distServer(t, distDir(t, fakeCtx))
	_, sh := get(t, srv.URL+"/install.sh")
	// Script served by srv, but archives taken from other via the override.
	r := runInstall(t, srv.URL, []string{"GHOSTTREE_DOWNLOAD_BASE=" + other.URL + "/dist"}, sh)
	if r.err != nil || !r.installed() {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, other.URL) {
		t.Errorf("source not shown:\n%s", r.out)
	}
}

func TestInstallScriptPathHintNamesTheLineAndTheProfile(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	r := runInstall(t, srv.URL, []string{"SHELL=/bin/zsh"}, "")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	bin := filepath.Join(r.home, ".local", "bin")
	for _, want := range []string{"is not on your PATH", "use its full path", filepath.Join(r.home, ".zshrc"), `export PATH="` + bin + `:$PATH"`, "--modify-path"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("hint lacks %q:\n%s", want, r.out)
		}
	}
	if _, err := os.Stat(filepath.Join(r.home, ".zshrc")); err == nil {
		t.Error("profile was created without being asked")
	}
}

func TestInstallScriptModifyPathAppendsOnceAndKeepsTheProfile(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	_, sh := get(t, srv.URL+"/install.sh")
	home := t.TempDir()
	profile := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(profile, []byte("alias ll='ls -l'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		cmd := exec.Command("sh", "-s", "--", "--modify-path")
		cmd.Stdin = strings.NewReader(sh)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_BIN_HOME=", "SHELL=/bin/zsh"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	run()
	run()
	got, _ := os.ReadFile(profile)
	line := `export PATH="` + filepath.Join(home, ".local", "bin") + `:$PATH"`
	if !strings.HasPrefix(string(got), "alias ll='ls -l'\n") || strings.Count(string(got), line) != 1 {
		t.Fatalf("profile:\n%s", got)
	}
}

func TestInstallScriptNoModifyPathAndFlagsStayAwayFromJoin(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtxWithJoin))
	r := runInstall(t, srv.URL, []string{"SHELL=/bin/bash"}, "", "--no-modify-path", "--pair", "ABCD-1234")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	for _, name := range []string{".bashrc", ".bash_profile", ".profile"} {
		if _, err := os.Stat(filepath.Join(r.home, name)); err == nil {
			t.Errorf("%s written despite --no-modify-path", name)
		}
	}
	b, _ := os.ReadFile(filepath.Join(r.home, "join-args"))
	if got := strings.TrimSpace(string(b)); got != "--server "+srv.URL+" --pair ABCD-1234" {
		t.Errorf("join args %q", got)
	}
}

func TestInstallScriptPathLineIsInertForAwkwardDirectories(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	_, sh := get(t, srv.URL+"/install.sh")
	home := t.TempDir()
	bin := filepath.Join(home, `a"b$(touch pwned)'c`+"`d`\\e")
	cmd := exec.Command("sh", "-s", "--", "--modify-path")
	cmd.Stdin = strings.NewReader(sh)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_BIN_HOME=" + bin, "SHELL=/bin/zsh"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// A shell reading the profile must end up with exactly this directory on PATH and run nothing.
	probe := exec.Command("sh", "-c", `. "$HOME/.zshrc"; printf '%s' "$PATH"`)
	probe.Dir = home
	probe.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	got, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), bin+":") {
		t.Errorf("PATH after sourcing the profile = %q, want prefix %q", got, bin+":")
	}
	if _, err := os.Stat(filepath.Join(home, "pwned")); err == nil {
		t.Error("the profile line executed a command substitution")
	}
}

func TestInstallScriptRefusesRelativeOrControlCharacterInstallDirectory(t *testing.T) {
	srv := distServer(t, distDir(t, fakeCtx))
	for name, dir := range map[string]string{"relative": "rel/bin", "newline": "/tmp/x\nexport EVIL=1"} {
		r := runInstall(t, srv.URL, []string{"XDG_BIN_HOME=" + dir}, "")
		if r.err == nil || !strings.Contains(r.out, "XDG_BIN_HOME") && !strings.Contains(r.out, "control character") {
			t.Errorf("%s: want a refusal, err=%v\n%s", name, r.err, r.out)
		}
	}
}
