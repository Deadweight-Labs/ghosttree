package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Features (REQ-612): die lebende Beschreibung dessen, was das Produkt tut. Wer ein
// Feature ändert, schreibt hier, wie es jetzt ist (feature_update); wer in einem
// Bereich arbeitet, bekommt die betroffenen Features kurz im Kontext. Ein Feature
// ist keine Idee (was es tun soll) und keine Datei-Beschreibung (was eine Datei
// tut): es sagt, was das Produkt für Nutzer tut, über Dateien hinweg.

// FeatureFields sind die Felder des Inhalts. Beim Anlegen sind alle außer name
// optional, beim Fortschreiben bleibt stehen, was fehlt.
type FeatureFields struct {
	Name     *string   `json:"name,omitempty" jsonschema:"what the product does, in a few words (unique in the project)"`
	Summary  *string   `json:"summary,omitempty" jsonschema:"one to three sentences for someone who does not know the feature"`
	Behavior *string   `json:"behavior,omitempty" jsonschema:"how it is NOW: what it does, in the present tense. Not a plan, not how it came to be"`
	Limits   *string   `json:"limits,omitempty" jsonschema:"what it deliberately does not do, and known gaps"`
	CLI      *string   `json:"cli,omitempty" jsonschema:"how to use it from the command line"`
	MCP      *string   `json:"mcp,omitempty" jsonschema:"which MCP tools use it and how"`
	API      *string   `json:"api,omitempty" jsonschema:"which API routes it has"`
	UI       *string   `json:"ui,omitempty" jsonschema:"what the web interface shows for it"`
	Areas    *[]string `json:"areas,omitempty" jsonschema:"repository path prefixes it lives in (internal/store, not every file); a pull request touching them gets the checklist item"`
}

func (f FeatureFields) patch() feature.Patch {
	return feature.Patch{Name: f.Name, Summary: f.Summary, Behavior: f.Behavior, Limits: f.Limits, CLI: f.CLI, MCP: f.MCP, API: f.API, UI: f.UI, Areas: f.Areas}
}

type FeatureCreateInput struct {
	Project string `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	FeatureFields
	Message  string  `json:"message,omitempty" jsonschema:"what this first version is, one line"`
	Ideas    []int64 `json:"ideas,omitempty" jsonschema:"ideas (IDEA-n as numbers) it came out of"`
	Requests []int64 `json:"requests,omitempty" jsonschema:"issues (REQ-n as numbers) it came out of"`
	FromIdea int64   `json:"from_idea,omitempty" jsonschema:"turn an implemented idea into this feature; refused when the idea changes an existing feature (feature_update with idea then)"`
	PR       int64   `json:"pr,omitempty" jsonschema:"the pull request this version belongs to"`
}

type FeatureGetInput struct {
	ID       int64 `json:"id" jsonschema:"the feature number, FEAT-n"`
	Versions bool  `json:"versions,omitempty" jsonschema:"also list the versions with what triggered each (idea, issues, pull request)"`
	Revision int   `json:"revision,omitempty" jsonschema:"read this older version instead of the current one"`
}

type FeatureSearchInput struct {
	Project string `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	Query   string `json:"query,omitempty" jsonschema:"words in the name, summary, behavior or areas"`
	Path    string `json:"path,omitempty" jsonschema:"only features whose areas contain this repository path"`
	State   string `json:"state,omitempty" jsonschema:"active (default), retired or all"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum results, default 100"`
}

type FeatureUpdateInput struct {
	ID int64 `json:"id" jsonschema:"the feature number, FEAT-n"`
	FeatureFields
	Message  string  `json:"message,omitempty" jsonschema:"what changed, one line"`
	Base     int     `json:"base,omitempty" jsonschema:"the version you built on; a newer one is refused so nobody's change is lost"`
	Idea     int64   `json:"idea,omitempty" jsonschema:"the idea (IDEA-n as a number) this version carries forward. Its card then shows the description as advanced"`
	Requests []int64 `json:"requests,omitempty" jsonschema:"issues (REQ-n as numbers) that triggered this version"`
	PR       int64   `json:"pr,omitempty" jsonschema:"the pull request this version belongs to; it closes the checklist item of that PR"`
}

type FeatureStateInput struct {
	ID     int64  `json:"id" jsonschema:"the feature number, FEAT-n"`
	State  string `json:"state" jsonschema:"retired (the product no longer does this; needs reason) or active (take it up again)"`
	Reason string `json:"reason,omitempty" jsonschema:"required for retired, one line"`
}

type FeatureLinkInput struct {
	ID     int64  `json:"id" jsonschema:"the feature number, FEAT-n"`
	Action string `json:"action,omitempty" jsonschema:"link (default) or unlink"`
	Kind   string `json:"kind" jsonschema:"idea or request"`
	Ref    int64  `json:"ref" jsonschema:"the number of the idea or request"`
	Rel    string `json:"rel,omitempty" jsonschema:"origin (it came out of that) or affects (that changes it: an idea with 'concerns FEAT-n'). Default affects for an idea, origin otherwise"`
}

type FeaturePRPathsInput struct {
	Project string   `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	PR      int64    `json:"pr" jsonschema:"the pull request number"`
	Paths   []string `json:"paths,omitempty" jsonschema:"the repository paths the pull request changes (git diff --name-only against the merge base). Replaces what was reported before. Without paths the checklist is only read"`
}

type FeatureCheckInput struct {
	ID   int64  `json:"id" jsonschema:"the feature number, FEAT-n"`
	PR   int64  `json:"pr" jsonschema:"the pull request the description was checked against"`
	Note string `json:"note" jsonschema:"why the description still holds, one line (for example: only renamed, behavior unchanged)"`
}

// FeatureOutput ist ein Feature mit, auf Wunsch, seinen Fassungen.
type FeatureOutput struct {
	Feature  feature.Feature   `json:"feature"`
	Versions []feature.Version `json:"versions,omitempty"`
	Version  *feature.Version  `json:"version,omitempty"`
	// Unchanged: nichts hat sich gegenüber der jüngsten Fassung geändert, es
	// entstand keine neue.
	Unchanged bool   `json:"unchanged,omitempty"`
	URL       string `json:"url,omitempty"`
}

type FeatureListOutput struct {
	Features []feature.Feature `json:"features"`
}

type FeatureChecklistOutput struct {
	Stored    int                          `json:"stored,omitempty"`
	Dropped   int                          `json:"dropped,omitempty"`
	Checklist []requestdomain.FeatureCheck `json:"checklist"`
}

func (s *Server) registerFeatureTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_get", Description: "Read the page of one feature: how it behaves now, its limits, how to use it (CLI, MCP, API, web interface), the paths it lives in, the ideas and issues it came out of or that change it. Optionally its versions, each with what triggered it (idea, issues, pull request).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleFeatureGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_search", Description: "List the project's features, or those whose paths contain a given path. A feature is something the product does for its users, across files; look here before describing the same thing twice.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleFeatureSearch)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_create", Description: "Create a feature page for something the product does now (not a plan: a wish is an idea). It needs a name; summary, behavior, limits, usage and the paths it lives in make it useful. An implemented idea becomes a feature with from_idea, unless it changes an existing feature, then write a new version of that one.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleFeatureCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_update", Description: "Write how a feature is NOW, after your change: a new version from the fields you pass on top of the current one. Name what triggered it (idea, requests, pr); the pull request's checklist item closes. Every version stays readable; unchanged text creates none.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleFeatureUpdate)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_state", Description: "Retire a feature the product no longer has (a reason is required; a lead does it) or take it up again. A retired feature stays readable but leaves context and checklists.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleFeatureState)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_link", Description: "Tie a feature to an idea or an issue: origin (it came out of that) or affects (that changes it). An idea that affects a feature shows its current description next to the wish, and its implementation carries that description forward instead of creating a new feature. Unlink takes a tie off; versions already written keep theirs.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleFeatureLink)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_pr_paths", Description: "Tell ghosttree which paths a pull request changes, and read the checklist that follows: for every feature whose paths contain one of them the item 'Feature X: Beschreibung angepasst?'. Get the paths with git diff --name-only <merge-base>...HEAD. Without paths it only reads the checklist of that pull request.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleFeaturePRPaths)
	mcp.AddTool(srv, &mcp.Tool{Name: "feature_check", Description: "Say that you looked at a feature's description for a pull request and it still holds (a refactoring, behavior unchanged): closes the checklist item without a new version. A note says why. If the behavior changed, write feature_update with pr instead.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleFeatureCheck)
}

func (s *Server) featureInput(f FeatureFields, message string, base int, idea int64, requests []int64, pr int64) client.FeatureInput {
	return client.FeatureInput{Patch: f.patch(), Message: message, Base: base, Idea: idea, Requests: requests, PR: pr, SessionID: s.currentSessionID()}
}

func (s *Server) handleFeatureGet(_ context.Context, _ *mcp.CallToolRequest, in FeatureGetInput) (*mcp.CallToolResult, FeatureOutput, error) {
	f, err := s.client.GetFeature(in.ID)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	if in.Revision > 0 {
		v, err := s.client.FeatureVersion(in.ID, in.Revision)
		if err != nil {
			return nil, FeatureOutput{}, err
		}
		out.Version = &v
	}
	if in.Versions {
		if out.Versions, err = s.client.FeatureVersions(in.ID, false); err != nil {
			return nil, FeatureOutput{}, err
		}
	}
	return s.withCard(textResult(RenderFeature(out)), s.builder().FeatureCard(f)), out, nil
}

func (s *Server) handleFeatureSearch(_ context.Context, _ *mcp.CallToolRequest, in FeatureSearchInput) (*mcp.CallToolResult, FeatureListOutput, error) {
	project, err := s.readProject(in.Project)
	if err != nil {
		return nil, FeatureListOutput{}, err
	}
	list, err := s.client.SearchFeatures(project, in.State, in.Query, in.Path, in.Limit)
	if err != nil {
		return nil, FeatureListOutput{}, err
	}
	out := FeatureListOutput{Features: list}
	if out.Features == nil {
		out.Features = []feature.Feature{}
	}
	var b strings.Builder
	if len(list) == 0 {
		b.WriteString("No features match.\n")
	}
	for _, f := range list {
		fmt.Fprintf(&b, "- %s %s [%s, version %d]", f.HumanID(), oneLine(f.Name), f.State, f.Revision)
		if f.Summary != "" {
			b.WriteString(": " + oneLine(f.Summary))
		}
		b.WriteString("\n")
	}
	return textResult(b.String()), out, nil
}

func (s *Server) handleFeatureCreate(_ context.Context, _ *mcp.CallToolRequest, in FeatureCreateInput) (*mcp.CallToolResult, FeatureOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	fi := s.featureInput(in.FeatureFields, in.Message, 0, 0, nil, in.PR)
	fi.Project, fi.Ideas, fi.Requests, fi.FromIdea = project, in.Ideas, in.Requests, in.FromIdea
	f, err := s.client.CreateFeature(fi)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	return s.withCard(textResult(RenderFeature(out)), s.builder().FeatureCard(f)), out, nil
}

func (s *Server) handleFeatureUpdate(_ context.Context, _ *mcp.CallToolRequest, in FeatureUpdateInput) (*mcp.CallToolResult, FeatureOutput, error) {
	res, err := s.client.UpdateFeature(in.ID, s.featureInput(in.FeatureFields, in.Message, in.Base, in.Idea, in.Requests, in.PR))
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	f, err := s.client.GetFeature(in.ID)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, Version: &res.Version, Unchanged: res.Unchanged, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	text := fmt.Sprintf("%s is at version %d.", f.HumanID(), res.Version.Revision)
	if res.Unchanged {
		text = fmt.Sprintf("Nothing changed: %s stays at version %d (no new version).", f.HumanID(), res.Version.Revision)
	}
	return s.withCard(textResult(text), s.builder().FeatureCard(f)), out, nil
}

func (s *Server) handleFeatureState(_ context.Context, _ *mcp.CallToolRequest, in FeatureStateInput) (*mcp.CallToolResult, FeatureOutput, error) {
	f, err := s.client.SetFeatureState(in.ID, in.State, in.Reason)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	return s.withCard(textResult(fmt.Sprintf("%s is now %s.", f.HumanID(), f.State)), s.builder().FeatureCard(f)), out, nil
}

func (s *Server) handleFeatureLink(_ context.Context, _ *mcp.CallToolRequest, in FeatureLinkInput) (*mcp.CallToolResult, FeatureOutput, error) {
	rel := in.Rel
	if rel == "" {
		rel = feature.RelOrigin
		if in.Kind == feature.KindIdea {
			rel = feature.RelAffects
		}
	}
	var f feature.Feature
	var err error
	switch in.Action {
	case "", "link":
		f, err = s.client.LinkFeature(in.ID, in.Kind, in.Ref, rel)
	case "unlink":
		f, err = s.client.UnlinkFeature(in.ID, in.Kind, in.Ref, rel)
	default:
		return nil, FeatureOutput{}, fmt.Errorf("action is link or unlink")
	}
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	return s.withCard(textResult(RenderFeature(out)), s.builder().FeatureCard(f)), out, nil
}

func (s *Server) handleFeaturePRPaths(_ context.Context, _ *mcp.CallToolRequest, in FeaturePRPathsInput) (*mcp.CallToolResult, FeatureChecklistOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, FeatureChecklistOutput{}, err
	}
	var out FeatureChecklistOutput
	if len(in.Paths) > 0 {
		res, err := s.client.ReportPullPaths(project, in.PR, in.Paths)
		if err != nil {
			return nil, FeatureChecklistOutput{}, err
		}
		out = FeatureChecklistOutput{Stored: res.Stored, Dropped: res.Dropped, Checklist: res.Checklist}
	} else {
		list, err := s.client.FeatureChecklist(project, in.PR)
		if err != nil {
			return nil, FeatureChecklistOutput{}, err
		}
		out.Checklist = list
	}
	if out.Checklist == nil {
		out.Checklist = []requestdomain.FeatureCheck{}
	}
	var b strings.Builder
	if in.Paths != nil && len(in.Paths) > 0 {
		fmt.Fprintf(&b, "Stored %d paths for PR #%d (%d dropped as not paths inside the repository).\n", out.Stored, in.PR, out.Dropped)
	}
	b.WriteString(RenderFeatureChecklist(in.PR, out.Checklist))
	return textResult(b.String()), out, nil
}

func (s *Server) handleFeatureCheck(_ context.Context, _ *mcp.CallToolRequest, in FeatureCheckInput) (*mcp.CallToolResult, FeatureOutput, error) {
	if err := s.client.ConfirmFeatureUnchanged(in.ID, in.PR, in.Note); err != nil {
		return nil, FeatureOutput{}, err
	}
	f, err := s.client.GetFeature(in.ID)
	if err != nil {
		return nil, FeatureOutput{}, err
	}
	out := FeatureOutput{Feature: f, URL: s.builder().WithProject(f.Project).Feature(f.ID)}
	return s.withCard(textResult(fmt.Sprintf("Recorded: the description of %s still holds for PR #%d.", f.HumanID(), in.PR)), s.builder().FeatureCard(f)), out, nil
}

// RenderFeatureChecklist schreibt die Punkte eines PRs.
func RenderFeatureChecklist(pr int64, list []requestdomain.FeatureCheck) string {
	if len(list) == 0 {
		return fmt.Sprintf("No feature description is touched by PR #%d (or no paths were reported: feature_pr_paths with paths).\n", pr)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Feature descriptions PR #%d touches:\n", pr)
	for _, c := range list {
		mark, how := "[ ]", fmt.Sprintf("%d changed paths, e.g. %s", c.PathCount, oneLine(strings.Join(c.Paths, ", ")))
		switch c.State {
		case requestdomain.FeatureCheckUpdated:
			mark, how = "[x]", fmt.Sprintf("a new version carries this PR (%v)", c.PRRevisions)
		case requestdomain.FeatureCheckUnchanged:
			mark, how = "[x]", "checked, still right: "+oneLine(c.Note)
		}
		fmt.Fprintf(&b, "- %s %s — %s\n", mark, oneLine(c.Text), how)
	}
	b.WriteString("Open item: feature_update with pr (the behavior changed) or feature_check with pr and a note (it did not).\n")
	return b.String()
}

// RenderFeature ist die Seite eines Features als Text: der Ist-Stand zuerst, dann
// Grenzen, Bedienung, Bereiche, Herkunft und auf Wunsch die Fassungen.
func RenderFeature(o FeatureOutput) string {
	f := o.Feature
	c := f.Content
	if o.Version != nil {
		c = o.Version.Content
	}
	var b strings.Builder
	state := f.State
	if f.State == feature.Retired && f.Reason != "" {
		state += ": " + oneLine(f.Reason)
	}
	rev := f.Revision
	if o.Version != nil {
		rev = o.Version.Revision
	}
	fmt.Fprintf(&b, "# %s %s [%s, version %d of %d]\n", f.HumanID(), oneLine(c.Name), state, rev, f.Versions)
	section := func(title, text string) {
		if strings.TrimSpace(text) != "" {
			fmt.Fprintf(&b, "\n## %s\n%s\n", title, strings.TrimSpace(text))
		}
	}
	section("What it is", c.Summary)
	section("How it behaves now", c.Behavior)
	section("Limits", c.Limits)
	var usage []string
	for _, u := range []struct{ name, text string }{{"CLI", c.CLI}, {"MCP", c.MCP}, {"API", c.API}, {"Web interface", c.UI}} {
		if strings.TrimSpace(u.text) != "" {
			usage = append(usage, "- "+u.name+": "+strings.TrimSpace(u.text))
		}
	}
	section("How to use it", strings.Join(usage, "\n"))
	if len(c.Areas) > 0 {
		section("Paths", "- "+strings.Join(c.Areas, "\n- "))
	}
	refs := func(title string, rs []feature.Ref) {
		if len(rs) == 0 {
			return
		}
		var lines []string
		for _, r := range rs {
			line := "- " + r.HumanID()
			if r.Title != "" {
				line += " " + oneLine(r.Title)
			}
			if r.State != "" {
				line += " [" + oneLine(r.State) + "]"
			}
			if title == "Changed by" && r.Advanced {
				line += " (carried forward)"
			} else if title == "Changed by" {
				line += " (description not carried forward yet)"
			}
			lines = append(lines, line)
		}
		section(title, strings.Join(lines, "\n"))
	}
	refs("Came out of", f.Origin)
	refs("Changed by", f.Affects)
	if len(o.Versions) > 0 {
		var lines []string
		for _, v := range o.Versions {
			line := fmt.Sprintf("- version %d, %s, %s", v.Revision, v.CreatedAt, oneLine(v.Person))
			if v.Message != "" {
				line += ": " + oneLine(v.Message)
			}
			var cause []string
			if v.Idea > 0 {
				cause = append(cause, fmt.Sprintf("IDEA-%d", v.Idea))
			}
			for _, r := range v.Requests {
				cause = append(cause, fmt.Sprintf("REQ-%d", r))
			}
			for _, pr := range v.PRs() {
				cause = append(cause, fmt.Sprintf("PR #%d", pr))
			}
			if len(cause) > 0 {
				line += " (" + strings.Join(cause, ", ") + ")"
			}
			if v.GitAnchor != nil && v.GitAnchor.Commit != "" {
				line += " @ " + v.GitAnchor.Short()
			}
			lines = append(lines, line)
		}
		section("Versions", strings.Join(lines, "\n"))
	}
	return b.String()
}
