package client

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// SetAnchorSource schaltet den Git-Anker für schreibende Aufrufe ein (REQ-597).
// Die Funktion wird bei jedem verankerten Schreibzugriff neu gefragt, denn HEAD
// wandert während einer Sitzung. Ohne Quelle (Hooks, Verwaltungsbefehle) geht
// nie ein Anker mit: ein Prozess, der nicht im Arbeitsverzeichnis des Agenten
// läuft, hätte keinen Stand zu melden.
func (c *Client) SetAnchorSource(f func() gitanchor.Anchor) { c.anchor = f }

// anchoredWrites sind die schreibenden Routen, deren Server-Handler einen Anker
// speichern: Wissen, Ghost-Beschreibungen, Request-Fortschritt, Pains und
// Raum-Nachrichten (auch Antworten in Diskussionen). Die Liste steht hier und
// nicht als "jedes POST", damit ein Schreibzugriff ohne Handler für den Anker
// auch keine git-Aufrufe auslöst.
var anchoredWrites = regexp.MustCompile(`^(` +
	`POST /api/(knowledge|ghosts|requests|pains|coord/messages)` +
	`|POST /api/requests/\d+/complete` +
	`|PATCH /api/criteria/\d+` +
	`|POST /api/pains/\d+/(state|links)` +
	`|PATCH /api/pains/\d+` +
	`)$`)

// anchorHeader liefert den Header-Wert für diesen Aufruf, leer wenn keiner
// mitgehen soll oder kein Anker zu ermitteln ist.
func (c *Client) anchorHeader(method, path string) string {
	if c.anchor == nil || !anchoredWrites.MatchString(method+" "+path) {
		return ""
	}
	return gitanchor.Encode(c.anchor())
}

func (c *Client) setAnchor(req *http.Request, method, path string) {
	if v := c.anchorHeader(method, path); v != "" {
		req.Header.Set(gitanchor.Header, v)
	}
}

// AnchorFilter sind die Lesefilter nach Git-Anker. Leer filtert nichts.
type AnchorFilter struct {
	// Commit ist ein Präfix, mindestens gitanchor.MinPrefix Hexzeichen.
	Commit string
	// PR filtert auf Einträge mit dieser PR-Nummer am Anker. Solange die
	// PR-Zuordnung fehlt (REQ-596), liefert das nichts.
	PR int64
}

func (f AnchorFilter) apply(q url.Values) {
	if f.Commit != "" {
		q.Set("commit", f.Commit)
	}
	if f.PR > 0 {
		q.Set("pr", strconv.FormatInt(f.PR, 10))
	}
}

// SearchAnchored ist Search mit Lesefilter nach Git-Anker. union wie bei
// SearchUnion.
func (c *Client) SearchAnchored(q, kind string, ax scope.Axes, limit int, union bool, f AnchorFilter) (SearchResult, error) {
	return c.searchWith(q, kind, ax, "", limit, union, f)
}

// SearchGhostsAnchored ist SearchGhosts mit Lesefilter nach Git-Anker.
func (c *Client) SearchGhostsAnchored(q, project string, limit int, f AnchorFilter) ([]store.GhostFile, error) {
	v := url.Values{}
	v.Set("q", q)
	v.Set("project", project)
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	f.apply(v)
	var out []store.GhostFile
	err := c.do("GET", "/api/ghosts/search", v, nil, &out)
	return out, err
}
