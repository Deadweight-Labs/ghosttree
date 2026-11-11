package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ideen-Werkstatt (REQ-611): Texte mit Fassungen, Konzept, Phasen, offene
// Entscheidungen, Anhänge, Issues aus der Idee.

// IdeaWorkshop liest die Idee mit allem, was an ihr hängt.
func (c *Client) IdeaWorkshop(id int64) (store.IdeaWorkshop, error) {
	var out store.IdeaWorkshop
	err := c.do("GET", idPathOf("/api/ideas/", id, "/workshop"), nil, nil, &out)
	return out, err
}

// IdeaText liest eine Fassung mit Text (revision 0 = jüngste).
func (c *Client) IdeaText(id int64, part string, revision int) (idea.Text, error) {
	var out idea.Text
	err := c.do("GET", idPathOf("/api/ideas/", id, "/texts"), url.Values{"part": {part}, "revision": {strconv.Itoa(revision)}}, nil, &out)
	return out, err
}

// IdeaVersions listet die Fassungen eines Teils ohne Text.
func (c *Client) IdeaVersions(id int64, part string) ([]idea.Text, error) {
	var out []idea.Text
	err := c.do("GET", idPathOf("/api/ideas/", id, "/versions"), url.Values{"part": {part}}, nil, &out)
	return out, err
}

// PutIdeaTextResult ist die Antwort auf das Anhängen einer Fassung.
type PutIdeaTextResult struct {
	Version   idea.Text `json:"version"`
	Unchanged bool      `json:"unchanged"`
}

// PutIdeaText hängt Rohfassung (part raw) oder Kurzfassung (summary) an.
func (c *Client) PutIdeaText(id int64, part, body, source, message string, base int, sessionID int64) (PutIdeaTextResult, error) {
	var out PutIdeaTextResult
	err := c.do("POST", idPathOf("/api/ideas/", id, "/texts"), nil, map[string]any{
		"part": part, "body": body, "source": source, "message": message, "base": base, "session_id": sessionID}, &out)
	return out, err
}

// IdeaConcept liest das Konzept (revision 0 = jüngste Fassung).
func (c *Client) IdeaConcept(id int64, revision int) (store.ConceptVersion, error) {
	var out store.ConceptVersion
	err := c.do("GET", idPathOf("/api/ideas/", id, "/concept"), url.Values{"revision": {strconv.Itoa(revision)}}, nil, &out)
	return out, err
}

// PutIdeaConceptResult ist die Antwort auf eine Änderung des Konzepts.
type PutIdeaConceptResult struct {
	Version   store.ConceptVersion `json:"version"`
	Unchanged bool                 `json:"unchanged"`
}

// PutIdeaConcept ändert Abschnitte des Konzepts; nil lässt einen stehen.
func (c *Client) PutIdeaConcept(id int64, patch idea.ConceptPatch, message string, base int, sessionID int64) (PutIdeaConceptResult, error) {
	body := map[string]any{"message": message, "base": base, "session_id": sessionID}
	for k, v := range map[string]*string{"about": patch.About, "what": patch.What, "how": patch.How, "not": patch.Not, "questions": patch.Questions} {
		if v != nil {
			body[k] = *v
		}
	}
	if patch.Requirements != nil {
		body["requirements"] = *patch.Requirements
	}
	var out PutIdeaConceptResult
	err := c.do("POST", idPathOf("/api/ideas/", id, "/concept"), nil, body, &out)
	return out, err
}

// SetIdeaPhase wechselt die Phase (approved nur durch Owner oder Maintainer).
func (c *Client) SetIdeaPhase(id int64, phase string, conceptRevision int) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", idPathOf("/api/ideas/", id, "/phase"), nil, map[string]any{"phase": phase, "concept_revision": conceptRevision}, &out)
	return out, err
}

// StartIdeaDiscussion vermerkt, dass an der Idee diskutiert wird.
func (c *Client) StartIdeaDiscussion(id int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", idPathOf("/api/ideas/", id, "/discussion"), nil, nil, &out)
	return out, err
}

// CreateIssuesFromIdea macht aus einer freigegebenen Idee Issues, optional in
// einem Arbeitspaket für den Branch.
func (c *Client) CreateIssuesFromIdea(id int64, issues []store.IssueSpec, branch, packageName string, sessionID int64) (store.IdeaIssuesResult, error) {
	var out store.IdeaIssuesResult
	err := c.do("POST", idPathOf("/api/ideas/", id, "/issues"), nil, map[string]any{
		"issues": issues, "branch": branch, "package_name": packageName, "session_id": sessionID}, &out)
	return out, err
}

// RequestBriefing ist der Idee-Abschnitt des Briefings für ein Issue.
func (c *Client) RequestBriefing(requestID int64) (string, error) {
	var out struct {
		Briefing string `json:"briefing"`
	}
	err := c.do("GET", idPathOf("/api/requests/", requestID, "/briefing"), nil, nil, &out)
	return out.Briefing, err
}

// OpenDecisionRequest stellt eine Frage. Genau eines von Subject+ID und ViaRequest.
type OpenDecisionRequest struct {
	Subject   string              `json:"subject,omitempty"`
	ID        int64               `json:"id,omitempty"`
	ViaReq    int64               `json:"via_request,omitempty"`
	Question  string              `json:"question"`
	Detail    string              `json:"detail,omitempty"`
	Way       string              `json:"way,omitempty"`
	Aspects   []string            `json:"aspects,omitempty"`
	Variants  []idea.VariantInput `json:"variants,omitempty"`
	SessionID int64               `json:"session_id,omitempty"`
}

// OpenDecision legt eine offene Entscheidung an.
func (c *Client) OpenDecision(in OpenDecisionRequest) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", "/api/decisions", nil, in, &out)
	return out, err
}

// GetDecision liest eine Entscheidung mit Varianten, Stimmen und Anhängen.
func (c *Client) GetDecision(id int64) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("GET", idPathOf("/api/decisions/", id, ""), nil, nil, &out)
	return out, err
}

// ListDecisions liefert die Entscheidungen eines Projekts (state "" = offene,
// "all" alle; way filtert; needsHuman ist der Posteingang).
func (c *Client) ListDecisions(project, state, way string, needsHuman bool, limit int) ([]idea.Decision, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	if way != "" {
		q.Set("way", way)
	}
	if needsHuman {
		q.Set("needs_human", "1")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []idea.Decision
	err := c.do("GET", "/api/decisions", q, nil, &out)
	return out, err
}

// DecisionsOf listet die Entscheidungen einer Idee oder eines Issues.
func (c *Client) DecisionsOf(subject string, id int64) ([]idea.Decision, error) {
	var out []idea.Decision
	err := c.do("GET", "/api/decisions", url.Values{"subject": {subject}, "id": {strconv.FormatInt(id, 10)}}, nil, &out)
	return out, err
}

// AddVariant fügt einer offenen Entscheidung eine Variante hinzu.
func (c *Client) AddVariant(decisionID int64, v idea.VariantInput, sessionID int64) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", idPathOf("/api/decisions/", decisionID, "/variants"), nil, map[string]any{
		"title": v.Title, "text": v.Text, "pro": v.Pro, "contra": v.Contra, "session_id": sessionID}, &out)
	return out, err
}

// VoteDecision hält fest, welche Variante jemand nehmen würde und warum.
func (c *Client) VoteDecision(decisionID int64, variant, reason string, sessionID int64) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", idPathOf("/api/decisions/", decisionID, "/votes"), nil, map[string]any{"variant": variant, "reason": reason, "session_id": sessionID}, &out)
	return out, err
}

// CloseDecisionRequest schließt eine Entscheidung (decided, deferred, dropped).
type CloseDecisionRequest struct {
	Outcome   string `json:"outcome"`
	Variant   string `json:"variant,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
	SessionID int64  `json:"session_id,omitempty"`
}

// CloseDecision schließt eine Entscheidung.
func (c *Client) CloseDecision(decisionID int64, in CloseDecisionRequest) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", idPathOf("/api/decisions/", decisionID, "/close"), nil, in, &out)
	return out, err
}

// ReopenDecision macht eine geschlossene Entscheidung wieder offen.
func (c *Client) ReopenDecision(decisionID int64, reason string, sessionID int64) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", idPathOf("/api/decisions/", decisionID, "/reopen"), nil, map[string]any{"reason": reason, "session_id": sessionID}, &out)
	return out, err
}

// SetDecisionWay stellt um, wer entscheidet (human oder agent).
func (c *Client) SetDecisionWay(decisionID int64, way string, sessionID int64) (idea.Decision, error) {
	var out idea.Decision
	err := c.do("POST", idPathOf("/api/decisions/", decisionID, "/way"), nil, map[string]any{"way": way, "session_id": sessionID}, &out)
	return out, err
}

// DecisionPolicy liest die Policy des Projekts (rule oder human).
func (c *Client) DecisionPolicy(project string) (string, error) {
	var out struct {
		Mode string `json:"mode"`
	}
	err := c.do("GET", "/api/decision-policy", url.Values{"project": {project}}, nil, &out)
	return out.Mode, err
}

// SetDecisionPolicy stellt die Policy des Projekts um (Maintainer).
func (c *Client) SetDecisionPolicy(project, mode string) error {
	return c.do("POST", "/api/decision-policy", nil, map[string]any{"project": project, "mode": mode}, nil)
}

// AttachRequest hängt ein Bild an. Subject ist idea oder request, Owner idea,
// variant (mit DecisionID und Variant oder VariantID) oder reply (mit ThreadID).
type AttachRequest struct {
	Subject     string `json:"subject"`
	ID          int64  `json:"id"`
	Owner       string `json:"owner"`
	DecisionID  int64  `json:"decision_id,omitempty"`
	Variant     string `json:"variant,omitempty"`
	VariantID   int64  `json:"variant_id,omitempty"`
	ThreadID    int64  `json:"thread_id,omitempty"`
	Data        []byte `json:"data"`
	ContentType string `json:"content_type,omitempty"`
	Alt         string `json:"alt,omitempty"`
	SessionID   int64  `json:"session_id,omitempty"`
}

// Attach hängt ein geprüftes Bild an (PNG, JPEG, WebP, höchstens 5 MB).
func (c *Client) Attach(in AttachRequest) (idea.AttachmentMeta, error) {
	var out idea.AttachmentMeta
	err := c.do("POST", "/api/attachments", nil, in, &out)
	return out, err
}

// RemoveAttachment löscht einen Anhang.
func (c *Client) RemoveAttachment(id int64) error {
	return c.do("DELETE", idPathOf("/api/attachments/", id, ""), nil, nil, nil)
}

// IdeaCoverage zeigt je Anforderung des Konzepts, ob ein erfülltes Kriterium sie
// deckt oder sie erlassen ist.
func (c *Client) IdeaCoverage(id int64) ([]idea.RequirementCoverage, error) {
	var out []idea.RequirementCoverage
	err := c.do("GET", idPathOf("/api/ideas/", id, "/coverage"), nil, nil, &out)
	return out, err
}

// WaiveIdeaRequirement erlässt eine Anforderung mit Grund. Das ist ein
// Menschen-Schritt der Weboberfläche; über ein Token antwortet der Server 403.
func (c *Client) WaiveIdeaRequirement(id int64, requirement int, reason string) ([]idea.RequirementCoverage, error) {
	var out []idea.RequirementCoverage
	err := c.do("POST", idPathOf("/api/ideas/", id, "/requirements/waive"), nil, map[string]any{"requirement": requirement, "reason": reason}, &out)
	return out, err
}
