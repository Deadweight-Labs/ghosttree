package client

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
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
// speichern: Wissen, Ghost-Beschreibungen, Request-Fortschritt, Pains, Ideen,
// Raum-Nachrichten (auch Antworten in Diskussionen), Thread-Aktionen (Anlegen,
// Zustand, Ergebnis, Verknüpfung, Zusammenfassung), die Korrektur eines Requests
// und Features (Anlegen, Fassungen; Zustand, Verweise und Bestätigungen
// speichern keinen Anker und schicken ihn darum nicht). Die Liste steht hier und
// nicht als "jedes POST", damit ein Schreibzugriff ohne Handler für den Anker
// auch keine git-Aufrufe auslöst.
var anchoredWrites = regexp.MustCompile(`^(` +
	`POST /api/(knowledge|ghosts|requests|pains|ideas|coord/messages)` +
	`|POST /api/requests/\d+/(complete|criteria|drop|relations|work)` +
	`|PATCH /api/criteria/\d+` +
	`|POST /api/criteria/\d+/reopen` +
	`|POST /api/pains/\d+/(state|links)` +
	`|PATCH /api/pains/\d+` +
	`|POST /api/threads` +
	`|POST /api/threads/\d+/(state|outcomes|links|summary)` +
	`|PATCH /api/requests/\d+` +
	`|POST /api/runbooks` +
	`|POST /api/runbooks/\d+/runs` +
	`|POST /api/runbook-proposals/\d+/decide` +
	`|POST /api/features|POST /api/features/\d+/versions` +
	`)$`)

// anchorHeader liefert den Header-Wert für diesen Aufruf, leer wenn keiner
// mitgehen soll oder kein Anker zu ermitteln ist.
func (c *Client) anchorHeader(method, path string) string {
	if c.anchor == nil || !anchoredWrites.MatchString(method+" "+path) {
		return ""
	}
	return gitanchor.Encode(c.anchor())
}

// SetOriginSession schaltet den Entstehungskontext (REQ-600) für schreibende
// Aufrufe ein: die Session geht im Header originctx.SessionHeader auf denselben
// Routen mit wie der Git-Anker. Mehr sendet der Client nicht; Pfade und Bereiche
// ermittelt der Server aus path_activity. Leer lässt den Header weg.
func (c *Client) SetOriginSession(ref string) { c.originSession = ref }

func (c *Client) setAnchor(req *http.Request, method, path string) {
	if v := c.anchorHeader(method, path); v != "" {
		req.Header.Set(gitanchor.Header, v)
	}
	if c.originSession != "" && originctx.ValidSession(c.originSession) && anchoredWrites.MatchString(method+" "+path) {
		req.Header.Set(originctx.SessionHeader, c.originSession)
	}
}

// OriginFilter ist die Angabe für die Lesefilter nach Entstehungskontext:
// Zeitraum (Datum, RFC3339 oder relativ wie 7d, 2w) und Bereiche (Pfad-Präfixe,
// ODER-verknüpft). Der Server löst sie auf; leer filtert nichts.
type OriginFilter = originctx.Spec

func applyOrigin(q url.Values, f OriginFilter) {
	if f.Since != "" {
		q.Set("since", f.Since)
	}
	if f.Until != "" {
		q.Set("until", f.Until)
	}
	for _, a := range f.Areas {
		q.Add("area", a)
	}
}

// AnchorFilter sind die Lesefilter nach Git-Anker. Leer filtert nichts.
type AnchorFilter struct {
	// Commit ist ein Präfix, mindestens gitanchor.MinPrefix Hexzeichen.
	Commit string
	// PR filtert auf Einträge mit dieser PR-Nummer am Anker. Solange die
	// PR-Zuordnung fehlt (REQ-596), liefert das nichts.
	PR int64
	// Origin ist der Filter nach Entstehungskontext (REQ-600): Zeitraum und
	// Bereich. Er steht hier, damit es für die Suche eine einzige Filter-
	// Struktur gibt, die eine Qualifier-Sprache später direkt füllen kann.
	Origin OriginFilter
	// Agent ist der registrierte Agent, in dessen Räumen die Suche Nachrichten
	// lesen darf (agent_external_id). Leer: die Räume des Kontos.
	Agent string
}

func (f AnchorFilter) apply(q url.Values) {
	if f.Agent != "" {
		q.Set("agent_external_id", f.Agent)
	}
	applyOrigin(q, f.Origin)
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
