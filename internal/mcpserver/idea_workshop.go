package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Ideen-Werkstatt (REQ-611) als Werkzeuge. idea_record bleibt eine Zeile ohne
// Pflichtfelder; wer den Inhalt schon hat (ein Chat, ein Transkript), hängt ihn
// selbst an, ohne zu fragen. Entscheidungen, Varianten, Stimmen und Bilder sind
// eigene Werkzeuge, damit eine Rückfrage aus der Umsetzung nicht im Chat endet.

// IdeaConceptFields sind die Abschnitte des Konzepts. Alle sind optional.
type IdeaConceptFields struct {
	About        string   `json:"about,omitempty" jsonschema:"Worum es geht: the problem and the wish, two or three sentences"`
	What         string   `json:"what,omitempty" jsonschema:"Was es können soll: what the finished thing does"`
	How          string   `json:"how,omitempty" jsonschema:"Wie wir es uns vorstellen: the approach in a few lines"`
	Not          string   `json:"not,omitempty" jsonschema:"Was ausdrücklich nicht: what is out of scope"`
	Questions    string   `json:"questions,omitempty" jsonschema:"Offene Fragen as free text. What has to be decided is an open decision (idea action=decision_open), not this text"`
	Requirements []string `json:"requirements,omitempty" jsonschema:"Anforderungen: drafts of acceptance criteria, one observable outcome per line. They become the criteria of the issues"`
}

func (f IdeaConceptFields) patch() (idea.ConceptPatch, bool) {
	var p idea.ConceptPatch
	any := false
	set := func(dst **string, v string) {
		if v != "" {
			s := v
			*dst = &s
			any = true
		}
	}
	set(&p.About, f.About)
	set(&p.What, f.What)
	set(&p.How, f.How)
	set(&p.Not, f.Not)
	set(&p.Questions, f.Questions)
	if f.Requirements != nil {
		r := f.Requirements
		p.Requirements = &r
		any = true
	}
	return p, any
}

// attachDraft hängt Rohfassung, Kurzfassung und Konzept-Entwurf an eine Idee, so
// weit sie angegeben sind. Scheitert ein Schritt, steht die Idee schon da; der
// Fehler nennt sie.
func (s *Server) attachDraft(i idea.Idea, raw, rawSource, summary string, concept *IdeaConceptFields) error {
	session := s.currentSessionID()
	if strings.TrimSpace(raw) != "" {
		if _, err := s.client.PutIdeaText(i.ID, idea.PartRaw, raw, rawSource, "", 0, session); err != nil {
			return fmt.Errorf("%s was recorded, the raw version was not attached: %w", i.HumanID(), err)
		}
	}
	if strings.TrimSpace(summary) != "" {
		if _, err := s.client.PutIdeaText(i.ID, idea.PartSummary, summary, "", "", 0, session); err != nil {
			return fmt.Errorf("%s was recorded, the summary was not attached: %w", i.HumanID(), err)
		}
	}
	if concept != nil {
		if p, ok := concept.patch(); ok {
			if _, err := s.client.PutIdeaConcept(i.ID, p, "draft", 0, session); err != nil {
				return fmt.Errorf("%s was recorded, the concept draft was not attached: %w", i.HumanID(), err)
			}
		}
	}
	return nil
}

// ---- idea_text, idea_concept

type IdeaTextInput struct {
	ID       int64  `json:"id" jsonschema:"the idea number, IDEA-n"`
	Part     string `json:"part" jsonschema:"raw (the original wording, kept unchanged) or summary (a short version an agent writes)"`
	Body     string `json:"body,omitempty" jsonschema:"the text. With it a new version is added, without it the version is read"`
	Source   string `json:"source,omitempty" jsonschema:"with raw: where it comes from, one line (voice message, meeting 2026-11-08)"`
	Message  string `json:"message,omitempty" jsonschema:"what changed in this version, one line"`
	Base     int    `json:"base,omitempty" jsonschema:"the version you built on; a newer one makes the call fail instead of overwriting"`
	Revision int    `json:"revision,omitempty" jsonschema:"when reading: the version, default the newest"`
}

type IdeaTextOutput struct {
	Ref       string `json:"ref"`
	Part      string `json:"part"`
	Revision  int    `json:"revision"`
	Source    string `json:"source,omitempty"`
	Person    string `json:"person,omitempty"`
	ByAgent   bool   `json:"by_agent,omitempty"`
	At        string `json:"at,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Body      string `json:"body,omitempty"`
}

func (s *Server) handleIdeaText(_ context.Context, _ *mcp.CallToolRequest, in IdeaTextInput) (*mcp.CallToolResult, IdeaTextOutput, error) {
	ref := fmt.Sprintf("IDEA-%d", in.ID)
	if in.Body == "" {
		t, err := s.client.IdeaText(in.ID, in.Part, in.Revision)
		if err != nil {
			return nil, IdeaTextOutput{}, err
		}
		out := IdeaTextOutput{Ref: ref, Part: t.Part, Revision: t.Revision, Source: t.Source, Person: t.Person, ByAgent: t.ByAgent, At: t.CreatedAt, Body: t.Body}
		return requestResult(out), out, nil
	}
	r, err := s.client.PutIdeaText(in.ID, in.Part, in.Body, in.Source, in.Message, in.Base, s.currentSessionID())
	if err != nil {
		return nil, IdeaTextOutput{}, err
	}
	t := r.Version
	out := IdeaTextOutput{Ref: ref, Part: t.Part, Revision: t.Revision, Source: t.Source, Person: t.Person, ByAgent: t.ByAgent, At: t.CreatedAt, Unchanged: r.Unchanged}
	return requestResult(out), out, nil
}

type IdeaConceptInput struct {
	ID int64 `json:"id" jsonschema:"the idea number, IDEA-n"`
	IdeaConceptFields
	Message  string `json:"message,omitempty" jsonschema:"what changed in this version, one line"`
	Base     int    `json:"base,omitempty" jsonschema:"the version you built on; a newer one makes the call fail instead of overwriting"`
	Revision int    `json:"revision,omitempty" jsonschema:"when reading (no section given): the version, default the newest"`
}

type IdeaConceptOutput struct {
	Ref       string       `json:"ref"`
	Revision  int          `json:"revision"`
	Person    string       `json:"person,omitempty"`
	ByAgent   bool         `json:"by_agent,omitempty"`
	Unchanged bool         `json:"unchanged,omitempty"`
	Concept   idea.Concept `json:"concept"`
}

func (s *Server) handleIdeaConcept(_ context.Context, _ *mcp.CallToolRequest, in IdeaConceptInput) (*mcp.CallToolResult, IdeaConceptOutput, error) {
	ref := fmt.Sprintf("IDEA-%d", in.ID)
	patch, write := in.IdeaConceptFields.patch()
	if !write {
		v, err := s.client.IdeaConcept(in.ID, in.Revision)
		if err != nil {
			return nil, IdeaConceptOutput{}, err
		}
		out := IdeaConceptOutput{Ref: ref, Revision: v.Revision, Person: v.Person, ByAgent: v.ByAgent, Concept: v.Concept}
		return requestResult(out), out, nil
	}
	r, err := s.client.PutIdeaConcept(in.ID, patch, in.Message, in.Base, s.currentSessionID())
	if err != nil {
		return nil, IdeaConceptOutput{}, err
	}
	out := IdeaConceptOutput{Ref: ref, Revision: r.Version.Revision, Person: r.Version.Person, ByAgent: r.Version.ByAgent, Unchanged: r.Unchanged, Concept: r.Version.Concept}
	return requestResult(out), out, nil
}

// ---- Gesamtansicht einer Idee

// IdeaWorkshopOutput ist die Idee mit dem, was an ihr hängt, kurz.
type IdeaWorkshopOutput struct {
	Phase        string             `json:"phase"`
	Summary      string             `json:"summary,omitempty"`
	Concept      *idea.Concept      `json:"concept,omitempty"`
	ConceptRev   int                `json:"concept_revision,omitempty"`
	Raw          []string           `json:"raw_versions,omitempty"`
	Requirements []string           `json:"requirements_coverage,omitempty"`
	Decisions    []DecisionOutput   `json:"decisions,omitempty"`
	Attachments  []AttachmentOutput `json:"attachments,omitempty"`
	Discussions  []string           `json:"discussions,omitempty"`
	// Affects sind die Features, die die Idee ändert, mit dem Ist-Stand neben dem
	// Konzept (Soll) (REQ-612).
	Affects []IdeaAffectsOutput `json:"affects,omitempty"`
}

// IdeaAffectsOutput ist ein Feature, das eine Idee ändert, mit seinem Ist-Stand.
type IdeaAffectsOutput struct {
	Feature  string `json:"feature"`
	Name     string `json:"name"`
	Revision int    `json:"version"`
	// Advanced: die Beschreibung des Features wurde für diese Idee fortgeschrieben.
	Advanced bool   `json:"advanced"`
	Summary  string `json:"summary,omitempty"`
	Behavior string `json:"behavior_now,omitempty"`
	Limits   string `json:"limits,omitempty"`
}

type AttachmentOutput struct {
	Ref   string `json:"ref"`
	Owner string `json:"owner"`
	Alt   string `json:"alt,omitempty"`
	Mime  string `json:"mime"`
	URL   string `json:"url"`
}

func (s *Server) workshopOutput(i idea.Idea) (IdeaWorkshopOutput, error) {
	ws, err := s.client.IdeaWorkshop(i.ID)
	if err != nil {
		return IdeaWorkshopOutput{}, err
	}
	out := IdeaWorkshopOutput{Phase: ws.Idea.Phase}
	if ws.Summary != nil {
		if t, err := s.client.IdeaText(i.ID, idea.PartSummary, 0); err == nil {
			out.Summary = t.Body
		}
	}
	if ws.Concept != nil {
		c := ws.Concept.Concept
		out.Concept, out.ConceptRev = &c, ws.Concept.Revision
	}
	for _, r := range ws.Raw {
		out.Raw = append(out.Raw, fmt.Sprintf("r%d %s, %d bytes, %s%s", r.Revision, orDash(r.Source), r.Bytes, r.Person, agentMark(r.ByAgent)))
	}
	for _, c := range ws.Coverage {
		line := fmt.Sprintf("%d [%s] %s", c.No, c.State, c.Text)
		if c.Reason != "" {
			line += " (waived: " + c.Reason + ")"
		}
		for _, cc := range c.Criteria {
			line += fmt.Sprintf(" AC-%d.%d:%s", cc.RequestID, cc.Number, cc.State)
		}
		out.Requirements = append(out.Requirements, line)
	}
	for _, a := range ws.Affects {
		out.Affects = append(out.Affects, IdeaAffectsOutput{Feature: fmt.Sprintf("FEAT-%d", a.ID), Name: a.Name, Revision: a.Revision, Advanced: a.Advanced,
			Summary: a.Current.Summary, Behavior: a.Current.Behavior, Limits: a.Current.Limits})
	}
	for _, d := range ws.Decisions {
		out.Decisions = append(out.Decisions, decisionOutput(d))
	}
	for _, a := range ws.Attachments {
		out.Attachments = append(out.Attachments, AttachmentOutput{Ref: fmt.Sprintf("ATT-%d", a.ID), Owner: a.Owner, Alt: a.Alt, Mime: a.Mime, URL: a.URL})
	}
	if threads, err := s.threadsForObjectEitherForm("idea", fmt.Sprintf("IDEA-%d", i.ID)); err == nil {
		for _, t := range threads {
			out.Discussions = append(out.Discussions, fmt.Sprintf("thread %d: %s (%s)", t.ID, t.Title, t.State))
		}
	}
	return out, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func agentMark(b bool) string {
	if b {
		return " (agent)"
	}
	return ""
}

// ---- Entscheidungen

type DecisionOutput struct {
	Ref        string          `json:"ref"`
	Subject    string          `json:"subject"`
	Question   string          `json:"question"`
	State      string          `json:"state"`
	Way        string          `json:"way"`
	WayWhy     []string        `json:"way_why,omitempty"`
	NeedsHuman bool            `json:"needs_a_person,omitempty"`
	Detail     string          `json:"detail,omitempty"`
	Variants   []VariantOutput `json:"variants,omitempty"`
	Votes      []string        `json:"votes,omitempty"`
	Chosen     string          `json:"chosen_variant,omitempty"`
	Answer     string          `json:"answer,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Trigger    string          `json:"trigger,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
	ByAgent    bool            `json:"decided_by_agent,omitempty"`
	Knowledge  string          `json:"knowledge,omitempty"`
}

type VariantOutput struct {
	Label  string   `json:"label"`
	Title  string   `json:"title"`
	Text   string   `json:"text,omitempty"`
	Pro    string   `json:"pro,omitempty"`
	Contra string   `json:"contra,omitempty"`
	Images []string `json:"images,omitempty"`
}

func decisionOutput(d idea.Decision) DecisionOutput {
	subject := fmt.Sprintf("IDEA-%d", d.SubjectID)
	if d.Subject == idea.SubjectRequest {
		subject = fmt.Sprintf("REQ-%d", d.SubjectID)
	}
	out := DecisionOutput{Ref: d.HumanID(), Subject: subject, Question: d.Question, State: d.State, Way: d.Way, WayWhy: d.WayWhy, NeedsHuman: d.NeedsHuman(),
		Detail: d.Detail, Chosen: d.ChosenVariant, Answer: d.Answer, Reason: d.Reason, Trigger: d.Trigger, DecidedBy: d.DecidedBy, ByAgent: d.ByAgent}
	if d.KnowledgeID != 0 {
		out.Knowledge = knref.Format(d.KnowledgeID)
	}
	for _, v := range d.Variants {
		vo := VariantOutput{Label: v.Label, Title: v.Title, Text: v.Text, Pro: v.Pro, Contra: v.Contra}
		for _, a := range v.Attachments {
			vo.Images = append(vo.Images, fmt.Sprintf("ATT-%d %s", a.ID, a.URL))
		}
		out.Variants = append(out.Variants, vo)
	}
	for _, v := range d.Votes {
		out.Votes = append(out.Votes, fmt.Sprintf("%s%s: %s, %s", v.Voter, agentMark(v.ByAgent), v.Variant, v.Reason))
	}
	return out
}

type DecisionListOutput struct {
	Decisions []DecisionOutput `json:"decisions"`
}

type DecisionOpenInput struct {
	Idea       int64                  `json:"idea,omitempty" jsonschema:"the idea number the question is about"`
	RequestID  int64                  `json:"request_id,omitempty" jsonschema:"the issue the question is about"`
	ViaRequest int64                  `json:"via_request,omitempty" jsonschema:"a question that came up while working on this issue: it lands on the idea the issue came out of (the issue itself if it has none), not in the chat"`
	Question   string                 `json:"question" jsonschema:"one line: what is to be decided or answered"`
	Detail     string                 `json:"detail,omitempty" jsonschema:"background, a few lines"`
	Way        string                 `json:"way,omitempty" jsonschema:"human (a person decides) or agent (you may decide and must say why). Leave it out and the rule applies: you decide what is reversible, local and within the conventions, a person decides the rest. A real question to a person: human"`
	Aspects    []string               `json:"aspects,omitempty" jsonschema:"say which of these apply, they hand it to a person: irreversible, security, cost, external, data_model, product, people, nonlocal, convention"`
	Variants   []DecisionVariantInput `json:"variants,omitempty" jsonschema:"two to eight options A, B, C with text, pro and contra"`
}

type DecisionVariantInput struct {
	Title  string `json:"title" jsonschema:"one line"`
	Text   string `json:"text,omitempty"`
	Pro    string `json:"pro,omitempty"`
	Contra string `json:"contra,omitempty"`
}

func variantInputs(in []DecisionVariantInput) []idea.VariantInput {
	out := make([]idea.VariantInput, 0, len(in))
	for _, v := range in {
		out = append(out, idea.VariantInput{Title: v.Title, Text: v.Text, Pro: v.Pro, Contra: v.Contra})
	}
	return out
}

func (s *Server) handleDecisionOpen(_ context.Context, _ *mcp.CallToolRequest, in DecisionOpenInput) (*mcp.CallToolResult, DecisionOutput, error) {
	req := client.OpenDecisionRequest{Question: in.Question, Detail: in.Detail, Way: in.Way, Aspects: in.Aspects, Variants: variantInputs(in.Variants), SessionID: s.currentSessionID()}
	switch {
	case in.ViaRequest != 0:
		req.ViaReq = in.ViaRequest
	case in.Idea != 0:
		req.Subject, req.ID = idea.SubjectIdea, in.Idea
	case in.RequestID != 0:
		req.Subject, req.ID = idea.SubjectRequest, in.RequestID
	default:
		return nil, DecisionOutput{}, fmt.Errorf("name what the question is about: idea, request_id or via_request")
	}
	d, err := s.client.OpenDecision(req)
	if err != nil {
		return nil, DecisionOutput{}, err
	}
	out := decisionOutput(d)
	return s.withCard(requestResult(out), s.builder().DecisionCard(d)), out, nil
}

type DecisionListInput struct {
	ID        int64  `json:"id,omitempty" jsonschema:"read one decision, DEC-n"`
	Idea      int64  `json:"idea,omitempty" jsonschema:"the decisions of this idea"`
	RequestID int64  `json:"request_id,omitempty" jsonschema:"the decisions of this issue"`
	State     string `json:"state,omitempty" jsonschema:"in the project list: open (default), decided, deferred, dropped or all"`
	Waiting   bool   `json:"waiting_for_a_person,omitempty" jsonschema:"only open decisions that wait for a person (the inbox)"`
	Limit     int    `json:"limit,omitempty"`
}

func (s *Server) handleDecisionList(_ context.Context, _ *mcp.CallToolRequest, in DecisionListInput) (*mcp.CallToolResult, DecisionListOutput, error) {
	var list []idea.Decision
	switch {
	case in.ID != 0:
		d, err := s.client.GetDecision(in.ID)
		if err != nil {
			return nil, DecisionListOutput{}, err
		}
		list = []idea.Decision{d}
	case in.Idea != 0:
		var err error
		if list, err = s.client.DecisionsOf(idea.SubjectIdea, in.Idea); err != nil {
			return nil, DecisionListOutput{}, err
		}
	case in.RequestID != 0:
		var err error
		if list, err = s.client.DecisionsOf(idea.SubjectRequest, in.RequestID); err != nil {
			return nil, DecisionListOutput{}, err
		}
	default:
		project, err := s.readProject("")
		if err != nil {
			return nil, DecisionListOutput{}, err
		}
		if list, err = s.client.ListDecisions(project, in.State, "", in.Waiting, in.Limit); err != nil {
			return nil, DecisionListOutput{}, err
		}
	}
	out := DecisionListOutput{Decisions: []DecisionOutput{}}
	for _, d := range list {
		out.Decisions = append(out.Decisions, decisionOutput(d))
	}
	return requestResult(out), out, nil
}

type DecisionUpdateInput struct {
	ID      int64  `json:"id" jsonschema:"the decision number, DEC-n"`
	Action  string `json:"action" jsonschema:"vote (your pick among the variants, with a reason), variant (add an option), close (decided, deferred or dropped), reopen (take a closed decision back), way (a person only: who decides)"`
	Variant string `json:"variant,omitempty" jsonschema:"vote and close: the letter of the variant"`
	Reason  string `json:"reason,omitempty" jsonschema:"one line: for a vote why you would take it; for close decided the reason; for dropped why it is not decided; for reopen why"`
	Outcome string `json:"outcome,omitempty" jsonschema:"with close: decided (a variant or an answer, and a reason), deferred (needs trigger) or dropped (needs reason)"`
	Answer  string `json:"answer,omitempty" jsonschema:"with close decided: the decision in words, when no variant fits"`
	Trigger string `json:"trigger,omitempty" jsonschema:"with close deferred: when it comes back, in words (when we build the webhook sync). It shows up in the briefing of the branch or package it matches"`
	Title   string `json:"title,omitempty" jsonschema:"with variant: the title of the new option"`
	Text    string `json:"text,omitempty" jsonschema:"with variant: the option in a few lines"`
	Pro     string `json:"pro,omitempty"`
	Contra  string `json:"contra,omitempty"`
	WayTo   string `json:"way,omitempty" jsonschema:"with action way: human or agent"`
}

func (s *Server) handleDecisionUpdate(_ context.Context, _ *mcp.CallToolRequest, in DecisionUpdateInput) (*mcp.CallToolResult, DecisionOutput, error) {
	session := s.currentSessionID()
	var d idea.Decision
	var err error
	switch in.Action {
	case "vote":
		d, err = s.client.VoteDecision(in.ID, in.Variant, in.Reason, session)
	case "variant":
		d, err = s.client.AddVariant(in.ID, idea.VariantInput{Title: in.Title, Text: in.Text, Pro: in.Pro, Contra: in.Contra}, session)
	case "close":
		d, err = s.client.CloseDecision(in.ID, client.CloseDecisionRequest{Outcome: in.Outcome, Variant: in.Variant, Answer: in.Answer, Reason: in.Reason, Trigger: in.Trigger, SessionID: session})
	case "reopen":
		d, err = s.client.ReopenDecision(in.ID, in.Reason, session)
	case "way":
		d, err = s.client.SetDecisionWay(in.ID, in.WayTo, session)
	default:
		return nil, DecisionOutput{}, fmt.Errorf("action is vote, variant, close, reopen or way")
	}
	if err != nil {
		return nil, DecisionOutput{}, err
	}
	out := decisionOutput(d)
	return s.withCard(requestResult(out), s.builder().DecisionCard(d)), out, nil
}

// ---- Anhänge

type IdeaAttachInput struct {
	Idea       int64  `json:"idea,omitempty" jsonschema:"the idea the image belongs to"`
	RequestID  int64  `json:"request_id,omitempty" jsonschema:"the issue, when the image belongs to a variant of one of its decisions"`
	Owner      string `json:"owner,omitempty" jsonschema:"idea (default for an idea), variant (needs decision and variant) or reply (needs thread: the discussion of the idea)"`
	DecisionID int64  `json:"decision,omitempty" jsonschema:"with owner variant: the decision, DEC-n"`
	Variant    string `json:"variant,omitempty" jsonschema:"with owner variant: the letter"`
	ThreadID   int64  `json:"thread,omitempty" jsonschema:"with owner reply: the thread number"`
	Path       string `json:"path,omitempty" jsonschema:"a PNG, JPEG or WebP file on this machine, up to 5 MB"`
	Data       string `json:"data_base64,omitempty" jsonschema:"the image as base64, instead of path"`
	Alt        string `json:"alt,omitempty" jsonschema:"what the image shows, one line"`
}

type IdeaAttachOutput struct {
	Ref    string `json:"ref"`
	Owner  string `json:"owner"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	URL    string `json:"url"`
}

// readImageFile liest eine Bilddatei bis zur Höchstgröße: erst über das geöffnete
// Handle prüfen, dass es eine gewöhnliche Datei ist, dann höchstens Limit+1 Bytes.
func readImageFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > idea.MaxAttachmentBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, idea.MaxAttachmentBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, idea.MaxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > idea.MaxAttachmentBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, idea.MaxAttachmentBytes)
	}
	return data, nil
}

func (s *Server) handleIdeaAttach(_ context.Context, _ *mcp.CallToolRequest, in IdeaAttachInput) (*mcp.CallToolResult, IdeaAttachOutput, error) {
	var data []byte
	switch {
	case in.Path != "" && in.Data != "":
		return nil, IdeaAttachOutput{}, fmt.Errorf("give path or data_base64, not both")
	case in.Path != "":
		b, err := readImageFile(in.Path)
		if err != nil {
			return nil, IdeaAttachOutput{}, err
		}
		data = b
	case in.Data != "":
		if base64.StdEncoding.DecodedLen(len(in.Data)) > idea.MaxAttachmentBytes {
			return nil, IdeaAttachOutput{}, fmt.Errorf("the image is larger than %d bytes", idea.MaxAttachmentBytes)
		}
		b, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, IdeaAttachOutput{}, fmt.Errorf("data_base64 is not base64: %w", err)
		}
		data = b
	default:
		return nil, IdeaAttachOutput{}, fmt.Errorf("give the image as path or data_base64")
	}
	req := client.AttachRequest{Owner: in.Owner, DecisionID: in.DecisionID, Variant: in.Variant, ThreadID: in.ThreadID, Data: data, Alt: in.Alt, SessionID: s.currentSessionID()}
	switch {
	case in.Idea != 0:
		req.Subject, req.ID = idea.SubjectIdea, in.Idea
		if req.Owner == "" {
			req.Owner = idea.OwnerIdea
		}
	case in.RequestID != 0:
		req.Subject, req.ID = idea.SubjectRequest, in.RequestID
		if req.Owner == "" {
			req.Owner = idea.OwnerVariant
		}
	default:
		return nil, IdeaAttachOutput{}, fmt.Errorf("name idea or request_id")
	}
	m, err := s.client.Attach(req)
	if err != nil {
		return nil, IdeaAttachOutput{}, err
	}
	out := IdeaAttachOutput{Ref: fmt.Sprintf("ATT-%d", m.ID), Owner: m.Owner, Mime: m.Mime, Width: m.Width, Height: m.Height, Bytes: m.Bytes, URL: m.URL}
	return requestResult(out), out, nil
}

// ---- idea_update: Phase, Diskussion, Issues

type IdeaIssueInput struct {
	Title        string   `json:"title,omitempty" jsonschema:"default: the idea title"`
	Description  string   `json:"description,omitempty"`
	Type         string   `json:"type,omitempty" jsonschema:"feature (default), change, bug or investigation"`
	Requirements []int    `json:"requirements,omitempty" jsonschema:"the numbers (from 1) of the concept's requirements that become criteria of this issue"`
	Criteria     []string `json:"criteria,omitempty" jsonschema:"more criteria, one observable outcome each"`
}

func issueSpecs(in []IdeaIssueInput) []store.IssueSpec {
	out := make([]store.IssueSpec, 0, len(in))
	for _, i := range in {
		out = append(out, store.IssueSpec{Type: i.Type, Title: i.Title, Description: i.Description, Criteria: i.Criteria, Requirements: i.Requirements})
	}
	return out
}

// workshopAction behandelt die Aktionen der Werkstatt in idea_update; false, wenn
// die Aktion keine davon ist.
func (s *Server) workshopAction(in IdeaUpdateInput) (*mcp.CallToolResult, IdeaOutput, bool, error) {
	switch in.Action {
	case "phase":
		i, err := s.client.SetIdeaPhase(in.ID, in.Phase, in.Revision)
		return s.ideaResult(i, err, "")
	case "requirement_waive":
		if _, err := s.client.WaiveIdeaRequirement(in.ID, in.Requirement, in.Reason); err != nil {
			return nil, IdeaOutput{}, true, err
		}
		i, err := s.client.GetIdea(in.ID)
		return s.ideaResult(i, err, fmt.Sprintf("requirement %d is waived", in.Requirement))
	case "discuss":
		i, err := s.client.StartIdeaDiscussion(in.ID)
		return s.ideaResult(i, err, fmt.Sprintf("Open the discussion with coord action=thread_open (link_kind idea, link_id IDEA-%d); anyone, agents included, answers in that thread. Put what is undecided as idea action=decision_open.", in.ID))
	case "issues":
		res, err := s.client.CreateIssuesFromIdea(in.ID, issueSpecs(in.Issues), in.Branch, in.PackageName, s.currentSessionID())
		if err != nil {
			return nil, IdeaOutput{}, true, err
		}
		out := ideaOutput(res.Idea)
		for _, id := range res.Requests {
			out.Created += fmt.Sprintf("REQ-%d ", id)
		}
		out.Created = strings.TrimSpace(out.Created)
		if res.Package != nil {
			out.Note = fmt.Sprintf("work package %s on branch %s", res.Package.HumanID(), res.Package.Branch)
		}
		return s.withCard(requestResult(out), s.builder().IdeaCard(res.Idea)), out, true, nil
	}
	return nil, IdeaOutput{}, false, nil
}

func (s *Server) ideaResult(i idea.Idea, err error, note string) (*mcp.CallToolResult, IdeaOutput, bool, error) {
	if err != nil {
		return nil, IdeaOutput{}, true, err
	}
	out := ideaOutput(i)
	out.Note = note
	return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, true, nil
}

func (s *Server) registerIdeaWorkshopTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_text", Description: "The raw version and the summary of an idea, both kept as versions. raw is the original wording (voice message, meeting transcript, a chat) and stays unchanged; summary is a short version you can write. With body a new version is added, without it one is read. If you already have the content (a ping-pong in this chat), attach it yourself, do not ask.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleIdeaText)
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_concept", Description: "The concept of an idea, a page with fixed sections (about, what, how, not, questions, requirements), versioned. Draft it from the raw version; people change it. Give some sections to write, none to read. Requirements are drafts of acceptance criteria and become the criteria of the issues. Acceptance criteria come out better here than on the issues.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleIdeaConcept)
	mcp.AddTool(srv, &mcp.Tool{Name: "decision_open", Description: "Put a question on an idea or an issue as an open decision instead of asking in chat. Optional variants A, B, C with text, pro and contra; a person deciding gets them as quick answers. You close it if it is reversible, local and within the conventions (say why; recorded as from the agent, a person can overturn it); a person closes it if it touches security, permissions, cost, anything outside, the data model, product direction, or people disagree (name those in aspects). A question from building an issue: via_request. Nothing blocks: carry on with what does not depend on it.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleDecisionOpen)
	mcp.AddTool(srv, &mcp.Tool{Name: "decision_list", Description: "Read decisions: one by id, those of an idea or an issue, or the project's (open ones by default; waiting_for_a_person is the inbox). Shows variants with their images, the votes per person and, when closed, the reason.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleDecisionList)
	mcp.AddTool(srv, &mcp.Tool{Name: "decision_update", Description: "Work on a decision: vote (which variant you would take and why, anyone who works on the idea), variant (add an option), close, reopen, way. close needs the right to edit the idea or issue: decided (a variant or an answer, plus the reason; it becomes knowledge and goes into the concept), deferred (with a trigger: it comes back in the briefing when the trigger matches), dropped (with a reason). A decision that waits for a person is closed by a person, not by an agent. reopen takes a closed decision back (a person only, except a deferred one).", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleDecisionUpdate)
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_attach", Description: "Attach an image (PNG, JPEG or WebP, up to 5 MB) to an idea, to a variant of a decision, or to a reply in the discussion of an idea: screenshot, mock, sketch. SVG is refused. The image is checked by its bytes and has the rights of the idea.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleIdeaAttach)
}
