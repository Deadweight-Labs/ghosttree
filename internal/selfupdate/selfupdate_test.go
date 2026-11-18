package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
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

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
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
	withSig    bool
	comment    string // trusted comment of the signature ("" = ghosttree v1.2.0)
	foreignKey bool   // sign with a key other than pk
	prefix     string // archive name prefix ("" = ctx)
	hits       map[string]int
	pk         minisign.PublicKey
}

// verifier checks against the release key of this fake.
func (f *fakeRelease) verifier() Verifier {
	return MinisignVerifier(func() (minisign.PublicKey, error) { return f.pk, nil })
}

// newRelease serves a GitHub release API plus its assets for tag v1.2.0.
func newRelease(t *testing.T, mod func(*fakeRelease)) *fakeRelease {
	t.Helper()
	f := &fakeRelease{binary: "#!/bin/sh\necho new\n", withSig: true, hits: map[string]int{}}
	if mod != nil {
		mod(f)
	}
	pk, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.pk = pk
	signer, signerPriv := pk, priv
	if f.foreignKey {
		if signer, signerPriv, err = minisign.GenerateKey(rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	prefix := f.prefix
	if prefix == "" {
		prefix = "ctx"
	}
	archiveName := fmt.Sprintf("%s_1.2.0_%s_%s.tar.gz", prefix, goos, goarch)
	archive := tarball(t, f.binary)
	sum := f.listedSum
	if sum == "" {
		sum = sha(archive)
	}
	sums := []byte(fmt.Sprintf("%s  %s\n", sum, archiveName))
	comment := f.comment
	if comment == "" {
		comment = "ghosttree v1.2.0"
	}
	sig := minisign.Sign(signer, signerPriv, sums, comment)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/Deadweight-Labs/ghosttree/releases", func(w http.ResponseWriter, r *http.Request) {
		assets := fmt.Sprintf(`{"name":%q,"browser_download_url":"%s/a/%s"},{"name":"checksums.txt","browser_download_url":"%s/a/checksums.txt"}`, archiveName, base, archiveName, base)
		if f.withSig {
			assets += fmt.Sprintf(`,{"name":"checksums.txt.minisig","browser_download_url":"%s/a/checksums.txt.minisig"}`, base)
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
			w.Write(sums)
		case "checksums.txt.minisig":
			w.Write(sig)
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

func latest(t *testing.T, f *fakeRelease) (Source, Candidate) {
	t.Helper()
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	if err != nil {
		t.Fatal(err)
	}
	return src, cand
}

func TestStableInstallsAfterSignatureAndChecksum(t *testing.T) {
	f := newRelease(t, nil)
	dir, target := installedDir(t)
	src, cand := latest(t, f)
	if cand.Version != "1.2.0" || cand.Tag != "v1.2.0" {
		t.Fatalf("picked %+v: want the newest non-draft non-prerelease with assets", cand)
	}
	got, err := src.Fetch(context.Background(), cand, dir, f.verifier(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Signed || got.Version != "1.2.0" {
		t.Fatalf("fetched %+v: the version has to come from the signed comment", got)
	}
	backup, err := Install(target, got.Path, "0.1.0-dev+abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != f.binary {
		t.Fatalf("target = %q", b)
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
	src, cand := latest(t, f)
	_, err := src.Fetch(context.Background(), cand, dir, f.verifier(), false)
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
	f := newRelease(t, func(f *fakeRelease) { f.withSig = false })
	src := Source{HTTP: f.srv.Client(), GOOS: goos, GOARCH: goarch}
	_, err := src.LatestStable(context.Background(), f.srv.URL, "Deadweight-Labs/ghosttree")
	if err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("err = %v", err)
	}
	if f.hits["checksums.txt"] != 0 {
		t.Fatal("downloaded before refusing")
	}
}

// A signature that does not come from the release key, a signed list of another
// release, a missing verifier and a build without the key all stop before the
// archive is downloaded, and --trust-server (meant for servers) does not turn a
// release into a trusted one.
func TestStableSignatureProblemsStopBeforeTheArchive(t *testing.T) {
	noKey := MinisignVerifier(func() (minisign.PublicKey, error) { return minisign.PublicKey{}, errors.New("no key yet") })
	cases := map[string]struct {
		mod    func(*fakeRelease)
		verify func(*fakeRelease) Verifier
		want   string
	}{
		"another key":           {func(f *fakeRelease) { f.foreignKey = true }, (*fakeRelease).verifier, "signature check failed"},
		"list of other release": {func(f *fakeRelease) { f.comment = "ghosttree v1.1.0" }, (*fakeRelease).verifier, "belongs to"},
		"no trusted label":      {func(f *fakeRelease) { f.comment = "v1.2.0" }, (*fakeRelease).verifier, "belongs to"},
		"no verifier":           {nil, func(*fakeRelease) Verifier { return nil }, "no signature verifier"},
		"build without a key":   {nil, func(*fakeRelease) Verifier { return noKey }, "cannot check"},
	}
	for name, c := range cases {
		for _, trust := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s trust=%v", name, trust), func(t *testing.T) {
				f := newRelease(t, c.mod)
				dir, _ := installedDir(t)
				src, cand := latest(t, f)
				_, err := src.Fetch(context.Background(), cand, dir, c.verify(f), trust)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want %q", err, c.want)
				}
				if f.hits[cand.Archive] != 0 || entries(t, dir) != "ctx" {
					t.Fatalf("archive fetched (%d) or directory changed: %s", f.hits[cand.Archive], entries(t, dir))
				}
			})
		}
	}
}

// What the checksums say is only believed after the signature: a list whose
// signature belongs to another version is not taken, and a changed list is not
// signed any more.
func TestStableChangedChecksumsFailTheSignature(t *testing.T) {
	f := newRelease(t, nil)
	dir, _ := installedDir(t)
	src, cand := latest(t, f)
	tampered := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a/checksums.txt" {
			fmt.Fprintf(w, "%s  %s\n", strings.Repeat("e", 64), cand.Archive)
			return
		}
		tampered.ServeHTTP(w, r)
	})
	if _, err := src.Fetch(context.Background(), cand, dir, f.verifier(), false); err == nil || !strings.Contains(err.Error(), "signature check failed") {
		t.Fatalf("err = %v", err)
	}
}

func devServer(t *testing.T, listing string, sig func(sums []byte) []byte) *httptest.Server {
	t.Helper()
	archive := tarball(t, "DEVBIN")
	name := "ctx_0.1.0-dev+b0cd1b9_linux_amd64.tar.gz"
	sums := []byte(listing)
	if listing == "" {
		sums = []byte(fmt.Sprintf("%s  %s\n", sha(archive), name))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dist/checksums.txt", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	if sig != nil {
		mux.HandleFunc("/dist/checksums.txt.minisig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig(sums)) })
	}
	mux.HandleFunc("/dist/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDevFromServerWithoutSignatureNeedsTheUsersAgreement(t *testing.T) {
	srv := devServer(t, "", nil)
	dir, target := installedDir(t)
	src := Source{HTTP: srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.DevFromServer(context.Background(), srv.URL)
	if err != nil || cand.Version != "0.1.0-dev+b0cd1b9" {
		t.Fatalf("cand %+v err %v", cand, err)
	}
	_, err = src.Fetch(context.Background(), cand, dir, nil, false)
	if !errors.Is(err, ErrServerUnsigned) || entries(t, dir) != "ctx" {
		t.Fatalf("an unsigned server build was taken on the server's word alone: err %v, dir %s", err, entries(t, dir))
	}
	got, err := src.Fetch(context.Background(), cand, dir, nil, true)
	if err != nil || got.Signed {
		t.Fatalf("err %v fetched %+v", err, got)
	}
	if _, err := Install(target, got.Path, "0.1.0-dev+1c48c74"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "DEVBIN" {
		t.Fatalf("target %q", b)
	}
}

func TestDevFromServerTakesAReleaseKeySignatureWithoutAgreement(t *testing.T) {
	pk, priv, _ := minisign.GenerateKey(rand.Reader)
	verify := MinisignVerifier(func() (minisign.PublicKey, error) { return pk, nil })
	sign := func(comment string) func([]byte) []byte {
		return func(sums []byte) []byte { return minisign.Sign(pk, priv, sums, comment) }
	}
	dir, _ := installedDir(t)

	ok := devServer(t, "", sign("ghosttree v0.1.0-dev+b0cd1b9"))
	src := Source{HTTP: ok.Client(), GOOS: goos, GOARCH: goarch}
	cand, _ := src.DevFromServer(context.Background(), ok.URL)
	got, err := src.Fetch(context.Background(), cand, dir, verify, false)
	if err != nil || !got.Signed || got.Version != "0.1.0-dev+b0cd1b9" {
		t.Fatalf("err %v fetched %+v", err, got)
	}
	os.Remove(got.Path)

	// A signature that is there and does not hold is final, with or without
	// the agreement: it is not a build "without" one.
	for name, srv := range map[string]*httptest.Server{
		"another release": devServer(t, "", sign("ghosttree v0.0.9")),
		"foreign key": devServer(t, "", func(sums []byte) []byte {
			opk, opriv, _ := minisign.GenerateKey(rand.Reader)
			return minisign.Sign(opk, opriv, sums, "ghosttree v0.1.0-dev+b0cd1b9")
		}),
		"garbage": devServer(t, "", func([]byte) []byte { return []byte("nonsense") }),
	} {
		src := Source{HTTP: srv.Client(), GOOS: goos, GOARCH: goarch}
		cand, _ := src.DevFromServer(context.Background(), srv.URL)
		for _, trust := range []bool{false, true} {
			if _, err := src.Fetch(context.Background(), cand, dir, verify, trust); err == nil {
				t.Fatalf("%s (trust=%v) was installed", name, trust)
			}
		}
	}
	if entries(t, dir) != "ctx" {
		t.Fatalf("directory changed: %s", entries(t, dir))
	}
}

// A server build with a signature this ctx has no key for counts as unsigned:
// it needs the user's agreement and never passes for checked.
func TestDevFromServerSignatureWithoutAKeyNeedsTheAgreement(t *testing.T) {
	pk, priv, _ := minisign.GenerateKey(rand.Reader)
	srv := devServer(t, "", func(sums []byte) []byte { return minisign.Sign(pk, priv, sums, "ghosttree v0.1.0-dev+b0cd1b9") })
	noKey := MinisignVerifier(func() (minisign.PublicKey, error) { return minisign.PublicKey{}, errors.New("placeholder") })
	dir, _ := installedDir(t)
	src := Source{HTTP: srv.Client(), GOOS: goos, GOARCH: goarch}
	cand, _ := src.DevFromServer(context.Background(), srv.URL)
	if _, err := src.Fetch(context.Background(), cand, dir, noKey, false); err == nil || !strings.Contains(err.Error(), "cannot check") {
		t.Fatalf("err = %v", err)
	}
	got, err := src.Fetch(context.Background(), cand, dir, noKey, true)
	if err != nil || got.Signed {
		t.Fatalf("err %v fetched %+v", err, got)
	}
}

func TestDevFromServerRejectsBadChecksumAndAmbiguousListing(t *testing.T) {
	bad := devServer(t, strings.Repeat("b", 64)+"  ctx_0.1.0-dev+b0cd1b9_linux_amd64.tar.gz\n", nil)
	dir, _ := installedDir(t)
	src := Source{HTTP: bad.Client(), GOOS: goos, GOARCH: goarch}
	cand, err := src.DevFromServer(context.Background(), bad.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Fetch(context.Background(), cand, dir, nil, true); err == nil || entries(t, dir) != "ctx" {
		t.Fatalf("err %v dir %s", err, entries(t, dir))
	}
	two := devServer(t, strings.Repeat("c", 64)+"  ctx_1_linux_amd64.tar.gz\n"+strings.Repeat("c", 64)+"  ctx_2_linux_amd64.tar.gz\n", nil)
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
	got, err := src.Fetch(context.Background(), cand, dir, f.verifier(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(target, got.Path, "0.1.0-dev+abc"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != f.binary {
		t.Fatalf("installed %q", b)
	}
}
