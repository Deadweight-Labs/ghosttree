package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// REQ-672: the diagrams of a text come from the library of this server, drawn
// in a frame of their own, and the policy of the pages stays as strict as it was.

// The policy of the pages is not loosened for the diagrams: no inline code, no
// other host. The only addition is the one frame of this origin.
func TestPagePolicyStaysStrictAndNamesOnlyTheFrameOfItsOwnOrigin(t *testing.T) {
	e := ovEnv(t)
	resp, err := e.Owner.Get(e.Base + "/ui/inbox")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "img-src 'self' data:", "connect-src 'self'", "frame-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:", "*", "blob:", "'unsafe-hashes'", "'nonce-"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP %q holds %q", csp, bad)
		}
	}
	// A page without a diagram loads nothing of it: no frame, no library, no script of the diagrams.
	for _, bad := range []string{"<iframe", "mermaid.min.js", "mermaid-frame.js", `src="/static/ui-mermaid.js`} {
		if strings.Contains(page, bad) {
			t.Errorf("the page names %q although it has no diagram", bad)
		}
	}
	// The address of the script that draws is on the body, with the hash of its content.
	m := regexp.MustCompile(`<body[^>]*data-mermaid="(/static/ui-mermaid\.js\?v=([0-9a-f]{12}))"`).FindStringSubmatch(page)
	if m == nil || loadAssets()["ui-mermaid.js"].version != m[2] {
		t.Errorf("the body does not carry the versioned address of ui-mermaid.js: %v", m)
	}
}

func TestMermaidIsNotPartOfTheBaseBundle(t *testing.T) {
	set := loadAssets()
	for name, parts := range assetBundles {
		for _, part := range parts {
			if strings.HasPrefix(part, "vendor/") || part == "ui-mermaid.js" || part == "mermaid-frame.js" {
				t.Errorf("%s holds %s", name, part)
			}
		}
	}
	if b := set["bundle-base.js"].body; bytes.Contains(b, []byte("__esbuild_esm_mermaid_nm")) || len(b) > 1<<20 {
		t.Errorf("bundle-base.js is %d bytes or holds the library", len(b))
	}
	if set["vendor/mermaid.min.js"] == nil || len(set["vendor/mermaid.min.js"].body) < 1<<20 {
		t.Fatal("the library is not among the static files")
	}
}

// The page of the frame needs no session, has a policy of its own that allows
// what the library needs and nothing else, and names the library and its script by
// versioned address.
func TestMermaidFrameIsAPageOfItsOwnWithItsOwnPolicy(t *testing.T) {
	e := ovEnv(t)
	resp, err := http.Get(e.Base + "/ui/mermaid-frame")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("frame: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	csp := resp.Header.Get("Content-Security-Policy")
	directives := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, val, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = val
	}
	want := map[string]string{
		"default-src": "'none'", "script-src": "'self'", "style-src": "'unsafe-inline'", "img-src": "data:",
		"base-uri": "'none'", "form-action": "'none'", "frame-ancestors": "'self'", "sandbox": "allow-scripts",
	}
	for k, v := range want {
		if directives[k] != v {
			t.Errorf("%s: %q, want %q (CSP %q)", k, directives[k], v, csp)
		}
	}
	// Nothing opens a way out of it: no connection, no frames of its own, no eval, no same-origin sandbox.
	for _, k := range []string{"connect-src", "frame-src", "child-src", "worker-src", "object-src"} {
		if _, ok := directives[k]; ok {
			t.Errorf("the frame allows %s", k)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-top-navigation") || strings.Contains(csp, "allow-popups") {
		t.Errorf("the frame is allowed too much: %q", csp)
	}
	// Two scripts from this origin with their hash, no script of its own text.
	scripts := regexp.MustCompile(`<script([^>]*)>`).FindAllStringSubmatch(page, -1)
	if len(scripts) != 2 {
		t.Fatalf("scripts in the frame: %d\n%s", len(scripts), page)
	}
	for i, name := range []string{"vendor/mermaid.min.js", "mermaid-frame.js"} {
		if !strings.Contains(scripts[i][1], `src="/static/`+name+`?v=`+loadAssets()[name].version+`"`) {
			t.Errorf("script %d is %q, want %s with its version", i, scripts[i][1], name)
		}
	}
	if strings.Contains(page, "</script>") && regexp.MustCompile(`<script[^>]*>[^<]+</script>`).MatchString(page) {
		t.Error("the frame has a script of its own text")
	}
	// The library is of this server: both files answer with the content they were hashed from.
	for _, name := range []string{"vendor/mermaid.min.js", "mermaid-frame.js"} {
		r, err := http.Get(e.Base + "/static/" + name + "?v=" + loadAssets()[name].version)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK || !bytes.Equal(b, loadAssets()[name].body) {
			t.Errorf("%s: %d, %d bytes", name, r.StatusCode, len(b))
		}
	}
}

func TestMermaidFrameOnlyAnswersGet(t *testing.T) {
	e := ovEnv(t)
	resp, err := http.Post(e.Base+"/ui/mermaid-frame", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST -> %d, want 405", resp.StatusCode)
	}
}

// The library is a file of 5.5 MB: it is cached for a year under its versioned
// address, sent compressed to a client that asks for it, and compressed once and
// only when somebody asks, not by every process that starts.
func TestVendorLibraryIsServedVersionedAndCompressedOnDemand(t *testing.T) {
	a := loadAssets()["vendor/mermaid.min.js"]
	if a == nil {
		t.Fatal("no vendor/mermaid.min.js")
	}
	addr := "/static/vendor/mermaid.min.js?v=" + a.version
	if got := assetURL("vendor/mermaid.min.js"); got != addr {
		t.Errorf("assetURL = %q, want %q", got, addr)
	}
	res := serveStatic(t, addr, map[string]string{"Accept-Encoding": "gzip"})
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		res.Header.Get("Content-Type") != "text/javascript; charset=utf-8" || res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("library: %d %v", res.StatusCode, res.Header)
	}
	packed := staticBody(t, res)
	if len(packed) > len(a.body)/2 {
		t.Errorf("sent %d bytes for %d", len(packed), len(a.body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); !bytes.Equal(plain, a.body) {
		t.Error("the compressed library is not the library")
	}
	res = serveStatic(t, addr, nil)
	if res.Header.Get("Content-Encoding") != "" || len(staticBody(t, res)) != len(a.body) {
		t.Errorf("without Accept-Encoding the file is sent as it is")
	}
	// The license text goes out with the library.
	lic := serveStatic(t, "/static/vendor/mermaid.LICENSE.txt", nil)
	if txt := string(staticBody(t, lic)); lic.StatusCode != http.StatusOK || !strings.Contains(txt, "MIT License") || !strings.Contains(txt, "Knut Sveidqvist") {
		t.Errorf("license: %d %q", lic.StatusCode, txt)
	}
	// A file of that size is not compressed when the process starts.
	lazy := newLazyAsset("vendor/x.js", bytes.Repeat([]byte("function a(){}\n"), 1000))
	if lazy.gz != nil || !lazy.lazyGz {
		t.Error("a vendor file is compressed at start")
	}
	if lazy.gzipped() == nil {
		t.Error("a vendor file is not compressed when asked for")
	}
	if small := newAsset("x.js", bytes.Repeat([]byte("function a(){}\n"), 1000)); small.gz == nil {
		t.Error("an ordinary file is no longer compressed at start")
	}
}

// What is in the binary is what the notice says: the file, its version and its hash.
func TestThirdPartyNoticeDescribesTheLibraryInTheBinary(t *testing.T) {
	lib := mustReadEmbedded(t, "static/vendor/mermaid.min.js")
	sum := sha256.Sum256(lib)
	notice, err := os.ReadFile("../../THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{hex.EncodeToString(sum[:]), "mermaid@12.3.0", "Version: 12.3.0", "MIT", "internal/web/static/vendor/mermaid.min.js", "internal/web/static/vendor/mermaid.LICENSE.txt"} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("THIRD_PARTY_NOTICES.md lacks %q", want)
		}
	}
	// The library is the unchanged file of the package: it brings its own banner of licenses and is a script, not a module.
	if !bytes.Contains(lib, []byte("globalThis[\"mermaid\"]")) || bytes.Contains(lib, []byte("\nexport ")) {
		t.Error("the library is not the self-contained script")
	}
	for _, bad := range [][]byte{[]byte("new Function("), []byte("eval(")} {
		if bytes.Contains(lib, bad) {
			t.Errorf("the library holds %s, which the policy of the frame does not allow", bad)
		}
	}
	if mit := mustReadEmbedded(t, "static/vendor/mermaid.LICENSE.txt"); !bytes.Contains(mit, []byte("The MIT License")) {
		t.Error("the license text of the library is not the MIT license")
	}
}

// ---------------------------------------------------------------- without the script

// A page without the script shows every one of the new blocks as readable text:
// the diagram as a code block, the note box as a quote, the section as paragraphs,
// the image as the words that were written. The script gets the same text from data-md.
func TestWithoutTheScriptEveryNewBlockStaysReadable(t *testing.T) {
	src := "```mermaid\nflowchart LR\n  A[Start] --> B{Done?}\n```\n\n" +
		"> [!WARNING]\n> Backups run **nightly** & take <b>long</b>.\n\n" +
		"<details>\n<summary>Why `x` <i>not</i>?</summary>\n\nBecause it is slow.\n\n- one\n\n</details>\n\n" +
		"Look: ![the graph](ATT-12) here."
	out := string(mdText(src))
	for _, want := range []string{
		// diagram: the source in a code block, arrows escaped
		`<pre class="kn-code"><code>flowchart LR` + "\n" + `  A[Start] --&gt; B{Done?}</code></pre>`,
		// note box: a quote with its marker and text
		`<blockquote><p>[!WARNING]`, `Backups run **nightly** &amp; take &lt;b&gt;long&lt;/b&gt;.</p></blockquote>`,
		// section: the summary as a paragraph of its own, the body as blocks, no tag of it
		`<p><strong>Why <code>x</code> &lt;i&gt;not&lt;/i&gt;?</strong></p>`, `<p>Because it is slow.</p>`, `<ul><li>one</li></ul>`,
		// image: the text as written
		`Look: ![the graph](ATT-12) here.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no script: missing %q in\n%s", want, out)
		}
	}
	// What the page shows (after the attribute that holds the source) has no tag of the section and no image.
	shown := out[strings.Index(out, `">`)+2:]
	for _, bad := range []string{"<details", "</details", "<summary", "<img", "<b>", "<i>", "&lt;details", "&lt;/details", "&lt;summary"} {
		if strings.Contains(shown, bad) {
			t.Errorf("no script: %q in the page\n%s", bad, shown)
		}
	}
	// The script renders the text that was written, in full: nothing of the lines of the section is lost.
	m := regexp.MustCompile(`data-md="([^"]*)"`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("no data-md")
	}
	got := strings.NewReplacer("&#34;", `"`, "&lt;", "<", "&gt;", ">", "&amp;", "&", "&#39;", "'").Replace(m[1])
	if got != src {
		t.Errorf("data-md = %q\nwant      %q", got, src)
	}
}

func TestSectionLinesAreBlocksOfTheirOwnForThePageWithoutScript(t *testing.T) {
	cases := []struct{ name, src, kinds, source string }{
		{"summary on the next line", "<details>\n<summary>S</summary>\nbody\n</details>", "details,p,details-end", "<details>\n<summary>S</summary>\n\nbody\n\n</details>"},
		{"summary on the same line", "<details open><summary>S</summary>\nbody\n</details>", "details,p,details-end", "<details open><summary>S</summary>\n\nbody\n\n</details>"},
		{"no summary", "<details>\nbody\n</details>", "details,p,details-end", "<details>\n\nbody\n\n</details>"},
		{"interrupts a paragraph", "text\n<details>\n<summary>S</summary>\n</details>", "p,details,details-end", "text\n\n<details>\n<summary>S</summary>\n\n</details>"},
		{"nested and unclosed", "<details>\n<details>\n<summary>in</summary>\nx", "details,details,p", "<details>\n\n<details>\n<summary>in</summary>\n\nx"},
		{"upper case", "<DETAILS>\n<SUMMARY>S</SUMMARY>\n</DETAILS>", "details,details-end", "<DETAILS>\n<SUMMARY>S</SUMMARY>\n\n</DETAILS>"},
		{"in a code block", "```\n<details>\n</details>\n```", "code", "```\n<details>\n</details>\n```"},
		{"not alone on its line", "a <details> b", "p", "a <details> b"},
		{"a closing line with nothing open is text", "a\n</details>\nb", "p,p", "a\n\n</details>\nb"},
	}
	for _, c := range cases {
		got := bodyBlocks(c.src, 0)
		if kinds(got) != c.kinds {
			t.Errorf("%s: kinds %s, want %s", c.name, kinds(got), c.kinds)
		}
		if mdSource(got) != c.source {
			t.Errorf("%s: source %q, want %q", c.name, mdSource(got), c.source)
		}
	}
}

// The sides of the frame keep the isolation: the page makes the frame with a sandbox that
// has no allow-same-origin, and the frame draws with the library's strict security level
// and no HTML labels.
func TestMermaidScriptsKeepTheFrameIsolated(t *testing.T) {
	page, err := os.ReadFile("static/ui-mermaid.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `frame.setAttribute("sandbox", "allow-scripts")`) || strings.Contains(string(page), "allow-same-origin") {
		t.Error("ui-mermaid.js must make the frame with sandbox=allow-scripts only")
	}
	frame, err := os.ReadFile("static/mermaid-frame.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`securityLevel: "strict"`, "htmlLabels: false", "ev.source !== parent"} {
		if !strings.Contains(string(frame), want) {
			t.Errorf("mermaid-frame.js lacks %q", want)
		}
	}
}
