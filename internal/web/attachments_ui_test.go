package web

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpBytes is a lossless WebP header of w by h pixels.
func webpBytes(w, h int) []byte {
	payload := make([]byte, 16)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:5], uint32(w-1)|uint32(h-1)<<14)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBPVP8L")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

// upload sends the form with a file the way a browser does: multipart, with the
// token as a field. claimed is the type the "browser" says the file has.
func upload(t *testing.T, e shellEnv, c *http.Client, path string, fields map[string]string, data []byte, claimed string, withToken bool) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if withToken {
		_ = mw.WriteField("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if data != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="evil.html"`)
		h.Set("Content-Type", claimed)
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, e.Base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", e.Base)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

var attachSrcRE = regexp.MustCompile(`src="(/ui/attachments/\d+)"`)

// Bilder an der Idee und an einer Variante: hochladen, nebeneinander zeigen,
// vergrößern, ausliefern mit strenger Richtlinie (REQ-611 K12).
func TestImagesOnTheIdeaAndOnAVariantAreUploadedShownSideBySideAndEnlargeable(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/attach"
	data := pngBytes(t, 3, 2)
	// The browser claims the file is HTML: what counts are the bytes.
	resp := upload(t, e, e.Member, path, map[string]string{"owner": "idea", "alt": "Sketch of the button"}, data, "text/html", true)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=image") {
		t.Fatalf("upload to the idea -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	varA := strconv.FormatInt(d.Variants[0].ID, 10)
	varB := strconv.FormatInt(d.Variants[1].ID, 10)
	if resp := upload(t, e, e.Owner, path, map[string]string{"owner": "variant", "variant": varA, "alt": "Variant A mock"}, data, "image/png", true); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload to variant A -> %d", resp.StatusCode)
	}
	if resp := upload(t, e, e.Owner, path, map[string]string{"owner": "variant", "variant": varB, "alt": "Variant B mock"}, webpBytes(4, 4), "image/webp", true); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload of a WebP to variant B -> %d", resp.StatusCode)
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?done=image")
	for _, want := range []string{
		"The image was attached.", `id="images"`, `alt="Sketch of the button"`, `alt="Variant A mock"`, `alt="Variant B mock"`,
		`<dialog class="ws-lb" id="ws-lb"`, `data-zoom`, `/static/ui-workshop.js`, `enctype="multipart/form-data"`, `type="file"`, `accept="image/png,image/jpeg,image/webp"`,
		`name="owner" value="variant"`, `<option value="` + varA + `">A`, `title="Enlarge image"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the idea page lacks %q", want)
		}
	}
	// The pictures of the variants stand in their own cards, side by side in the grid of the variants.
	vars := page[strings.Index(page, `class="ws-vars"`):]
	for _, alt := range []string{`alt="Variant A mock"`, `alt="Variant B mock"`} {
		if !strings.Contains(vars, alt) {
			t.Errorf("the variants lack %s", alt)
		}
	}
	if strings.Index(vars, `alt="Variant A mock"`) > strings.Index(vars, "On the server") {
		t.Error("the picture of variant A is not in the card of variant A")
	}
	// Every picture is a link that works without script, and the script is one file of the page.
	if n := strings.Count(page, `class="ws-zoom"`); n != 3 {
		t.Errorf("%d enlargeable pictures, want 3", n)
	}
	// The pictures come from the page's own address, under a policy that allows nothing else.
	src := attachSrcRE.FindStringSubmatch(page)
	if src == nil {
		t.Fatal("no picture address in the page")
	}
	got, err := e.Owner.Get(e.Base + src[1])
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	h := got.Header
	if got.StatusCode != 200 || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(body, data) ||
		!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "inline; filename=\"attachment-") {
		t.Errorf("picture response: %d %v", got.StatusCode, h)
	}
	req, _ := http.NewRequest(http.MethodGet, e.Base+src[1], nil)
	req.Header.Set("If-None-Match", h.Get("ETag"))
	if again, err := e.Owner.Do(req); err != nil || again.StatusCode != http.StatusNotModified {
		t.Errorf("a repeated request is not answered 304: %v", err)
	}
	// A guest who may read the idea sees the pictures but has no form; the page loads nothing from elsewhere.
	_, guest := fetchPage(t, e.Guest, e.Base+ideaPath(i))
	if !strings.Contains(guest, `alt="Sketch of the button"`) || strings.Contains(guest, `enctype="multipart/form-data"`) {
		t.Errorf("a guest should see the pictures without the upload form")
	}
	if regexp.MustCompile(`<img[^>]+src="(https?:)?//`).MatchString(page) {
		t.Error("a picture comes from another origin")
	}
	script, err := e.Owner.Get(e.Base + "/static/ui-workshop.js")
	if err != nil {
		t.Fatal(err)
	}
	js, _ := io.ReadAll(script.Body)
	script.Body.Close()
	for _, want := range []string{"showModal", "ArrowRight", "ArrowLeft", "data-zoom"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	// A stranger gets the answer of a picture that does not exist.
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	a, _ := stranger.Get(e.Base + src[1])
	b, _ := stranger.Get(e.Base + "/ui/attachments/9999")
	if a.StatusCode != http.StatusNotFound || b.StatusCode != http.StatusNotFound {
		t.Errorf("stranger: %d, a missing picture: %d, want 404 for both", a.StatusCode, b.StatusCode)
	}
}

// Wer hochlädt, braucht Recht (ab member), Sitzung und Token; der Typ kommt aus
// den Bytes, Größe und Inhalt sind begrenzt.
func TestUploadNeedsTheRightATokenAndARealImageOfAllowedSize(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/attach"
	ok := pngBytes(t, 2, 2)
	owner := map[string]string{"owner": "idea"}
	if r := upload(t, e, e.Guest, path, owner, ok, "image/png", true); r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusNotFound {
		t.Errorf("a guest uploads -> %d", r.StatusCode)
	}
	if r := upload(t, e, e.Member, path, owner, ok, "image/png", false); r.StatusCode != http.StatusForbidden {
		t.Errorf("without a token -> %d, want 403", r.StatusCode)
	}
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	got := upload(t, e, stranger, path, owner, ok, "image/png", true)
	missing := upload(t, e, stranger, "/ui/ideas/9999/attach", owner, ok, "image/png", true)
	if got.StatusCode != http.StatusNotFound || missing.StatusCode != http.StatusNotFound {
		t.Errorf("stranger %d, missing idea %d, want 404 for both", got.StatusCode, missing.StatusCode)
	}
	fail := func(name string, fields map[string]string, data []byte, claimed, wantFail string) {
		t.Helper()
		r := upload(t, e, e.Member, path, fields, data, claimed, true)
		if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "fail="+wantFail) {
			t.Errorf("%s -> %d %q, want fail=%s", name, r.StatusCode, loc, wantFail)
		}
	}
	fail("svg", owner, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "image/png", "img_type")
	fail("html claiming png", owner, []byte("<html><script>alert(1)</script></html>"), "image/png", "img_type")
	fail("gif", owner, []byte("GIF89a\x01\x00\x01\x00"), "image/gif", "img_type")
	fail("png header, no image", owner, append([]byte("\x89PNG\r\n\x1a\n"), []byte("garbage")...), "image/png", "img_bad")
	fail("empty file", owner, []byte{}, "image/png", "img_none")
	fail("no file", owner, nil, "", "img_none")
	big := append(append([]byte{}, ok...), make([]byte, idea.MaxAttachmentBytes)...)
	fail("larger than the limit", owner, big, "image/png", "img_size")
	fail("a variant of nowhere", map[string]string{"owner": "variant", "variant": "99999"}, ok, "image/png", "img_other")
	if r := upload(t, e, e.Member, path, map[string]string{"owner": "variant"}, ok, "image/png", true); r.StatusCode != http.StatusBadRequest {
		t.Errorf("a variant without a number -> %d, want 400", r.StatusCode)
	}
	if r := upload(t, e, e.Member, path, map[string]string{"owner": "reply"}, ok, "image/png", true); r.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown owner -> %d, want 400", r.StatusCode)
	}
	// A file far over the limit is turned away before it is read.
	huge := append(append([]byte{}, ok...), make([]byte, maxUploadForm+1<<20)...)
	if r := upload(t, e, e.Member, path, owner, huge, "image/png", true); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a file over the form limit -> %d, want 413", r.StatusCode)
	}
	// None of the refused uploads left a picture behind.
	if metas, err := tpOwner(e).AttachmentsOf(idea.SubjectIdea, i.ID); err != nil || len(metas) != 0 {
		t.Errorf("refused uploads left %d pictures (%v)", len(metas), err)
	}
	// What is allowed is stored once: the same picture again is no new one.
	for n := 0; n < 2; n++ {
		if r := upload(t, e, e.Member, path, map[string]string{"owner": "variant", "variant": strconv.FormatInt(d.Variants[0].ID, 10)}, ok, "image/jpeg", true); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("upload %d -> %d", n, r.StatusCode)
		}
	}
	if metas, _ := tpOwner(e).AttachmentsOf(idea.SubjectIdea, i.ID); len(metas) != 1 || metas[0].Mime != "image/png" {
		t.Errorf("stored pictures: %+v (one PNG, whatever the browser claimed)", metas)
	}
}

// Nur die Upload-Route liest Multipart: an jeder anderen Route bleibt ein
// Multipart-Körper ungelesen (kein Token, kein Temporärdatei-Weg), also 403.
func TestOnlyTheUploadRouteParsesMultipartBodies(t *testing.T) {
	e, _ := listEnv(t)
	big := append(pngBytes(t, 2, 2), make([]byte, 2<<20)...)
	for _, path := range []string{"/ui/labels/assign", "/ui/pains/1/idea", "/ui/reactions", "/ui/logout"} {
		r := upload(t, e, e.Owner, path, map[string]string{"kind": "request", "id": "1", "add": "x", "title": "x"}, big, "image/png", true)
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a multipart body -> %d, want 403 (the token is not read from it)", path, r.StatusCode)
		}
	}
}
