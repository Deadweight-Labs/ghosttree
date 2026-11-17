package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// REQ-671 AC-3: agents attach a screenshot to a pain and to an issue with ctx.
func TestCtxAttachesImagesToPainsAndRequests(t *testing.T) {
	st := moveCLIFixture(t)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	other := func() []byte {
		img := image.NewRGBA(image.Rect(0, 0, 5, 5))
		img.Set(1, 1, color.RGBA{R: 255, A: 255})
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}()
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "bug", Title: "Button bricht um", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	if code, out := painTaskRun(t, "pain", "add", "Der Button bricht um", "--project", cliHere); code != 0 || !strings.Contains(out, "PAIN-1") {
		t.Fatalf("add: %d %s", code, out)
	}
	row := func(subject string) (owner, alt, mime string, n int) {
		st.DB().QueryRow(`SELECT COUNT(*) FROM attachments WHERE subject_kind=?`, subject).Scan(&n)
		st.DB().QueryRow(`SELECT owner_kind,alt,mime FROM attachments WHERE subject_kind=? ORDER BY id LIMIT 1`, subject).Scan(&owner, &alt, &mime)
		return
	}
	shot := write("shot.png", testCLIPNG(t))

	code, out := painTaskRun(t, "pain", "attach", "1", shot, "--alt", "Der Umbruch")
	if code != 0 || !strings.Contains(out, "ATT-1 image/png 4x4 /api/attachments/1") {
		t.Fatalf("pain attach: %d %s", code, out)
	}
	if owner, alt, mime, n := row("pain"); owner != "pain" || alt != "Der Umbruch" || mime != "image/png" || n != 1 {
		t.Errorf("stored for the pain: %s %q %s %d", owner, alt, mime, n)
	}
	// PAIN-1 reads like 1; a second, other picture is a second attachment.
	if code, out := painTaskRun(t, "pain", "attach", "PAIN-1", write("two.png", other)); code != 0 || !strings.Contains(out, "ATT-2") {
		t.Fatalf("pain attach by key: %d %s", code, out)
	}
	if _, _, _, n := row("pain"); n != 2 {
		t.Errorf("%d pictures on the pain, want 2", n)
	}

	code, out = painTaskRun(t, "request", "attach", "1", shot, "--alt", "Beleg")
	if code != 0 || !strings.Contains(out, "ATT-3 image/png") {
		t.Fatalf("request attach: %d %s", code, out)
	}
	if owner, alt, _, n := row("request"); owner != "request" || alt != "Beleg" || n != 1 {
		t.Errorf("stored for the request: %s %q %d", owner, alt, n)
	}
	if code, out := painTaskRun(t, "request", "attach", "REQ-1", write("three.png", other), "--json"); code != 0 || !strings.Contains(out, `"owner":"request"`) {
		t.Fatalf("request attach as json: %d %s", code, out)
	}

	// What is refused leaves nothing behind: an SVG, a missing file, a pain that is not there, wrong arguments.
	before := 0
	st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&before)
	for name, args := range map[string][]string{
		"svg":          {"pain", "attach", "1", write("x.svg", []byte(`<svg onload="x"/>`))},
		"no file":      {"pain", "attach", "1", filepath.Join(dir, "missing.png")},
		"unknown pain": {"pain", "attach", "99", shot},
		"unknown req":  {"request", "attach", "99", shot},
	} {
		if code, out := painTaskRun(t, args...); code == 0 {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
	for name, args := range map[string][]string{
		"pain without a file":    {"pain", "attach", "1"},
		"pain without a number":  {"pain", "attach", shot},
		"request without a file": {"request", "attach", "1"},
		"request a word":         {"request", "attach", "abc", shot},
	} {
		if code, out := painTaskRun(t, args...); code != 2 || !strings.Contains(out, "usage") {
			t.Errorf("%s -> %d %q, want the usage and 2", name, code, out)
		}
	}
	var after int
	st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&after)
	if after != before {
		t.Errorf("refused calls stored %d pictures", after-before)
	}
}
