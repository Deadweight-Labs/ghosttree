package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchLanguageHelp steht in der Beschreibung von context_search: die Sprache
// in einem Absatz, damit ein Agent sie ohne Nachschlagen benutzt.
const searchLanguageHelp = "Qualifiers (like GitHub, several allowed, - negates, comma means or, \"…\" groups): is:open|closed|done|pain|idea|issue|knowledge|<state>, kind:, type:, repo:owner/name, branch:, commit:<7+ hex>, pr:12 or pr:10..14, package:N, req:N, task:N, pain:N, idea:N, parent:N, blocks:N, author:/for: (@me is you), label:/-label:, area:<path prefix>, topic:<named area>, created:/updated:>2026-11-01 (also <, >=, a..b, 7d), since:/until: (origin time). Words that are not qualifiers search the text. An unknown qualifier is an error, not ignored. Qualified searches also cover pains, ideas, criteria, human tasks and file descriptions."

// ---- Ideen

type IdeaRecordInput struct {
	Project        string `json:"project,omitempty" jsonschema:"the project when it is not the session's own: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to"`
	Title          string `json:"title" jsonschema:"the wish in one line"`
	Body           string `json:"body,omitempty" jsonschema:"one to three sentences, high level. No design, no criteria"`
	AllowDuplicate bool   `json:"allow_duplicate,omitempty" jsonschema:"record it even though a similar open idea exists (the answer names it otherwise)"`
	// Optional, never required: when you already have the content, attach it now.
	Raw       string             `json:"raw,omitempty" jsonschema:"optional: the original wording (voice message, transcript, the chat so far), unchanged. Attach it yourself when you have it, do not ask"`
	RawSource string             `json:"raw_source,omitempty" jsonschema:"with raw: where it comes from, one line"`
	Summary   string             `json:"summary,omitempty" jsonschema:"optional: a short version, two to four sentences"`
	Concept   *IdeaConceptFields `json:"concept,omitempty" jsonschema:"optional: a concept draft with the fixed sections"`
	// MotivatedByPain makes the idea the solution idea of a pain (relation
	// motivates, pain to idea).
	MotivatedByPain int64 `json:"motivated_by_pain,omitempty" jsonschema:"optional: the pain (PAIN-n as a number) this idea is the answer to. The idea is recorded and linked to the pain in one call"`
}

type IdeaGetInput struct {
	ID int64 `json:"id" jsonschema:"the idea number, IDEA-n"`
}

type IdeaSearchInput struct {
	Query string `json:"query,omitempty" jsonschema:"words in the title or text"`
	State string `json:"state,omitempty" jsonschema:"open (default), accepted, dismissed or all. Dismissed ideas stay searchable"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 50"`
}

type IdeaUpdateInput struct {
	ID          int64            `json:"id" jsonschema:"the idea number, IDEA-n"`
	Action      string           `json:"action" jsonschema:"promote (make a request out of it and link both; for a small idea), link_request (link an existing request, needs request_id), dismiss (needs reason), reopen, phase (needs phase), discuss (mark it as under discussion), issues (create the issues of an approved idea, with criteria from its requirements), requirement_waive (needs requirement and reason; a person only)"`
	RequestID   int64            `json:"request_id,omitempty" jsonschema:"with link_request: the request that came out of the idea"`
	Reason      string           `json:"reason,omitempty" jsonschema:"required for dismiss"`
	Title       string           `json:"title,omitempty" jsonschema:"with promote: the request title; default is the idea title"`
	Description string           `json:"description,omitempty" jsonschema:"with promote: the request description; default is the idea text"`
	Type        string           `json:"type,omitempty" jsonschema:"with promote: feature (default), change, bug or investigation"`
	Criteria    []string         `json:"criteria,omitempty" jsonschema:"with promote: observable acceptance criteria of the new request"`
	Phase       string           `json:"phase,omitempty" jsonschema:"with phase: concept, discussion or done (when its issues are done and every requirement has a met criterion or is waived). Approving is for a person in the web interface (the owner of the idea or a maintainer), a token cannot do it: propose it as a decision"`
	Issues      []IdeaIssueInput `json:"issues,omitempty" jsonschema:"with issues: the issues to create from the approved idea; none means one issue with all requirements as criteria"`
	Revision    int              `json:"revision,omitempty" jsonschema:"with phase approved (a person, in the web interface): the version of the concept that was seen"`
	Requirement int              `json:"requirement,omitempty" jsonschema:"with requirement_waive: the number (from 1) of the requirement of the concept"`
	Branch      string           `json:"branch,omitempty" jsonschema:"with issues: put them in the work package of this branch (new or existing)"`
	PackageName string           `json:"package_name,omitempty" jsonschema:"with issues: the name of a new work package"`
}

type IdeaOutput struct {
	Ref      string   `json:"ref"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Body     string   `json:"body,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Requests []string `json:"requests,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Created  string   `json:"created_request,omitempty"`
	// Phase is how far the idea is worked out; Hint is the one-time, optional
	// suggestion to work it out; Note says what to do next.
	Phase string `json:"phase,omitempty"`
	// Pains are the problems that motivated the idea; UncoveredRequirements counts
	// the requirements of the concept that no met criterion covers yet.
	Pains                 []string            `json:"motivated_by,omitempty"`
	UncoveredRequirements int                 `json:"uncovered_requirements,omitempty"`
	OpenDecisions         int                 `json:"open_decisions,omitempty"`
	Hint                  string              `json:"hint,omitempty"`
	Note                  string              `json:"note,omitempty"`
	Workshop              *IdeaWorkshopOutput `json:"workshop,omitempty"`
	// URL is set on the items of a list only; a single idea carries its card.
	URL string `json:"url,omitempty"`
}

type IdeaListOutput struct {
	Ideas []IdeaOutput `json:"ideas"`
}

func ideaOutput(i idea.Idea) IdeaOutput {
	out := IdeaOutput{Ref: i.HumanID(), Title: i.Title, State: i.State, Body: i.Body, Reason: i.Reason, Phase: i.Phase, OpenDecisions: i.OpenDecisions, Hint: i.Hint}
	for _, r := range i.Requests {
		out.Requests = append(out.Requests, fmt.Sprintf("REQ-%d", r))
	}
	for _, l := range i.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	for _, p := range i.Pains {
		out.Pains = append(out.Pains, fmt.Sprintf("PAIN-%d %s", p.ID, p.Title))
	}
	out.UncoveredRequirements = i.UncoveredRequirements
	return out
}

func (s *Server) readProject(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return s.client.WriteTarget(explicit, s.ctxAxes.Project)
	}
	if s.ctxAxes.Project == "" {
		return "", fmt.Errorf("this session has no project: pass project")
	}
	return s.ctxAxes.Project, nil
}

func (s *Server) handleIdeaRecord(_ context.Context, _ *mcp.CallToolRequest, in IdeaRecordInput) (*mcp.CallToolResult, IdeaOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, IdeaOutput{}, err
	}
	i, err := s.client.CreateIdea(project, in.Title, in.Body, in.AllowDuplicate, s.currentSessionID())
	if err != nil {
		return nil, IdeaOutput{}, err
	}
	if in.MotivatedByPain != 0 {
		if _, err := s.client.AddPainLink(in.MotivatedByPain, pain.Target{IdeaID: i.ID}, s.currentSessionID()); err != nil {
			return nil, IdeaOutput{}, fmt.Errorf("%s was recorded, linking it to PAIN-%d failed: %w", i.HumanID(), in.MotivatedByPain, err)
		}
	}
	if err := s.attachDraft(i, in.Raw, in.RawSource, in.Summary, in.Concept); err != nil {
		return nil, IdeaOutput{}, err
	}
	if in.MotivatedByPain != 0 || in.Raw != "" || in.Summary != "" || in.Concept != nil {
		// Der Stand nach dem Anhängen: Phase, Pain, Konzept-Kennzeichen.
		hint := i.Hint
		if fresh, err := s.client.GetIdea(i.ID); err == nil {
			i = fresh
			i.Hint = hint
		}
	}
	out := ideaOutput(i)
	return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
}

func (s *Server) handleIdeaGet(_ context.Context, _ *mcp.CallToolRequest, in IdeaGetInput) (*mcp.CallToolResult, IdeaOutput, error) {
	i, err := s.client.GetIdea(in.ID)
	if err != nil {
		return nil, IdeaOutput{}, err
	}
	out := ideaOutput(i)
	if w, err := s.workshopOutput(i); err == nil {
		out.Workshop = &w
	}
	return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
}

func (s *Server) handleIdeaSearch(_ context.Context, _ *mcp.CallToolRequest, in IdeaSearchInput) (*mcp.CallToolResult, IdeaListOutput, error) {
	project, err := s.readProject("")
	if err != nil {
		return nil, IdeaListOutput{}, err
	}
	list, err := s.client.SearchIdeas(project, in.State, in.Query, in.Limit)
	if err != nil {
		return nil, IdeaListOutput{}, err
	}
	out := IdeaListOutput{Ideas: []IdeaOutput{}}
	for _, i := range list {
		o := ideaOutput(i)
		o.Body = ""
		o.URL = s.builder().WithProject(i.Project).Idea(i.ID)
		out.Ideas = append(out.Ideas, o)
	}
	return requestResult(out), out, nil
}

func (s *Server) handleIdeaUpdate(_ context.Context, _ *mcp.CallToolRequest, in IdeaUpdateInput) (*mcp.CallToolResult, IdeaOutput, error) {
	if res, out, handled, err := s.workshopAction(in); handled {
		return res, out, err
	}
	switch in.Action {
	case "promote":
		ref := s.sessionRef
		if ref == "" {
			ref = "mcp"
		}
		detail, i, err := s.client.PromoteIdea(in.ID, requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: in.Type, Title: in.Title, Description: in.Description, Origin: "agent", SessionRef: ref},
			Criteria: in.Criteria,
		})
		if err != nil {
			return nil, IdeaOutput{}, err
		}
		out := ideaOutput(i)
		out.Created = fmt.Sprintf("REQ-%d", detail.Request.ID)
		return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
	case "link_request":
		if in.RequestID == 0 {
			return nil, IdeaOutput{}, fmt.Errorf("link_request needs request_id")
		}
		i, err := s.client.LinkIdeaRequest(in.ID, in.RequestID)
		if err != nil {
			return nil, IdeaOutput{}, err
		}
		out := ideaOutput(i)
		return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
	case "dismiss":
		if strings.TrimSpace(in.Reason) == "" {
			return nil, IdeaOutput{}, fmt.Errorf("dismiss needs a reason")
		}
		i, err := s.client.SetIdeaState(in.ID, idea.Dismissed, in.Reason)
		if err != nil {
			return nil, IdeaOutput{}, err
		}
		out := ideaOutput(i)
		return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
	case "reopen":
		i, err := s.client.SetIdeaState(in.ID, idea.Open, "")
		if err != nil {
			return nil, IdeaOutput{}, err
		}
		out := ideaOutput(i)
		return s.withCard(requestResult(out), s.builder().IdeaCard(i)), out, nil
	}
	return nil, IdeaOutput{}, fmt.Errorf("action is promote, link_request, dismiss, reopen, phase, discuss or issues")
}

// ---- Labels, Themen

type LabelInput struct {
	Action      string   `json:"action" jsonschema:"list, create, update, delete, add (put labels on an object) or remove (take labels off it)"`
	Name        string   `json:"name,omitempty" jsonschema:"with create, update or delete: the label name; one line, no commas, quotation marks or colons"`
	NewName     string   `json:"new_name,omitempty" jsonschema:"with update: the new name"`
	Color       string   `json:"color,omitempty" jsonschema:"with create or update: six hexadecimal digits such as d73a4a"`
	Description string   `json:"description,omitempty" jsonschema:"with create or update: one short line"`
	Org         string   `json:"org,omitempty" jsonschema:"with create, update, delete or list: the org (slug) instead of the project, for a label that applies in all its projects. Needs an owner of the org"`
	Kind        string   `json:"kind,omitempty" jsonschema:"with add or remove: request (or issue), pain, idea or knowledge"`
	ID          int64    `json:"id,omitempty" jsonschema:"with add or remove: the number of the request, pain, idea or knowledge entry"`
	Labels      []string `json:"labels,omitempty" jsonschema:"with add or remove: the label names"`
	Project     string   `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type LabelOutput struct {
	Labels  []string `json:"labels,omitempty"`
	Changed string   `json:"changed,omitempty"`
}

func labelNames(ls []store.Label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		n := l.Name
		if l.Scope == "org" {
			n += " (org)"
		}
		if l.Color != "" {
			n += " #" + l.Color
		}
		if l.Description != "" {
			n += " — " + oneLine(l.Description)
		}
		out = append(out, n)
	}
	return out
}

func (s *Server) handleLabel(_ context.Context, _ *mcp.CallToolRequest, in LabelInput) (*mcp.CallToolResult, LabelOutput, error) {
	switch in.Action {
	case "list":
		project := ""
		if in.Org == "" {
			p, err := s.readProject(in.Project)
			if err != nil {
				return nil, LabelOutput{}, err
			}
			project = p
		}
		ls, err := s.client.ListLabels(project, in.Org)
		if err != nil {
			return nil, LabelOutput{}, err
		}
		out := LabelOutput{Labels: labelNames(ls)}
		return requestResult(out), out, nil
	case "create":
		project := ""
		if in.Org == "" {
			p, err := s.writeTarget(in.Project)
			if err != nil {
				return nil, LabelOutput{}, err
			}
			project = p
		}
		l, err := s.client.CreateLabel(project, in.Org, in.Name, in.Color, in.Description)
		if err != nil {
			return nil, LabelOutput{}, err
		}
		out := LabelOutput{Changed: "created " + l.Name}
		return requestResult(out), out, nil
	case "update", "delete":
		project := ""
		if in.Org == "" {
			p, err := s.readProject(in.Project)
			if err != nil {
				return nil, LabelOutput{}, err
			}
			project = p
		}
		ls, err := s.client.ListLabels(project, in.Org)
		if err != nil {
			return nil, LabelOutput{}, err
		}
		var id int64
		for _, l := range ls {
			if strings.EqualFold(l.Name, strings.TrimSpace(in.Name)) {
				if in.Org == "" && l.Scope == "org" {
					continue
				}
				id = l.ID
			}
		}
		if id == 0 {
			return nil, LabelOutput{}, fmt.Errorf("no label %q here", in.Name)
		}
		if in.Action == "delete" {
			if err := s.client.DeleteLabel(id); err != nil {
				return nil, LabelOutput{}, err
			}
			out := LabelOutput{Changed: "deleted " + in.Name}
			return requestResult(out), out, nil
		}
		var name, color, desc *string
		if in.NewName != "" {
			name = &in.NewName
		}
		if in.Color != "" {
			color = &in.Color
		}
		if in.Description != "" {
			desc = &in.Description
		}
		l, err := s.client.UpdateLabel(id, name, color, desc)
		if err != nil {
			return nil, LabelOutput{}, err
		}
		out := LabelOutput{Changed: "updated " + l.Name}
		return requestResult(out), out, nil
	case "add", "remove":
		if in.ID == 0 || in.Kind == "" || len(in.Labels) == 0 {
			return nil, LabelOutput{}, fmt.Errorf("%s needs kind, id and labels", in.Action)
		}
		var add, remove []string
		if in.Action == "add" {
			add = in.Labels
		} else {
			remove = in.Labels
		}
		ls, err := s.client.SetLabels(in.Kind, in.ID, add, remove)
		if err != nil {
			return nil, LabelOutput{}, err
		}
		out := LabelOutput{Labels: labelNames(ls), Changed: in.Action}
		return requestResult(out), out, nil
	}
	return nil, LabelOutput{}, fmt.Errorf("action is list, create, update, delete, add or remove")
}

type TopicInput struct {
	Action      string   `json:"action" jsonschema:"list, save (create or replace) or delete"`
	Name        string   `json:"name,omitempty" jsonschema:"the topic name, for example Search"`
	Prefixes    []string `json:"prefixes,omitempty" jsonschema:"with save: path prefixes relative to the repository root, for example internal/embed. topic:<name> in a search then acts like all of them"`
	Description string   `json:"description,omitempty"`
	Project     string   `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type TopicOutput struct {
	Topics  []string `json:"topics,omitempty"`
	Changed string   `json:"changed,omitempty"`
}

func (s *Server) handleTopic(_ context.Context, _ *mcp.CallToolRequest, in TopicInput) (*mcp.CallToolResult, TopicOutput, error) {
	switch in.Action {
	case "list":
		project, err := s.readProject(in.Project)
		if err != nil {
			return nil, TopicOutput{}, err
		}
		ts, err := s.client.ListTopics(project)
		if err != nil {
			return nil, TopicOutput{}, err
		}
		out := TopicOutput{}
		for _, t := range ts {
			out.Topics = append(out.Topics, t.Name+": "+strings.Join(t.Prefixes, ", "))
		}
		return requestResult(out), out, nil
	case "save":
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, TopicOutput{}, err
		}
		t, err := s.client.SaveTopic(project, in.Name, in.Description, in.Prefixes)
		if err != nil {
			return nil, TopicOutput{}, err
		}
		out := TopicOutput{Changed: "saved " + t.Name + ": " + strings.Join(t.Prefixes, ", ")}
		return requestResult(out), out, nil
	case "delete":
		project, err := s.readProject(in.Project)
		if err != nil {
			return nil, TopicOutput{}, err
		}
		if err := s.client.DeleteTopic(project, in.Name); err != nil {
			return nil, TopicOutput{}, err
		}
		out := TopicOutput{Changed: "deleted " + in.Name}
		return requestResult(out), out, nil
	}
	return nil, TopicOutput{}, fmt.Errorf("action is list, save or delete")
}

// ---- Gespeicherte Suchen

type SearchPresetInput struct {
	Action      string   `json:"action" jsonschema:"list, run, save or delete"`
	Name        string   `json:"name,omitempty" jsonschema:"the saved search, for example pr-ergebnis (what was done in a pull request), branch-offen (what is open on a branch), ordner (what is known about a folder), seit (what changed since a date) or fuer-mich (what waits for me)"`
	PR          string   `json:"pr,omitempty" jsonschema:"with run: the value of {pr}, a pull request number"`
	Branch      string   `json:"branch,omitempty" jsonschema:"with run: the value of {branch}"`
	Area        string   `json:"area,omitempty" jsonschema:"with run: the value of {area}, a path prefix"`
	Since       string   `json:"since,omitempty" jsonschema:"with run: the value of {since}, a date such as 2026-11-01"`
	Queries     []string `json:"queries,omitempty" jsonschema:"with save: one to five search lines in the search language, with the placeholders {pr}, {branch}, {area}, {since}"`
	Scope       string   `json:"scope,omitempty" jsonschema:"with save: user (default, only yours), project (members) or org (owners of the org)"`
	Description string   `json:"description,omitempty"`
	ID          int64    `json:"id,omitempty" jsonschema:"with delete: the id from list"`
	Project     string   `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	Limit       int      `json:"limit,omitempty" jsonschema:"with run: hits per kind, default 20"`
}

type SearchPresetOutput struct {
	Searches []string `json:"searches,omitempty"`
	Changed  string   `json:"changed,omitempty"`
}

func (s *Server) handleSearchPreset(_ context.Context, _ *mcp.CallToolRequest, in SearchPresetInput) (*mcp.CallToolResult, SearchPresetOutput, error) {
	switch in.Action {
	case "list":
		project, err := s.readProject(in.Project)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		ps, err := s.client.ListPresets(project)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		out := SearchPresetOutput{}
		for _, p := range ps {
			line := fmt.Sprintf("%s [%s]", p.Name, p.Scope)
			if len(p.Args) > 0 {
				line += " takes {" + strings.Join(p.Args, "} {") + "}"
			}
			line += ": " + strings.Join(p.Queries, " | ")
			if p.Description != "" {
				line += " — " + oneLine(p.Description)
			}
			if p.ID != 0 {
				line += fmt.Sprintf(" (id %d)", p.ID)
			}
			out.Searches = append(out.Searches, line)
		}
		return requestResult(out), out, nil
	case "run":
		project, err := s.readProject(in.Project)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		args := map[string]string{}
		for k, v := range map[string]string{"pr": in.PR, "branch": in.Branch, "area": in.Area, "since": in.Since} {
			if strings.TrimSpace(v) != "" {
				args[k] = v
			}
		}
		run, err := s.client.RunPreset(project, in.Name, args, in.Limit)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "search %s: %s\n", run.Preset.Name, strings.Join(run.Queries, " | "))
		b.WriteString(RenderQualifiedLinked(run.Result, s.builder()))
		return textResult(b.String()), SearchPresetOutput{}, nil
	case "save":
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		scopeKind := in.Scope
		if scopeKind == "" {
			scopeKind = "user"
		}
		p, err := s.client.SavePreset(project, scopeKind, in.Name, in.Description, in.Queries)
		if err != nil {
			return nil, SearchPresetOutput{}, err
		}
		out := SearchPresetOutput{Changed: fmt.Sprintf("saved %s [%s]", p.Name, p.Scope)}
		return requestResult(out), out, nil
	case "delete":
		if in.ID == 0 {
			return nil, SearchPresetOutput{}, fmt.Errorf("delete needs the id from list")
		}
		if err := s.client.DeletePreset(in.ID); err != nil {
			return nil, SearchPresetOutput{}, err
		}
		out := SearchPresetOutput{Changed: fmt.Sprintf("deleted %d", in.ID)}
		return requestResult(out), out, nil
	}
	return nil, SearchPresetOutput{}, fmt.Errorf("action is list, run, save or delete")
}

// ---- Ausgabe einer Suche mit Qualifiern

// RenderQualified gibt die Treffer einer Suchzeile als Text aus, ein Abschnitt je
// Art. Jede Zeile trägt, was Qualifier darauf anwendet (Zustand, Nummer).
func RenderQualified(res client.SearchResult) string {
	return RenderQualifiedLinked(res, links.Builder{})
}

// RenderQualifiedLinked is RenderQualified with the web address after every
// hit (REQ-603). A hit carries nothing but the link, no card: the list has to
// stay small. A Builder without a server leaves the lines as they were.
func RenderQualifiedLinked(res client.SearchResult, b links.Builder) string {
	var out strings.Builder
	if len(res.Knowledge) > 0 {
		out.WriteString("## knowledge\n")
		for _, k := range res.Knowledge {
			out.WriteString(hitLink(renderKnowledge(k), b.WithProject(k.Scope.Project).Knowledge(k.ID)))
		}
	}
	if len(res.Requests) > 0 {
		out.WriteString("\n## issues (requests)\n")
		for _, h := range res.Requests {
			out.WriteString(hitLink(fmt.Sprintf("- [request|status=%s|type=%s%s] REQ-%d %s\n", h.Request.State, h.Request.Type, originctx.Label(h.OriginContext, h.Request.CreatedAt), h.Request.ID, oneLine(h.Request.Title)),
				b.WithProject(h.Request.Scope.Project).Issue(h.Request.ID)))
		}
	}
	if len(res.Criteria) > 0 {
		out.WriteString("\n## criteria\n")
		for _, c := range res.Criteria {
			var ev []string
			for _, e := range c.Evidence {
				ev = append(ev, e.Kind+":"+oneLine(e.Ref))
			}
			line := fmt.Sprintf("- [criterion|%s] %s %s (REQ-%d %s)", c.State, c.HumanID(), oneLine(c.Description), c.RequestID, oneLine(c.RequestTitle))
			if len(ev) > 0 {
				line += " evidence: " + strings.Join(ev, ", ")
			}
			out.WriteString(hitLink(line+"\n", b.Criterion(c.RequestID, c.Number)))
		}
	}
	if len(res.Pains) > 0 {
		out.WriteString("\n## pains\n")
		for _, p := range res.Pains {
			line := fmt.Sprintf("- [pain|state=%s] %s %s", p.State, p.HumanID(), oneLine(p.Title))
			if len(p.Ideas) > 0 {
				var is []string
				for _, i := range p.Ideas {
					is = append(is, fmt.Sprintf("IDEA-%d", i.ID))
				}
				line += " → " + strings.Join(is, ", ")
			}
			out.WriteString(hitLink(line+"\n", b.WithProject(p.Project).Pain(p.ID)))
		}
	}
	if len(res.Ideas) > 0 {
		out.WriteString("\n## ideas\n")
		for _, i := range res.Ideas {
			line := fmt.Sprintf("- [idea|state=%s] %s %s", i.State, i.HumanID(), oneLine(i.Title))
			if len(i.Requests) > 0 {
				var rs []string
				for _, r := range i.Requests {
					rs = append(rs, fmt.Sprintf("REQ-%d", r))
				}
				line += " → " + strings.Join(rs, ", ")
			}
			if len(i.Pains) > 0 {
				var ps []string
				for _, p := range i.Pains {
					ps = append(ps, fmt.Sprintf("PAIN-%d", p.ID))
				}
				line += " ← " + strings.Join(ps, ", ")
			}
			out.WriteString(hitLink(line+"\n", b.WithProject(i.Project).Idea(i.ID)))
		}
	}
	if len(res.Tasks) > 0 {
		out.WriteString("\n## human tasks\n")
		for _, t := range res.Tasks {
			out.WriteString(hitLink(fmt.Sprintf("- [task|state=%s|kind=%s] %s %s (REQ-%d)\n", t.State, t.Kind, t.HumanID(), oneLine(t.Title), t.RequestID), b.Task(t.ID)))
		}
	}
	if len(res.Files) > 0 {
		out.WriteString("\n## files\n")
		for _, g := range res.Files {
			out.WriteString(renderGhostHit(g))
		}
	}
	if out.Len() == 0 {
		return "(no results)\n"
	}
	return out.String()
}

// qualifiedSearch beantwortet context_search, wenn die Suchzeile Qualifier trägt:
// ein Aufruf an /api/search, der alle Arten auf einmal liefert.
func (s *Server) qualifiedSearch(in SearchInput, parsed query.Query, anchor client.AnchorFilter) (*mcp.CallToolResult, any, error) {
	ax, crossProject := s.searchAxes(in)
	if in.Branch == "" && !in.AllBranches {
		// Ein Qualifier ist eine ausdrückliche Frage: die Branch-Sicht der Session
		// würde Wissen anderer Branches verbergen, nach dem branch:/pr:/commit: fragt.
		ax.AnyBranch = true
	}
	kind := in.Kind
	switch kind {
	case "", "all":
		kind = "all"
	case "knowledge", "requests", "files", "pains", "ideas", "criteria", "tasks":
	case "sessions":
		return textResult("(sessions carry no qualifiers; search them without)\n"), nil, nil
	default:
		return nil, nil, fmt.Errorf("kind is knowledge, requests, pains, ideas, criteria, tasks, files or all")
	}
	res, err := s.client.SearchAnchored(in.Query, kind, ax, searchLimit, !crossProject, anchor)
	if err != nil {
		return nil, nil, err
	}
	var out strings.Builder
	if res.Query != "" {
		fmt.Fprintf(&out, "query: %s\n", res.Query)
	}
	out.WriteString(RenderQualifiedLinked(res, s.builder()))
	return textResult(out.String()), nil, nil
}

func (s *Server) registerQueryTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_record", Description: "Record a wish without working it out: a title and one to three sentences, high level. Ideas are the light inbox next to pains (a pain is a problem without a solution, an idea is a wish). Nothing is decided: no type, no priority, no criteria. A similar open idea is named instead of recording a second one. Later an idea becomes a request with idea_update promote. Recording never needs a question back and has no required field, whatever the topic. If you already have the content (a chat, a transcript), pass raw, summary and concept to attach it in the same call. A bigger idea can be worked out later (idea_text, idea_concept, decision_open); the answer may suggest that once, nothing is blocked.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleIdeaRecord)
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_get", Description: "Read one idea with its state, the requests that came out of it and what is attached: summary, concept, versions of the raw text, open and closed decisions with variants and votes, images, discussions (threads linked with link_kind idea).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleIdeaGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_search", Description: "List the current project's ideas, open ones by default. Look here before recording an idea.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleIdeaSearch)
	mcp.AddTool(srv, &mcp.Tool{Name: "idea_update", Description: "Turn an idea into work or put it aside: promote creates a request from it and links both (the idea counts as taken up), link_request links a request that already exists, dismiss needs a reason (dismissed ideas stay searchable), reopen takes a dismissed one back. For a worked-out idea: phase moves it along (approved is for the owner of the idea or a maintainer in the web interface, not for a token; it needs a concept; done needs every requirement covered by a met criterion or waived), discuss marks it as under discussion, issues creates the issues of an approved idea with criteria from its requirements, optionally in the work package of a branch.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleIdeaUpdate)
	s.registerIdeaWorkshopTools(srv)
	mcp.AddTool(srv, &mcp.Tool{Name: "label", Description: "Labels like on GitHub: list them, create one for the project or its org (name, color, description), change or delete it, or put them on and take them off a request, pain, idea or knowledge entry. Search with label:name and -label:name in context_search. Type and area are not labels; use type: and area:.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleLabel)
	mcp.AddTool(srv, &mcp.Tool{Name: "topic", Description: "Named areas of the repository: a topic is a name for several path prefixes (Search = internal/embed + internal/store/embeddings). topic:<name> in context_search then filters like all of them.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleTopic)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_preset", Description: "Saved searches. run executes one by name with arguments and answers with the hits per kind: pr-ergebnis (pr: what was done, decided and learned in a pull request), branch-offen (branch: what is still open on a branch), ordner (area: what is known about a folder), seit (since: what changed since a date), fuer-mich (what waits for me). list shows them with the ones saved for you, the project or the org; save stores one of your own (search lines with {pr} {branch} {area} {since}).", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleSearchPreset)
}
