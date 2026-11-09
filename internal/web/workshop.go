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
	idea.PhaseApproved: "workshop.phase.approved", idea.PhaseBuilding: "workshop.phase.building", idea.PhaseDone: "workshop.phase.done",
	idea.PhaseDismissed: "workshop.phase.dismissed",
}

var workshopPhaseTone = map[string]string{
	idea.PhaseRaw: "p-mute", idea.PhaseConcept: "p-warn", idea.PhaseDiscussion: "p-warn", idea.PhaseApproved: "p-ok",
	idea.PhaseBuilding: "p-ok", idea.PhaseDone: "p-done", idea.PhaseDismissed: "p-mute",
}

// workshopSteps ist der Weg der Ausarbeitung, wie ihn die Leiste zeigt.
var workshopSteps = []string{idea.PhaseRaw, idea.PhaseConcept, idea.PhaseDiscussion, idea.PhaseApproved, idea.PhaseBuilding, idea.PhaseDone}

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
	MyVote                        string
	// CanAttach shows the form that adds a picture to one of the variants.
	CanAttach bool
}

type workshopView struct {
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
	CanDiscuss            bool
	CanApprove            bool
	CanWithdraw           bool
	Revision              int
	Result                *requestResult
}

func (a *app) registerWorkshopPages(mux *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"/ui/ideas/{id}/phase":      a.workshopPhase,
		"/ui/decisions/{id}/vote":   a.workshopVote,
		"/ui/decisions/{id}/close":  a.workshopClose,
		"/ui/decisions/{id}/reopen": a.workshopReopen,
	} {
		a.handle(mux, "POST "+path, a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(h)))))
	}
	// Images: the form carries a file, so its limit is that of an image.
	a.handle(mux, "POST /ui/ideas/{id}/attach", a.requirePerson(a.requireInteractive(limitBodyN(maxUploadForm, a.requireCSRFMultipart(http.HandlerFunc(a.ideaAttach))))))
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
	if phase == idea.PhaseRaw && ws.Summary == nil && ws.Concept == nil && len(ws.Decisions) == 0 && len(ws.Raw) == 0 {
		return
	}
	now := overviewNow().UTC()
	age := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	w := &workshopView{ID: i.ID, Token: csrfOf(r), Anchor: "workshop", Phase: phase, PhaseLabel: label(l, workshopPhaseKeys, phase), PhaseTone: workshopPhaseTone[phase]}
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
	human := interactive(r)
	canWork := human && pa.CheckIdea(i, store.ActWork) == nil
	canEdit := human && pa.CheckIdea(i, store.ActEdit) == nil
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
				Ist: ellipsize(f.Current.Summary, 280), Rev: l.msg("features.rev", f.Revision), Advanced: f.Advanced}
			af.Soll = ellipsize(c.Concept.What, 280)
			if af.Soll == "" {
				af.Soll = ellipsize(c.Concept.About, 280)
			}
			w.Affects = append(w.Affects, af)
		}
	} else {
		for _, f := range ws.Affects {
			w.Affects = append(w.Affects, workshopAffect{Name: f.Name, Href: repoHref(i.Project) + "/features/" + strconv.FormatInt(f.ID, 10), Num: "#" + strconv.FormatInt(f.ID, 10),
				Ist: ellipsize(f.Current.Summary, 280), Rev: l.msg("features.rev", f.Revision), Advanced: f.Advanced})
		}
	}
	me := browserPrincipal(r).Label
	for _, d := range ws.Decisions {
		wd := workshopDecision{ID: d.ID, Num: d.HumanID(), Question: d.Question, Detail: bodyBlocks(d.Detail, 0), State: d.State, StateLabel: label(l, workshopDecisionKeys, d.State),
			Tone: workshopDecisionTone[d.State], WayLabel: label(l, workshopWayKeys, d.Way), WayWhy: strings.Join(d.WayWhy, "; "), Open: d.State == idea.DecisionOpen,
			Answer: d.Answer, Reason: d.Reason, Trigger: d.Trigger, DecidedBy: d.DecidedBy, DecidedAge: age(d.DecidedAt)}
		if wd.Open {
			w.OpenCount++
		}
		// Schließen darf, wer die Idee bearbeiten darf; abstimmen, wer mitarbeiten darf.
		wd.CanDecide, wd.CanVote, wd.CanReopen = wd.Open && canEdit, wd.Open && canWork && len(d.Variants) > 0, !wd.Open && canWork
		for _, vr := range d.Variants {
			wv := workshopVariant{ID: vr.ID, Label: vr.Label, Title: vr.Title, Images: attachImages(l, vr.Attachments, func(idea.AttachmentMeta) bool { return true }), Text: bodyBlocks(vr.Text, 0), Pro: bodyBlocks(vr.Pro, 0), Contra: bodyBlocks(vr.Contra, 0), Chosen: d.ChosenVariant == vr.Label}
			for _, vt := range d.Votes {
				if vt.Variant == vr.Label {
					wv.Votes = append(wv.Votes, workshopVote{Voter: vt.Voter, Reason: vt.Reason, Tip: vt.Voter + ": " + vt.Reason})
					if vt.Voter == me && !vt.ByAgent {
						wd.MyVote = vr.Label
					}
				}
			}
			if wv.Chosen {
				wd.Chosen = vr.Label + " · " + vr.Title
			}
			wd.Variants = append(wd.Variants, wv)
		}
		for n := range wd.Variants {
			wd.Variants[n].Checked = wd.Variants[n].Label == wd.MyVote
		}
		wd.CanAttach = canWork && len(wd.Variants) > 0 && phase != idea.PhaseDismissed && phase != idea.PhaseDone
		w.Decisions = append(w.Decisions, wd)
	}
	// Die Wege der Phase: Diskussion anstoßen kann, wer mitarbeitet; freigeben und die Freigabe
	// zurücknehmen nur, wer die Idee bearbeiten darf.
	w.CanDiscuss = canWork && (phase == idea.PhaseRaw || phase == idea.PhaseConcept) && ws.Concept != nil
	w.CanApprove = canEdit && ws.Concept != nil && (phase == idea.PhaseConcept || phase == idea.PhaseDiscussion)
	w.CanWithdraw = canEdit && phase == idea.PhaseApproved
	q := r.URL.Query()
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
	v.Workshop = w
}

// workshopDated sind die Einträge, die die Werkstatt zur Zeitleiste der Idee beisteuert:
// wer eine Frage entschieden, vertagt oder fallengelassen hat und wer für welche Variante
// war. Die Fragen selbst und die Fassungen der Texte stehen schon in ideaItems.
func (a *app) workshopDated(l lang, ws store.IdeaWorkshop, age func(string) string) []datedItem {
	var out []datedItem
	sentence := func(key string, args ...any) string { return html.EscapeString(l.msg(key, args...)) }
	for _, d := range ws.Decisions {
		for _, vt := range d.Votes {
			out = append(out, datedItem{vt.At, tlItem{ID: "vote-" + strconv.FormatInt(d.ID, 10) + "-" + url.QueryEscape(vt.Voter), Act: "idea.vote", Who: vt.Voter, Icon: "thumb",
				Head: template.HTML(sentence("workshop.ev.vote", vt.Variant) + " <span class=\"rn\">" + html.EscapeString(d.HumanID()) + "</span>"), Age: age(vt.At), Body: template.HTML(html.EscapeString(vt.Reason))}})
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
					body = append(body, "<b>"+html.EscapeString(vr.Label+" · "+vr.Title)+"</b>")
				}
			}
		}
		for _, s := range []string{d.Answer, d.Reason, d.Trigger} {
			if s != "" {
				body = append(body, html.EscapeString(s))
			}
		}
		out = append(out, datedItem{d.DecidedAt, tlItem{ID: "decided-" + strconv.FormatInt(d.ID, 10), Act: "idea.decided", Who: d.DecidedBy, Icon: icon, Tone: tone,
			Head: template.HTML(sentence(key) + " <span class=\"rn\">" + html.EscapeString(d.HumanID()) + "</span>"), Age: age(d.DecidedAt), Body: template.HTML(strings.Join(body, "<br>"))}})
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
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound):
		http.NotFound(w, r)
	default:
		code := requestdomain.Code(err)
		switch code {
		case "idea_not_found", "decision_not_found":
			http.NotFound(w, r)
		case "":
			http.Error(w, "workshop step failed", http.StatusInternalServerError)
		default:
			http.Redirect(w, r, workshopBack(project, ideaID, "wfail", code), http.StatusSeeOther)
		}
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

// workshopDecision liest die Entscheidung des Schrittes samt ihrer Idee.
func (a *app) workshopDecisionOf(w http.ResponseWriter, r *http.Request) (idea.Decision, idea.Idea, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return idea.Decision{}, idea.Idea{}, false
	}
	d, err := a.access(r).DecisionView(id)
	if err != nil || d.Subject != idea.SubjectIdea {
		http.NotFound(w, r)
		return idea.Decision{}, idea.Idea{}, false
	}
	i, ok := a.workshopIdea(w, r, d.SubjectID)
	return d, i, ok
}

func formLine(r *http.Request, name string, max int) (string, bool) {
	v := r.FormValue(name)
	return v, len(v) <= max
}

// workshopPhase: POST /ui/ideas/{id}/phase. mode ist discuss (Diskussion beginnen), approve (Konzept
// freigeben, mit der gesehenen Fassung) oder withdraw (Freigabe zurücknehmen).
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
	d, i, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	reason, fits := formLine(r, "reason", maxFieldBytes)
	if !fits {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	voter := store.IdeaActorFor(browserPrincipal(r), "")
	if _, err := a.access(r).VoteDecision(d.ID, r.FormValue("variant"), reason, voter); err != nil {
		a.workshopFail(w, r, err, i.Project, i.ID)
		return
	}
	a.stampActor(r, actor.Vote, actor.VoteEntryID(d.ID, store.VoterKey(voter)))
	http.Redirect(w, r, workshopBack(i.Project, i.ID, "wdone", "vote")+"", http.StatusSeeOther)
}

// workshopClose: POST /ui/decisions/{id}/close: der Mensch wählt eine Variante oder gibt eine
// eigene Antwort und sagt warum. Vertagen und Fallenlassen gibt es hier nicht.
func (a *app) workshopClose(w http.ResponseWriter, r *http.Request) {
	d, i, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	reason, fits1 := formLine(r, "reason", maxFieldBytes)
	answer, fits2 := formLine(r, "answer", maxFieldBytes)
	if !fits1 || !fits2 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	_, err := a.access(r).CloseDecision(store.CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Variant: r.FormValue("variant"), Answer: answer, Reason: reason,
		Actor: store.IdeaActorFor(browserPrincipal(r), "")})
	if err != nil {
		a.workshopFail(w, r, err, i.Project, i.ID)
		return
	}
	http.Redirect(w, r, workshopBack(i.Project, i.ID, "wdone", "decided"), http.StatusSeeOther)
}

// workshopReopen: POST /ui/decisions/{id}/reopen.
func (a *app) workshopReopen(w http.ResponseWriter, r *http.Request) {
	d, i, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	reason, fits := formLine(r, "reason", maxFieldBytes)
	if !fits {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if _, err := a.access(r).ReopenDecision(d.ID, reason, store.IdeaActorFor(browserPrincipal(r), "")); err != nil {
		a.workshopFail(w, r, err, i.Project, i.ID)
		return
	}
	http.Redirect(w, r, workshopBack(i.Project, i.ID, "wdone", "reopen"), http.StatusSeeOther)
}

var _ = time.Second
