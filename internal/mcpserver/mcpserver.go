// Package mcpserver exposes ghosttree to agents as four MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	client         *client.Client
	ctxAxes        scope.Axes
	baseActivation activation.Context
	// repoRoot ist das Arbeitsverzeichnis der Repo-Wurzel. Ghost-Dateien
	// brauchen die echte Datei, um ihren Zustand festzuhalten — der Agent soll
	// die Frische nicht behaupten können.
	repoRoot string
	// afterWrite schreibt den Baum neu. Als Rückruf, weil das Schreiben in
	// cmd/ctx sitzt und mcpserver nicht von main abhängen darf.
	afterWrite    func()
	afterSnapshot func(context.Context, string) error
	// mentioned sind die Pfade, auf die dieser Prozess schon hingewiesen hat.
	// Der Prozess ist die Sitzung, deshalb reicht der Hauptspeicher; siehe
	// firstMentionOf. Unter einem Schloss, weil Werkzeugaufrufe im go-sdk
	// asynchron behandelt werden und parallel hier ankommen können.
	mentionedMu sync.Mutex
	mentioned   map[string]bool
	// sessionRef ist die external_id der Sitzung, die diesen MCP-Prozess hält.
	// Ohne sie könnte eine Suche den gerade hochgeladenen Prompt als Beleg für
	// sich selbst zurückgeben.
	sessionRef string
}

// SetRepoRoot wird von cmd/ctx/mcp.go aus dem aufgelösten Git-Kontext gesetzt.
func (s *Server) SetRepoRoot(root string) { s.repoRoot = root }

func (s *Server) SetAfterWrite(f func()) { s.afterWrite = f }

func (s *Server) SetAfterSnapshot(f func(context.Context, string) error) { s.afterSnapshot = f }

func (s *Server) SetSessionRef(ref string) { s.sessionRef = ref }

func NewServer(c *client.Client, axes scope.Axes, base ...activation.Context) *Server {
	s := &Server{client: c, ctxAxes: axes}
	if len(base) > 0 {
		s.baseActivation = base[0]
	}
	return s
}

type SearchInput struct {
	// Optional, because a targeted read names an id and has nothing to search
	// for. It was required until a probe agent tried to follow the tool's own
	// instruction — "pass knowledge_id" — and got
	// `required: missing properties: ["query"]` back from schema validation,
	// before the handler ever ran. The handler tests below called the function
	// directly and never saw it.
	Query string `json:"query,omitempty" jsonschema:"what to search for. Omit only when reading one entry by knowledge_id"`
	// The id comes back on every knowledge hit, so reading one entry in full is
	// a second call rather than a guess. Same shape as session_id on
	// context_sessions: list or search, or name one and read it whole.
	KnowledgeID int64  `json:"knowledge_id,omitempty" jsonschema:"read this knowledge entry in full instead of searching. The id is on every hit; use it when a snippet is not enough, for instance for a stored plan or spec"`
	Kind        string `json:"kind,omitempty" jsonschema:"knowledge, requests, sessions, files or all (default all). \"files\" searches the per-file and per-directory descriptions of this repository — use it to find a file by what it does rather than by what it is called"`
	AllBranches bool   `json:"all_branches,omitempty" jsonschema:"search across all branches of the project"`
	Project     string `json:"project,omitempty" jsonschema:"search another project instead of the current one, given as a normalized remote like github.com/owner/repo"`
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"search every project. Use when a problem here may already have been solved elsewhere; the default stays the current project"`
	Machine     string `json:"machine,omitempty" jsonschema:"restrict to a machine hostname"`
}

// searchAxes resolves the project axis a search runs on. Scope separation is
// the default; reaching past it is something the agent has to ask for, because
// only the agent knows whether a lesson from another repo is relevant here.
func (s *Server) searchAxes(in SearchInput) (ax scope.Axes, crossProject bool) {
	ax = s.ctxAxes
	switch {
	case in.AllProjects:
		return scope.Axes{Machine: in.Machine}, true
	case in.Project != "":
		// The other project's branch names mean nothing in this context.
		return scope.Axes{Project: scope.NormalizeRemote(in.Project), Machine: in.Machine}, true
	}
	if in.AllBranches {
		// Not ax.Branch = "": that removes every branch clause and hides the
		// branch-scoped entries this flag exists to reveal.
		ax.AnyBranch = true
	}
	if in.Machine != "" {
		ax.Machine = in.Machine
	}
	return ax, false
}

type GetInput struct {
	Paths []string `json:"paths,omitempty" jsonschema:"repository-relative paths currently being worked on"`
}

type RememberInput struct {
	Type      string `json:"type" jsonschema:"pitfall, decision, note or plan"`
	Title     string `json:"title" jsonschema:"one line summary"`
	Body      string `json:"body" jsonschema:"the knowledge itself; for a decision, cover why it was taken, which alternatives were rejected and what the tradeoffs are"`
	ScopeHint string `json:"scope_hint" jsonschema:"where this belongs — required, because it is a judgement nobody else can make for you: project, branch, machine or global.\nThe question that separates project from branch: does this stop being true once the branch is merged or abandoned? A migration in flight, a temporary flag, a workaround for something only this branch broke — that is branch. Anything that outlives the branch is project, and most things do.\nA branch entry is read by this branch and by every branch cut from it afterwards, never by a sibling. Machine is for facts about the box you are standing on, global for facts about how you work anywhere."`
}

type SessionsInput struct {
	Query       string `json:"query,omitempty" jsonschema:"full text search across session transcripts"`
	SessionID   int64  `json:"session_id,omitempty" jsonschema:"read this session instead of listing"`
	Project     string `json:"project,omitempty" jsonschema:"list or search another project's sessions, given as a normalized remote like github.com/owner/repo"`
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"cover every project instead of the current one"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum results (default 20)"`
}

func (s *Server) Register(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_create", Description: "Create an immutable named snapshot of the bound project's durable context and observed Git state.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleSnapshotCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_list", Description: "List immutable context snapshot metadata for the bound project, with an opaque cursor and a maximum limit of 100.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleSnapshotList)
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_get", Description: "Read one snapshot safely: no filter returns only head and counts, domain returns at most 100 summaries, and domain plus key returns exactly one payload.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleSnapshotGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_search",
		Description: "Search ghosttree knowledge and past session transcripts, or read one knowledge entry in full. Hits are snippets and carry an id; pass knowledge_id to get that entry's whole body back verbatim, which is how stored plans, specs and other long documents are read. Defaults to the current project, branch and machine context. Set project to search another repository, or all_projects to search every one of them — worth doing when a problem here may already have been solved elsewhere.",
	}, s.handleSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_get",
		Description: "Get the context package for the current project, branch and machine. With repository-relative paths, return the descriptions of those paths and their ancestor directories instead of repeating the general bootstrap.",
	}, s.handleGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_remember",
		Description: "Store a pitfall, decision, note or plan in ghosttree instead of writing it into the source tree. A decision should record why it was taken, which alternatives were rejected and what the tradeoffs are, so it can be revisited later.",
	}, s.handleRemember)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_sessions",
		Description: "List or search past agent sessions, or read one session's transcript. Defaults to the current project; set project or all_projects to look further.",
	}, s.handleSessions)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_describe_file",
		Description: "Describe what a file or directory does, stored against its repository-relative path instead of as a comment in the source. Use it whenever you would otherwise write an explanatory comment, and whenever you create a file. The description is what a later reader — including a small model that cannot hold the file in context — needs to understand this path without reading it. Ghost descriptions are browsable as real files under .ghosttree/tree/. If you have read the path and there is nothing to say that is not already in the code, pass nothing_to_say instead of writing a description that restates the source — an empty entry costs nothing, a restatement costs trust in every other description.",
	}, s.handleDescribe)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_file_history",
		Description: "Read how a path's description CHANGED — sentence by sentence, newest change first, the way you would read a diff. Not two versions side by side for you to compare: the removed sentences carry a -, the new ones a +, everything that stayed is counted and left out. The file's own history is in git; this is the history of the understanding, and it exists nowhere else. Use it when a description reads as if it no longer matches the code, when you suspect a good description was overwritten, or when you want to know how a component's purpose drifted. Pass full:true only when the exact wording of an old version is what you need — it costs the full text of every version.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, s.handleFileHistory)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_regression_cover",
		Description: "Say which test keeps a fixed defect from coming back — or that none does. ghosttree is not what prevents a regression; a test is. A pitfall only helps while somebody reads it. Call this after fixing a bug you recorded: state=covered with the test that would catch its return, state=uncovered when no such test exists and could, state=not_applicable when there is nothing to test here (a pitfall about a tool's behaviour is not a regression candidate — say so rather than leaving it blank, or a considered decision reads as an open task). Call it with no arguments to list the fixes nothing guards, together with how many pitfalls nobody has judged yet.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed},
	}, s.handleRegressionCover)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_search", Description: "List or search the current project's work ledger. Works like listing issues: call it with no query to see what is open, or name a subject to narrow. A question that names no subject — \"what is left to do\" — returns the list rather than guessing. Answers with a compact list; call request_get for one entry's full text. Use it before substantial feature, architecture, migration, or multi-session work.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleRequestSearch)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_get", Description: "Get one request as readable text with its original paragraphs. Concise includes the request, description, criteria, relations, and latest work; it omits activity, older work, and criterion evidence. Use response_format=detailed for the complete history and all evidence.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleRequestGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_create", Description: "Create a ledger entry for substantial work when request_search found no match. Include observable acceptance criteria; do not use for trivial local fixes.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRequestCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_start_work", Description: "Associate a Ghosttree session with an existing request as its primary task or as related work. Repeating the same association is safe.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleRequestStartWork)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_finish_work", Description: "End a session's work association with a paused, completed, or abandoned outcome and a concise handoff. This does not complete the request.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleRequestFinishWork)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_record_progress", Description: "Record evidenced request progress: add or satisfy criteria, complete or drop the request, add or remove a relation, or correct what the request says. Completion without evidence or with open criteria is rejected. A correction and a relation removal each need a reason, which goes into the activity list — nothing changes silently. Answers with what changed and how many criteria remain — call request_get for the full picture.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRequestProgress)
}

func Run(ctx context.Context, s *Server, version string) error {
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghosttree", Version: version}, nil)
	s.Register(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func textResult(s string) *mcp.CallToolResult {
	if s == "" {
		s = "(no results)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// searchLimit ist die Zahl der Treffer je Bereich. Sie ist nicht das Problem —
// eine Liste muss enden. Das Problem war, dass sie schwieg: genau zwanzig
// Treffer sehen aus wie "das ist alles", und der Leser schliesst aus der Form
// auf den Bestand. Derselbe Fehlschluss wie bei null Treffern (#732), gefunden
// am 2026-08-25 von einem Codex-Prüflauf.
const searchLimit = 20

// limitNote sagt, dass eine Liste am Anschlag endet — und nur dann.
func limitNote(shown, limit int) string {
	if shown < limit {
		return ""
	}
	return fmt.Sprintf("(cut at %d; there may be more — narrow the query, or call context_search with kind set to one area)\n", limit)
}

func withoutSession(hits []store.SessionHit, externalID string) []store.SessionHit {
	if externalID == "" {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if hit.Session.ExternalID != externalID {
			out = append(out, hit)
		}
	}
	return out
}

func (s *Server) handleSearch(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
	if in.KnowledgeID != 0 {
		k, err := s.client.KnowledgeByID(in.KnowledgeID)
		if err != nil {
			return nil, nil, err
		}
		return textResult(renderKnowledgeFull(k)), nil, nil
	}
	// Neither an id nor words: nothing to answer, and saying so beats returning
	// the whole archive or an empty list that reads like "there is nothing".
	if strings.TrimSpace(in.Query) == "" {
		return nil, nil, fmt.Errorf("give a query to search for, or a knowledge_id to read one entry in full")
	}
	ax, crossProject := s.searchAxes(in)
	if (in.Kind == "" || in.Kind == "all" || in.Kind == "requests") && store.ClassifySearch(in.Query) == store.SearchInterrupted {
		threads, err := s.client.InterruptedWork(ax, s.sessionRef)
		if err != nil {
			return nil, nil, err
		}
		var out strings.Builder
		out.WriteString("## interrupted work (exact state, not full-text matches)\n")
		for _, thread := range threads {
			fmt.Fprintf(&out, "- REQ-%d %s", thread.RequestID, thread.Title)
			if thread.Handoff != "" {
				fmt.Fprintf(&out, " — handoff: %s", thread.Handoff)
			}
			out.WriteByte('\n')
		}
		return textResult(out.String()), nil, nil
	}
	kind := in.Kind
	if kind == "" {
		kind = "all"
	}
	limit := searchLimit
	var out strings.Builder

	if kind == "knowledge" || kind == "all" {
		// The scope union is what a session reads here. Across projects that
		// union is meaningless, so the axes are matched as given instead.
		search := s.client.SearchUnion
		if crossProject {
			search = s.client.Search
		}
		res, err := search(in.Query, "knowledge", ax, limit)
		if err != nil {
			return nil, nil, err
		}
		if len(res.Knowledge) > 0 {
			out.WriteString("## knowledge\n")
			shortened := false
			for _, k := range res.Knowledge {
				out.WriteString(renderKnowledge(k))
				shortened = shortened || len([]rune(oneLine(k.Body))) > snippetChars
			}
			// Only said when something was actually held back. A pointer to the
			// rest on a hit that already showed all of it is noise, and a
			// shortened hit that says nothing is the entry lying about its size.
			if shortened {
				out.WriteString("(an entry ending in … is shortened; call context_search with knowledge_id=<id> for its full text)\n")
			}
			out.WriteString(limitNote(len(res.Knowledge), limit))
		}
	}
	if kind == "sessions" || kind == "all" {
		// Sessions carry all three axes, so an exact filter on the full context
		// would only ever match this very session; project is the useful scope.
		// An empty project means every project, which is what all_projects asks for.
		filter := scope.Axes{Project: ax.Project, Machine: in.Machine}
		res, err := s.client.SearchExcludingSession(in.Query, "sessions", filter, s.sessionRef, limit)
		if err != nil {
			return nil, nil, err
		}
		res.Sessions = withoutSession(res.Sessions, s.sessionRef)
		if len(res.Sessions) > 0 {
			out.WriteString("\n## sessions\n")
			for _, h := range res.Sessions {
				out.WriteString(renderHit(h))
			}
			out.WriteString(limitNote(len(res.Sessions), limit))
		}
	}
	if kind == "requests" || kind == "all" {
		filter := scope.Axes{Project: s.ctxAxes.Project}
		res, err := s.client.Search(in.Query, "requests", filter, limit)
		if err != nil {
			return nil, nil, err
		}
		if len(res.Requests) > 0 {
			out.WriteString("\n## requests\n")
			for _, h := range res.Requests {
				fmt.Fprintf(&out, "- [request|status=%s|type=%s|source=ledger] REQ-%d %s — %s\n", h.Request.State, h.Request.Type, h.Request.ID, h.Request.Title, h.MatchReason)
			}
			out.WriteString(limitNote(len(res.Requests), limit))
		}
	}
	if kind == "files" || kind == "all" {
		// Der Ghost-Baum ist projektgebunden: ein Pfad ist nur im Repo
		// eindeutig, zu dem er gehört.
		res, err := s.client.SearchGhosts(in.Query, ax.Project, limit)
		if err != nil {
			return nil, nil, err
		}
		if len(res) > 0 {
			out.WriteString("\n## files\n")
			for _, g := range res {
				out.WriteString(renderGhostHit(g))
			}
		}
	}
	return textResult(out.String()), nil, nil
}

func (s *Server) handleGet(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, any, error) {
	actx := s.baseActivation
	actx.Paths = append([]string(nil), in.Paths...)
	normalized, err := activation.NormalizeContext(actx)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Paths) > 0 {
		ghosts, err := s.client.GhostTree(s.ctxAxes.Project, "")
		if err != nil {
			return nil, nil, err
		}
		return textResult(renderGhostContext(ghosts, normalized.Paths)), nil, nil
	}
	md, err := s.client.Bootstrap(s.ctxAxes, normalized, 0, "")
	if err != nil {
		return nil, nil, err
	}
	return textResult(md), nil, nil
}

func renderGhostContext(ghosts []store.GhostFile, paths []string) string {
	byPath := make(map[string]store.GhostFile, len(ghosts))
	for _, g := range ghosts {
		byPath[g.Path] = g
	}

	var out strings.Builder
	out.WriteString("## What is known about the requested paths\n\n")
	seen := make(map[string]bool)
	for _, target := range paths {
		targetGhost, targetExists := byPath[target]
		targetDescribed := targetExists && strings.TrimSpace(targetGhost.Description) != ""
		for _, candidate := range append(store.ParentPaths(target), target) {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			g, ok := byPath[candidate]
			if !ok || strings.TrimSpace(g.Description) == "" {
				continue
			}
			name := g.Path
			if name == "" {
				name = "(Repo-Wurzel)"
			}
			fmt.Fprintf(&out, "### %s\n%s\n\n", name, strings.TrimRight(g.Description, "\n"))
		}
		if !targetDescribed {
			name := target
			if name == "" {
				name = "(Repo-Wurzel)"
			}
			fmt.Fprintf(&out, "### %s\nNo description for this path.\n\n", name)
		}
	}
	return out.String()
}

func (s *Server) handleRemember(ctx context.Context, _ *mcp.CallToolRequest, in RememberInput) (*mcp.CallToolResult, any, error) {
	if in.Type == "" || in.Title == "" {
		return nil, nil, fmt.Errorf("type and title are required")
	}
	k := store.Knowledge{Type: in.Type, Title: in.Title, Body: in.Body, Harness: "mcp"}
	autoCtx := s.ctxAxes
	// Placement is asked for rather than defaulted. A default put everything on
	// the branch and stranded 127 entries; the correction put everything on the
	// project, which is right more often and still nobody's decision. Where a
	// thing belongs is a judgement about how long it stays true, and only the
	// writer is in a position to make it.
	switch in.ScopeHint {
	case "project":
		k.Scope = scope.Axes{Project: s.ctxAxes.Project}
	case "branch":
		if s.ctxAxes.Branch == "" {
			return nil, nil, fmt.Errorf("scope_hint branch, but this session is not on a named branch; use project or machine")
		}
		k.Scope = scope.Axes{Project: s.ctxAxes.Project, Branch: s.ctxAxes.Branch}
	case "machine":
		k.Scope = scope.Axes{Machine: s.ctxAxes.Machine}
	case "global":
		// Empty scope plus empty context: the server's write defaults resolve
		// to global rather than filling anything in.
		autoCtx = scope.Axes{}
	case "":
		return nil, nil, fmt.Errorf("scope_hint is required: project, branch, machine or global. " +
			"Ask whether this stops being true once the branch is merged or abandoned — if yes it is branch, otherwise project")
	default:
		return nil, nil, fmt.Errorf("unknown scope_hint %q; use project, branch, machine or global", in.ScopeHint)
	}
	saved, err := s.client.Remember(k, autoCtx)
	if err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("stored #%d [%s|%s]", saved.ID, saved.Type, scopeLabel(saved.Scope))
	if hint := reasoningHint(saved); hint != "" {
		msg += "\n" + hint
	}
	return textResult(msg), nil, nil
}

// reasoningHint nudges decisions towards why/alternatives/tradeoffs. A decision
// that records only its outcome cannot be revisited later, because the reason
// it was taken is exactly what a future reader needs. It stays a hint rather
// than a rejection: a half-recorded decision beats a rejected one.
func reasoningHint(k store.Knowledge) string {
	if k.Type != "decision" {
		return ""
	}
	body := strings.ToLower(k.Body)
	for _, part := range []string{"why", "alternativ", "tradeoff"} {
		if !strings.Contains(body, part) {
			return "hint: decisions are most useful when the body covers why, alternatives and tradeoffs"
		}
	}
	return ""
}

func (s *Server) handleSessions(ctx context.Context, _ *mcp.CallToolRequest, in SessionsInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if in.SessionID != 0 {
		chunks, err := s.client.ReadSession(in.SessionID, 0, limit)
		if err != nil {
			return nil, nil, err
		}
		var out strings.Builder
		for _, c := range chunks {
			if c.Text == "" {
				continue
			}
			fmt.Fprintf(&out, "### %s (%d)\n%s\n\n", c.Role, c.Seq, c.Text)
		}
		return textResult(out.String()), nil, nil
	}
	// An empty project matches every project, which is what all_projects asks for.
	filter := scope.Axes{Project: s.ctxAxes.Project}
	switch {
	case in.AllProjects:
		filter.Project = ""
	case in.Project != "":
		filter.Project = scope.NormalizeRemote(in.Project)
	}
	if in.Query != "" {
		res, err := s.client.SearchExcludingSession(in.Query, "sessions", filter, s.sessionRef, limit)
		if err != nil {
			return nil, nil, err
		}
		res.Sessions = withoutSession(res.Sessions, s.sessionRef)
		var out strings.Builder
		for _, h := range res.Sessions {
			out.WriteString(renderHit(h))
		}
		return textResult(out.String()), nil, nil
	}
	sessions, err := s.client.Sessions(filter, limit)
	if err != nil {
		return nil, nil, err
	}
	var out strings.Builder
	for _, se := range sessions {
		out.WriteString(renderSession(se))
	}
	return textResult(out.String()), nil, nil
}

// snippetChars is what a hit shows of a body. Long enough to tell two entries
// apart, short enough that twenty hits stay readable — the archive holds plans
// of nine thousand characters, and a search that returned them whole was how a
// single query came to fifty-six thousand.
const snippetChars = 280

func knowledgeLabel(k store.Knowledge) string {
	activationLabel := "none"
	if k.Type == "instruction" {
		var activationParts []string
		if len(k.Activation.Paths) > 0 {
			activationParts = append(activationParts, "paths:"+strings.Join(k.Activation.Paths, ","))
		}
		if len(activationParts) > 0 {
			activationLabel = strings.Join(activationParts, ";")
		}
	}
	source := k.SessionRef
	if source == "" {
		source = k.Origin
	}
	label := fmt.Sprintf("type:%s|scope:%s|status:%s|confidence:%s|activation:%s|source:%s",
		k.Type, scopeLabel(k.Scope), k.Status, k.Confidence, activationLabel, source)
	if provenance := store.KnowledgeProvenance(k); provenance != "" {
		label += "|" + provenance
	}
	return label
}

func renderKnowledge(k store.Knowledge) string {
	body := oneLine(k.Body)
	if len([]rune(body)) > snippetChars {
		body = string([]rune(body)[:snippetChars]) + "…"
	}
	return fmt.Sprintf("- #%d [%s] %s — %s\n", k.ID, knowledgeLabel(k), k.Title, body)
}

// renderKnowledgeFull answers the other question: not "which entries are there"
// but "what does this one say". The body goes out untouched — the newlines,
// bullets and fenced blocks it was written with are the entry, not decoration
// around it.
func renderKnowledgeFull(k store.Knowledge) string {
	header := fmt.Sprintf("# #%d %s\n[%s]", k.ID, k.Title, knowledgeLabel(k))
	if k.ObservedAt != "" {
		header += " observed:" + k.ObservedAt
	}
	return header + "\n\n" + k.Body + "\n"
}

func renderSession(se store.Session) string {
	return fmt.Sprintf("- #%d %s %s %s (%s)\n", se.ID, se.Harness, se.Scope.Project, se.Scope.Branch, se.LastSeenAt)
}

func renderHit(h store.SessionHit) string {
	return fmt.Sprintf("- #%d %s %s %s (%s) — %s\n", h.Session.ID, h.Session.Harness,
		h.Session.Scope.Project, h.Session.Scope.Branch, h.Session.LastSeenAt, oneLine(h.Snippet))
}

func scopeLabel(ax scope.Axes) string { return ax.Label() }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
