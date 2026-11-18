// Package installscript tests deploy/downloads/ghosttree/install.sh, the public
// installer, against a local release server with throwaway minisign keys.
package installscript

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
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
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
)

const scriptPath = "../../deploy/downloads/ghosttree/install.sh"

// fakeCtx is the "binary" in the archives: it records how it was called.
const fakeCtx = `#!/bin/sh
case "$1 $2" in
"version "*) echo "ctx 0.1.0-test" ;;
"join --help" | "server install") if [ "$3" = --help ]; then exit 0; fi ;;
esac
case "$1" in
join) [ "$2" = --help ] && exit 0; shift; echo "$*" > "$HOME/join-args" ;;
server) if [ "$3" = --help ]; then exit 0; fi; shift 2; echo "$*" > "$HOME/server-args" ;;
esac
exit 0
`

type key struct {
	pub  minisign.PublicKey
	priv ed25519.PrivateKey
}

func newKey(t *testing.T) key {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key{pub, priv}
}

func archiveOf(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ctx", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// release is what the server serves: path below the release base -> bytes.
type release struct {
	mu    sync.Mutex
	files map[string][]byte
	// latest is what the API answers for the newest stable release ("" = 404).
	latest string
}

func (r *release) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.HasSuffix(req.URL.Path, "/releases/latest") {
		if r.latest == "" {
			http.NotFound(w, req)
			return
		}
		fmt.Fprintf(w, `{"url": "x", "tag_name": %q, "draft": false}`, r.latest)
		return
	}
	b, ok := r.files[strings.TrimPrefix(req.URL.Path, "/dl/")]
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Write(b)
}

func archiveName(version string) string {
	return fmt.Sprintf("ghosttree_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

// signedRelease publishes tag with a checksums.txt signed by k.
func (r *release) publish(t *testing.T, k key, tag string, ctxScript string) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	arch := archiveOf(t, ctxScript)
	sum := sha256.Sum256(arch)
	sums := []byte(fmt.Sprintf("%s  %s\n%s  ghosttree_%s_plan9_mips.tar.gz\n",
		hex.EncodeToString(sum[:]), archiveName(version), strings.Repeat("0", 64), version))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[tag+"/"+archiveName(version)] = arch
	r.files[tag+"/checksums.txt"] = sums
	r.files[tag+"/checksums.txt.minisig"] = minisign.Sign(k.pub, k.priv, sums, "ghosttree "+tag)
}

func (r *release) set(path string, b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b == nil {
		delete(r.files, path)
		return
	}
	r.files[path] = b
}

func (r *release) get(path string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.files[path]...)
}

// toolbox is a directory of links to the tools install.sh needs, so a run sees
// exactly the checkers the test wants it to have.
func toolbox(t *testing.T, openssl, minisignBin bool) string {
	t.Helper()
	dir := t.TempDir()
	tools := []string{"sh", "uname", "mktemp", "rm", "mkdir", "cp", "mv", "chmod", "cat", "tr", "awk", "sed", "grep",
		"cut", "wc", "head", "tail", "od", "tar", "gzip", "id", "sha256sum", "curl", "env", "dirname", "sleep", "ls"}
	if openssl {
		tools = append(tools, "openssl")
	}
	if minisignBin {
		tools = append(tools, "minisign")
	}
	for _, name := range tools {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s is not installed", name)
		}
		if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type run struct {
	dir  string // $HOME
	out  string
	code int
}

// install runs the script, with the placeholder replaced by k's public key, as
// a user with the given checkers on PATH.
func install(t *testing.T, srv *httptest.Server, k key, tools string, args ...string) run {
	t.Helper()
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "__GHOSTTREE_MINISIGN_PUBKEY__") {
		t.Fatal("install.sh no longer has the key placeholder the test replaces")
	}
	body := strings.Replace(string(src), "__GHOSTTREE_MINISIGN_PUBKEY__", k.pub.String(), 1)
	return runScript(t, srv, body, tools, args...)
}

func runScript(t *testing.T, srv *httptest.Server, body, tools string, args ...string) run {
	t.Helper()
	home := t.TempDir()
	script := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{script, "--no-modify-path"}, args...)...)
	cmd.Env = []string{
		"PATH=" + tools, "HOME=" + home, "TMPDIR=" + t.TempDir(), "SHELL=/bin/sh",
		"GHOSTTREE_RELEASE_BASE=" + srv.URL + "/dl",
		"GHOSTTREE_API_URL=" + srv.URL + "/api",
	}
	out, err := cmd.CombinedOutput()
	r := run{dir: home, out: string(out)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r run) installed() bool {
	_, err := os.Stat(filepath.Join(r.dir, ".local", "bin", "ctx"))
	return err == nil
}

func setup(t *testing.T) (*release, key, *httptest.Server) {
	t.Helper()
	k := newKey(t)
	rel := &release{files: map[string][]byte{}}
	rel.publish(t, k, "v0.1.0", fakeCtx)
	srv := httptest.NewServer(rel)
	t.Cleanup(srv.Close)
	return rel, k, srv
}

// verifiers are the ways install.sh can check a signature on this machine.
func verifiers(t *testing.T) map[string]string {
	t.Helper()
	v := map[string]string{"openssl": toolbox(t, true, false)}
	if _, err := exec.LookPath("minisign"); err == nil {
		v["minisign"] = toolbox(t, false, true)
	}
	return v
}

func TestGenuineReleaseInstallsWithEachVerifier(t *testing.T) {
	for name, tools := range verifiers(t) {
		t.Run(name, func(t *testing.T) {
			_, k, srv := setup(t)
			r := install(t, srv, k, tools, "--version", "v0.1.0")
			if r.code != 0 || !r.installed() {
				t.Fatalf("exit %d, installed %v:\n%s", r.code, r.installed(), r.out)
			}
			for _, want := range []string{"Signature OK (ghosttree v0.1.0)", "Checksum OK", "Installed: ctx 0.1.0-test"} {
				if !strings.Contains(r.out, want) {
					t.Errorf("output lacks %q:\n%s", want, r.out)
				}
			}
		})
	}
}

// Every way a release can be wrong must end with nothing installed and the
// reason in the output.
func TestTamperedReleaseIsRefused(t *testing.T) {
	otherKey := newKey(t)
	cases := []struct {
		name   string
		tamper func(t *testing.T, rel *release, k key)
		want   string
	}{
		{"archive changed", func(t *testing.T, rel *release, k key) {
			a := rel.get("v0.1.0/" + archiveName("0.1.0"))
			a[len(a)/2] ^= 0xff
			rel.set("v0.1.0/"+archiveName("0.1.0"), a)
		}, "checksum mismatch"},
		{"archive swapped, checksums edited, signature kept", func(t *testing.T, rel *release, k key) {
			evil := archiveOf(t, "#!/bin/sh\necho evil\n")
			sum := sha256.Sum256(evil)
			rel.set("v0.1.0/"+archiveName("0.1.0"), evil)
			rel.set("v0.1.0/checksums.txt", []byte(hex.EncodeToString(sum[:])+"  "+archiveName("0.1.0")+"\n"))
		}, "does not match the ghosttree release key"},
		{"everything signed by another key", func(t *testing.T, rel *release, k key) {
			rel.publish(t, otherKey, "v0.1.0", "#!/bin/sh\necho evil\n")
		}, "does not match the ghosttree release key"},
		{"signature missing", func(t *testing.T, rel *release, k key) {
			rel.set("v0.1.0/checksums.txt.minisig", nil)
		}, "a release without a signature is not installed"},
		{"signature garbled", func(t *testing.T, rel *release, k key) {
			rel.set("v0.1.0/checksums.txt.minisig", []byte("untrusted comment: x\nAAAA\ntrusted comment: ghosttree v0.1.0\nAAAA\n"))
		}, "does not match the ghosttree release key"},
		{"genuine list of another release", func(t *testing.T, rel *release, k key) {
			rel.publish(t, k, "v0.0.9", fakeCtx)
			rel.set("v0.1.0/checksums.txt", rel.get("v0.0.9/checksums.txt"))
			rel.set("v0.1.0/checksums.txt.minisig", rel.get("v0.0.9/checksums.txt.minisig"))
			rel.set("v0.1.0/"+archiveName("0.1.0"), rel.get("v0.0.9/"+archiveName("0.0.9")))
		}, "belongs to 'ghosttree v0.0.9'"},
	}
	for vname, tools := range verifiers(t) {
		for _, c := range cases {
			t.Run(vname+"/"+c.name, func(t *testing.T) {
				rel, k, srv := setup(t)
				c.tamper(t, rel, k)
				r := install(t, srv, k, tools, "--version", "v0.1.0")
				if r.code == 0 || r.installed() {
					t.Fatalf("accepted (exit %d, installed %v):\n%s", r.code, r.installed(), r.out)
				}
				if !strings.Contains(r.out, c.want) {
					t.Errorf("output lacks %q:\n%s", c.want, r.out)
				}
				if strings.Contains(r.out, "evil") {
					t.Errorf("the tampered binary ran:\n%s", r.out)
				}
			})
		}
	}
}

func TestWithoutAVerifierNothingIsInstalled(t *testing.T) {
	_, k, srv := setup(t)
	r := install(t, srv, k, toolbox(t, false, false), "--version", "v0.1.0")
	if r.code == 0 || r.installed() {
		t.Fatalf("installed without a way to check (exit %d):\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "neither minisign nor OpenSSL 3 is installed") {
		t.Errorf("no instruction for the user:\n%s", r.out)
	}
}

func TestInstallerWithoutReleaseKeyRefusesToRun(t *testing.T) {
	_, _, srv := setup(t)
	cmd := exec.Command("sh", scriptPath, "--version", "v0.1.0")
	cmd.Env = []string{"PATH=" + toolbox(t, true, false), "HOME=" + t.TempDir(), "GHOSTTREE_RELEASE_BASE=" + srv.URL + "/dl"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "carries no release key yet") {
		t.Fatalf("ran with the placeholder key: %v\n%s", err, out)
	}
}

func TestNewestStableReleaseIsFoundThroughTheAPI(t *testing.T) {
	for name, tools := range verifiers(t) {
		t.Run(name, func(t *testing.T) {
			rel, k, srv := setup(t)
			rel.latest = "v0.1.0"
			r := install(t, srv, k, tools)
			if r.code != 0 || !r.installed() {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
		})
	}
}

func TestNoStableReleaseYetPointsToTheCandidates(t *testing.T) {
	_, k, srv := setup(t)
	r := install(t, srv, k, toolbox(t, true, false))
	if r.code == 0 || r.installed() || !strings.Contains(r.out, "--version v0.1.0-rc.3") {
		t.Fatalf("exit %d, installed %v:\n%s", r.code, r.installed(), r.out)
	}
}

func TestOddVersionIsRefusedBeforeAnythingIsFetched(t *testing.T) {
	_, k, srv := setup(t)
	for _, v := range []string{"../../etc", "v1.2", "v1.2.3;rm", "main"} {
		r := install(t, srv, k, toolbox(t, true, false), "--version", v)
		if r.code == 0 || !strings.Contains(r.out, "unexpected release tag") {
			t.Errorf("%q: exit %d:\n%s", v, r.code, r.out)
		}
	}
}

func TestModesHandTheRestOfTheArgumentsOn(t *testing.T) {
	for name, tools := range verifiers(t) {
		t.Run(name, func(t *testing.T) {
			_, k, srv := setup(t)
			r := install(t, srv, k, tools, "--version", "v0.1.0", "--connect", "https://gt.example.com", "--pair", "ABCD-1234", "--yes")
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			b, err := os.ReadFile(filepath.Join(r.dir, "join-args"))
			if err != nil {
				t.Fatalf("ctx join was not called:\n%s", r.out)
			}
			if got := strings.TrimSpace(string(b)); got != "--server https://gt.example.com --pair ABCD-1234 --yes" {
				t.Errorf("ctx join got %q", got)
			}
		})
	}
}

func TestPlainHttpFromAnotherHostIsRefused(t *testing.T) {
	_, k, srv := setup(t)
	src, _ := os.ReadFile(scriptPath)
	script := filepath.Join(t.TempDir(), "install.sh")
	os.WriteFile(script, []byte(strings.Replace(string(src), "__GHOSTTREE_MINISIGN_PUBKEY__", k.pub.String(), 1)), 0o755)
	cmd := exec.Command("sh", script, "--version", "v0.1.0")
	cmd.Env = []string{"PATH=" + toolbox(t, true, false), "HOME=" + t.TempDir(), "GHOSTTREE_RELEASE_BASE=http://downloads.example.org/dl"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "refusing to download over plain http") {
		t.Fatalf("%v\n%s", err, out)
	}
	_ = srv
}

func TestPublishedKeyIsTheOneInTheInstaller(t *testing.T) {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile("../../deploy/downloads/ghosttree/minisign.pub")
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimSpace(pub)
	line := string(trimmed[bytes.LastIndexByte(trimmed, '\n')+1:])
	if !strings.Contains(string(script), "pubkey='"+line+"'") {
		t.Fatalf("install.sh does not carry the key of minisign.pub (%q)", line)
	}
	// Until the release key exists both files hold the placeholder; once it
	// does, it has to be a key minisign made.
	if !strings.HasPrefix(line, "__GHOSTTREE") {
		if _, err := minisign.ParsePublicKey(line); err != nil {
			t.Fatalf("the key in minisign.pub is not a minisign public key: %v", err)
		}
	}
}

// scripts/release-key.sh is how the public key gets into the repository, by
// Robin after the key ceremony or when the key is rotated. What it writes must
// be an installer that accepts a release signed with that key.
func TestReleaseKeyScriptMakesAWorkingInstaller(t *testing.T) {
	_, k, srv := setup(t)
	root := t.TempDir()
	dir := filepath.Join(root, "deploy", "downloads", "ghosttree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install.sh", "minisign.pub"} {
		b, err := os.ReadFile(filepath.Join("../../deploy/downloads/ghosttree", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pubFile := filepath.Join(t.TempDir(), "ghosttree-release.pub")
	if err := os.WriteFile(pubFile, []byte(k.pub.File()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "../../scripts/release-key.sh", pubFile, root).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	r := runScript(t, srv, string(body), toolbox(t, true, false), "--version", "v0.1.0")
	if r.code != 0 || !r.installed() {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "minisign.pub")); err != nil || string(got) != k.pub.File() {
		t.Errorf("minisign.pub = %q (%v)", got, err)
	}
	// A file that is not a minisign public key is refused and install.sh stays as it was.
	for name, content := range map[string]string{"empty": "", "secret-key-shaped": "untrusted comment: x\nRWQ=\n", "one line": "RWRO2MKMhNKzIw7tQgFomtGlgJwXklN+sy5/5922LpGTCnebY7G9Zt+e\n"} {
		bad := filepath.Join(t.TempDir(), "bad.pub")
		os.WriteFile(bad, []byte(content), 0o644)
		if out, err := exec.Command("sh", "../../scripts/release-key.sh", bad, root).CombinedOutput(); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "install.sh"))
	if string(after) != string(body) {
		t.Error("a refused key file changed install.sh")
	}
}
