// Package originctx beschreibt den Entstehungskontext eines Eintrags (REQ-600):
// wann er geschrieben wurde, von welcher Session und in welchen Bereichen des
// Repos diese Session bis dahin gearbeitet hat. Das Paket kennt nur Form,
// Gewichtung, Verdichtung und Filter. Gespeichert wird im store (Tabellen
// origin_contexts, origin_paths, origin_areas), ermittelt vom Server aus
// path_activity beim Schreiben.
//
// Die Regeln stehen hier und nirgends sonst, damit sie sich nachlesen und
// ändern lassen:
//
//   - Gewicht je Pfad: gelesen 1, geschrieben 3, ausdrücklich genannt 10
//     (z. B. der Pfad bei context_describe_file). Zählt ein Pfad mehrfach, gilt
//     die stärkste Art, und jede Wiederholung gibt +1, höchstens +3.
//   - Bereich: das Verzeichnis der Datei, auf höchstens zwei Ebenen gekürzt
//     (internal/store/x.go ergibt internal/store, web/src/a/b.tsx ergibt
//     web/src, README.md ergibt "."). Das Gewicht eines Bereichs ist die Summe
//     seiner Pfade.
//   - Gespeichert werden die fünf schwersten Bereiche und die dreißig
//     schwersten Pfade. Gleiches Gewicht entscheidet die alphabetische Folge.
//   - Nur Pfade im Repo zählen: ein absoluter Pfad gilt, wenn er im Checkout der
//     Session liegt, und wird dann relativ gespeichert. Alles andere (Pfade
//     außerhalb, ".." , .git) bleibt weg.
package originctx

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SessionHeader trägt die Kennung der schreibenden Session vom Client zum
// Server, auf denselben Routen wie der Git-Anker (client.anchoredWrites). Der
// Server prüft, dass die Session dem Konto des Tokens gehört, bevor er daraus
// einen Kontext baut; der Body ist nie die Quelle.
const SessionHeader = "X-Ghosttree-Session"

// ValidSession sagt, ob ein Header-Wert wie eine Session-Kennung aussieht.
func ValidSession(v string) bool {
	if v == "" || len(v) > 200 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

// ViaWeb kennzeichnet einen Eintrag der Weboberfläche (Context.Via).
const ViaWeb = "web"

// Arten eines berührten Pfads, von schwach nach stark.
const (
	KindRead  = "read"
	KindWrite = "write"
	KindNamed = "named"
)

// Gewichte und Grenzen der Regel (siehe Paketkommentar).
const (
	WeightRead     = 1
	WeightWrite    = 3
	WeightNamed    = 10
	MaxRepeatBonus = 3
	MaxAreas       = 5
	MaxPaths       = 30
	AreaDepth      = 2
)

// Path ist ein berührter Pfad samt Art und Gewicht.
type Path struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Weight int    `json:"weight"`
}

// Area ist ein verdichteter Bereich (Verzeichnis-Präfix) samt Gewicht.
type Area struct {
	Path   string `json:"path"`
	Weight int    `json:"weight"`
}

// Context ist der eingefrorene Entstehungskontext. Ein Eintrag ohne ermittelbare
// Session und ohne genannten Pfad hat keinen (nil), er wird nie geraten.
type Context struct {
	// SessionRef ist die Kennung der Session, die geschrieben hat.
	SessionRef string `json:"session_ref,omitempty"`
	// CapturedAt setzt der Server beim Speichern (bei einem Nachtrag: der
	// Zeitpunkt des Eintrags).
	CapturedAt string `json:"captured_at,omitempty"`
	// Via nennt den Weg des Eintrags, wenn er nicht über eine Agenten-Session lief:
	// ViaWeb für die Bearbeitung in der Weboberfläche. Leer sonst.
	Via string `json:"via,omitempty"`
	// Backfilled: aus session_ref und path_activity nachgetragen, nicht beim
	// Schreiben eingefroren.
	Backfilled bool   `json:"backfilled,omitempty"`
	Paths      []Path `json:"paths,omitempty"`
	Areas      []Area `json:"areas,omitempty"`
}

// Main ist der Hauptbereich: der schwerste. Leer ohne Bereiche.
func (c *Context) Main() string {
	if c == nil || len(c.Areas) == 0 {
		return ""
	}
	return c.Areas[0].Path
}

// Touch ist eine beobachtete Berührung (eine Zeile aus path_activity).
type Touch struct {
	Path     string
	Writes   bool
	Checkout string
}

// RepoPath bringt einen Pfad in die Form relativ zum Repo-Wurzelverzeichnis.
// Leer heißt: gehört nicht dazu.
func RepoPath(p, checkout string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	dir := strings.HasSuffix(p, "/")
	clean := path.Clean(p)
	if path.IsAbs(clean) {
		co := path.Clean(checkout)
		if checkout == "" || !path.IsAbs(co) || co == "/" {
			return ""
		}
		rest, ok := strings.CutPrefix(clean, co+"/")
		if !ok {
			return ""
		}
		clean = rest
	}
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") ||
		clean == ".git" || strings.HasPrefix(clean, ".git/") || strings.ContainsAny(clean, "*?[") {
		return ""
	}
	if dir {
		clean += "/"
	}
	return clean
}

// areaOf ist der Bereich eines Pfads. Ein Pfad mit Schrägstrich am Ende ist
// ein Verzeichnis und sein eigener Bereich.
func areaOf(p string) string {
	d := p
	if strings.HasSuffix(p, "/") {
		d = strings.TrimSuffix(p, "/")
	} else {
		d = path.Dir(p)
	}
	if d == "." || d == "" {
		return "."
	}
	parts := strings.Split(d, "/")
	if len(parts) > AreaDepth {
		parts = parts[:AreaDepth]
	}
	return strings.Join(parts, "/")
}

type acc struct {
	kind  string
	count int
}

var kindRank = map[string]int{KindRead: 1, KindWrite: 2, KindNamed: 3}

func baseWeight(kind string) int {
	switch kind {
	case KindNamed:
		return WeightNamed
	case KindWrite:
		return WeightWrite
	}
	return WeightRead
}

// Build verdichtet Berührungen und genannte Pfade zu einem Kontext. nil, wenn
// weder eine Session noch ein Pfad vorliegt: dann gibt es nichts festzuhalten.
// Eine bekannte Session ohne Pfade ergibt einen Kontext ohne Bereiche.
func Build(session, capturedAt string, touches []Touch, named []string) *Context {
	byPath := map[string]*acc{}
	add := func(p, kind string) {
		if p == "" {
			return
		}
		a := byPath[p]
		if a == nil {
			a = &acc{kind: kind}
			byPath[p] = a
		}
		a.count++
		if kindRank[kind] > kindRank[a.kind] {
			a.kind = kind
		}
	}
	for _, t := range touches {
		kind := KindRead
		if t.Writes {
			kind = KindWrite
		}
		add(RepoPath(t.Path, t.Checkout), kind)
	}
	for _, n := range named {
		add(RepoPath(n, ""), KindNamed)
	}
	if session == "" && len(byPath) == 0 {
		return nil
	}
	c := &Context{SessionRef: session, CapturedAt: capturedAt}
	areas := map[string]int{}
	for p, a := range byPath {
		w := baseWeight(a.kind) + min(a.count-1, MaxRepeatBonus)
		c.Paths = append(c.Paths, Path{Path: p, Kind: a.kind, Weight: w})
		areas[areaOf(p)] += w
	}
	sort.Slice(c.Paths, func(i, j int) bool {
		if c.Paths[i].Weight != c.Paths[j].Weight {
			return c.Paths[i].Weight > c.Paths[j].Weight
		}
		return c.Paths[i].Path < c.Paths[j].Path
	})
	if len(c.Paths) > MaxPaths {
		c.Paths = c.Paths[:MaxPaths]
	}
	for a, w := range areas {
		c.Areas = append(c.Areas, Area{Path: a, Weight: w})
	}
	sort.Slice(c.Areas, func(i, j int) bool {
		if c.Areas[i].Weight != c.Areas[j].Weight {
			return c.Areas[i].Weight > c.Areas[j].Weight
		}
		return c.Areas[i].Path < c.Areas[j].Path
	})
	if len(c.Areas) > MaxAreas {
		c.Areas = c.Areas[:MaxAreas]
	}
	return c
}

// Label ist die Kurzform für Trefferlisten: "on:2026-11-07 in:internal/store".
// created ist der Ersatz für das Datum, wenn der Eintrag keinen Kontext hat.
func Label(c *Context, created string) string {
	date := created
	if c != nil && c.CapturedAt != "" {
		date = c.CapturedAt
	}
	out := ""
	if len(date) >= 10 {
		out = "|on:" + date[:10]
	}
	if m := c.Main(); m != "" {
		out += "|in:" + m
	}
	return out
}

// Filter sind die Lesefilter nach Entstehungskontext, aufgelöst: Zeiten als
// RFC3339 in UTC, Bereiche normalisiert. Leer filtert nichts. Ein Parser für
// eine Suchsprache füllt genau diese Struktur (über Spec.Resolve).
type Filter struct {
	// Since und Until begrenzen den Zeitpunkt des Eintrags, beide einschließlich.
	Since, Until string
	// Areas sind Pfad-Präfixe, ODER-verknüpft: ein Eintrag passt, wenn einer
	// seiner Bereiche zu einem davon passt (gleich, darunter oder darüber).
	Areas []string
}

// Active sagt, ob überhaupt gefiltert wird.
func (f Filter) Active() bool { return f.Since != "" || f.Until != "" || len(f.Areas) > 0 }

// Spec ist ein Filter, wie ihn ein Mensch oder Agent angibt: Zeiten als
// Datum, RFC3339 oder relativ, Bereiche als Pfade.
type Spec struct {
	Since, Until string
	Areas        []string
}

// Active sagt, ob die Angabe etwas filtert.
func (s Spec) Active() bool { return s.Since != "" || s.Until != "" || len(s.Areas) > 0 }

var relativeRE = regexp.MustCompile(`^(\d+)([hdw])$`)

// ParseTime liest eine Zeitangabe: RFC3339, ein Datum (JJJJ-MM-TT) oder relativ
// (4h, 7d, 2w, gemessen von now rückwärts). endOfDay gilt nur für ein Datum:
// false ergibt 00:00:00 des Tages, true 23:59:59.
func ParseTime(s string, now time.Time, endOfDay bool) (string, error) {
	s = strings.TrimSpace(s)
	if m := relativeRE.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n > 100000 {
			return "", fmt.Errorf("invalid relative time %q", s)
		}
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		return now.UTC().Add(-time.Duration(n) * unit).Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t.Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("invalid time %q: use a date (2026-11-01), an RFC3339 time or a relative one like 7d, 2w, 12h", s)
}

// NormalizeArea bringt einen Bereichsfilter in die gespeicherte Form: relativ
// zur Repo-Wurzel, ohne "./" und ohne Schrägstrich am Ende.
func NormalizeArea(a string) (string, error) {
	a = strings.TrimSpace(a)
	if a == "" {
		return "", fmt.Errorf("empty area")
	}
	if strings.HasPrefix(a, "/") {
		return "", fmt.Errorf("area %q must be relative to the repository root", a)
	}
	c := path.Clean(a)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("area %q must stay inside the repository", a)
	}
	return c, nil
}

// Resolve macht aus der Angabe einen Filter. Ein ungültiger Wert ist ein Fehler
// und wird nicht ignoriert: still alles zu liefern wäre die falsche Antwort auf
// "was entstand in diesem Zeitraum".
func (s Spec) Resolve(now time.Time) (Filter, error) {
	var f Filter
	var err error
	if s.Since != "" {
		if f.Since, err = ParseTime(s.Since, now, false); err != nil {
			return Filter{}, fmt.Errorf("since: %w", err)
		}
	}
	if s.Until != "" {
		if f.Until, err = ParseTime(s.Until, now, true); err != nil {
			return Filter{}, fmt.Errorf("until: %w", err)
		}
	}
	if f.Since != "" && f.Until != "" && f.Since > f.Until {
		return Filter{}, fmt.Errorf("since is after until")
	}
	for _, a := range s.Areas {
		if strings.TrimSpace(a) == "" {
			continue
		}
		n, err := NormalizeArea(a)
		if err != nil {
			return Filter{}, err
		}
		f.Areas = append(f.Areas, n)
	}
	return f, nil
}
