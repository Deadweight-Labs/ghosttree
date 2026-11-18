package serverinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
)

type testKey struct {
	pub  minisign.PublicKey
	priv ed25519.PrivateKey
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub, priv}
}

func tarGz(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ctx", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeReleases serves <tag>/<file> and the newest-release answer.
type fakeReleases struct {
	mu     sync.Mutex
	files  map[string][]byte
	latest string
	srv    *httptest.Server
}

func newFakeReleases(t *testing.T) *fakeReleases {
	t.Helper()
	f := &fakeReleases{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			if f.latest == "" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"tag_name": %q}`, f.latest)
			return
		}
		b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReleases) release(k testKey) Release {
	return Release{APIBase: f.srv.URL + "/api", DLBase: f.srv.URL + "/dl", Key: k.pub, GOOS: "linux", GOARCH: "amd64"}
}

func (f *fakeReleases) publish(t *testing.T, k testKey, tag, ctxContent string) {
	t.Helper()
	archive := tarGz(t, ctxContent)
	name := fmt.Sprintf("ghosttree_%s_linux_amd64.tar.gz", strings.TrimPrefix(tag, "v"))
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[tag+"/"+name] = archive
	f.files[tag+"/checksums.txt"] = sums
	f.files[tag+"/checksums.txt.minisig"] = minisign.Sign(k.pub, k.priv, sums, "ghosttree "+tag)
}

func (f *fakeReleases) set(path string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b == nil {
		delete(f.files, path)
	} else {
		f.files[path] = b
	}
}

func (f *fakeReleases) get(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.files[path]...)
}

func TestFetchInstallsAGenuineRelease(t *testing.T) {
	k := newTestKey(t)
	f := newFakeReleases(t)
	f.publish(t, k, "v0.1.0", "#!/bin/sh\necho ctx\n")
	dir := t.TempDir()
	got, err := f.release(k).Fetch(context.Background(), "v0.1.0", dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got.Binary)
	if string(b) != "#!/bin/sh\necho ctx\n" || got.Version != "0.1.0" || filepath.Dir(got.Binary) != dir {
		t.Fatalf("fetched %+v: %q", got, b)
	}
	if fi, _ := os.Stat(got.Binary); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

// What install.sh refuses, the update refuses too, and leaves no file behind.
func TestFetchRefusesATamperedRelease(t *testing.T) {
	other := newTestKey(t)
	cases := []struct {
		name   string
		tamper func(t *testing.T, f *fakeReleases, k testKey)
		want   string
	}{
		{"archive changed", func(t *testing.T, f *fakeReleases, k testKey) {
			a := f.get("v0.1.0/ghosttree_0.1.0_linux_amd64.tar.gz")
			a[len(a)/2] ^= 0xff
			f.set("v0.1.0/ghosttree_0.1.0_linux_amd64.tar.gz", a)
		}, "checksum mismatch"},
		{"archive and checksums swapped, signature kept", func(t *testing.T, f *fakeReleases, k testKey) {
			evil := tarGz(t, "evil")
			sum := sha256.Sum256(evil)
			f.set("v0.1.0/ghosttree_0.1.0_linux_amd64.tar.gz", evil)
			f.set("v0.1.0/checksums.txt", []byte(hex.EncodeToString(sum[:])+"  ghosttree_0.1.0_linux_amd64.tar.gz\n"))
		}, "does not match the ghosttree release key"},
		{"signed by another key", func(t *testing.T, f *fakeReleases, k testKey) {
			f.publish(t, other, "v0.1.0", "evil")
		}, "does not match the ghosttree release key"},
		{"no signature", func(t *testing.T, f *fakeReleases, k testKey) {
			f.set("v0.1.0/checksums.txt.minisig", nil)
		}, "without a signature"},
		{"another release's genuine list", func(t *testing.T, f *fakeReleases, k testKey) {
			f.publish(t, k, "v0.0.9", "old")
			for _, n := range []string{"checksums.txt", "checksums.txt.minisig"} {
				f.set("v0.1.0/"+n, f.get("v0.0.9/"+n))
			}
			f.set("v0.1.0/ghosttree_0.1.0_linux_amd64.tar.gz", f.get("v0.0.9/ghosttree_0.0.9_linux_amd64.tar.gz"))
		}, `belongs to "ghosttree v0.0.9"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := newTestKey(t)
			f := newFakeReleases(t)
			f.publish(t, k, "v0.1.0", "good")
			c.tamper(t, f, k)
			dir := t.TempDir()
			_, err := f.release(k).Fetch(context.Background(), "v0.1.0", dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
			if left, _ := os.ReadDir(dir); len(left) != 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

func TestFetchRefusesOddTagsAndPlainHTTPFromElsewhere(t *testing.T) {
	k := newTestKey(t)
	f := newFakeReleases(t)
	for _, tag := range []string{"", "main", "v1.2", "../x", "v1.2.3;x"} {
		if _, err := f.release(k).Fetch(context.Background(), tag, t.TempDir()); err == nil {
			t.Errorf("%q accepted", tag)
		}
	}
	r := f.release(k)
	r.DLBase = "http://downloads.example.org/dl"
	if _, err := r.Fetch(context.Background(), "v0.1.0", t.TempDir()); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Errorf("plain http: %v", err)
	}
}

func TestLatestFindsTheStableTagOrSaysThereIsNone(t *testing.T) {
	k := newTestKey(t)
	f := newFakeReleases(t)
	if _, err := f.release(k).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Errorf("no release: %v", err)
	}
	f.latest = "v0.2.0"
	if tag, err := f.release(k).Latest(context.Background()); err != nil || tag != "v0.2.0" {
		t.Errorf("latest = %q, %v", tag, err)
	}
	f.latest = "nightly"
	if _, err := f.release(k).Latest(context.Background()); err == nil {
		t.Error("an odd tag was accepted")
	}
}

// "http://localhost:1@example.org" is a request to example.org.
func TestPlainHttpIsOnlyForThisMachine(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://github.com/x":                true,
		"http://localhost:8080/dl":            true,
		"http://127.0.0.1:1/x":                true,
		"http://[::1]:8080/x":                 true,
		"http://localhost:1@example.org/dl":   false,
		"http://localhost@example.org/dl":     false,
		"http://127.0.0.1:1@example.org/dl":   false,
		"http://[::1]:1@example.org/dl":       false,
		"http://localhost.example.org/dl":     false,
		"http://127.0.0.1.example.org/dl":     false,
		"http://example.org/dl":               false,
		"ftp://localhost/dl":                  false,
		"http://localhost:1\\@example.org/dl": false,
	} {
		if err := httpsOrLoopback(raw); (err == nil) != ok {
			t.Errorf("%s: accepted=%v, want %v (%v)", raw, err == nil, ok, err)
		}
	}
}

// The case the rule is for: the address is https and the answer sends the
// client to plain http, where a genuine copy of the file is waiting. Following
// it would succeed, so refusing it is what the test sees.
func TestReleaseDownloadDoesNotFollowARedirectFromHttpsToHttp(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("genuine")) }))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.Write([]byte("genuine"))
			return
		}
		http.Redirect(w, r, plain.URL+"/x", http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	rel := Release{HTTP: secure.Client()}
	if b, err := rel.get(context.Background(), secure.URL+"/ok", 100); err != nil || string(b) != "genuine" {
		t.Fatalf("the control request failed: %q %v", b, err)
	}
	if b, err := rel.get(context.Background(), secure.URL+"/x", 100); err == nil {
		t.Fatalf("followed a redirect to plain http: %q", b)
	} else if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error does not say why: %v", err)
	}
}

// A mirror on this machine may redirect on this machine, and nowhere else.
func TestReleaseDownloadFollowsRedirectsOnThisMachineOnly(t *testing.T) {
	var self *httptest.Server
	self = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, self.URL+"/b", http.StatusFound)
		case "/b":
			w.Write([]byte("b"))
		default:
			http.Redirect(w, r, "http://downloads.example.org/x", http.StatusFound)
		}
	}))
	t.Cleanup(self.Close)
	rel := Release{}
	if b, err := rel.get(context.Background(), self.URL+"/a", 100); err != nil || string(b) != "b" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := rel.get(context.Background(), self.URL+"/away", 100); err == nil {
		t.Error("followed a redirect to another host over plain http")
	}
}
