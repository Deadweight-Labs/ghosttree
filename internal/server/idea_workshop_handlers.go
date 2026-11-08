package server

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ideen-Werkstatt (REQ-611): Texte mit Fassungen, Konzept, Phasen, offene
// Entscheidungen mit Varianten und Stimmen, Bild-Anhänge, Issues aus der Idee,
// Briefing. Jede Route läuft durch ProjectAccess: wer die Idee oder das Issue
// nicht lesen darf, bekommt dieselbe Antwort wie bei einer Nummer, die es nicht
// gibt. Ob ein Agent schreibt, entscheidet die Session der Anfrage (callerSession).
var ideaWorkshopRoutes = map[string]routeClass{
	"GET /api/ideas/{id}/workshop":            classProject,
	"GET /api/ideas/{id}/versions":            classProject,
	"GET /api/ideas/{id}/texts":               classProject,
	"POST /api/ideas/{id}/texts":              classProject,
	"GET /api/ideas/{id}/concept":             classProject,
	"POST /api/ideas/{id}/concept":            classProject,
	"POST /api/ideas/{id}/phase":              classProject,
	"POST /api/ideas/{id}/discussion":         classProject,
	"POST /api/ideas/{id}/issues":             classProject,
	"POST /api/ideas/{id}/requirements/waive": classProject,
	"GET /api/ideas/{id}/coverage":            classProject,
	"GET /api/requests/{id}/briefing":         classProject,
	"POST /api/decisions":                     classProject,
	"GET /api/decisions":                      classProject,
	"GET /api/decisions/{id}":                 classProject,
	"POST /api/decisions/{id}/variants":       classProject,
	"POST /api/decisions/{id}/votes":          classProject,
	"POST /api/decisions/{id}/close":          classProject,
	"POST /api/decisions/{id}/reopen":         classProject,
	"POST /api/decisions/{id}/way":            classProject,
	"GET /api/decision-policy":                classProject,
	"POST /api/decision-policy":               classProject,
	"POST /api/attachments":                   classProject,
	"GET /api/attachments/{id}":               classProject,
	"DELETE /api/attachments/{id}":            classProject,
}

func init() {
	for pattern, class := range ideaWorkshopRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerIdeaWorkshopRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/ideas/{id}/workshop", a.ideaWorkshop)
	a.routeFunc(mux, "GET /api/ideas/{id}/versions", a.ideaVersions)
	a.routeFunc(mux, "GET /api/ideas/{id}/texts", a.getIdeaText)
	a.routeFunc(mux, "POST /api/ideas/{id}/texts", a.putIdeaText)
	a.routeFunc(mux, "GET /api/ideas/{id}/concept", a.getIdeaConcept)
	a.routeFunc(mux, "POST /api/ideas/{id}/concept", a.putIdeaConcept)
	a.routeFunc(mux, "POST /api/ideas/{id}/phase", a.setIdeaPhase)
	a.routeFunc(mux, "POST /api/ideas/{id}/discussion", a.startIdeaDiscussion)
	a.routeFunc(mux, "POST /api/ideas/{id}/issues", a.createIssuesFromIdea)
	a.routeFunc(mux, "POST /api/ideas/{id}/requirements/waive", a.waiveRequirement)
	a.routeFunc(mux, "GET /api/ideas/{id}/coverage", a.ideaCoverage)
	a.routeFunc(mux, "GET /api/requests/{id}/briefing", a.requestBriefing)
	a.routeFunc(mux, "POST /api/decisions", a.openDecision)
	a.routeFunc(mux, "GET /api/decisions", a.listDecisions)
	a.routeFunc(mux, "GET /api/decisions/{id}", a.getDecision)
	a.routeFunc(mux, "POST /api/decisions/{id}/variants", a.addVariant)
	a.routeFunc(mux, "POST /api/decisions/{id}/votes", a.voteDecision)
	a.routeFunc(mux, "POST /api/decisions/{id}/close", a.closeDecision)
	a.routeFunc(mux, "POST /api/decisions/{id}/reopen", a.reopenDecision)
	a.routeFunc(mux, "POST /api/decisions/{id}/way", a.setDecisionWay)
	a.routeFunc(mux, "GET /api/decision-policy", a.getDecisionPolicy)
	a.routeFunc(mux, "POST /api/decision-policy", a.setDecisionPolicy)
	a.routeFunc(mux, "POST /api/attachments", a.attach)
	a.routeFunc(mux, "GET /api/attachments/{id}", a.getAttachment)
	a.routeFunc(mux, "DELETE /api/attachments/{id}", a.removeAttachment)
}

// Größen der Körper: die Rohfassung darf bis 400 KB Text haben, sechsfach
// aufgebläht durch JSON-Escapes; ein Bild bis 5 MB, als Base64 etwa 7 MB.
const (
	maxWorkshopBody   = 3 << 20
	maxWorkshopSmall  = 256 << 10
	maxAttachmentBody = 8 << 20
)

// workshopActor ist der Schreiber. Mensch ist nur, wer aus einer interaktiven
// Websitzung handelt (TokenKind web); jeder Aufruf mit einem Bearer- oder
// Gerätetoken (API, MCP, CLI) gilt als Agent, ob eine session_id mitkommt oder
// nicht. Die Session-ID dient nur der Herkunft. Die Weboberfläche baut ihren
// Schreiber mit store.IdeaActorFor und ruft ProjectAccess direkt.
func (a *api) workshopActor(r *http.Request, sessionID int64) idea.Actor {
	act := a.ideaActor(r, sessionID)
	act.Agent = principalOf(r).TokenKind != store.WebSessionKind
	return act
}

func (a *api) ideaIDFrom(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad idea id")
	}
	return id, ok
}

func (a *api) ideaWorkshop(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	ws, err := a.access(r).IdeaWorkshop(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (a *api) ideaVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	list, err := a.access(r).IdeaTextVersions(id, r.URL.Query().Get("part"))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) getIdeaText(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	t, err := a.access(r).IdeaText(id, r.URL.Query().Get("part"), intParam(r, "revision", 0))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) putIdeaText(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Part      string `json:"part"`
		Body      string `json:"body"`
		Source    string `json:"source"`
		Message   string `json:"message"`
		Base      int    `json:"base"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopBody) {
		return
	}
	t, unchanged, err := a.access(r).PutIdeaText(id, store.PutIdeaTextInput{Part: body.Part, Body: body.Body, Source: body.Source,
		Message: body.Message, Base: body.Base, Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	status := http.StatusCreated
	if unchanged {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"version": t, "unchanged": unchanged})
}

func (a *api) getIdeaConcept(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	v, found, err := a.access(r).IdeaConcept(id, intParam(r, "revision", 0))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	if !found {
		writeCoded(w, http.StatusNotFound, "concept_not_found", "this idea has no concept yet")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *api) putIdeaConcept(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		idea.ConceptPatch
		Message   string `json:"message"`
		Base      int    `json:"base"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopBody) {
		return
	}
	v, unchanged, err := a.access(r).PutIdeaConcept(id, store.PutIdeaConceptInput{Patch: body.ConceptPatch, Message: body.Message, Base: body.Base,
		Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "unchanged": unchanged})
}

func (a *api) setIdeaPhase(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Phase string `json:"phase"`
		// ConceptRevision ist die Fassung, die der Mensch beim Freigeben gesehen hat.
		ConceptRevision int `json:"concept_revision"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	i, err := a.access(r).SetIdeaPhase(id, body.Phase, a.workshopActor(r, 0), body.ConceptRevision)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (a *api) startIdeaDiscussion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	i, err := a.access(r).StartIdeaDiscussion(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (a *api) createIssuesFromIdea(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Issues      []store.IssueSpec `json:"issues"`
		Branch      string            `json:"branch"`
		PackageName string            `json:"package_name"`
		SessionID   int64             `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	res, err := a.access(r).CreateIssuesFromIdea(store.IdeaIssuesInput{Idea: id, Issues: body.Issues, Branch: body.Branch, PackageName: body.PackageName,
		Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// ideaBriefingChars begrenzt den Abschnitt: er steht im Kontext eines Agenten.
const ideaBriefingChars = 2400

func (a *api) requestBriefing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActRead) {
		return
	}
	text, err := a.access(r).IdeaBriefingForRequest(id, intParam(r, "budget", ideaBriefingChars))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"briefing": text})
}

// ---- Entscheidungen

func (a *api) openDecision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject   string              `json:"subject"`
		ID        int64               `json:"id"`
		ViaReq    int64               `json:"via_request"`
		Question  string              `json:"question"`
		Detail    string              `json:"detail"`
		Way       string              `json:"way"`
		Aspects   []string            `json:"aspects"`
		Variants  []idea.VariantInput `json:"variants"`
		SessionID int64               `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	pa := a.access(r)
	subject, id := body.Subject, body.ID
	if body.ViaReq != 0 {
		// Eine Rückfrage aus der Umsetzung landet an der Idee, aus der das Issue
		// entstand; hat es keine, am Issue selbst.
		if i, found := pa.IdeaOfRequest(body.ViaReq); found {
			subject, id = idea.SubjectIdea, i.ID
		} else {
			subject, id = idea.SubjectRequest, body.ViaReq
		}
	}
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: subject, SubjectID: id, Question: body.Question, Detail: body.Detail, Way: body.Way,
		Aspects: body.Aspects, Variants: body.Variants, Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (a *api) listDecisions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pa := a.access(r)
	if subject := q.Get("subject"); subject != "" {
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil {
			writeCoded(w, http.StatusBadRequest, "id_required", "name the number of the idea or request")
			return
		}
		list, err := pa.DecisionsOf(subject, id)
		if err != nil {
			writeIdeaError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nonNil(list))
		return
	}
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGateEntries(w, r, project, store.ResIdea) {
		return
	}
	list, err := pa.SearchDecisions(store.DecisionFilter{Project: project, State: q.Get("state"), Way: q.Get("way"),
		NeedsHuman: q.Get("needs_human") == "1", Limit: intParam(r, "limit", 50)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func decisionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad decision id")
	}
	return id, ok
}

func (a *api) getDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	d, err := a.access(r).DecisionView(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) addVariant(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	var body struct {
		idea.VariantInput
		SessionID int64 `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	d, err := a.access(r).AddVariant(id, body.VariantInput, a.workshopActor(r, body.SessionID))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) voteDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Variant   string `json:"variant"`
		Reason    string `json:"reason"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	d, err := a.access(r).VoteDecision(id, body.Variant, body.Reason, a.workshopActor(r, body.SessionID))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) closeDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Outcome   string `json:"outcome"`
		Variant   string `json:"variant"`
		Answer    string `json:"answer"`
		Reason    string `json:"reason"`
		Trigger   string `json:"trigger"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	d, err := a.access(r).CloseDecision(store.CloseDecisionInput{ID: id, Outcome: body.Outcome, Variant: body.Variant, Answer: body.Answer,
		Reason: body.Reason, Trigger: body.Trigger, Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) reopenDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason    string `json:"reason"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	d, err := a.access(r).ReopenDecision(id, body.Reason, a.workshopActor(r, body.SessionID))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) setDecisionWay(w http.ResponseWriter, r *http.Request) {
	id, ok := decisionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Way       string `json:"way"`
		SessionID int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	d, err := a.access(r).SetDecisionWay(id, body.Way, a.workshopActor(r, body.SessionID))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) getDecisionPolicy(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	mode, err := a.access(r).DecisionPolicy(project)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"project": project, "mode": mode})
}

func (a *api) setDecisionPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Mode    string `json:"mode"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok {
		return
	}
	if err := a.access(r).SetDecisionPolicy(project, body.Mode, personOf(r), a.workshopActor(r, 0)); err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"project": project, "mode": body.Mode})
}

// ---- Anhänge

func (a *api) attach(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject     string `json:"subject"`
		ID          int64  `json:"id"`
		Owner       string `json:"owner"`
		VariantID   int64  `json:"variant_id"`
		DecisionID  int64  `json:"decision_id"`
		Variant     string `json:"variant"`
		ThreadID    int64  `json:"thread_id"`
		Data        []byte `json:"data"`
		ContentType string `json:"content_type"`
		Alt         string `json:"alt"`
		SessionID   int64  `json:"session_id"`
	}
	if !readLimitedJSON(w, r, &body, maxAttachmentBody) {
		return
	}
	pa := a.access(r)
	variantID := body.VariantID
	if body.Owner == idea.OwnerVariant && variantID == 0 && body.DecisionID != 0 {
		// Die Variante über Entscheidung und Buchstaben; der Betrachter muss die
		// Entscheidung lesen dürfen, sonst antwortet die Auflösung wie bei einer
		// unbekannten.
		if d, err := pa.DecisionView(body.DecisionID); err == nil {
			for _, v := range d.Variants {
				if strings.EqualFold(v.Label, body.Variant) {
					variantID = v.ID
				}
			}
		}
	}
	meta, err := pa.Attach(store.AttachInput{Subject: body.Subject, SubjectID: body.ID, Owner: body.Owner, VariantID: variantID, ThreadID: body.ThreadID,
		Data: body.Data, DeclaredType: body.ContentType, Alt: body.Alt, Actor: a.workshopActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

// getAttachment liefert die Bytes mit dem Typ aus den ersten Bytes. Die Antwort
// ist ein Bild und nie etwas anderes: nosniff, kein Skript, kein Rahmen, ein
// fester Dateiname nach der Nummer (nie der des Absenders), nur für Berechtigte.
func (a *api) getAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad attachment id")
		return
	}
	meta, data, err := a.access(r).AttachmentData(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[meta.Mime]
	if ext == "" {
		writeCoded(w, http.StatusInternalServerError, "attachment_invalid", "the stored attachment has an unknown type")
		return
	}
	h := w.Header()
	h.Set("Content-Type", meta.Mime)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="attachment-%d.%s"`, meta.ID, ext))
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("ETag", `"`+meta.SHA256+`"`)
	if r.Header.Get("If-None-Match") == h.Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *api) removeAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad attachment id")
		return
	}
	if err := a.access(r).RemoveAttachment(id); err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ---- Briefing beim Sessionstart: vertagte Entscheidungen, die jetzt passen

// writeDueDecisions hängt an das Briefing des Branches die vertagten
// Entscheidungen, deren Auslöser zu Branch, Paketname oder den Titeln der
// Requests des Pakets passt. Der Text läuft durch html.EscapeString wie der Rest
// des Briefings.
func (a *api) writeDueDecisions(w http.ResponseWriter, pa *store.ProjectAccess, project, branch, packageName string, titles []string) {
	due, err := pa.DueDecisions(project, append([]string{branch, packageName}, titles...)...)
	if err != nil || len(due) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n## Deferred decisions now due (ghosttree)\n\n")
	for n, d := range due {
		if n >= 5 {
			fmt.Fprintf(&b, "- and %d more (decision_list)\n", len(due)-n)
			break
		}
		fmt.Fprintf(&b, "- %s %s (deferred until: %s)\n", d.HumanID(), idea.Brief(d.Question, 160), idea.Brief(d.Trigger, 120))
	}
	b.WriteString("Decide them, defer them again or drop them with decision_close; a person decides what needs a person.\n")
	fmt.Fprint(w, html.EscapeString(b.String()))
}

// noteIdeaDiscussion vermerkt an der Idee, dass an ihr diskutiert wird, wenn ein
// Thread mit ihr verknüpft wird (kind idea, id IDEA-n oder n). Wer an der Idee
// nicht mitarbeiten darf, ändert nichts: der Fehler bleibt unbeachtet, denn der
// Thread selbst ist längst angelegt und die Phase nur ein Hinweis.
func (a *api) noteIdeaDiscussion(r *http.Request, link *store.ThreadLink) {
	if link == nil || link.Kind != "idea" {
		return
	}
	if n, ok := idea.ParseRef(link.ID); ok {
		_, _ = a.access(r).StartIdeaDiscussion(n)
	}
}

func (a *api) ideaCoverage(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	cov, err := a.access(r).IdeaCoverage(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cov)
}

// waiveRequirement erlässt eine Anforderung der Idee mit Grund. Das ist ein
// Menschen-Schritt: ein Token wird abgelehnt (siehe workshopActor).
func (a *api) waiveRequirement(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Requirement int    `json:"requirement"`
		Reason      string `json:"reason"`
	}
	if !readLimitedJSON(w, r, &body, maxWorkshopSmall) {
		return
	}
	cov, err := a.access(r).WaiveIdeaRequirement(id, body.Requirement, body.Reason, a.workshopActor(r, 0))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cov)
}
