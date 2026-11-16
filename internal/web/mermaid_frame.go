package web

import (
	"io"
	"net/http"
	"strings"
)

// The page of the frame that draws Mermaid diagrams (REQ-672). The script of
// the library and the stylesheet it writes into every diagram cannot run under the
// CSP of the interface pages (style-src 'self': measured, the layout of a
// flowchart comes out wrong and every diagram logs dozens of violations), and that
// CSP is not loosened for it. The diagrams are drawn here instead: a page of its
// own, loaded by static/ui-mermaid.js into an invisible iframe with sandbox="allow-scripts",
// that takes the source of a diagram by postMessage and gives the SVG back as
// text. The interface shows that SVG only as the address of an <img>
// (data:, which img-src allows), so it never becomes part of a page.
//
// The CSP of this one page is the only place where inline styles are allowed, and
// it is as narrow as can be: no connection, no form, no frame, images only as
// data:, scripts only from this origin, and a sandbox without allow-same-origin,
// so a flaw in the library finds no cookie, no storage and no page to reach.

const mermaidFrameCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"

func init() { webRoutes["GET /ui/mermaid-frame"] = webPublic }

// mermaidFrame is GET /ui/mermaid-frame. It holds no data of any reader, so it
// needs no session; a page of the library and a script, both with the hash in their address.
func (a *app) mermaidFrame(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", mermaidFrameCSP)
	h.Set("Cache-Control", "no-cache")
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Mermaid</title>`)
	b.WriteString(`<script src="` + assetURL("vendor/mermaid.min.js") + `" defer></script>`)
	b.WriteString(`<script src="` + assetURL("mermaid-frame.js") + `" defer></script>`)
	b.WriteString(`</head><body></body></html>`)
	_, _ = io.WriteString(w, b.String())
}
