// Package feature beschreibt Features (FEAT-n, REQ-612): fest eingebaute
// Funktionen des Produkts, deren aktueller Stand an einer Stelle steht. Ein
// Feature ist keine Idee (was es tun soll) und keine Ghost-Datei (was eine Datei
// tut): es sagt, was das Produkt für Nutzer tut, über Dateien hinweg.
//
// Der Inhalt steht in Fassungen. Jede Fassung ist ein vollständiger Stand (Name,
// Kurzbeschreibung, Ist-Verhalten, Grenzen, Bedienung, Bereiche) und verweist auf
// Idee, Issues und PR, die sie ausgelöst haben. Fassungen werden nur angefügt.
package feature

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Zustände eines Features. Ein eingestelltes Feature bleibt lesbar (seine
// Fassungen zeigen, wie es war), taucht aber in keinem Kontext und keiner
// Checkliste mehr auf.
const (
	Active  = "active"
	Retired = "retired"
)

// Beziehungen eines Features zu Ideen und Issues.
const (
	// RelOrigin: daraus ist das Feature entstanden.
	RelOrigin = "origin"
	// RelAffects: das ändert das Feature („betrifft FEAT-n“).
	RelAffects = "affects"
)

// Arten der Verweise.
const (
	KindIdea    = "idea"
	KindRequest = "request"
)

// Maße. Eine Fassung wird gelesen, nicht geschrieben wie ein Handbuch; was darüber
// hinausgeht, gehört in ein Dokument.
const (
	MaxName      = 100
	MaxSummary   = 600
	MaxBehavior  = 8000
	MaxLimits    = 4000
	MaxUsage     = 2000
	MaxAreas     = 20
	MaxAreaPath  = 200
	MaxNote      = 200
	MaxRefs      = 30
	MaxPRPaths   = 2000
	MaxReason    = 300
	MaxNameWords = 12
)

// Feature ist ein Feature mit seiner jüngsten Fassung.
type Feature struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	State   string `json:"state"`
	// Reason ist der Grund des Einstellens.
	Reason string `json:"reason,omitempty"`
	// Revision ist die Nummer der jüngsten Fassung.
	Revision int `json:"revision"`
	Content
	// Ideas und Requests sind die Verweise des Features, getrennt nach Beziehung.
	Origin  []Ref `json:"origin,omitempty"`
	Affects []Ref `json:"affects,omitempty"`
	// Versions zählt die Fassungen.
	Versions  int    `json:"versions"`
	Person    string `json:"person,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// GitAnchor und OriginContext sind Stand und Kontext der jüngsten Fassung.
	GitAnchor     *gitanchor.Anchor  `json:"git_anchor,omitempty"`
	OriginContext *originctx.Context `json:"origin_context,omitempty"`
	// AuthorID verlässt den Server nie.
	AuthorID int64 `json:"-"`
	// VersionPerson ist der Schreiber der jüngsten Fassung; nur der Server wertet
	// ihn aus (Auslieferung an Agenten).
	VersionPerson string `json:"-"`
	// VersionPersons sind die Schreiber aller Fassungen. Eine Fassung ist ein
	// voller Stand und kann Text einer früheren unverändert übernehmen; vertraut
	// wird darum nur, wenn jeder von ihnen im Projekt schreiben darf.
	VersionPersons []string `json:"-"`
	// Match ist "semantic", wenn nur die Bedeutung das Feature fand; sonst leer.
	Match string `json:"match,omitempty"`
}

// HumanID ist FEAT-n.
func (f Feature) HumanID() string { return fmt.Sprintf("FEAT-%d", f.ID) }

// Content ist der Inhalt einer Fassung.
type Content struct {
	Name     string `json:"name"`
	Summary  string `json:"summary"`
	Behavior string `json:"behavior"`
	Limits   string `json:"limits"`
	// Bedienung je Weg: CLI, MCP, API, Oberfläche.
	CLI   string   `json:"cli"`
	MCP   string   `json:"mcp"`
	API   string   `json:"api"`
	UI    string   `json:"ui"`
	Areas []string `json:"areas"`
}

// Patch ändert Felder; nil lässt ein Feld stehen. Eine neue Fassung ist die
// jüngste mit dem Patch darüber.
type Patch struct {
	Name     *string   `json:"name,omitempty"`
	Summary  *string   `json:"summary,omitempty"`
	Behavior *string   `json:"behavior,omitempty"`
	Limits   *string   `json:"limits,omitempty"`
	CLI      *string   `json:"cli,omitempty"`
	MCP      *string   `json:"mcp,omitempty"`
	API      *string   `json:"api,omitempty"`
	UI       *string   `json:"ui,omitempty"`
	Areas    *[]string `json:"areas,omitempty"`
}

// Ref verweist auf eine Idee oder einen Issue, mit dem, was ein Betrachter ohne
// Nachfrage braucht.
type Ref struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Title string `json:"title,omitempty"`
	State string `json:"state,omitempty"`
	// Advanced: eine Fassung dieses Features trägt diesen Verweis, die
	// Beschreibung wurde also fortgeschrieben.
	Advanced bool `json:"advanced,omitempty"`
}

// HumanID ist IDEA-n oder REQ-n.
func (r Ref) HumanID() string {
	if r.Kind == KindIdea {
		return fmt.Sprintf("IDEA-%d", r.ID)
	}
	return fmt.Sprintf("REQ-%d", r.ID)
}

// Version ist eine Fassung. Body ohne Inhalt (nur Kopf) gibt es in Listen.
type Version struct {
	Revision int `json:"revision"`
	Content
	Digest  string `json:"digest"`
	Message string `json:"message,omitempty"`
	// Idea, Requests und PR sind, was die Fassung ausgelöst hat.
	Idea      int64   `json:"idea,omitempty"`
	Requests  []int64 `json:"requests,omitempty"`
	PR        int64   `json:"pr,omitempty"`
	Person    string  `json:"person,omitempty"`
	ByAgent   bool    `json:"by_agent,omitempty"`
	CreatedAt string  `json:"created_at"`
	// GitAnchor trägt, wenn bekannt, die PR-Nummer des Branches (anchor.pr).
	GitAnchor     *gitanchor.Anchor  `json:"git_anchor,omitempty"`
	OriginContext *originctx.Context `json:"origin_context,omitempty"`
}

// PRs sind die Pull Requests, die die Fassung tragen: der genannte und der des
// Ankers. Leer, wenn keiner bekannt ist.
func (v Version) PRs() []int64 {
	var out []int64
	if v.PR > 0 {
		out = append(out, v.PR)
	}
	if v.GitAnchor != nil && v.GitAnchor.PR != nil && *v.GitAnchor.PR > 0 && *v.GitAnchor.PR != v.PR {
		out = append(out, *v.GitAnchor.PR)
	}
	return out
}

// Actor ist der Schreiber, vom Server aus dem Token gebildet.
type Actor struct {
	AccountID  int64
	Person     string
	SessionRef string
	// Agent: ein Token (API, MCP, CLI) schreibt, kein Mensch aus der Websitzung.
	Agent     bool
	GitAnchor *gitanchor.Anchor
	Origin    *originctx.Context
}

// Filter begrenzt eine Suche. State "" heißt aktive, "all" alle.
type Filter struct {
	Project string
	State   string
	Query   string
	// Path liefert nur Features, deren Bereiche diesen Pfad enthalten.
	Path  string
	Limit int
}

// ValidState sagt, ob s ein Zustand ist.
func ValidState(s string) bool { return s == Active || s == Retired }

// ParseRef liest FEAT-3 oder 3.
func ParseRef(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimPrefix(s, "FEAT-")
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != s {
		return 0, false
	}
	return n, true
}

func rule(code, msg, res string) error { return requestdomain.NewRuleError(code, msg, res, nil) }

// CleanName prüft den Namen: eine Zeile, kurz, ohne Steuerzeichen.
func CleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", rule("name_required", "a feature needs a name", "name what the product does, in a few words")
	}
	if err := idea.OneLine(s, MaxName); err != nil {
		return "", rule("name_invalid", "name: "+err.Error(), "use a short name in one line")
	}
	return s, nil
}

// CleanText prüft ein mehrzeiliges Feld: gültiges UTF-8, ohne Steuer- und
// Formatzeichen außer Zeilenumbruch und Tab, höchstens max Zeichen.
func CleanText(s, field string, max int) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", rule(field+"_invalid", fmt.Sprintf("%s is longer than %d characters or not valid text", field, max), "shorten it; what a feature does now, not how it came to be")
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if err := idea.OneLine(string(r), 1); err != nil {
			return "", rule(field+"_invalid", field+" has control or formatting characters", "remove them")
		}
	}
	return s, nil
}

// CleanArea prüft einen Bereich: ein Pfad-Präfix im Repo, ohne führenden
// Schrägstrich, ohne "..", nicht .git. Ein Verzeichnis darf einen Schrägstrich am
// Ende tragen, er fällt weg. Leer heißt ungültig: ein Feature, das „überall“
// ist, hat keinen Bereich.
func CleanArea(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, `\`, "/"))
	if err := idea.OneLine(s, MaxAreaPath); err != nil || s == "" {
		return "", rule("area_invalid", "an area is a path inside the repository, one line", "use a prefix like internal/store or cmd/ctx/feature.go")
	}
	c := path.Clean(strings.TrimSuffix(s, "/"))
	if c == "." || c == ".." || strings.HasPrefix(c, "/") || strings.HasPrefix(c, "../") || strings.Contains(c, "/../") || strings.HasSuffix(c, "/..") ||
		c == ".git" || strings.HasPrefix(c, ".git/") || strings.ContainsAny(c, "*?[]\x00") {
		return "", rule("area_invalid", fmt.Sprintf("%q is not an area", s), "use a repository-relative path prefix without .., globs or .git")
	}
	return c, nil
}

// CleanAreas prüft die Bereiche: höchstens MaxAreas, keine Doppelten, sortiert.
func CleanAreas(in []string) ([]string, error) {
	if len(in) > MaxAreas {
		return nil, rule("areas_too_many", fmt.Sprintf("at most %d areas", MaxAreas), "name the directories, not every file")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		if strings.TrimSpace(a) == "" {
			continue
		}
		c, err := CleanArea(a)
		if err != nil {
			return nil, err
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CleanPath prüft einen Pfad aus einem PR: wie ein Bereich, aber Dateien sind
// erlaubt und ein ungültiger Pfad wird verworfen statt gemeldet (ok false).
func CleanPath(s string) (string, bool) {
	c, err := CleanArea(s)
	return c, err == nil
}

// Apply legt den Patch auf den Inhalt und prüft jedes Feld.
func (c Content) Apply(p Patch) (Content, error) {
	if p.Name != nil {
		n, err := CleanName(*p.Name)
		if err != nil {
			return Content{}, err
		}
		c.Name = n
	}
	for _, f := range []struct {
		dst   *string
		v     *string
		field string
		max   int
	}{
		{&c.Summary, p.Summary, "summary", MaxSummary}, {&c.Behavior, p.Behavior, "behavior", MaxBehavior}, {&c.Limits, p.Limits, "limits", MaxLimits},
		{&c.CLI, p.CLI, "cli", MaxUsage}, {&c.MCP, p.MCP, "mcp", MaxUsage}, {&c.API, p.API, "api", MaxUsage}, {&c.UI, p.UI, "ui", MaxUsage},
	} {
		if f.v == nil {
			continue
		}
		clean, err := CleanText(*f.v, f.field, f.max)
		if err != nil {
			return Content{}, err
		}
		*f.dst = clean
	}
	if p.Areas != nil {
		areas, err := CleanAreas(*p.Areas)
		if err != nil {
			return Content{}, err
		}
		c.Areas = areas
	}
	if c.Areas == nil {
		c.Areas = []string{}
	}
	return c, nil
}

// Covers sagt, ob ein Pfad in einem der Bereiche liegt: gleich oder darunter.
func (c Content) Covers(p string) bool {
	for _, a := range c.Areas {
		if InArea(p, a) {
			return true
		}
	}
	return false
}

// InArea sagt, ob p gleich area ist oder darunter liegt.
func InArea(p, area string) bool {
	p = strings.TrimPrefix(path.Clean(p), "./")
	return p == area || strings.HasPrefix(p, area+"/")
}

// CleanRefs prüft Verweise (Nummern): positiv, ohne Doppelte, höchstens MaxRefs.
func CleanRefs(in []int64, what string) ([]int64, error) {
	if len(in) > MaxRefs {
		return nil, rule("refs_too_many", fmt.Sprintf("at most %d %s", MaxRefs, what), "link the ones that matter")
	}
	seen := map[int64]bool{}
	var out []int64
	for _, n := range in {
		if n <= 0 {
			return nil, rule("ref_invalid", fmt.Sprintf("%d is not a number of %s", n, what), "use the number without the prefix")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// CleanReason prüft den Grund des Einstellens: eine Zeile.
func CleanReason(s string) (string, error) {
	return idea.CleanOneLine(s, "reason", "the reason", MaxReason, true)
}

// DedupInts streicht doppelte Nummern, die Reihenfolge bleibt.
func DedupInts(in []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
