package idea

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Ideen-Werkstatt (REQ-611). Eine Idee bleibt eine Zeile, wenn sie eine bleiben
// soll. Wer sie ausarbeiten will, hängt Rohfassung, Kurzfassung und Konzept an;
// alles ist versioniert, nichts davon ist Pflicht.

// Phasen der Ausarbeitung. Sie laufen neben dem Eingangs-Zustand (open,
// accepted, dismissed): die Phase sagt, wie weit die Ausarbeitung ist, der
// Zustand, ob schon ein Issue daraus wurde oder die Idee verworfen ist. Eine
// verworfene Idee zeigt Phase "dismissed", gleich was die Spalte sagt.
const (
	PhaseRaw        = "raw"        // Rohidee
	PhaseConcept    = "concept"    // Konzept liegt vor
	PhaseDiscussion = "discussion" // in Diskussion
	PhaseApproved   = "approved"   // freigegeben: „so wollen wir es“
	PhaseBuilding   = "building"   // in Umsetzung: Issues laufen
	PhaseDone       = "done"       // umgesetzt
	PhaseDismissed  = "dismissed"  // verworfen
)

// Phases sind alle Phasen in der Reihenfolge des Ablaufs.
var Phases = []string{PhaseRaw, PhaseConcept, PhaseDiscussion, PhaseApproved, PhaseBuilding, PhaseDone, PhaseDismissed}

// ValidPhase sagt, ob s eine Phase ist.
func ValidPhase(s string) bool {
	for _, v := range Phases {
		if v == s {
			return true
		}
	}
	return false
}

// EffectivePhase ist die Phase, die jemand sieht: verworfen schlägt alles.
func EffectivePhase(state, phase string) string {
	if state == Dismissed {
		return PhaseDismissed
	}
	if !ValidPhase(phase) || phase == PhaseDismissed {
		return PhaseRaw
	}
	return phase
}

// ValidatePhase prüft einen Phasenwechsel. hasConcept sagt, ob ein Konzept
// vorliegt, hasRequests, ob schon Issues aus der Idee entstanden sind,
// openRequests, ob davon noch welche offen sind. Verwerfen und Wiederöffnen
// laufen über den Zustand (SetIdeaState), nicht hierüber.
//
// Vor- und Rückwärts: raw, concept und discussion wechseln frei untereinander;
// freigegeben wird nur mit Konzept; ab Umsetzung führt kein Weg zurück, denn aus
// der Idee entstand Arbeit; umgesetzt gilt erst, wenn alle Issues zu sind.
func ValidatePhase(from, to string, hasConcept, hasRequests, openRequests bool) error {
	if !ValidPhase(to) || to == PhaseDismissed {
		return requestdomain.NewRuleError("invalid_phase", fmt.Sprintf("unknown phase %q", to), "use raw, concept, discussion, approved, building or done; dismiss with the state", nil)
	}
	if from == to {
		return requestdomain.NewRuleError("invalid_phase", fmt.Sprintf("the idea is already in phase %s", from), "nothing to do", nil)
	}
	switch from {
	case PhaseBuilding, PhaseDone:
		if from == PhaseBuilding && to == PhaseDone {
			if !hasRequests {
				return requestdomain.NewRuleError("requests_required", "an idea is done when its issues are done", "create the issues from the idea first", nil)
			}
			if openRequests {
				return requestdomain.NewRuleError("requests_open", "issues of this idea are still open", "finish or drop them first", nil)
			}
			return nil
		}
		return requestdomain.NewRuleError("invalid_phase", fmt.Sprintf("an idea cannot go from %s to %s", from, to), "work came out of the idea; it does not go back", nil)
	}
	switch to {
	case PhaseApproved:
		if !hasConcept {
			return requestdomain.NewRuleError("concept_required", "an idea is approved with a concept", "write the concept first (idea action=concept)", nil)
		}
	case PhaseBuilding:
		// In Umsetzung geht nur eine freigegebene Idee, und sie kommt von selbst
		// dorthin, wenn ein Issue aus ihr entsteht; von Hand nur mit Issues.
		if from != PhaseApproved {
			return requestdomain.NewRuleError("not_approved", "work starts on an approved idea", "approve the idea first; only a person can, in the web interface: its owner or a maintainer", nil)
		}
		if !hasRequests {
			return requestdomain.NewRuleError("requests_required", "an idea is in work when issues came out of it", "create the issues from the idea (idea action=issues)", nil)
		}
	case PhaseDone:
		return requestdomain.NewRuleError("invalid_phase", "an idea is done after it was in work", "create the issues from the idea first", nil)
	}
	if to == PhaseConcept && !hasConcept {
		return requestdomain.NewRuleError("concept_required", "the phase concept needs a concept", "write the concept first (idea action=concept)", nil)
	}
	if hasRequests && (to == PhaseRaw || to == PhaseConcept || to == PhaseDiscussion) {
		return requestdomain.NewRuleError("invalid_phase", "issues already came out of this idea", "an approved idea with issues does not go back", nil)
	}
	return nil
}

// Teile eines Textes der Idee.
const (
	PartRaw     = "raw"
	PartSummary = "summary"
	PartConcept = "concept"
)

// Maße der Texte. Die Rohfassung ist ein Transkript und darf groß sein; alles
// andere wird gelesen und bleibt kurz.
const (
	MaxRawBytes     = 400_000
	MaxSummary      = 4000
	MaxSection      = 8000
	MaxRequirements = 30
	MaxRequirement  = 500
	MaxSourceLabel  = 100
	MaxVersionNote  = 200
)

// Text ist eine Fassung der Rohfassung oder der Kurzfassung.
type Text struct {
	Part      string `json:"part"`
	Revision  int    `json:"revision"`
	Body      string `json:"body,omitempty"`
	Digest    string `json:"digest"`
	Bytes     int    `json:"bytes"`
	Source    string `json:"source,omitempty"`
	Message   string `json:"message,omitempty"`
	Person    string `json:"person,omitempty"`
	ByAgent   bool   `json:"by_agent,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Concept hat feste Abschnitte: Worum, Was, Wie, Was nicht, Offene Fragen,
// Anforderungen. Offene Fragen sind Fließtext; was entschieden werden muss,
// steht als offene Entscheidung daneben (decision_open). Anforderungen sind
// Kriterien-Entwürfe, eine Zeile je Kriterium.
type Concept struct {
	About        string   `json:"about"`
	What         string   `json:"what"`
	How          string   `json:"how"`
	Not          string   `json:"not"`
	Questions    string   `json:"questions"`
	Requirements []string `json:"requirements"`
}

// ConceptPatch ändert Abschnitte; nil lässt einen stehen.
type ConceptPatch struct {
	About        *string   `json:"about,omitempty"`
	What         *string   `json:"what,omitempty"`
	How          *string   `json:"how,omitempty"`
	Not          *string   `json:"not,omitempty"`
	Questions    *string   `json:"questions,omitempty"`
	Requirements *[]string `json:"requirements,omitempty"`
}

// SectionNames ordnet die Schlüssel den Überschriften zu, in der Reihenfolge des
// Konzepts.
var SectionNames = []struct{ Key, Title string }{
	{"about", "Worum"}, {"what", "Was"}, {"how", "Wie"}, {"not", "Was nicht"}, {"questions", "Offene Fragen"}, {"requirements", "Anforderungen"},
}

// Empty sagt, ob noch nichts im Konzept steht.
func (c Concept) Empty() bool {
	return c.About == "" && c.What == "" && c.How == "" && c.Not == "" && c.Questions == "" && len(c.Requirements) == 0
}

// Apply legt den Patch auf das Konzept und prüft jeden Abschnitt.
func (c Concept) Apply(p ConceptPatch) (Concept, error) {
	set := func(dst *string, v *string, name string) error {
		if v == nil {
			return nil
		}
		clean, err := CleanSection(*v, name)
		if err != nil {
			return err
		}
		*dst = clean
		return nil
	}
	for _, f := range []struct {
		dst  *string
		v    *string
		name string
	}{{&c.About, p.About, "about"}, {&c.What, p.What, "what"}, {&c.How, p.How, "how"}, {&c.Not, p.Not, "not"}, {&c.Questions, p.Questions, "questions"}} {
		if err := set(f.dst, f.v, f.name); err != nil {
			return Concept{}, err
		}
	}
	if p.Requirements != nil {
		reqs, err := CleanRequirements(*p.Requirements)
		if err != nil {
			return Concept{}, err
		}
		c.Requirements = reqs
	}
	if c.Requirements == nil {
		c.Requirements = []string{}
	}
	return c, nil
}

func ideaRule(code, msg, res string) error { return requestdomain.NewRuleError(code, msg, res, nil) }

// OneLine prüft einen Text, der in Briefings, Listen und Karten landet: eine
// Zeile, ohne Steuerzeichen und ohne unsichtbare Formatzeichen (Bidi, Nullbreite,
// unicode.Cf), höchstens max Zeichen, gültiges UTF-8.
func OneLine(s string, max int) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("not valid UTF-8")
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("longer than %d characters", max)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return fmt.Errorf("must be one line without control or formatting characters")
		}
	}
	return nil
}

// CleanOneLine schneidet ab und prüft mit OneLine; code benennt den Fehler.
func CleanOneLine(s, code, what string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" && required {
		return "", ideaRule(code+"_required", what+" is required", "say it in one line")
	}
	if err := OneLine(s, max); err != nil {
		return "", ideaRule(code+"_invalid", what+": "+err.Error(), "use one short line")
	}
	return s, nil
}

// CleanSection prüft einen Abschnitt des Konzepts oder die Kurzfassung:
// mehrzeilig, aber ohne Steuer- und Formatzeichen außer Zeilenumbruch und Tab.
func CleanSection(s, name string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	limit := MaxSection
	if name == "summary" {
		limit = MaxSummary
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > limit {
		return "", ideaRule("section_invalid", fmt.Sprintf("%s is longer than %d characters or not valid text", name, limit), "shorten it; the full story belongs in the raw version")
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return "", ideaRule("section_invalid", name+" has control or formatting characters", "remove them")
		}
	}
	return s, nil
}

// CleanRequirements prüft die Kriterien-Entwürfe: höchstens MaxRequirements,
// jeder eine Zeile, keine leeren, keine Doppelten.
func CleanRequirements(in []string) ([]string, error) {
	if len(in) > MaxRequirements {
		return nil, ideaRule("requirements_too_many", fmt.Sprintf("at most %d requirements", MaxRequirements), "merge or split the idea")
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := OneLine(r, MaxRequirement); err != nil {
			return nil, ideaRule("requirement_invalid", "requirement: "+err.Error(), "one line per requirement, as an observable outcome")
		}
		if key := strings.ToLower(r); !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// CleanRaw prüft die Rohfassung. Sie bleibt unverändert, Byte für Byte: kein
// Trimmen, keine Zeilenenden-Umschreibung. Abgelehnt wird nur, was kein Text ist
// (kein UTF-8, NUL) oder zu groß ist.
func CleanRaw(s string) error {
	if strings.TrimSpace(s) == "" {
		return ideaRule("raw_required", "the raw version is empty", "paste the text or transcript as it is")
	}
	if len(s) > MaxRawBytes {
		return ideaRule("raw_too_long", fmt.Sprintf("the raw version is longer than %d bytes", MaxRawBytes), "attach it in parts; each part is its own version")
	}
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return ideaRule("raw_invalid", "the raw version is not text (invalid UTF-8 or NUL)", "convert it to UTF-8 text first")
	}
	return nil
}

// Briefing-Zeile: ein Text, mehrzeilig oder nicht, wird auf eine Zeile gebracht
// und gekürzt. Er geht in den Agentenkontext.
func Brief(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if r := []rune(out); len(r) > max {
		return string(r[:max]) + "…"
	}
	return out
}

// ElaborationHintText ist der einmalige Vorschlag. Er verlangt nichts.
const ElaborationHintText = "Optional, nothing is blocked: this idea looks like a bigger undertaking. If you already have the substance (a chat, a transcript), attach it yourself without asking: idea action=text for the raw version and the summary, idea action=concept for a draft. Otherwise it stays one line."

// largeWords sind Themen, bei denen eine Idee meist mehr als eine Zeile braucht.
var largeWords = []string{
	"auth", "oauth", "login", "sso", "sicherheit", "security", "rechte", "rollen", "permission",
	"anbindung", "integration", "schnittstelle", "migration", "datenmodell", "schema", "architektur",
	"konzept", "planung", "umbau", "billing", "zahlung", "payment", "webhook", "synchronis",
}

// LooksLarge sagt, ob eine Idee nach einem größeren Vorhaben aussieht: ein
// langer Text oder ein Stichwort wie Auth, Anbindung, Migration. Es entscheidet
// nur, ob der Vorschlag „ausarbeiten?“ überhaupt erscheint; er bleibt ein Vorschlag.
func LooksLarge(title, body string) bool {
	if len([]rune(body)) >= 400 {
		return true
	}
	text := strings.ToLower(title + " " + body)
	for _, w := range largeWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// Zustände einer Anforderung.
const (
	CoverMet    = "met"    // mindestens ein Kriterium ist erfüllt
	CoverWaived = "waived" // ausdrücklich erlassen, mit Grund
	CoverOpen   = "open"   // Kriterien da, keines erfüllt
	CoverNone   = "none"   // noch kein Kriterium
)

// CoverageCriterion ist ein Kriterium, das aus einer Anforderung entstand.
type CoverageCriterion struct {
	RequestID   int64  `json:"request_id"`
	CriterionID int64  `json:"criterion_id"`
	Number      int    `json:"number"`
	State       string `json:"state"`
}

// RequirementCoverage sagt, ob eine Anforderung des Konzepts abgedeckt ist: ein
// erfülltes Kriterium eines Issues aus der Idee, oder ausdrücklich erlassen.
type RequirementCoverage struct {
	No       int                 `json:"no"`
	Text     string              `json:"text"`
	State    string              `json:"state"`
	Criteria []CoverageCriterion `json:"criteria,omitempty"`
	Reason   string              `json:"waived_reason,omitempty"`
}

// Covered sagt, ob die Anforderung erfüllt oder erlassen ist.
func (c RequirementCoverage) Covered() bool { return c.State == CoverMet || c.State == CoverWaived }

// RequirementKey ist der Schlüssel einer Anforderung: ohne Groß- und
// Kleinschreibung und Leerraum. Die Verknüpfung eines Kriteriums hält den Text,
// der galt, als das Issue entstand; ändert jemand den Text später, ist die
// Anforderung wieder unabgedeckt, bis ein Kriterium zu ihr passt.
func RequirementKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
