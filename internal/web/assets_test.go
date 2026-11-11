package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func serveStatic(t *testing.T, target string, header map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	(&app{}).staticFiles().ServeHTTP(rec, req)
	return rec.Result()
}

func staticBody(t *testing.T, r *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAssetURLCarriesTheContentHash(t *testing.T) {
	set := loadAssets()
	got := assetURL("ui-base.css")
	if want := "/static/ui-base.css?v=" + set["ui-base.css"].version; got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
	if len(set["ui-base.css"].version) != 12 {
		t.Errorf("version %q is not 12 hex digits", set["ui-base.css"].version)
	}
	// The hash follows the content: other bytes, other address.
	if a, b := newAsset("x.css", []byte("a{}")), newAsset("x.css", []byte("b{}")); a.version == b.version {
		t.Error("two different contents share a version")
	}
	if got := assetURL("nothere.css"); got != "/static/nothere.css" {
		t.Errorf("a missing file got %q", got)
	}
}

func TestVersionedAddressIsImmutableAndOthersRevalidate(t *testing.T) {
	v := loadAssets()["ui-tokens.css"].version
	res := serveStatic(t, "/static/ui-tokens.css?v="+v, nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("versioned: %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	for _, target := range []string{"/static/ui-tokens.css", "/static/ui-tokens.css?v=old", "/static/ui-tokens.css?v="} {
		res := serveStatic(t, target, nil)
		if res.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", target, res.Header.Get("Cache-Control"))
		}
		if res.Header.Get("ETag") == "" {
			t.Errorf("%s: no ETag", target)
		}
	}
}

func TestStaticFileIsGzippedOnlyWhenAccepted(t *testing.T) {
	plain := serveStatic(t, "/static/ui-md.js", nil)
	raw := staticBody(t, plain)
	if plain.Header.Get("Content-Encoding") != "" || plain.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("without Accept-Encoding: encoding %q vary %q", plain.Header.Get("Content-Encoding"), plain.Header.Get("Vary"))
	}
	zipped := serveStatic(t, "/static/ui-md.js", map[string]string{"Accept-Encoding": "gzip, deflate"})
	if zipped.Header.Get("Content-Encoding") != "gzip" || zipped.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("with gzip: encoding %q vary %q", zipped.Header.Get("Content-Encoding"), zipped.Header.Get("Vary"))
	}
	if zipped.Header.Get("ETag") == plain.Header.Get("ETag") {
		t.Error("the gzip and the plain body share an ETag")
	}
	zr, err := gzip.NewReader(bytes.NewReader(staticBody(t, zipped)))
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(zr)
	if !bytes.Equal(back, raw) {
		t.Error("the gzip body does not unpack to the plain body")
	}
	if zipped.ContentLength >= int64(len(raw)) {
		t.Errorf("gzip body %d bytes is not smaller than %d", zipped.ContentLength, len(raw))
	}
	for _, refuse := range []string{"gzip;q=0", "identity", "br", "*;q=0", "gzip;q=0, *"} {
		res := serveStatic(t, "/static/ui-md.js", map[string]string{"Accept-Encoding": refuse})
		if res.Header.Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q still got %q", refuse, res.Header.Get("Content-Encoding"))
		}
	}
	for _, allow := range []string{"gzip", "GZIP", "gzip;q=0.5", "br, gzip", "*"} {
		res := serveStatic(t, "/static/ui-md.js", map[string]string{"Accept-Encoding": allow})
		if res.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding %q got %q, want gzip", allow, res.Header.Get("Content-Encoding"))
		}
	}
}

func TestStaticFileAnswersNotModifiedForItsETag(t *testing.T) {
	for _, ae := range []string{"", "gzip"} {
		first := serveStatic(t, "/static/ui-shell.css", map[string]string{"Accept-Encoding": ae})
		etag := first.Header.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag")
		}
		for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
			res := serveStatic(t, "/static/ui-shell.css", map[string]string{"Accept-Encoding": ae, "If-None-Match": inm})
			if res.StatusCode != http.StatusNotModified {
				t.Errorf("ae %q If-None-Match %q: status %d", ae, inm, res.StatusCode)
			}
			if res.Header.Get("Vary") != "Accept-Encoding" || res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("ETag") != etag {
				t.Errorf("the 304 lost its headers: %v", res.Header)
			}
			if n := len(staticBody(t, res)); n != 0 {
				t.Errorf("the 304 carries %d bytes", n)
			}
		}
		if res := serveStatic(t, "/static/ui-shell.css", map[string]string{"Accept-Encoding": ae, "If-None-Match": `"stale"`}); res.StatusCode != http.StatusOK {
			t.Errorf("a stale ETag got %d", res.StatusCode)
		}
	}
	// A validator of the plain body does not match the gzip body.
	plain := serveStatic(t, "/static/ui-shell.css", nil).Header.Get("ETag")
	if res := serveStatic(t, "/static/ui-shell.css", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": plain}); res.StatusCode != http.StatusOK {
		t.Errorf("the plain ETag matched the gzip body: %d", res.StatusCode)
	}
}

func TestImagesAreNotCompressedAndMissingFilesStay404(t *testing.T) {
	res := serveStatic(t, "/static/favicon-32.png", map[string]string{"Accept-Encoding": "gzip"})
	if res.Header.Get("Content-Encoding") != "" || res.Header.Get("Vary") != "" || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("png: %v", res.Header)
	}
	if res := serveStatic(t, "/static/nothere.css", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing file: %d", res.StatusCode)
	}
}

func TestBundleIsTheFilesInOrder(t *testing.T) {
	set := loadAssets()
	for name, parts := range assetBundles {
		body := string(set[name].body)
		at := 0
		for _, part := range parts {
			i := strings.Index(body[at:], strings.TrimSpace(string(set[part].body)))
			if i < 0 {
				t.Fatalf("%s: %s is missing or out of order", name, part)
			}
			at += i + len(strings.TrimSpace(string(set[part].body)))
		}
		if set[name].gz == nil {
			t.Errorf("%s is not compressed", name)
		}
		// Every script of the bundle is a closed function, so a bundle cannot
		// make one script see the names of another.
		if strings.HasSuffix(name, ".js") {
			for _, part := range parts {
				if !strings.HasSuffix(strings.TrimSpace(string(set[part].body)), "})();") {
					t.Errorf("%s does not end as a closed function", part)
				}
			}
		}
	}
}

// Every static address a page writes carries the version, and the pages do not
// load the files of the bundles one by one.
func TestPagesNameStaticFilesWithVersionAndLoadTheBundles(t *testing.T) {
	e := ovEnv(t)
	inBundle := map[string]bool{}
	for _, parts := range assetBundles {
		for _, p := range parts {
			inBundle[p] = true
		}
	}
	ref := regexp.MustCompile(`(?:href|src|srcset)="(/static/[^"]+)"`)
	for _, path := range []string{"/ui/inbox", "/ui/agents", "/ui/knowledge", "/ui/profile", "/ui/coord"} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		refs := ref.FindAllStringSubmatch(page, -1)
		if len(refs) == 0 {
			t.Errorf("%s names no static file", path)
		}
		bundles := 0
		for _, m := range refs {
			name, v, ok := strings.Cut(strings.TrimPrefix(m[1], "/static/"), "?v=")
			if !ok || loadAssets()[name] == nil || loadAssets()[name].version != v {
				t.Errorf("%s: %q has no current version", path, m[1])
			}
			if inBundle[name] {
				t.Errorf("%s loads %s on its own although the bundle has it", path, name)
			}
			if strings.HasPrefix(name, "bundle-base.") {
				bundles++
			}
		}
		if bundles != 2 {
			t.Errorf("%s loads %d bundles, want 2", path, bundles)
		}
	}
}

// Dynamic pages are not compressed here: they carry CSRF tokens and text a
// reader typed, the mix that lets BREACH guess a secret from the size of a
// compressed response.
func TestDynamicPagesAreNotCompressed(t *testing.T) {
	e := ovEnv(t)
	req, _ := http.NewRequest(http.MethodGet, e.Base+"/ui/inbox", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	res, err := e.Owner.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "" {
		t.Errorf("an HTML page came back %q", res.Header.Get("Content-Encoding"))
	}
}
