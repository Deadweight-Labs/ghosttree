package web

import (
	"strings"
	"testing"
)

func TestRenderDescriptionKnowsBlocksAndInlineCode(t *testing.T) {
	got := string(renderDescription("Erster **Absatz** mit `internal/` Code.\nzweite Zeile\n\n> Vorsicht: `store` zuerst.\n\n- eins\n- zwei\n\n## Teil\nText"))
	for _, want := range []string{
		"<p>Erster <strong>Absatz</strong> mit <code>internal/</code> Code. zweite Zeile</p>",
		"<blockquote><p>Vorsicht: <code>store</code> zuerst.</p></blockquote>",
		"<ul><li>eins</li><li>zwei</li></ul>",
		"<h3>Teil</h3><p>Text</p>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderDescriptionNeverPassesMarkupOrLinks(t *testing.T) {
	got := string(renderDescription("<script>alert(1)</script> [x](javascript:alert(1)) `<img src=x onerror=y>` **<b>**"))
	for _, bad := range []string{"<script", "<img", "<b>", "<a "} {
		if strings.Contains(got, bad) {
			t.Errorf("markup %q reached the page: %q", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "<code>&lt;img src=x onerror=y&gt;</code>") {
		t.Errorf("text is not escaped: %q", got)
	}
}

func TestRenderDescriptionKeepsAnUnpairedBacktick(t *testing.T) {
	if got := string(renderDescription("a ` b")); got != "<p>a ` b</p>" {
		t.Errorf("unpaired backtick: %q", got)
	}
	if got := renderDescription("   \n\n"); got != "" {
		t.Errorf("empty source must render nothing: %q", got)
	}
}

func TestPlainLineIsOneSentenceWithoutMarkup(t *testing.T) {
	if got := plainLine("Hält `Branch`, **Commit** und PR. Zweiter Satz.", 200); got != "Hält Branch, Commit und PR." {
		t.Errorf("got %q", got)
	}
}
