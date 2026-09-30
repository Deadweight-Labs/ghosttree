package web

import (
	"strings"
	"testing"
)

func TestCoordLinksUseOneToken(t *testing.T) {
	requireCSS(t, ".coord-paging a", "color: var(--coord-link);")
	requireCSS(t, ".coord-message-action-primary", "color: var(--coord-link);")
	css := string(mustReadEmbedded(t, "static/app.css"))
	if strings.Contains(css, "#0e4da8") {
		t.Error("stray blue #0e4da8 must use a token")
	}
}

func TestCoordMentionMarkerSitsOnItsOwnRow(t *testing.T) {
	requireCSS(t, ".coord-message article > header", "flex-wrap: wrap;")
	requireCSS(t, ".coord-mention-you", "flex-basis: 100%;")
}

func TestCoordComposerOptionsPanelStaysInsideTheViewport(t *testing.T) {
	requireCSS(t, ".coord-compose-options[open] .coord-option-panel", "max-height: min(11rem, 40vh);", "overflow-y: auto;")
}
