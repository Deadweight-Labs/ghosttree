package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The forms with which a person works an idea out in the browser (REQ-709): the raw text and the
// summary, the concept section by section, a question with variants (for and against), and one more
// variant for a question that is open. What used to need an agent has the same rules here as there
// (store.PutIdeaText, PutIdeaConcept, OpenDecision, AddVariant); the writer is always
// store.IdeaActorFor(browserPrincipal(r), ""), which counts only a web session as a person.

// workshopPanels are the forms that open from the buttons at the top of the workshop (?panel=…).
const (
	panelRaw      = "raw"
	panelSummary  = "summary"
	panelConcept  = "concept"
	panelDecision = "decision"
)

// workshopVariantSlots is the number of variants the form of a new question offers; more can be added to the open question.
const workshopVariantSlots = 4

// workshopPanelHref is the address of the idea with one form open.
func workshopPanelHref(project string, id int64, panel string) string {
	return repoHref(project) + "/ideas/" + strconv.FormatInt(id, 10) + "?panel=" + panel + "#ws-panel"
}

// workshopFormFailKeys names, for the forms that write a text, the refusals of the rules; what is not named is "did not work".
var workshopFormFailKeys = map[string]string{
	"raw_required": "workshop.fail.raw", "raw_invalid": "workshop.fail.raw", "raw_too_long": "workshop.fail.raw_long",
	"summary_required": "workshop.fail.summary", "section_invalid": "workshop.fail.section", "requirement_invalid": "workshop.fail.requirement",
	"requirements_too_many": "workshop.fail.requirement", "message_too_long": "workshop.fail.note", "message_invalid": "workshop.fail.note",
	"source_too_long": "workshop.fail.source", "source_invalid": "workshop.fail.source",
	"question_required": "workshop.fail.question", "question_too_long": "workshop.fail.question", "question_invalid": "workshop.fail.question",
	"detail_too_long": "workshop.fail.detail", "detail_invalid": "workshop.fail.detail",
	"variant_title_required": "workshop.fail.variant_title", "variant_title_too_long": "workshop.fail.variant_title", "variant_title_invalid": "workshop.fail.variant_title",
	"variant_too_long": "workshop.fail.variant_long", "variant_invalid": "workshop.fail.variant_long",
	"variants_too_few": "workshop.fail.variants_few", "variants_too_many": "workshop.fail.variants_many",
	"concept_changed": "workshop.fail.concept_changed", "text_changed": "workshop.fail.concept_changed", "idea_closed": "workshop.fail.phase",
	"decision_closed": "workshop.fail.closed", "human_only": "workshop.fail.human",
}

// workshopFormFail answers a refused form of this file: no right is 403, an object that is not there 404, a rule of the store is a
// message above the form with everything the person typed still in its fields.
func (a *app) workshopFormFail(w http.ResponseWriter, r *http.Request, err error, back string) {
	if a.secretRefused(w, r, err, back) {
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
			key, ok := workshopFormFailKeys[code]
			if !ok {
				key = "workshop.fail.failed"
			}
			a.failFormTo(w, r, back, http.StatusUnprocessableEntity, a.langFor(r).msg(key), "")
		}
	}
}

// workshopTooLong refuses a field above its limit with the count, the text stays in the field.
func (a *app) workshopTooLong(w http.ResponseWriter, r *http.Request, back string, fields map[string]int) bool {
	for name, limit := range fields {
		if v := r.FormValue(name); tooLong(v, limit) {
			a.tooLongForm(w, r, back, name, v, limit)
			return true
		}
	}
	return false
}

// ideaOfStep reads the idea a form belongs to; one that cannot be read is as unknown as one that is not there.
func (a *app) ideaOfStep(w http.ResponseWriter, r *http.Request) (idea.Idea, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return idea.Idea{}, false
	}
	return a.workshopIdea(w, r, id)
}

// workshopText: POST /ui/ideas/{id}/text. part is raw (a new version of the raw text, with its source) or summary.
func (a *app) workshopText(w http.ResponseWriter, r *http.Request) {
	i, ok := a.ideaOfStep(w, r)
	if !ok {
		return
	}
	part := r.FormValue("part")
	if part != idea.PartRaw && part != idea.PartSummary {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	back := workshopPanelHref(i.Project, i.ID, part)
	if a.workshopTooLong(w, r, back, map[string]int{"source": idea.MaxSourceLabel, "message": idea.MaxVersionNote}) {
		return
	}
	body := lineBreaks(r.FormValue("body"))
	if part == idea.PartSummary {
		body = strings.TrimSpace(body)
	}
	in := store.PutIdeaTextInput{Part: part, Body: body, Source: strings.TrimSpace(r.FormValue("source")), Message: strings.TrimSpace(r.FormValue("message")),
		Actor: store.IdeaActorFor(browserPrincipal(r), "")}
	if _, _, err := a.access(r).PutIdeaText(i.ID, in); err != nil {
		a.workshopFormFail(w, r, err, back)
		return
	}
	http.Redirect(w, r, workshopBack(i.Project, i.ID, "wdone", "text_"+part), http.StatusSeeOther)
}

// conceptSections are the fields of the concept form in the order of the concept.
var conceptSections = []string{"about", "what", "how", "not", "questions"}

// workshopConceptSave: POST /ui/ideas/{id}/concept. Every section the form carries goes as a patch; the version the person saw
// (base) makes the store refuse the write when somebody wrote a newer one in the meantime.
func (a *app) workshopConceptSave(w http.ResponseWriter, r *http.Request) {
	i, ok := a.ideaOfStep(w, r)
	if !ok {
		return
	}
	back := workshopPanelHref(i.Project, i.ID, panelConcept)
	limits := map[string]int{"requirements": idea.MaxRequirements * (idea.MaxRequirement + 1), "message": idea.MaxVersionNote}
	for _, s := range conceptSections {
		limits[s] = idea.MaxSection
	}
	if a.workshopTooLong(w, r, back, limits) {
		return
	}
	base, err := strconv.Atoi(r.FormValue("base"))
	if err != nil || base < 0 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	var patch idea.ConceptPatch
	for _, s := range conceptSections {
		v := lineBreaks(r.FormValue(s))
		switch s {
		case "about":
			patch.About = &v
		case "what":
			patch.What = &v
		case "how":
			patch.How = &v
		case "not":
			patch.Not = &v
		case "questions":
			patch.Questions = &v
		}
	}
	reqs := []string{}
	for _, line := range strings.Split(lineBreaks(r.FormValue("requirements")), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			reqs = append(reqs, line)
		}
	}
	patch.Requirements = &reqs
	_, unchanged, err := a.access(r).PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Patch: patch, Message: strings.TrimSpace(r.FormValue("message")), Base: base,
		Actor: store.IdeaActorFor(browserPrincipal(r), "")})
	if err != nil {
		a.workshopFormFail(w, r, err, back)
		return
	}
	done := "concept_saved"
	if unchanged {
		done = "concept_same"
	}
	http.Redirect(w, r, workshopBack(i.Project, i.ID, "wdone", done), http.StatusSeeOther)
}

// variantsOfForm reads the variants of a form of a new question: slot n has the fields v{n}_title, v{n}_text, v{n}_pro and v{n}_contra.
// A slot without any text is not a variant.
func variantsOfForm(r *http.Request, slots int) []idea.VariantInput {
	var out []idea.VariantInput
	for n := 1; n <= slots; n++ {
		p := "v" + strconv.Itoa(n) + "_"
		v := idea.VariantInput{Title: strings.TrimSpace(r.FormValue(p + "title")), Text: strings.TrimSpace(lineBreaks(r.FormValue(p + "text"))),
			Pro: strings.TrimSpace(lineBreaks(r.FormValue(p + "pro"))), Contra: strings.TrimSpace(lineBreaks(r.FormValue(p + "contra")))}
		if v.Title != "" || v.Text != "" || v.Pro != "" || v.Contra != "" {
			out = append(out, v)
		}
	}
	return out
}

// variantLimits are the limits of the fields of the variant slots, by field name.
func variantLimits(slots int) map[string]int {
	limits := map[string]int{}
	for n := 1; n <= slots; n++ {
		p := "v" + strconv.Itoa(n) + "_"
		limits[p+"title"] = idea.MaxVariantTit
		limits[p+"text"], limits[p+"pro"], limits[p+"contra"] = idea.MaxVariant, idea.MaxVariant, idea.MaxVariant
	}
	return limits
}

// workshopDecisionOpen: POST /ui/ideas/{id}/decisions: a question of the idea, with variants if the person has some. A person who asks
// wants a person to decide, so the way is human (the store would pick it the same way).
func (a *app) workshopDecisionOpen(w http.ResponseWriter, r *http.Request) {
	i, ok := a.ideaOfStep(w, r)
	if !ok {
		return
	}
	back := workshopPanelHref(i.Project, i.ID, panelDecision)
	limits := variantLimits(workshopVariantSlots)
	limits["question"], limits["detail"] = idea.MaxQuestion, idea.MaxDetail
	if a.workshopTooLong(w, r, back, limits) {
		return
	}
	d, err := a.access(r).OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: r.FormValue("question"), Detail: lineBreaks(r.FormValue("detail")),
		Way: idea.WayHuman, Variants: variantsOfForm(r, workshopVariantSlots), Actor: store.IdeaActorFor(browserPrincipal(r), "")})
	if err != nil {
		a.workshopFormFail(w, r, err, back)
		return
	}
	http.Redirect(w, r, repoHref(i.Project)+"/ideas/"+strconv.FormatInt(i.ID, 10)+"?wdone=asked&wdec="+strconv.FormatInt(d.ID, 10)+"#decision-"+strconv.FormatInt(d.ID, 10), http.StatusSeeOther)
}

// workshopVariantAdd: POST /ui/decisions/{id}/variant: one more variant (title, text, for, against) for an open question.
func (a *app) workshopVariantAdd(w http.ResponseWriter, r *http.Request) {
	d, ok := a.workshopDecisionOf(w, r)
	if !ok {
		return
	}
	back := decisionBack(d)
	fields := map[string]int{"title": idea.MaxVariantTit, "text": idea.MaxVariant, "pro": idea.MaxVariant, "contra": idea.MaxVariant}
	if a.workshopTooLong(w, r, back("", ""), fields) {
		return
	}
	v := idea.VariantInput{Title: strings.TrimSpace(r.FormValue("title")), Text: strings.TrimSpace(lineBreaks(r.FormValue("text"))),
		Pro: strings.TrimSpace(lineBreaks(r.FormValue("pro"))), Contra: strings.TrimSpace(lineBreaks(r.FormValue("contra")))}
	if _, err := a.access(r).AddVariant(d.ID, v, store.IdeaActorFor(browserPrincipal(r), "")); err != nil {
		a.workshopFormFail(w, r, err, back("", ""))
		return
	}
	http.Redirect(w, r, back("wdone", "variant"), http.StatusSeeOther)
}

// phaseNeedsAsk says whether a step of the phase asks first, and what it asks about: closing the discussion while a question is
// open, approving a concept that has no requirements. The person confirms with confirm=1.
func (a *app) phaseNeedsAsk(r *http.Request, id int64, mode string) string {
	pa := a.access(r)
	switch mode {
	case "close":
		if ds, err := pa.DecisionsOf(idea.SubjectIdea, id); err == nil {
			for _, d := range ds {
				if d.State == idea.DecisionOpen {
					return "close"
				}
			}
		}
		// The concept's own section of open questions counts as well: a
		// decision does not strike a line out of the free text, so a person
		// who closes the discussion is asked once more (N-I).
		if c, ok, err := pa.IdeaConcept(id, 0); err == nil && ok && strings.TrimSpace(c.Concept.Questions) != "" {
			return "close"
		}
	case "approve":
		if c, ok, err := pa.IdeaConcept(id, 0); err == nil && ok && len(c.Concept.Requirements) == 0 {
			return "approve"
		}
	}
	return ""
}

// phaseAskHref is the idea with the question before the step; rev is the version the person saw when it is an approval.
func phaseAskHref(project string, id int64, ask, rev string) string {
	u := repoHref(project) + "/ideas/" + strconv.FormatInt(id, 10) + "?wask=" + ask
	if rev != "" {
		u += "&wrev=" + url.QueryEscape(rev)
	}
	return u + "#ws-ask"
}
