package web

import (
	"strings"
	"testing"
)

// AC-701.1: the history of a thread is the page, not a small box in it; the newest message
// stands above the composer when the thread opens; the guiding question is two lines until opened.
func TestCoordThreadHistoryIsThePageNotABox(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/ui-rooms.css"))
	for _, want := range []string{
		// One column that the page scrolls, no height taken from the window.
		".coord-shell:has(.coord-thread-detail) { display: block; height: auto;",
		".coord-thread-messages { overflow: visible;",
		// The bar and the composer stay while the page scrolls.
		".rtbar { position: sticky; top: 0;",
		".coord-thread-composer { position: sticky; bottom: 0;",
		// The guiding question: two lines, then opened by a click.
		"-webkit-line-clamp: 2",
		".rlead[open] p { display: block; -webkit-line-clamp: unset;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("ui-rooms.css lacks %q", want)
		}
	}
	// The height of a thread is no longer nailed to the window, on a wide page or a narrow one.
	if strings.Contains(css, ".coord-shell:has(.coord-thread-detail) .coord-conversation { height: calc(") {
		t.Error("the thread conversation is given the height of the window again")
	}

	js := string(mustReadEmbedded(t, "static/ui-coord.js"))
	for _, want := range []string{
		// A chat that is not a scroll box of its own is the page: the end, the near-end test and the anchor.
		"const flowScrolls = ",
		"const scrollerOf = ",
		"const scrollEnd = ",
		// What grows after the first paint does not take the end away from the reader.
		"const stickToEnd = ",
		"new ResizeObserver(",
		"state.pageFlow",
		// The send button keeps its mark when the status changes its word.
		`button.querySelector(".comp-send-t")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("ui-coord.js lacks %q", want)
		}
	}
}

// The thread page opens its history as the page does: the messages list is no scroll area, the
// guiding question is a closed disclosure and the end of the page is the composer.
func TestCoordThreadPageOrderIsBarQuestionMessagesComposer(t *testing.T) {
	_, _, _, page := threadPageFixture(t)
	html := page()
	order := []string{`class="rtbar"`, `<details class="rlead"`, `class="coord-thread-messages coord-messages"`, `class="coord-thread-composer"`}
	last := -1
	for _, marker := range order {
		i := strings.Index(html, marker)
		if i < 0 || i < last {
			t.Fatalf("%s is missing or out of order (at %d, after %d)", marker, i, last)
		}
		last = i
	}
}
