package web

import (
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Ideen-Werkstatt auf der Seite einer Idee (REQ-599, REQ-611): was Agenten und
// Menschen aus der Idee gemacht haben (Kurzfassung, Konzept, offene Fragen mit
// Varianten) und der Weg, den nur ein Mensch geht: eine Variante wählen, das Konzept
// freigeben. Die Seite zeigt nur, was der Store liefert; Entscheiden, Abstimmen und
// Freigeben laufen mit store.IdeaActorFor(browserPrincipal(r), ""), das nur eine
// Websitzung als Mensch zählt. Wer die Idee nicht lesen darf, bekommt für jeden dieser
// Wege dieselbe Antwort wie bei einer Nummer, die es nicht gibt.

// workshopPhaseKeys sind die Texte der Phasen.
var workshopPhaseKeys = map[string]string{
	idea.PhaseRaw: "workshop.phase.raw", idea.PhaseConcept: "workshop.phase.concept", idea.PhaseDiscussion: "workshop.phase.discussion",
	idea.PhaseDiscussed: "workshop.phase.discussed", idea.PhaseApproved: "workshop.phase.approved", idea.PhaseBuilding: "workshop.phase.building", idea.PhaseDone: "workshop.phase.done",
	idea.PhaseDismissed: "workshop.phase.dismissed",
}

var workshopPhaseTone = map[string]string{
	idea.PhaseRaw: "p-mute", idea.PhaseConcept: "p-warn", idea.PhaseDiscussion: "p-warn", idea.PhaseDiscussed: "p-ok", idea.PhaseApproved: "p-ok",
	idea.PhaseBuilding: "p-ok", idea.PhaseDone: "p-done", idea.PhaseDismissed: "p-mute",
}

// workshopSteps ist der Weg der Ausarbeitung, wie ihn die Leiste zeigt.
var workshopSteps = []string{idea.PhaseRaw, idea.PhaseConcept, idea.PhaseDiscussion, idea.PhaseDiscussed, idea.PhaseApproved, idea.PhaseBuilding, idea.PhaseDone}

var workshopDecisionKeys = map[string]string{
	idea.DecisionOpen: "workshop.dec.open", idea.DecisionDecided: "workshop.dec.decided", idea.DecisionDeferred: "workshop.dec.deferred", idea.DecisionDropped: "workshop.dec.dropped",
}

var workshopDecisionTone = map[string]string{
	idea.DecisionOpen: "p-warn", idea.DecisionDecided: "p-ok", idea.DecisionDeferred: "p-mute", idea.DecisionDropped: "p-mute",
}

var workshopWayKeys = map[string]string{
	idea.WayHuman: "workshop.way.human", idea.WayAgent: "workshop.way.agent", idea.WayDeferred: "workshop.way.deferred", idea.WayDropped: "workshop.way.dropped",
}

var workshopCoverKeys = map[string]string{
	idea.CoverMet: "workshop.cov.met", idea.CoverWaived: "workshop.cov.waived", idea.CoverOpen: "workshop.cov.open", idea.CoverNone: "workshop.cov.none",
}

var workshopCoverTone = map[string]string{
	idea.CoverMet: "ok", idea.CoverWaived: "mute", idea.CoverOpen: "warn", idea.CoverNone: "warn",
}

// workshopFailKeys übersetzt die Regelcodes des Stores, die ein Mensch an dieser Stelle
// auslösen kann; alles andere heißt „hat nicht geklappt“.
var workshopFailKeys = map[string]string{
	"reason_required": "workshop.fail.reason", "reason_invalid": "workshop.fail.reason",
	"variant_required": "workshop.fail.choose", "answer_required": "workshop.fail.choose", "variant_not_found": "workshop.fail.choose",
	"decision_closed": "workshop.fail.closed", "concept_changed": "workshop.fail.concept_changed", "revision_required": "workshop.fail.concept_changed",
	"concept_required": "workshop.fail.no_concept", "human_decision": "workshop.fail.human", "human_only": "workshop.fail.human",
	"invalid_phase": "workshop.fail.phase", "idea_closed": "workshop.fail.phase",
}

var workshopSectionKeys = map[string]string{
	"about": "workshop.section.about", "what": "workshop.section.what", "how": "workshop.section.how", "not": "workshop.section.not", "questions": "workshop.section.questions",
}

var workshopDoneKeys = map[string]string{
	"vote": "workshop.done.vote", "decided": "workshop.done.decided", "reopen": "workshop.done.reopen", "approve": "workshop.done.approve",
	"discuss": "workshop.done.discuss", "withdraw": "workshop.done.withdraw",
	"close": "workshop.done.close", "reopen_discussion": "workshop.done.reopen_discussion",
	"text_raw": "workshop.done.text_raw", "text_summary": "workshop.done.text_summary", "concept_saved": "workshop.done.concept_saved", "concept_same": "workshop.done.concept_same",
	"asked": "workshop.done.asked", "variant": "workshop.done.variant",
}

type workshopStep struct{ Label, State string }

type workshopSection struct {
	Title  string
	Blocks []bodyBlock
}

type workshopRequirement struct {
	No, Text, State, Tone string
}

type workshopConcept struct {
	Revision     int
	By, Age      string
	Sections     []workshopSection
	Requirements []workshopRequirement
}

type workshopAffect struct {
	Name, Href, Ist, Soll, Rev string
	Num                        string
	Advanced                   bool
}

type workshopVote struct{ Voter, Reason, Tip string }

type workshopVariant struct {
	Label, Title      string
	ID                int64
	Text, Pro, Contra []bodyBlock
	Votes             []workshopVote
	Chosen, Checked   bool
	// Images are the pictures of the variant, shown above its text.
	Images []attachImage
}

type workshopDecision struct {
	ID                            int64
	Num, Question                 string
	Detail                        []bodyBlock
	State, StateLabel, Tone       string
	WayLabel, WayWhy              string
	Open                          bool
	Variants                      []workshopVariant
	Answer, Reason, Trigger       string
	DecidedBy, DecidedAge, Chosen string
	CanDecide, CanVote, CanReopen bool
	// CanAddVariant puts the form for one more variant into the card of an open choice.
	CanAddVariant bool
	MyVote        string
	// CanAttach puts the form that adds a picture into the card of each variant.
	CanAttach bool
	// Stack puts the cards one below the other: four variants side by side are too narrow to read.
	Stack bool
}

// workshopStackFrom is the number of variants from which the cards stand one below the other.
const workshopStackFrom = 4

// workshopNext is what comes next on an idea: the first open decision (with its anchor) or the next step of the way.
// Mode puts the button of that step there instead of the text (DEC-46, REQ-709): the next step is one click.
type workshopNext struct {
	Text, Href string
	// Mode is the step of the phase the text is a button for (discuss, close, approve); Revision belongs to approve.
	Mode     string
	Revision int
}

type workshopView struct {
	// ResultDecision is the decision the result of the last step is about; its card shows the
	// result, the top of the workshop does not repeat it.
	ResultDecision        int64
	ID                    int64
	Token, Anchor         string
	Phase, PhaseLabel     string
	PhaseTone             string
	Steps                 []workshopStep
	Summary               []bodyBlock
	SummaryBy, SummaryAge string
	Raw                   string
	Concept               *workshopConcept
	Affects               []workshopAffect
	Decisions             []workshopDecision
	OpenCount             int
	// Stand says how many of the decisions are open; Next what comes next.
	Stand       string
	Next        *workshopNext
	CanDiscuss  bool
	CanApprove  bool
	CanWithdraw bool
	// CanClose closes the discussion (DEC-46), CanReopen opens a closed one again.
	CanClose, CanReopen bool
	// ClosedBy names who closed the discussion: a person, or an agent and the person whose word it acted on (Said).
	ClosedBy, ClosedSaid, ClosedAge string
	Revision                        int
	Result                          *requestResult
	// Tools are the buttons that open a form (raw text, summary, concept, question); Panel is the form that is open.
	Tools []workshopTool
	Panel *workshopPanel
	// Empty says that nothing is worked out yet: the box then has one line and the tools.
	Empty bool
	// Ask is the question before a step that has a consequence (confirm closing with an open question, approving without requirements).
	Ask *workshopAsk
}

type workshopTool struct {
	Key, Label, Href, Icon string
	Current                bool
}

// workshopAsk asks before closing the discussion with a question open (Open) or before approving a concept without requirements.
type workshopAsk struct {
	Mode, Text, Cancel string
	Revision           int
}

type panelField struct {
	Name, Label, Value string
	Rows, Max          int
	Required           bool
}

type panelSlot struct {
	No                       int
	Label                    string
	Title, Text, Pro, Contra string
}

// workshopPanel is one form of the workshop with what is already in it: the concept as it stands, the summary as it stands.
type workshopPanel struct {
	Kind, Action, Token, Cancel, Title, Submit string
	Base                                       int
	Fields                                     []panelField
	Slots                                      []panelSlot
}

func (a *app) registerWorkshopPages(mux *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"/ui/ideas/{id}/phase":       a.workshopPhase,
		"/ui/decisions/{id}/vote":    a.workshopVote,
		"/ui/decisions/{id}/close":   a.workshopClose,
		"/ui/decisions/{id}/reopen":  a.workshopReopen,
		"/ui/decisions/{id}/variant": a.workshopVariantAdd,
		"/ui/ideas/{id}/text":        a.workshopText,
		"/ui/ideas/{id}/concept":     a.workshopConceptSave,
		"/ui/ideas/{id}/decisions":   a.workshopDecisionOpen,
	} {
		a.handle(mux, "POST "+path, a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(h)))))
	}
	// Images: the form carries a file, so its limit is that of an image.
	a.handle(mux, "POST /ui/ideas/{id}/attach", a.requirePerson(a.requireInteractive(limitBodyN(maxUploadForm, a.requireCSRFMultipart(http.HandlerFunc(a.ideaAttach))))))
	a.handle(mux, "POST /ui/requests/{id}/attach", a.requirePerson(a.requireInteractive(limitBodyN(maxUploadForm, a.requireCSRFMultipart(http.HandlerFunc(a.requestAttach))))))
	a.handle(mux, "POST /ui/pains/{id}/attach", a.requirePerson(a.requireInteractive(limitBodyN(maxUploadForm, a.requireCSRFMultipart(http.HandlerFunc(a.painAttach))))))
	a.handle(mux, "GET /ui/attachments/{id}", a.requirePerson(http.HandlerFunc(a.attachmentImage)))
}

// datedItem ist ein Eintrag der Zeitleiste mit seiner Zeit, damit die Einträge der
// Idee und der Werkstatt in einer Reihe nach der Zeit stehen.
type datedItem struct {
	at   string
	item tlItem
}

// attachWorkshop füllt die Werkstatt der Idee. Hat noch niemand etwas ausgearbeitet, bleibt
// sie leer und die Seite zeigt nichts davon.
func (a *app) attachWorkshop(l lang, r *http.Request, pa *store.ProjectAccess, ws store.IdeaWorkshop, v *ideaPageView) {
	i := ws.Idea
	phase := idea.EffectivePhase(i.State, i.Phase)
	human := interactive(r)
	canWork := human && pa.CheckIdea(i, store.ActWork) == nil
	canEdit := human && pa.CheckIdea(i, store.ActEdit) == nil
	// Writing a text or opening a question needs the right to work on the idea; once the concept is approved, the right to edit it.
	canWrite := canWork && phase != idea.PhaseDismissed && phase != idea.PhaseDone && (canEdit || (phase != idea.PhaseApproved && phase != idea.PhaseBuilding))
	empty := phase == idea.PhaseRaw && ws.Summary == nil && ws.Concept == nil && len(ws.Decisions) == 0 && len(ws.Raw) == 0
	if empty && !canWrite {
		return
	}
	now := overviewNow().UTC()
	age := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	w := &workshopView{Empty: empty, ID: i.ID, Token: csrfOf(r), Anchor: "workshop", Phase: phase, PhaseLabel: label(l, workshopPhaseKeys, phase), PhaseTone: workshopPhaseTone[phase]}
	cur := -1
	for n, p := range workshopSteps {
		if p == phase {
			cur = n
		}
	}
	if phase != idea.PhaseDismissed {
		for n, p := range workshopSteps {
			state := "todo"
			switch {
			case n < cur:
				state = "done"
			case n == cur:
				state = "cur"
			}
			w.Steps = append(w.Steps, workshopStep{Label: label(l, workshopPhaseKeys, p), State: state})
		}
	}
	if s := ws.Summary; s != nil {
		w.Summary, w.SummaryBy, w.SummaryAge = bodyBlocks(s.Body, 0), s.Person, age(s.CreatedAt)
	}
	if len(ws.Raw) > 0 {
		w.Raw = l.msg("workshop.raw", len(ws.Raw))
		if src := ws.Raw[len(ws.Raw)-1].Source; src != "" {
			w.Raw += " · " + src
		}
	}
	if c := ws.Concept; c != nil {
		w.Revision = c.Revision
		wc := &workshopConcept{Revision: c.Revision, By: c.Person, Age: age(c.CreatedAt)}
		for _, s := range []struct{ key, text string }{{"about", c.Concept.About}, {"what", c.Concept.What}, {"how", c.Concept.How}, {"not", c.Concept.Not}, {"questions", c.Concept.Questions}} {
			if blocks := bodyBlocks(s.text, 0); len(blocks) > 0 {
				wc.Sections = append(wc.Sections, workshopSection{Title: l.msg(workshopSectionKeys[s.key]), Blocks: blocks})
			}
		}
		if len(ws.Coverage) > 0 {
			for _, c := range ws.Coverage {
				wc.Requirements = append(wc.Requirements, workshopRequirement{No: strconv.Itoa(c.No), Text: c.Text, State: label(l, workshopCoverKeys, c.State), Tone: workshopCoverTone[c.State]})
			}
		} else {
			for n, text := range c.Concept.Requirements {
				wc.Requirements = append(wc.Requirements, workshopRequirement{No: strconv.Itoa(n + 1), Text: text})
			}
		}
		w.Concept = wc
		for _, f := range ws.Affects {
			af := workshopAffect{Name: f.Name, Href: repoHref(i.Project) + "/features/" + strconv.FormatInt(f.ID, 10), Num: "#" + strconv.FormatInt(f.ID, 10),
				Ist: plainSnippet(l, f.Current.Summary, 280), Rev: l.msg("features.rev", f.Revision), Advanced: f.Advanced}
			af.Soll = plainSnippet(l, c.Concept.What, 280)
			if af.Soll == "" {
				af.Soll = plainSnippet(l, c.Concept.About, 280)
			}
			w.Affects = append(w.Affects, af)
		}
	} else {
		for _, f := range ws.Affects {
			w.Affects = append(w.Affects, workshopAffect{Name: f.Name, Href: repoHref(i.Project) + "/features/" + strconv.FormatInt(f.ID, 10), Num: "#" + strconv.FormatInt(f.ID, 10),
				Ist: plainSnippet(l, f.Current.Summary, 280), Rev: l.msg("features.rev", f.Revision), Advanced: f.Advanced})
		}
	}
	for _, d := range ws.Decisions {
		wd := decisionViewOf(l, r, d, now, canWork, canEdit)
		if wd.Open {
			w.OpenCount++
		}
		wd.CanAttach = canWork && wd.Open && len(wd.Variants) > 0 && phase != idea.PhaseDismissed && phase != idea.PhaseDone
		w.Decisions = append(w.Decisions, wd)
	}
	// Die Wege der Phase, je Phase nur der passende: die Diskussion beginnen (aus Rohidee und Konzept), sie beenden (in der Diskussion),
	// freigeben (nach der Diskussion), die Diskussion wieder öffnen (nach ihrem Ende) und die Freigabe zurücknehmen. Beginnen kann, wer
	// mitarbeitet; beenden, freigeben und zurücknehmen nur, wer die Idee bearbeiten darf.
	w.CanDiscuss = canWork && (phase == idea.PhaseRaw || phase == idea.PhaseConcept) && ws.Concept != nil
	w.CanClose = canEdit && ws.Concept != nil && phase == idea.PhaseDiscussion
	w.CanReopen = canEdit && phase == idea.PhaseDiscussed
	w.CanApprove = canEdit && ws.Concept != nil && phase == idea.PhaseDiscussed
	w.CanWithdraw = canEdit && phase == idea.PhaseApproved
	issueHref := ""
	if human && i.State == idea.Open && phase == idea.PhaseApproved {
		issueHref = repoHref(i.Project) + "/issues/new?derived_from=IDEA-" + strconv.FormatInt(i.ID, 10)
	}
	w.standAndNext(l, phase, cur, ws.Concept != nil, canWrite, func(panel string) string { return workshopPanelHref(i.Project, i.ID, panel) }, issueHref)
	if canWrite {
		w.fillTools(l, r, i, ws)
	}
	if c := ws.Closed; c != nil {
		w.ClosedBy, w.ClosedAge = c.Person, age(c.At)
		if c.ByAgent {
			w.ClosedBy, w.ClosedSaid = l.msg("workshop.closed_agent", c.Person), c.Said
		}
	}
	q := r.URL.Query()
	switch ask := q.Get("wask"); {
	case ask == "close" && w.CanClose:
		w.Ask = &workshopAsk{Mode: "close", Text: l.msg("workshop.ask.close", w.OpenCount), Cancel: repoHref(i.Project) + "/ideas/" + strconv.FormatInt(i.ID, 10) + "#workshop"}
	case ask == "approve" && w.CanApprove:
		w.Ask = &workshopAsk{Mode: "approve", Text: l.msg("workshop.ask.approve"), Revision: w.Revision, Cancel: repoHref(i.Project) + "/ideas/" + strconv.FormatInt(i.ID, 10) + "#workshop"}
		if n, err := strconv.Atoi(q.Get("wrev")); err == nil && n > 0 {
			w.Ask.Revision = n
		}
	}
	switch {
	case q.Get("wfail") != "":
		key, ok := workshopFailKeys[q.Get("wfail")]
		if !ok {
			key = "workshop.fail.failed"
		}
		w.Result = &requestResult{Text: l.msg(key), Kind: "failed", Fail: true}
	case q.Get("wdone") != "":
		if key, ok := workshopDoneKeys[q.Get("wdone")]; ok {
			w.Result = &requestResult{Text: l.msg(key), Kind: "done"}
		}
	}
	if n, err := strconv.ParseInt(q.Get("wdec"), 10, 64); err == nil && n > 0 && w.Result != nil {
		for _, d := range w.Decisions {
			if d.ID == n {
				w.ResultDecision = n
			}
		}
	}
	v.Workshop = w
}

// standAndNext fills the top of the workshop: how many decisions are open and what comes next.
// Decisions first: while one is open it is the next thing; then the next step of the way, as the button
// of that step where the viewer may take it (REQ-709).
func (w *workshopView) standAndNext(l lang, phase string, cur int, hasConcept, canWrite bool, panel func(string) string, issueHref string) {
	if n := len(w.Decisions); n > 0 {
		switch {
		case w.OpenCount > 0:
			w.Stand = l.msg("workshop.stand.open", w.OpenCount, n)
		case n == 1:
			w.Stand = l.msg("workshop.stand.done_one")
		default:
			w.Stand = l.msg("workshop.stand.done", n)
		}
	}
	for _, d := range w.Decisions {
		if d.Open {
			w.Next = &workshopNext{Text: d.Num + " · " + d.Question, Href: "#decision-" + strconv.FormatInt(d.ID, 10)}
			return
		}
	}
	step := func(mode, key string, args ...any) *workshopNext {
		return &workshopNext{Mode: mode, Text: l.msg(key, args...), Revision: w.Revision}
	}
	switch {
	case phase == idea.PhaseDismissed || cur < 0 || cur+1 >= len(workshopSteps):
	case !hasConcept && (phase == idea.PhaseRaw || phase == idea.PhaseConcept) && canWrite:
		w.Next = &workshopNext{Text: l.msg("workshop.tool.concept"), Href: panel(panelConcept)}
	case hasConcept && phase == idea.PhaseDiscussion:
		// No decision is open (any open one returned above): the workshop proposes closing the discussion.
		w.Next = step("", "workshop.close")
		if w.CanClose {
			w.Next.Mode = "close"
		}
	case hasConcept && (phase == idea.PhaseRaw || phase == idea.PhaseConcept):
		w.Next = step("", "workshop.discuss")
		if w.CanDiscuss {
			w.Next.Mode = "discuss"
		}
	case hasConcept && w.Revision > 0 && phase == idea.PhaseDiscussed:
		w.Next = step("", "workshop.approve", w.Revision)
		if w.CanApprove {
			w.Next.Mode = "approve"
		}
	case phase == idea.PhaseApproved && issueHref != "":
		w.Next = &workshopNext{Text: l.msg("ideas.make_issue"), Href: issueHref}
	default:
		w.Next = &workshopNext{Text: label(l, workshopPhaseKeys, workshopSteps[cur+1])}
	}
}

// fillTools sets the buttons at the top of the workshop that open a form, and the form that is open (?panel=).
func (w *workshopView) fillTools(l lang, r *http.Request, i idea.Idea, ws store.IdeaWorkshop) {
	has := map[string]bool{panelRaw: len(ws.Raw) > 0, panelSummary: ws.Summary != nil, panelConcept: ws.Concept != nil}
	want := r.URL.Query().Get("panel")
	for _, t := range []struct{ key, icon, label, edit string }{
		{panelRaw, "file", "workshop.tool.raw", ""}, {panelSummary, "list", "workshop.tool.summary", "workshop.tool.summary_edit"},
		{panelConcept, "book", "workshop.tool.concept", "workshop.tool.concept_edit"}, {panelDecision, "chat", "workshop.tool.decision", ""},
	} {
		key := t.label
		if has[t.key] && t.edit != "" {
			key = t.edit
		}
		w.Tools = append(w.Tools, workshopTool{Key: t.key, Icon: t.icon, Label: l.msg(key), Href: workshopPanelHref(i.Project, i.ID, t.key), Current: want == t.key})
	}
	p := &workshopPanel{Kind: want, Token: w.Token, Cancel: repoHref(i.Project) + "/ideas/" + strconv.FormatInt(i.ID, 10) + "#workshop"}
	id := strconv.FormatInt(i.ID, 10)
	field := func(name, label, value string, rows int) panelField {
		return panelField{Name: name, Label: label, Value: value, Rows: rows}
	}
	required := func(f panelField) panelField { f.Required = true; return f }
	limit := func(f panelField, n int) panelField { f.Max = n; return f }
	switch want {
	case panelRaw:
		p.Action, p.Title, p.Submit = "/ui/ideas/"+id+"/text", l.msg("workshop.tool.raw"), l.msg("workshop.save_text")
		p.Fields = []panelField{required(field("body", l.msg("workshop.panel.raw"), "", 10)), limit(field("source", l.msg("workshop.panel.source"), "", 0), idea.MaxSourceLabel)}
	case panelSummary:
		body := ""
		if ws.Summary != nil {
			body = ws.Summary.Body
		}
		p.Action, p.Title, p.Submit = "/ui/ideas/"+id+"/text", l.msg("workshop.summary"), l.msg("workshop.save_text")
		p.Fields = []panelField{required(field("body", l.msg("workshop.summary"), body, 5))}
	case panelConcept:
		var c idea.Concept
		if ws.Concept != nil {
			c = ws.Concept.Concept
			p.Base = ws.Concept.Revision
		}
		p.Action, p.Title, p.Submit = "/ui/ideas/"+id+"/concept", l.msg("workshop.concept"), l.msg("workshop.save_concept")
		for _, s := range []struct{ key, text string }{{"about", c.About}, {"what", c.What}, {"how", c.How}, {"not", c.Not}, {"questions", c.Questions}} {
			p.Fields = append(p.Fields, field(s.key, l.msg(workshopSectionKeys[s.key]), s.text, 4))
		}
		p.Fields = append(p.Fields, field("requirements", l.msg("workshop.panel.requirements"), strings.Join(c.Requirements, "\n"), 5), limit(field("message", l.msg("workshop.panel.note"), "", 0), idea.MaxVersionNote))
	case panelDecision:
		p.Action, p.Title, p.Submit = "/ui/ideas/"+id+"/decisions", l.msg("workshop.tool.decision"), l.msg("workshop.ask_decision")
		p.Fields = []panelField{required(limit(field("question", l.msg("workshop.panel.question"), "", 0), idea.MaxQuestion)), field("detail", l.msg("workshop.panel.detail"), "", 3)}
		for n := 1; n <= workshopVariantSlots; n++ {
			p.Slots = append(p.Slots, panelSlot{No: n, Label: idea.VariantLabel(n - 1)})
		}
	default:
		return
	}
	w.Panel = p
}

// decisionViewOf is the view of one decision, on the page of its idea and on its own page.
// Closing needs the right to edit the subject, voting the right to work on it; the caller has
// looked both up (and ruled out a session that is no person). CanAttach is the caller's to set.
func decisionViewOf(l lang, r *http.Request, d idea.Decision, now time.Time, canWork, canEdit bool) workshopDecision {
	age := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	me := browserPrincipal(r).Label
	wd := workshopDecision{ID: d.ID, Num: d.HumanID(), Question: d.Question, Detail: bodyBlocks(d.Detail, 0), State: d.State, StateLabel: label(l, workshopDecisionKeys, d.State),
		Tone: workshopDecisionTone[d.State], WayLabel: label(l, workshopWayKeys, d.Way), WayWhy: workshopWhy(l, d.WayWhy), Open: d.State == idea.DecisionOpen,
		Answer: d.Answer, Reason: d.Reason, Trigger: d.Trigger, DecidedBy: d.DecidedBy, DecidedAge: age(d.DecidedAt)}
	// Once somebody has decided, the way says who: "The agent decides" would be a lie after a person chose.
	if d.State == idea.DecisionDecided && d.DecidedBy != "" && !d.ByAgent {
		wd.WayLabel, wd.WayWhy = l.msg("workshop.way.decided_by", d.DecidedBy), ""
	}
	// Schließen darf, wer den Gegenstand bearbeiten darf; abstimmen, wer mitarbeiten darf.
	wd.CanDecide, wd.CanVote, wd.CanReopen = wd.Open && canEdit, wd.Open && canWork && len(d.Variants) > 0, !wd.Open && canWork
	wd.CanAddVariant = wd.Open && canWork && len(d.Variants) >= 2 && len(d.Variants) < idea.MaxVariants
	if d.ByAgent && d.DecidedBy != "" {
		wd.DecidedBy = l.msg("workshop.closed_agent", d.DecidedBy)
	}
	var personVote, agentVote string
	for _, vr := range d.Variants {
		wv := workshopVariant{ID: vr.ID, Label: vr.Label, Title: vr.Title, Images: attachImages(l, vr.Attachments, func(idea.AttachmentMeta) bool { return true }), Text: bodyBlocks(vr.Text, 0), Pro: bodyBlocks(vr.Pro, 0), Contra: bodyBlocks(vr.Contra, 0), Chosen: d.ChosenVariant == vr.Label}
		for _, vt := range d.Votes {
			if vt.Variant == vr.Label {
				wv.Votes = append(wv.Votes, workshopVote{Voter: vt.Voter, Reason: vt.Reason, Tip: vt.Voter + ": " + vt.Reason})
				// A vote of the viewer's agent is the viewer's vote: the card shows it picked. A vote
				// the viewer cast themselves wins over it.
				if vt.Voter == me {
					if vt.ByAgent {
						agentVote = vr.Label
					} else {
						personVote = vr.Label
					}
				}
			}
		}
		if wv.Chosen {
			wd.Chosen = vr.Label + " · " + vr.Title
		}
		wd.Variants = append(wd.Variants, wv)
	}
	if wd.MyVote = personVote; wd.MyVote == "" {
		wd.MyVote = agentVote
	}
	wd.Stack = len(wd.Variants) >= workshopStackFrom
	for n := range wd.Variants {
		wd.Variants[n].Checked = wd.MyVote != "" && wd.Variants[n].Label == wd.MyVote
	}
	return wd
}

// workshopDated sind die Einträge, die die Werkstatt zur Zeitleiste der Idee beisteuert:
// wer eine Frage entschieden, vertagt oder fallengelassen hat und wer für welche Variante
// war. Die Fragen selbst und die Fassungen der Texte stehen schon in ideaItems.
func (a *app) workshopDated(l lang, ws store.IdeaWorkshop, age func(string) string) []datedItem {
	var out []datedItem
	sentence := func(key string, args ...any) string { return html.EscapeString(l.msg(key, args...)) }
	for _, d := range ws.Decisions {
		for _, vt := range d.Votes {
			vote := tlItem{ID: "vote-" + strconv.FormatInt(d.ID, 10) + "-" + url.QueryEscape(vt.Voter), Act: "idea.vote", Who: vt.Voter, Icon: "thumb",
				Head: template.HTML(sentence("workshop.ev.vote", vt.Variant) + " <span class=\"rn\">" + html.EscapeString(d.HumanID()) + "</span>"), Age: age(vt.At), Body: mdText(vt.Reason)}
			if vt.ByAgent {
				// The vote of an agent is not the vote of its person: it reads "Pia’s Agent" with the agent's mark.
				vote.withActor(actorView{Person: vt.Voter, Agent: true, Kind: harnessKind(""), Label: authorAgentName(l, vt.Voter, "")})
			}
			out = append(out, datedItem{vt.At, vote})
		}
		if d.State == idea.DecisionOpen || d.DecidedAt == "" {
			continue
		}
		icon, key, tone := "check", "workshop.ev.decided", "ok"
		switch d.State {
		case idea.DecisionDeferred:
			icon, key, tone = "pause", "workshop.ev.deferred", ""
		case idea.DecisionDropped:
			icon, key, tone = "drop", "workshop.ev.dropped", ""
		}
		var body []string
		if d.ChosenVariant != "" {
			for _, vr := range d.Variants {
				if vr.Label == d.ChosenVariant {
					body = append(body, "<p><b>"+html.EscapeString(vr.Label+" · "+vr.Title)+"</b></p>")
				}
			}
		}
		for _, s := range []string{d.Answer, d.Reason, d.Trigger} {
			if s != "" {
				body = append(body, string(mdText(s)))
			}
		}
		out = append(out, datedItem{d.DecidedAt, tlItem{ID: "decided-" + strconv.FormatInt(d.ID, 10), Act: "idea.decided", Who: d.DecidedBy, Icon: icon, Tone: tone,
			Head: template.HTML(sentence(key) + " <span class=\"rn\">" + html.EscapeString(d.HumanID()) + "</span>"), Age: age(d.DecidedAt), Body: template.HTML(strings.Join(body, ""))}})
		if d.ByAgent {
			// A decision an agent made is not the decision of its person.
			out[len(out)-1].item.withActor(byAgentActor(l, d.DecidedBy, true))
		}
	}
	return out
}

// ---- Schritte eines Menschen

// workshopBack ist die Adresse der Idee mit dem Ergebnis; Anker auf die Werkstatt.
func workshopBack(project string, id int64, key, value string) string {
	return repoHref(project) + "/ideas/" + strconv.FormatInt(id, 10) + "?" + key + "=" + url.QueryEscape(value) + "#workshop"
}

// workshopFail antwortet auf einen Fehler eines Schrittes: ohne Recht 403, unbekannt oder fremd
// 404, eine Regel des Stores als Hinweis auf der Idee.
func (a *app) workshopFail(w http.ResponseWriter, r *http.Request, err error, project string, ideaID int64) {
	a.workshopFailTo(w, r, err, func(key, value string) string { return workshopBack(project, ideaID, key, value) })
}

// workshopFailTo is workshopFail for any page a step returns to (back adds the result to its address).
func (a *app) workshopFailTo(w http.ResponseWriter, r *http.Request, err error, back func(key, value string) string) {
	if a.secretRefused(w, r, err, back("", "")) {
		return
	}
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound):
		http.NotFound(w, r)
	default:
		code := requestdomain.Code(err)
		switch code {
		case "idea_not_found", "decision_not_found", "request_not_found":
			http.NotFound(w, r)
		case "":
			http.Error(w, "workshop step failed", http.StatusInternalServerError)
		default:
			http.Redirect(w, r, back("wfail", code), http.StatusSeeOther)
		}
	}
}

// decisionBack is the address a step on a decision returns to, with the result: the workshop
// of its idea, or for a decision of an issue the page of the decision itself.
func decisionBack(d idea.Decision) func(key, value string) string {
	if d.Subject == idea.SubjectIdea {
		// The answer stands at the decision it is about, not at the top of the workshop.
		return func(key, value string) string {
			u := repoHref(d.Project) + "/ideas/" + strconv.FormatInt(d.SubjectID, 10)
			if key != "" {
				u += "?" + key + "=" + url.QueryEscape(value) + "&wdec=" + strconv.FormatInt(d.ID, 10)
			}
			return u + "#decision-" + strconv.FormatInt(d.ID, 10)
		}
	}
	return func(key, value string) string {
		u := decisionPageHref(d)
		if key != "" {
			u += "?" + key + "=" + url.QueryEscape(value)
		}
		return u
	}
}

// workshopIdea liest die Idee des Schrittes. Wer sie nicht lesen darf, bekommt 404.
func (a *app) workshopIdea(w http.ResponseWriter, r *http.Request, id int64) (idea.Idea, bool) {
	i, err := a.access(r).IdeaView(id)
	if err != nil {
		http.NotFound(w, r)
		return idea.Idea{}, false
	}
	return i, true
}

// workshopDecisionOf liest die Entscheidung des Schrittes, ihrer Idee oder ihres Issues. Wer sie
// nicht lesen darf, bekommt 404.
func (a *app) workshopDecisionOf(w http.ResponseWriter, r *http.Request) (idea.Decision, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return idea.Decision{}, false
	}
	d, err := a.access(r).DecisionView(id)
	if err != nil {
		http.NotFound(w, r)
		return idea.Decision{}, false
	}
	return d, true
}

func formLine(r *http.Request, name string, max int) (string, bool) {
	v := r.FormValue(name)
	return v, !tooLong(v, max)
}

// workshopPhase: POST /ui/ideas/{id}/phase. mode ist discuss (Diskussion beginnen), close (Diskussion
// beenden, DEC-46), reopen_discussion (wieder öffnen), approve (Konzept freigeben, mit der gesehenen
// Fassung) oder withdraw (Freigabe zurücknehmen).
func (a *app) workshopPhase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	i, ok := a.workshopIdea(w, r, id)
	if !ok {
		return
	}
	pa := a.access(r)
	actor := store.IdeaActorFor(browserPrincipal(r), "")
	mode := r.FormValue("mode")
	// Closing with a question open and approving without requirements ask first; confirm=1 is the answer.
	if r.FormValue("confirm") != "1" {
		if ask := a.phaseNeedsAsk(r, id, mode); ask != "" {
			target := phaseAskHref(i.Project, id, ask, r.FormValue("revision"))
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	switch mode {
	case "discuss":
		_, err = pa.StartIdeaDiscussion(id)
	case "approve":
		rev, convErr := strconv.Atoi(r.FormValue("revision"))
		if convErr != nil {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
		_, err = pa.SetIdeaPhase(id, idea.PhaseApproved, actor, rev)
	case "withdraw":
		_, err = pa.SetIdeaPhase(id, idea.PhaseDiscussion, actor, 0)
	case "close":
		_, err = pa.SetIdeaPhase(id, idea.PhaseDiscussed, actor, 0)
	case "reopen_discussion":
		_, err = pa.SetIdeaPhase(id, idea.PhaseDiscussion, actor, 0)
	default:
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if err != nil {
		a.workshopFail(w, r, err, i.Project, id)
		return
	}
	http.Redirect(w, r, workshopBack(i.Project, id, "wdone", mode), http.StatusSeeOther)
}

// workshopVote: POST /ui/decisions/{id}/vote.
func (a *app) workshopVote(w http.ResponseWriter, r *http.Request) {
	d, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	back := decisionBack(d)
	reason, fits := formLine(r, "reason", maxFieldChars)
	if !fits {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	voter := store.IdeaActorFor(browserPrincipal(r), "")
	if _, err := a.access(r).VoteDecision(d.ID, r.FormValue("variant"), reason, voter); err != nil {
		a.workshopFailTo(w, r, err, back)
		return
	}
	a.stampActor(r, actor.Vote, actor.VoteEntryID(d.ID, store.VoterKey(voter)))
	http.Redirect(w, r, back("wdone", "vote"), http.StatusSeeOther)
}

// workshopClose: POST /ui/decisions/{id}/close: der Mensch wählt eine Variante oder gibt eine
// eigene Antwort und sagt warum. Vertagen und Fallenlassen gibt es hier nicht.
func (a *app) workshopClose(w http.ResponseWriter, r *http.Request) {
	d, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	back := decisionBack(d)
	reason, fits1 := formLine(r, "reason", maxFieldChars)
	answer, fits2 := formLine(r, "answer", maxFieldChars)
	if !fits1 || !fits2 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	_, err := a.access(r).CloseDecision(store.CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Variant: r.FormValue("variant"), Answer: answer, Reason: reason,
		Actor: store.IdeaActorFor(browserPrincipal(r), "")})
	if err != nil {
		a.workshopFailTo(w, r, err, back)
		return
	}
	http.Redirect(w, r, back("wdone", "decided"), http.StatusSeeOther)
}

// workshopReopen: POST /ui/decisions/{id}/reopen.
func (a *app) workshopReopen(w http.ResponseWriter, r *http.Request) {
	d, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	back := decisionBack(d)
	reason, fits := formLine(r, "reason", maxFieldChars)
	if !fits {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if _, err := a.access(r).ReopenDecision(d.ID, reason, store.IdeaActorFor(browserPrincipal(r), "")); err != nil {
		a.workshopFailTo(w, r, err, back)
		return
	}
	http.Redirect(w, r, back("wdone", "reopen"), http.StatusSeeOther)
}

var _ = time.Second

// workshopWhyKeys names, in the reader's language, the reasons the idea package
// gives for a question that a person has to decide.
var workshopWhyKeys = map[string]string{
	"not easily reversible":                                      "workshop.why.irreversible",
	"touches security or permissions":                            "workshop.why.security",
	"costs money or capacity":                                    "workshop.why.cost",
	"reaches outside: third-party services or public interfaces": "workshop.why.external",
	"breaks the data model":                                      "workshop.why.data_model",
	"sets the product direction":                                 "workshop.why.product",
	"people disagree":                                            "workshop.why.people",
	"is not local":                                               "workshop.why.nonlocal",
	"departs from existing conventions or decisions":             "workshop.why.convention",
}

func workshopWhy(l lang, reasons []string) string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		if key, ok := workshopWhyKeys[r]; ok {
			r = l.msg(key)
		}
		out = append(out, r)
	}
	return strings.Join(out, "; ")
}
