package idea

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Offene Entscheidungen (REQ-611): eine knappe Frage an einer Idee oder an einem
// Issue, optional mit Varianten A, B, C. Jede hat eine Art, sie zu schließen.

// Arten, eine Entscheidung zu schließen.
const (
	WayHuman    = "human"    // ein Mensch entscheidet
	WayAgent    = "agent"    // ein Agent darf entscheiden und begründet es
	WayDeferred = "deferred" // bewusst vertagt, mit Auslöser
	WayDropped  = "dropped"  // bewusst fallengelassen, mit Grund
)

// Zustände einer Entscheidung.
const (
	DecisionOpen     = "open"
	DecisionDecided  = "decided"
	DecisionDeferred = "deferred"
	DecisionDropped  = "dropped"
)

// Gegenstände, an denen eine Entscheidung hängt.
const (
	SubjectIdea    = "idea"
	SubjectRequest = "request"
)

// Maße.
const (
	MaxQuestion   = 300
	MaxDetail     = 4000
	MaxTrigger    = 200
	MaxReasonLine = 500
	MaxVariants   = 8
	MaxVariant    = 2000
	MaxVariantTit = 120
)

// Aspekte, an denen sich entscheidet, wer entscheiden darf. Sie sind die
// Aussage dessen, der die Frage stellt; hier steht die Regel, nicht die Prüfung
// der Aussage.
const (
	AspectIrreversible = "irreversible" // schwer oder nicht umkehrbar
	AspectSecurity     = "security"     // Sicherheit oder Rechte
	AspectCost         = "cost"         // Kosten
	AspectExternal     = "external"     // Außenwirkung: Fremddienste, öffentliche Schnittstellen
	AspectDataModel    = "data_model"   // Datenmodell-Bruch
	AspectProduct      = "product"      // Produktrichtung
	AspectPeople       = "people"       // Widerspruch zwischen Personen
	AspectNonLocal     = "nonlocal"     // betrifft mehr als den lokalen Teil
	AspectConvention   = "convention"   // bricht bestehende Konventionen oder Entscheidungen
)

// aspectReasons erklärt, warum ein Aspekt einen Menschen verlangt.
var aspectReasons = map[string]string{
	AspectIrreversible: "not easily reversible",
	AspectSecurity:     "touches security or permissions",
	AspectCost:         "costs money or capacity",
	AspectExternal:     "reaches outside: third-party services or public interfaces",
	AspectDataModel:    "breaks the data model",
	AspectProduct:      "sets the product direction",
	AspectPeople:       "people disagree",
	AspectNonLocal:     "is not local",
	AspectConvention:   "departs from existing conventions or decisions",
}

// Aspects nennt die bekannten Aspekte, sortiert.
func Aspects() []string {
	out := make([]string, 0, len(aspectReasons))
	for k := range aspectReasons {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NormalizeAspects prüft die Aspekte und liefert sie sortiert ohne Doppelte. Ein
// unbekannter Aspekt ist ein Fehler, damit sich ein Tippfehler („secuirty“) nicht
// als „kein Grund für einen Menschen“ liest.
func NormalizeAspects(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, ok := aspectReasons[a]; !ok {
			return nil, ideaRule("aspect_unknown", fmt.Sprintf("unknown aspect %q", a), "known: "+strings.Join(Aspects(), ", "))
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DecideWho ist die Regel, wer eine Entscheidung trifft. Der Agent entscheidet
// selbst, wenn sie umkehrbar, lokal und im Rahmen bestehender Konventionen und
// Entscheidungen ist, also wenn keiner der Aspekte gesetzt ist. Bei Unumkehrbarem,
// Sicherheit und Rechten, Kosten, Außenwirkung, Datenmodell-Brüchen, Produktrichtung
// und Widerspruch zwischen Personen entscheidet der Mensch. Dazu die Gründe.
func DecideWho(aspects []string) (way string, why []string) {
	for _, a := range aspects {
		if r, ok := aspectReasons[a]; ok {
			why = append(why, r)
		}
	}
	sort.Strings(why)
	if len(why) > 0 {
		return WayHuman, why
	}
	return WayAgent, []string{"reversible, local and within existing conventions"}
}

// Policy je Projekt: "rule" (Vorgabe) oder "human" (jede Entscheidung wartet auf
// einen Menschen).
const (
	PolicyRule  = "rule"
	PolicyHuman = "human"
)

// ChooseWay bestimmt die Art für eine neue Entscheidung. requested ist, was der
// Fragende ausdrücklich will ("" = Regel). Ein Agent darf nicht "agent" wählen,
// wenn die Regel einen Menschen verlangt oder die Projekt-Policy alles beim
// Menschen lässt; ein Mensch darf jede Art setzen. byAgent sagt, wer fragt.
func ChooseWay(requested string, aspects []string, policy string, byAgent bool) (way string, why []string, err error) {
	ruleWay, ruleWhy := DecideWho(aspects)
	if policy == PolicyHuman {
		ruleWay, ruleWhy = WayHuman, []string{"this project lets people decide everything"}
	}
	switch requested {
	case "":
		if byAgent {
			return ruleWay, ruleWhy, nil
		}
		// Wer als Mensch fragt, will in der Regel selbst entscheiden.
		return WayHuman, []string{"asked by a person"}, nil
	case WayHuman:
		return WayHuman, []string{"set explicitly"}, nil
	case WayAgent:
		if byAgent && ruleWay == WayHuman {
			return "", nil, ideaRule("way_not_allowed", "an agent cannot take this decision: "+strings.Join(ruleWhy, "; "), "leave it to a person (way human) or ask them")
		}
		return WayAgent, []string{"set explicitly"}, nil
	}
	return "", nil, ideaRule("way_invalid", fmt.Sprintf("unknown way %q", requested), "use human or agent; deferred and dropped are how a decision is closed")
}

// ValidPolicy sagt, ob s eine Projekt-Policy ist.
func ValidPolicy(s string) bool { return s == PolicyRule || s == PolicyHuman }

// Decision ist eine offene (oder geschlossene) Entscheidung.
type Decision struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	// Subject ist idea oder request, SubjectID die Nummer.
	Subject   string `json:"subject"`
	SubjectID int64  `json:"subject_id"`
	Question  string `json:"question"`
	Detail    string `json:"detail,omitempty"`
	// Way ist die Art, sie zu schließen; nach dem Schließen die gewählte.
	Way string `json:"way"`
	// Decider ist, wer entscheiden darf (human oder agent), auch wenn die
	// Entscheidung vertagt oder fallengelassen wurde.
	Decider string `json:"decider"`
	// WayWhy sagt, warum diese Art gewählt wurde (Regel oder ausdrücklich).
	WayWhy  []string `json:"way_why,omitempty"`
	Aspects []string `json:"aspects,omitempty"`
	State   string   `json:"state"`
	// Antwort und Begründung nach dem Schließen; Trigger bei vertagt.
	Answer  string `json:"answer,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Trigger string `json:"trigger,omitempty"`
	// ChosenVariant ist die Nummer (Buchstabe) der übernommenen Variante.
	ChosenVariant string `json:"chosen_variant,omitempty"`
	// KnowledgeID ist das Wissen (decision), das beim Entscheiden entstand.
	KnowledgeID int64 `json:"knowledge_id,omitempty"`
	// DecidedBy und ByAgent: wer geschlossen hat; ByAgent heißt „vom Agenten“.
	DecidedBy string    `json:"decided_by,omitempty"`
	ByAgent   bool      `json:"decided_by_agent,omitempty"`
	DecidedAt string    `json:"decided_at,omitempty"`
	Person    string    `json:"person,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Variants  []Variant `json:"variants,omitempty"`
	Votes     []Vote    `json:"votes,omitempty"`
	// AuthorID ist das Konto des Fragenden, es verlässt den Server nie.
	AuthorID int64 `json:"-"`
}

// HumanID ist DEC-n.
func (d Decision) HumanID() string { return fmt.Sprintf("DEC-%d", d.ID) }

// NeedsHuman sagt, ob die Entscheidung auf einen Menschen wartet: offen und von
// der Art human. Nur diese erscheint im Posteingang; alles andere ist ein Hinweis.
func (d Decision) NeedsHuman() bool { return d.State == DecisionOpen && d.Way == WayHuman }

// Variant ist ein Zweig einer Entscheidung: A, B, C mit Text, Pro und Contra.
type Variant struct {
	ID     int64  `json:"id"`
	Label  string `json:"label"`
	Title  string `json:"title"`
	Text   string `json:"text,omitempty"`
	Pro    string `json:"pro,omitempty"`
	Contra string `json:"contra,omitempty"`
	// Attachments sind die Bilder an der Variante.
	Attachments []AttachmentMeta `json:"attachments,omitempty"`
	// Votes: wer sich für die Variante ausgesprochen hat (Zahl je Variante steht
	// in len(Votes); die Stimmen selbst an der Entscheidung).
	Chosen bool `json:"chosen,omitempty"`
}

// VariantInput legt eine Variante an. Das Label vergibt der Store (A, B, …).
type VariantInput struct {
	Title  string `json:"title"`
	Text   string `json:"text,omitempty"`
	Pro    string `json:"pro,omitempty"`
	Contra string `json:"contra,omitempty"`
}

// Vote ist eine Stimme: wer, wofür, warum. Je Person und Art (Mensch oder Agent)
// zählt die letzte.
type Vote struct {
	Voter   string `json:"voter"`
	ByAgent bool   `json:"by_agent,omitempty"`
	Variant string `json:"variant"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
}

// VariantLabel ist der Buchstabe der n-ten Variante (0 = A).
func VariantLabel(n int) string { return string(rune('A' + n)) }

// CleanVariant prüft eine Variante.
func CleanVariant(v VariantInput) (VariantInput, error) {
	var err error
	if v.Title, err = CleanOneLine(v.Title, "variant_title", "the variant title", MaxVariantTit, true); err != nil {
		return v, err
	}
	for _, f := range []struct {
		dst  *string
		name string
	}{{&v.Text, "text"}, {&v.Pro, "pro"}, {&v.Contra, "contra"}} {
		s := strings.TrimSpace(strings.ReplaceAll(*f.dst, "\r\n", "\n"))
		if len([]rune(s)) > MaxVariant {
			return v, ideaRule("variant_too_long", fmt.Sprintf("the variant %s is longer than %d characters", f.name, MaxVariant), "shorten it")
		}
		for _, r := range s {
			if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ') {
				return v, ideaRule("variant_invalid", "the variant "+f.name+" has control or formatting characters", "remove them")
			}
		}
		*f.dst = s
	}
	return v, nil
}

// CleanQuestion prüft die Frage: eine Zeile, höchstens MaxQuestion Zeichen.
func CleanQuestion(s string) (string, error) {
	return CleanOneLine(s, "question", "the question", MaxQuestion, true)
}

// CleanTrigger prüft den Auslöser einer vertagten Entscheidung.
func CleanTrigger(s string) (string, error) {
	return CleanOneLine(s, "trigger", "the trigger", MaxTrigger, true)
}

// CleanReasonLine prüft eine Begründung oder einen Grund: eine Zeile.
func CleanReasonLine(s string, required bool) (string, error) {
	return CleanOneLine(s, "reason", "the reason", MaxReasonLine, required)
}

// TriggerMatches sagt, ob ein Auslöser („wenn wir die Auth bauen“) zu einem
// Kontext passt: Branch, Paketname, Titel der Requests. Er passt, wenn ein
// bedeutungstragendes Wort des Auslösers (mindestens vier Zeichen, keine
// Füllwörter) in einem Kontexttext vorkommt, oder wenn er ausdrücklich eine
// REQ-n oder einen branch:name nennt, die im Kontext stehen. Das ist bewusst
// grob: ein Treffer zeigt die Entscheidung im Briefing, mehr passiert nicht.
func TriggerMatches(trigger string, context ...string) bool {
	trigger = strings.ToLower(trigger)
	hay := strings.ToLower(strings.Join(context, "\n"))
	if strings.TrimSpace(trigger) == "" || strings.TrimSpace(hay) == "" {
		return false
	}
	for _, ref := range triggerRefRE.FindAllString(trigger, -1) {
		if containsToken(hay, ref) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(triggerRefRE.ReplaceAllString(trigger, " "), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(tok)) < 4 || triggerStop[tok] {
			continue
		}
		if strings.Contains(hay, tok) {
			return true
		}
	}
	return false
}

var triggerRefRE = regexp.MustCompile(`(?i)\b(?:req|pkg|idea)-\d+`)

func containsToken(hay, tok string) bool {
	for i := 0; ; {
		j := strings.Index(hay[i:], tok)
		if j < 0 {
			return false
		}
		end := i + j + len(tok)
		if end >= len(hay) || !unicode.IsDigit(rune(hay[end])) {
			return true
		}
		i = end
	}
}

var triggerStop = map[string]bool{
	"wenn": true, "dann": true, "wir": true, "bauen": true, "machen": true, "wird": true, "werden": true,
	"when": true, "then": true, "that": true, "this": true, "with": true, "once": true, "build": true,
	"haben": true, "sind": true, "sobald": true, "damit": true, "nach": true, "oder": true, "und": true,
	"dies": true, "diese": true, "dieser": true, "feature": true, "branch": true,
}
