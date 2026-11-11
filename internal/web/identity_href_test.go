package web

import (
	"strings"
	"testing"
)

// Der href eines Verweises kommt aus Daten; nur relative Pfade, http und https
// werden Link, alles andere wird "#".
func TestRefLinksOnlyKeepRelativeHttpAndHttpsTargets(t *testing.T) {
	ok := []string{"/ui/x/shell/issues/1", "#c3", "?q=a", "issues/1", "http://example.org/a", "HTTPS://example.org/a"}
	bad := []string{"javascript:alert(1)", "JaVaScRiPt:alert(1)", " javascript:alert(1)", "java\tscript:alert(1)", "data:text/html,x",
		"vbscript:x", "//evil.example/x", "/\\evil.example", "\\\\evil.example", "ftp://example.org/x"}
	for _, h := range ok {
		for name, got := range map[string]string{"ref": string(refHTML(langDE, "request", 1, h)), "pill": string(refPillHTML(langDE, "request", 1, h))} {
			if !strings.Contains(got, `href="`+strings.ReplaceAll(h, "&", "&amp;")+`"`) {
				t.Errorf("%s drops the good target %q: %s", name, h, got)
			}
		}
	}
	for _, h := range bad {
		for name, got := range map[string]string{"ref": string(refHTML(langDE, "request", 1, h)), "pill": string(refPillHTML(langDE, "request", 1, h))} {
			if !strings.Contains(got, `href="#"`) {
				t.Errorf("%s keeps the bad target %q: %s", name, h, got)
			}
		}
	}
}
