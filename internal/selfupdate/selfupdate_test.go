package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goos, goarch = "linux", "amd64"

func tarball(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"README.md", "readme"}, {"ctx", content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type fakeRelease struct {
	srv        *httptest.Server
	binary     string
	listedSum  string // checksum to put in checksums.txt ("" = the real one)
	withBundle bool
	prefix     string // archive name prefix ("" = ctx)
	hits       map[string]int
}

// newRelease serves a GitHub release API plus its assets for tag v1.2.0.
func newRelease(t *testing.T, mod func(*fakeRelease)) *fakeRelease {
	t.Helper()
	f := &fakeRelease{binary: "#!/bin/sh\necho new\n", withBundle: true, hits: map[string]int{}}
	if mod != nil {
		mod(f)
	}
	prefix := f.prefix
	if prefix == "" {
		prefix = "ctx"
	}
	archiveName := fmt.Sprintf("%s_1.2.0_%s_%s.tar.gz", prefix, goos, goarch)
	archive := tarball(t, f.binary)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/Deadweight-Labs/ghosttree/releases", func(w http.ResponseWriter, r *http.Request) {
		assets := fmt.Sprintf(`{"name":%q,"browser_download_url":"%s/a/%s"},{"name":"checksums.txt","browser_download_url":"%s/a/checksums.txt"}`, archiveName, base, archiveName, base)
		if f.withBundle {
			assets += fmt.Sprintf(`,{"name":"checksums.txt.sigstore.json","browser_download_url":"%s/a/checksums.txt.sigstore.json"}`, base)
		}
		fmt.Fprintf(w, `[{"tag_name":"v1.3.0-rc.1","prerelease":true,"assets":[]},{"tag_name":"v1.1.0","assets":[]},{"tag_name":"v1.2.0","assets":[%s]},{"tag_name":"v9.9.9","draft":true,"assets":[]}]`, assets)
	})
	mux.HandleFunc("/a/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/a/")
		f.hits[name]++
		switch name {
		case archiveName:
			w.Write(archive)
		case "checksums.txt":
			sum := f.listedSum
			if sum == "" {
				sum = sha(archive)
			}
			fmt.Fprintf(w, "%s  %s\n", sum, archiveName)
		case "checksums.txt.sigstore.json":
			w.Write([]byte(`{"bundle":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	base = f.srv.URL
	return f
}

func installedDir(t *testing.T) (dir, target string) {
	t.Helper()
	dir = t.TempDir()
	target = filepath.Join(dir, "ctx")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

func entries(t *testing.T, dir string) string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

func okVerifier(calls *[]string) Verifier {
	return func(_ context.Context, sums, bundle, tag string) error {
		b, err := os.ReadFile(bundle)
		if err != nil || !strings.Contains(string(b), "bundle") {
			return errors.New("bundle not handed over")
		}
		*calls = append(*calls, tag)
		return nil
	}
}

func TestStableInstallsAfterSignatureAndChecksum(t *testing.T) {
	f := newRelease(t, nil)
	dir, target := installedDir(t)
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	if err != nil {
		t.Fatal(err)
	}
	if cand.Version != "1.2.0" || cand.Tag != "v1.2.0" {
		t.Fatalf("picked %+v: want the newest non-draft non-prerelease with assets", cand)
	}
	var calls []string
	path, err := src.Fetch(context.Background(), cand, dir, okVerifier(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "v1.2.0" {
		t.Fatalf("verifier calls %v", calls)
	}
	backup, err := Install(target, path, "0.1.0-dev+abc")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != f.binary {
		t.Fatalf("target = %q", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if old, _ := os.ReadFile(backup); string(old) != "OLD" || filepath.Base(backup) != "ctx.prev-0.1.0-dev+abc" {
		t.Fatalf("backup %s = %q", backup, old)
	}
	if strings.Contains(entries(t, dir), ".ctx-update") {
		t.Fatalf("temp file left: %s", entries(t, dir))
	}
}

func TestStableWrongChecksumChangesNothing(t *testing.T) {
	f := newRelease(t, func(f *fakeRelease) { f.listedSum = strings.Repeat("a", 64) })
	dir, target := installedDir(t)
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, _ := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	var calls []string
	_, err := src.Fetch(context.Background(), cand, dir, okVerifier(&calls))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if entries(t, dir) != "ctx" {
		t.Fatalf("directory changed: %s", entries(t, dir))
	}
	if b, _ := os.ReadFile(target); string(b) != "OLD" {
		t.Fatal("target changed")
	}
}

func TestStableMissingSignatureIsRefused(t *testing.T) {
	f := newRelease(t, func(f *fakeRelease) { f.withBundle = false })
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	_, err := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	if err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("err = %v", err)
	}
	if f.hits["checksums.txt"] != 0 {
		t.Fatal("downloaded before refusing")
	}
}

func TestStableFailedSignatureCheckStopsBeforeTheArchive(t *testing.T) {
	f := newRelease(t, nil)
	dir, _ := installedDir(t)
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, _ := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	bad := func(context.Context, string, string, string) error { return errors.New("wrong identity") }
	_, err := src.Fetch(context.Background(), cand, dir, bad)
	if err == nil || !strings.Contains(err.Error(), "signature check failed") {
		t.Fatalf("err = %v", err)
	}
	if f.hits[cand.Archive] != 0 || entries(t, dir) != "ctx" {
		t.Fatalf("archive fetched (%d) or directory changed: %s", f.hits[cand.Archive], entries(t, dir))
	}
	if _, err := src.Fetch(context.Background(), cand, dir, nil); err == nil {
		t.Fatal("no verifier must mean no install")
	}
}

func TestCosignVerifierWithoutCosignRefuses(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := CosignVerifier("Deadweight-Labs/ghosttree", "")(context.Background(), "a", "b", "v1.2.0")
	if err == nil || !strings.Contains(err.Error(), "cosign not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestCosignVerifierPinsIdentityAndTag(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	cosign := filepath.Join(dir, "cosign")
	os.WriteFile(cosign, []byte(script), 0o755)
	if err := CosignVerifier("Deadweight-Labs/ghosttree", cosign)(context.Background(), "sums", "bundle", "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(argsFile)
	args := string(b)
	for _, want := range []string{"verify-blob", "--bundle", "bundle", "--certificate-oidc-issuer", OIDCIssuer, "release\\.yml@refs/tags/v1\\.2\\.0$", "sums"} {
		if !strings.Contains(args, want) {
			t.Errorf("cosign args lack %q:\n%s", want, args)
		}
	}
	fail := filepath.Join(dir, "cosignfail")
	os.WriteFile(fail, []byte("#!/bin/sh\necho bad >&2\nexit 1\n"), 0o755)
	if err := CosignVerifier("x/y", fail)(context.Background(), "s", "b", "v1"); err == nil {
		t.Fatal("failing cosign must fail the check")
	}
}

func devServer(t *testing.T, listing string) *httptest.Server {
	t.Helper()
	archive := tarball(t, "DEVBIN")
	name := "ctx_0.1.0-dev+b0cd1b9_linux_amd64.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/dist/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		if listing != "" {
			fmt.Fprint(w, listing)
			return
		}
		fmt.Fprintf(w, "%s  %s\n", sha(archive), name)
	})
	mux.HandleFunc("/dist/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDevFromServerChecksAgainstTheServersChecksums(t *testing.T) {
	srv := devServer(t, "")
	dir, target := installedDir(t)
	src := Source{HTTP: srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.DevFromServer(context.Background(), srv.URL)
	if err != nil || cand.Version != "0.1.0-dev+b0cd1b9" {
		t.Fatalf("cand %+v err %v", cand, err)
	}
	path, err := src.Fetch(context.Background(), cand, dir, nil) // dev needs no signature verifier
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(target, path, "0.1.0-dev+1c48c74"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "DEVBIN" {
		t.Fatalf("target %q", b)
	}
}

func TestDevFromServerRejectsBadChecksumAndAmbiguousListing(t *testing.T) {
	bad := devServer(t, strings.Repeat("b", 64)+"  ctx_0.1.0-dev+b0cd1b9_linux_amd64.tar.gz\n")
	dir, _ := installedDir(t)
	src := Source{HTTP: bad.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.DevFromServer(context.Background(), bad.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Fetch(context.Background(), cand, dir, nil); err == nil || entries(t, dir) != "ctx" {
		t.Fatalf("err %v dir %s", err, entries(t, dir))
	}
	two := devServer(t, strings.Repeat("c", 64)+"  ctx_1_linux_amd64.tar.gz\n"+strings.Repeat("c", 64)+"  ctx_2_linux_amd64.tar.gz\n")
	if _, err := (Source{HTTP: two.Client(), GOOS: goos, GOARCH: goarch}).DevFromServer(context.Background(), two.URL); err == nil {
		t.Fatal("two versions must be refused")
	}
}

func TestDevRefusesPlainHTTPToARemoteServer(t *testing.T) {
	_, err := (Source{GOOS: goos, GOARCH: goarch}).DevFromServer(context.Background(), "http://ghosttree.example.invalid")
	if err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallIsAtomicAndKeepsFewBackups(t *testing.T) {
	dir, target := installedDir(t)
	for i := 0; i < 5; i++ {
		tmp := filepath.Join(dir, fmt.Sprintf(".ctx-update-%d", i))
		os.WriteFile(tmp, []byte(fmt.Sprintf("v%d", i)), 0o600)
		if _, err := Install(target, tmp, fmt.Sprintf("1.0.%d", i)); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(filepath.Join(dir, fmt.Sprintf("ctx.prev-1.0.%d", i)), timeAt(i), timeAt(i))
	}
	if b, _ := os.ReadFile(target); string(b) != "v4" {
		t.Fatalf("target %q", b)
	}
	if n := strings.Count(entries(t, dir), "ctx.prev-"); n > keepBackups {
		t.Fatalf("%d backups kept: %s", n, entries(t, dir))
	}
	if _, err := Install(target, filepath.Join(t.TempDir(), "x"), "1"); err == nil {
		t.Fatal("a file from another directory cannot be renamed atomically")
	}
}

func TestBuildFromSourceBuildsTheDevBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	repo := t.TempDir()
	sh := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	sh("init", "-q", "-b", "dev")
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module fake\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "VERSION"), []byte("0.4.0-rc.1\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, "cmd", "ctx"), 0o755)
	os.WriteFile(filepath.Join(repo, "cmd", "ctx", "main.go"), []byte("package main\n\nvar version = \"x\"\n\nfunc main() { println(version) }\n"), 0o644)
	sh("add", ".")
	sh("commit", "-q", "-m", "init")
	dest, cache := t.TempDir(), filepath.Join(t.TempDir(), "src")
	path, v, err := BuildFromSource(context.Background(), repo, "dev", cache, dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "0.4.0-dev+") || len(v) != len("0.4.0-dev+")+7 {
		t.Fatalf("version %q", v)
	}
	out, _ := exec.Command(path).CombinedOutput() // our own fake program, built here
	if strings.TrimSpace(string(out)) != v {
		t.Fatalf("binary says %q, want %q", out, v)
	}
	// Second call fetches instead of cloning.
	if _, _, err := BuildFromSource(context.Background(), repo, "dev", cache, dest); err != nil {
		t.Fatal(err)
	}
}

func timeAt(i int) time.Time { return time.Unix(1_700_000_000+int64(i)*100, 0) }

func TestLatestReleaseWithPreTakesTheNewestIncludingCandidates(t *testing.T) {
	f := newRelease(t, nil)
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	ctx := context.Background()
	// The fake lists v1.3.0-rc.1 (prerelease, no assets), v1.1.0 and v1.2.0. The
	// release candidate is newer than both, so with pre it is the pick, and
	// without pre it stays out.
	cand, err := src.LatestRelease(ctx, f.srv.URL, "Deadweight-Labs/ghosttree", true)
	if cand.Version != "1.3.0-rc.1" || cand.Tag != "v1.3.0-rc.1" {
		t.Fatalf("pre picked %+v (err %v)", cand, err)
	}
	if err == nil {
		t.Fatalf("the candidate has no assets and must be reported as incomplete, not installed")
	}
	cand, err = src.LatestRelease(ctx, f.srv.URL, "Deadweight-Labs/ghosttree", false)
	if err != nil || cand.Version != "1.2.0" {
		t.Fatalf("without pre: %+v (err %v)", cand, err)
	}
}

func TestLatestReleaseWithPreStillRefusesAnUnsignedCandidate(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Deadweight-Labs/ghosttree/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"tag_name":"v0.1.0-rc.3","prerelease":true,"assets":[{"name":"ctx_0.1.0-rc.3_linux_amd64.tar.gz","browser_download_url":"%[1]s/a"},{"name":"checksums.txt","browser_download_url":"%[1]s/s"}]},{"tag_name":"v0.1.0-beta.1","prerelease":true,"assets":[]}]`, base)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	src := Source{HTTP: srv.Client(), GOOS: goos, GOARCH: goarch}
	_, err := src.LatestRelease(context.Background(), srv.URL, "Deadweight-Labs/ghosttree", true)
	if err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("err = %v, want the unsigned release refused", err)
	}
}

// Die GitHub-Releases benennt goreleaser nach dem Projekt: ghosttree_<v>_<os>_<arch>.
// Mit dem Namen ctx_ fand der stable-Kanal das Archiv am Release v0.1.0-rc.3 nicht.
func TestStableFindsTheReleaseArchiveNamedAfterTheProject(t *testing.T) {
	f := newRelease(t, func(f *fakeRelease) { f.prefix = "ghosttree" })
	dir, target := installedDir(t)
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ghosttree_1.2.0_%s_%s.tar.gz", goos, goarch); cand.Archive != want {
		t.Fatalf("archive %q, want %q", cand.Archive, want)
	}
	var calls []string
	path, err := src.Fetch(context.Background(), cand, dir, okVerifier(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(target, path, "0.1.0-dev+abc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != f.binary {
		t.Fatalf("installed %q", got)
	}
}
