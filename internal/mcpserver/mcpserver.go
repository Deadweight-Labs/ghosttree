// Package mcpserver exposes ghosttree to agents as four MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	client *client.Client
	// searchBudget is the time one area of context_search may take; zero is
	// defaultAreaBudget.
	searchBudget   time.Duration
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
	// coordOverride ersetzt sessionRef ausschließlich in coordRef().
	coordOverride string
	// agentRole ist die beim Start per ctx claude --role angeforderte Rolle.
	// Der Server kappt sie live am Rang des Kontos; leer lässt sie bei member.
	agentRole string
	// sessionUUID ist die Transkript-Session, wenn der Launcher sie vorgegeben hat.
	sessionUUID string
	// lineageOf liefert die Vorfahren eines Branches aus dem lokalen Repo
	// (SetLineageResolver); nil ohne Repo.
	lineageOf func(branch string) []string
	// live liefert den Git-Stand des Arbeitsverzeichnisses jetzt (SetLive).
	live func() Live
	// usageHarness and reportUses: see usage.go.
	usageHarness string
	reportUses   func(store.ToolUseBatch)
	// profile says which core tools are loaded up front; the zero value is full.
	profile profile.Profile
}

// SetProfile sets the profile of the session (GHOSTTREE_PROFILE). It decides
// which tools carry alwaysLoad in the tool list and what the server instructions
// say; every tool stays listed and callable under every profile.
func (s *Server) SetProfile(p profile.Profile) { s.profile = p }

// SetLineageResolver setzt, wie die Vorfahren eines anderen Branches als des
// ausgecheckten bestimmt werden. Als Rückruf, weil git in collector sitzt.
func (s *Server) SetLineageResolver(f func(branch string) []string) { s.lineageOf = f }

// SetRepoRoot wird von cmd/ctx/mcp.go aus dem aufgelösten Git-Kontext gesetzt.
func (s *Server) SetRepoRoot(root string) { s.repoRoot = root }

func (s *Server) SetAfterWrite(f func()) { s.afterWrite = f }

func (s *Server) SetAfterSnapshot(f func(context.Context, string) error) { s.afterSnapshot = f }

func (s *Server) SetSessionRef(ref string) { s.sessionRef = ref }

// SetCoordRef setzt die Koordinationsidentität, wenn sie von der
// Harness-Session abweicht (ctx claude). Suche, unterbrochene Arbeit und
// Snapshots bleiben bei sessionRef, denn nur die hat ein Transkript.
func (s *Server) SetCoordRef(ref string) { s.coordOverride = ref }

// SetAgentRole setzt die angeforderte Rolle dieser Session (lead, member, guest).
func (s *Server) SetAgentRole(role string) { s.agentRole = role }

// SetSessionID setzt die vom Launcher vorgegebene Session-UUID, die der Agent
// bei der Anmeldung meldet.
func (s *Server) SetSessionID(id string) { s.sessionUUID = id }

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
	Query string `json:"query,omitempty" jsonschema:"what to search for: words, qualifiers or both, for example is:open label:bug area:internal/store. Omit only when reading one entry by knowledge_id. The qualifier list is in the tool description"`
	// The id comes back on every knowledge hit, so reading one entry in full is
	// a second call rather than a guess. Same shape as session_id on
	// context_sessions: list or search, or name one and read it whole.
	KnowledgeID int64  `json:"knowledge_id,omitempty" jsonschema:"read this knowledge entry in full instead of searching. The id is on every hit; use it when a snippet is not enough, for instance for a stored plan or spec"`
	Kind        string `json:"kind,omitempty" jsonschema:"knowledge, requests, sessions, files or all (default all, which leaves out sessions: ask for them with sessions); with qualifiers also pains, ideas, criteria, tasks and messages (room messages you may read). \"files\" searches the per-file and per-directory descriptions of this repository — use it to find a file by what it does rather than by what it is called"`
	AllBranches bool   `json:"all_branches,omitempty" jsonschema:"search across all branches of the project"`
	Project     string `json:"project,omitempty" jsonschema:"search another project instead of the current one, given as a normalized remote like github.com/owner/repo"`
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"search every project. Use when a problem here may already have been solved elsewhere; the default stays the current project"`
	Machine     string `json:"machine,omitempty" jsonschema:"restrict to a machine hostname"`
	// Branch, Commit and PR filter by where an entry was written (REQ-597).
	Branch string `json:"branch,omitempty" jsonschema:"search as on this branch: entries filed on it and on the branches it descends from (derived from the local repository). Default is the current branch"`
	Commit string `json:"commit,omitempty" jsonschema:"only entries written at this commit: a prefix of at least 7 hexadecimal characters of the commit that was checked out when the entry was written. Sessions carry no commit and are left out"`
	PR     int64  `json:"pr,omitempty" jsonschema:"only entries whose commit belongs to this pull request number. Entries carry a pull request number only once the GitHub connection assigns it; until then this finds nothing"`
	// Since, Until and Area filter by when and where an entry arose (REQ-600).
	Since string   `json:"since,omitempty" jsonschema:"only entries written on or after this time: a date (2026-11-01), an RFC3339 time or a relative one such as 7d, 2w, 12h. Entries filed before the origin was recorded count by their creation time"`
	Until string   `json:"until,omitempty" jsonschema:"only entries written on or before this time, same formats as since; a date includes that whole day"`
	Area  []string `json:"area,omitempty" jsonschema:"only entries that arose in this area: a path prefix relative to the repository root, such as internal/store. Repeat for several, an entry matching any of them counts. Entries without a recorded origin area are left out"`
	// Limit was offered by the shared field list of the core tool (sessions takes
	// it) and refused by the search itself.
	Limit int `json:"limit,omitempty" jsonschema:"maximum results per area (default 20, at most 50)"`
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
	} else if in.Branch != "" && in.Branch != ax.Branch {
		// Another branch than the checked-out one: its ancestors are asked of the
		// local repository. Without a resolver, or without that branch locally,
		// the chain stays empty and the search reads project-wide plus that branch.
		ax.Branch = in.Branch
		ax.Lineage = nil
		if s.lineageOf != nil {
			ax.Lineage = s.lineageOf(in.Branch)
		}
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
	ScopeHint string `json:"scope_hint" jsonschema:"where this belongs, required (a judgement only you can make): project, branch, machine or global. Branch only if it stops being true once the branch is merged or abandoned (a migration in flight, a temporary flag, a workaround for something only this branch broke); anything that outlives the branch is project, and most things do. A branch entry is read by this branch and branches cut from it later, never by a sibling. Machine is for facts about the host alone and no repository, global for how you work anywhere. Running a repository on a host is BOTH: project plus machine."`
	Project   string `json:"project,omitempty" jsonschema:"file it under this project instead of the session's own: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to. For a problem with another project's tool, work or code. Applies to scope_hint project or branch"`
	Machine   string `json:"machine,omitempty" jsonschema:"with scope_hint machine: file it for this machine instead of the one you are on. With scope_hint project: the machine this project knowledge is about (use \"here\" for the one you are on); the entry stays a project entry, seen by everyone with a role in the project, and the machine only marks where it applies. Any machine name, including one no agent ever ran on, such as an ssh target. Not with branch or global"`
	// Supersedes makes this entry the replacement of older ones, so the old ones
	// leave search and the briefing instead of standing next to a follow-up.
	Supersedes      []int64 `json:"supersedes,omitempty" jsonschema:"ids of older entries this one replaces because the state they describe changed. They stop appearing in search and the briefing but stay readable through their history. You need the right to edit each of them, and they must belong to the same project. Search first to find them"`
	SupersedeReason string  `json:"supersede_reason,omitempty" jsonschema:"required with supersedes: what changed, in one sentence"`
	Explanation     string  `json:"explanation,omitempty" jsonschema:"optional \"Kurz erklärt\" for the people who review it: two to four plain sentences in their language, in everyday words: what it is about, why it matters, what has to be done or decided, your recommendation. No markdown or links, never a secret; without it the server writes one later from the text alone"`
	// ShareWithOrgs gibt einen Maschinen-Eintrag gleich an Orgs frei.
	ShareWithOrgs []string `json:"share_with_orgs,omitempty" jsonschema:"with scope_hint machine (no project): also share the entry with these organizations (slugs), for which a person has already shared the machine. Without it a machine entry stays private to the machine's owner; on a machine that belongs to an organization it goes to that organization. Not for an entry with a project: that one is seen through the project"`
}

// RetireInput is the context_retire tool.
type RetireInput struct {
	ID     int64  `json:"id" jsonschema:"the knowledge entry to retire"`
	Reason string `json:"reason" jsonschema:"required: why the entry is no longer true or needed, for example that the thing it describes was removed"`
}

// SupersedeInput is the context_supersede tool.
type SupersedeInput struct {
	ID         int64   `json:"id" jsonschema:"the existing entry that replaces the older ones"`
	Supersedes []int64 `json:"supersedes" jsonschema:"ids of the older entries it replaces because the state they describe changed"`
	Reason     string  `json:"reason" jsonschema:"required: what changed, in one sentence"`
}

type SessionsInput struct {
	Query       string `json:"query,omitempty" jsonschema:"full text search across session transcripts"`
	SessionID   int64  `json:"session_id,omitempty" jsonschema:"read this session instead of listing"`
	SessionRef  string `json:"session_ref,omitempty" jsonschema:"read the session with this address (listed as @address when no number is shown) instead of listing"`
	Project     string `json:"project,omitempty" jsonschema:"list or search another project's sessions, given as a normalized remote like github.com/owner/repo"`
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"cover every project instead of the current one"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum results (default 20)"`
}

func (s *Server) Register(srv *mcp.Server) {
	srv.AddReceivingMiddleware(s.coreMiddleware)
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_create", Description: "Create an immutable named snapshot of the bound project's durable context and observed Git state.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleSnapshotCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_list", Description: "List immutable context snapshot metadata for the bound project, with an opaque cursor and a maximum limit of 100.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleSnapshotList)
	mcp.AddTool(srv, &mcp.Tool{Name: "context_snapshot_get", Description: "Read one snapshot safely: no filter returns only head and counts, domain returns at most 100 summaries, and domain plus key returns exactly one payload.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleSnapshotGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_search",
		Description: "Search ghosttree knowledge, requests and file descriptions, or read one knowledge entry in full. The default search does not include session transcripts; ask for them with kind=sessions (or is:session in the query). Hits are snippets and carry an id; pass knowledge_id to get that entry's whole body back verbatim, which is how stored plans, specs and other long documents are read. Defaults to the current project, branch and machine context. Set project to search another repository, or all_projects to search every one of them — worth doing when a problem here may already have been solved elsewhere. " + searchLanguageHelp,
	}, s.handleSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_get",
		Description: "Get the context package for the current project, branch and machine. With repository-relative paths, return the descriptions of those paths and their ancestor directories instead of repeating the general bootstrap.",
	}, s.handleGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_remember",
		Description: "Store a pitfall, decision, note or plan in ghosttree instead of writing it into the source tree. A decision should record why it was taken, which alternatives were rejected and what the tradeoffs are, so it can be revisited later. Search with context_search first: if the entry would only update a state that is already recorded (\"X created\" followed by \"X removed again\"), pass supersedes=[the old ids] with supersede_reason instead of writing a follow-up that leaves both active. If what the old entry describes no longer exists and there is nothing new to record, use context_remember action=retire. Machine knowledge that belongs to a repository (running, deploying or debugging that repo on a host) gets BOTH project and machine (scope_hint project plus machine=<name>), so everyone with a role in the project reads it; only knowledge that concerns the host alone gets the machine without a project and stays private to its owner. Add explanation, a short plain-language note for the people who review it: they change things but do not read every agent finding in depth, so say what it is about, why it matters and what you recommend." + targetHint,
	}, s.handleRemember)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_retire",
		Description: "Retire a knowledge entry because what it describes no longer exists or is no longer true, and there is nothing new to record (the resource was removed, the workaround is obsolete). It leaves search and the briefing and stays readable through its history. A reason is required. It needs the same right as editing the entry in the web. If the state changed rather than vanished, write the new state with context_remember and supersedes instead.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed},
	}, s.handleRetire)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_supersede",
		Description: "Mark an entry that already exists as the replacement of older entries, when you notice only afterwards that it makes them obsolete (to do it while writing, pass supersedes to context_remember). The older entries leave search and the briefing and stay readable through their history. A reason is required. You need the right to edit the replacement and each older entry, and all of them must belong to the same project and scope.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed},
	}, s.handleSupersede)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_move",
		Description: "Move a knowledge entry to another project, to another machine, or to global, when it was filed in the wrong place. You need write access to where it is and to where it goes. The move is recorded. To move a request, use request with action=correct and project.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed},
	}, s.handleMove)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_sessions",
		Description: "List or search past agent sessions, or read one session's transcript. Defaults to the current project; set project or all_projects to look further.",
	}, s.handleSessions)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_describe_file",
		Description: "Record what a file or directory does that a reader cannot see in the code (its intent, its limits, how it hangs together with other parts, what went wrong here), stored against its repository-relative path instead of as a comment in the source. The gardener describes new and changed files itself after a merge, from the code, the diff and the pull request text; call this only when you know something it cannot read there, typically where you would otherwise write an explanatory comment. What you write is kept word for word when the gardener revises the description. Ghost descriptions are browsable as real files under .ghosttree/tree/. If you have read the path and there is nothing to say that is not already in the code, pass nothing_to_say instead of writing a description that restates the source — an empty entry costs nothing, a restatement costs trust in every other description.",
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
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_peers", Description: "Who else is working in this repository or on this machine — independently started sessions, not subagents you spawned. Nobody here is anyone's parent. Use it before changing something another agent is likely to be in the middle of.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCoordPeers)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_status", Description: "Say in one sentence what you are working on (a PR, a branch, a goal), so that other agents and people see it in coord action=peers and next to your messages. Set it when you start work on a PR or branch, update it when that changes, clear it (empty note) when you are done. It is shown as self-reported and unverified; no secrets.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleCoordStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_send", Description: "Say something to the other agents in this repository or on this machine: a contract you are about to change, a service you restarted, a question for whoever owns a path. For a question to people (intent question) add explanation, so they can judge it without reading the whole history. Stored durably. It reports stored, never delivered — whether a running session is interrupted for it depends on that session's harness.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleCoordSend)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_inbox", Description: "Read what other agents have said since you last looked. Continues from your stored cursor by default, so calling it twice does not repeat itself. Messages past their expiry are shown as history and marked — an old \"restarting in 10 seconds\" is not an instruction today.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCoordInbox)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_touched", Description: "Has anyone else worked on this path lately? Ask before changing something another session may be in the middle of. It distinguishes the same checkout from a separate worktree, because those are different risks. Nothing is locked and no answer is a clean bill of health — absence of observation is not proof that nobody is there.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCoordTouched)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_dm", Description: "Talk to one peer or a small group privately, without involving the whole project room. Nobody outside the conversation can read it — not through search, not through summaries. Use it when the exchange only concerns the people in it.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleCoordDM)
	mcp.AddTool(srv, &mcp.Tool{Name: "coord_dm_read", Description: "Read a private conversation, or list the ones you are part of. Continues from your cursor.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCoordDMRead)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_open", Description: "Start a lasting discussion about one question — something that outlives this session and that a later agent can pick up. Attach it to the knowledge entry, request or document it is about; a request link must be an existing request of this project (REQ-n or the number), otherwise nothing is opened. Not for chatter: a question nobody will ask again belongs in coord action=send. If it belongs to a different project than the current one, pass project.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleThreadOpen)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_read", Description: "Read where a discussion stands: the working state, what is still open, what came out of it, and the posts since the summary's reach. It tells you what it left out and how to get it — it never silently truncates.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleThreadRead)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_reply", Description: "Add a contribution to an existing discussion. Disagreeing with what is already there is the useful case — two agents agreeing does not make a guess a fact.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleThreadReply)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_find", Description: "Find discussions by words or, better, by the object they hang on — before opening a second thread about a question someone already investigated.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleThreadFind)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_resolve", Description: "Close a discussion because the question is answered, park it as deferred, or reopen it. This closes the discussion, never the work: linked requests keep their own criteria and evidence.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleThreadResolve)
	mcp.AddTool(srv, &mcp.Tool{Name: "thread_propose", Description: "Record what a discussion suggests should become a decision, pitfall or request. It is a proposal, not knowledge: promoting it takes its own write and the usual review. A rejected proposal stays as a result, so the same idea is not re-argued in three months.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleThreadPropose)
	mcp.AddTool(srv, &mcp.Tool{Name: "comment_add", Description: "Write a comment on an issue (REQ-n), an idea (IDEA-n) or a pain (PAIN-n) you can read. Comments are for what people reading the object should know: a finding, a decision, a question that is not an open decision. Members may comment, guests only read. It stays attributed to you and to the commit you are on. Earlier versions stay visible to every reader; delete what must no longer be seen.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleCommentAdd)
	mcp.AddTool(srv, &mcp.Tool{Name: "comment_list", Description: "Read the comments on an issue (REQ-n), an idea (IDEA-n) or a pain (PAIN-n), oldest first, with author and time. A deleted comment stays as a placeholder without text. Earlier versions stay visible to every reader; delete what must no longer be seen.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCommentList)
	mcp.AddTool(srv, &mcp.Tool{Name: "pull_list", Description: "List the pull requests GitHub reported for a project, most recently changed first: state (open, merged, closed), head and base branch, author, number of commits. Filter by state, branch or author; pages of at most 100. Needs read access to the project.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePullList)
	mcp.AddTool(srv, &mcp.Tool{Name: "pull_get", Description: "Read one pull request: its state, its commits, the work package bound to it (required requests with open criteria, human tasks, uncovered requirements), the feature checklist (is each touched feature's description up to date?) and what was said about it (knowledge and ghost paths anchored to it).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePullGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "reaction_set", Description: "React with a fixed emoji (+1, -1, laugh, hooray, confused, heart, rocket, eyes) to a comment (comment:n), an idea (IDEA-n) or an issue (REQ-n) you can read; members only. Setting it twice changes nothing; remove=true takes it back. Use it for a quick yes or acknowledgement instead of a comment that says nothing more.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleReactionSet)
	mcp.AddTool(srv, &mcp.Tool{Name: "reaction_list", Description: "The reactions on a comment (comment:n), an idea (IDEA-n) or an issue (REQ-n) you can read: which ones, how many, whether you set them, and whether you may react.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleReactionList)
	mcp.AddTool(srv, &mcp.Tool{Name: "commit_get", Description: "What was said about a commit: the ledger entries (knowledge, issues, pains, ideas, features, ghost descriptions, comments) whose git anchor is this commit, as far as you may read them, plus the title and author when the commit belongs to a pull request. Pass the hash or its first 7 characters. The server knows a commit only from anchors and from pull requests, never from the code.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleCommitGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_search", Description: "List or search the current project's work ledger. Works like listing issues: call it with no query to see what is open, or name a subject to narrow. A question that names no subject — \"what is left to do\" — returns the list rather than guessing. Answers with a compact list; call request action=get for one entry's full text. Use it before substantial feature, architecture, migration, or multi-session work.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleRequestSearch)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_get", Description: "Get one request as readable text with its original paragraphs. Concise includes the request, description, criteria, relations, and latest work; it omits activity, older work, and criterion evidence. Use response_format=detailed for the complete history and all evidence.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleRequestGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_create", Description: "Create a ledger entry for substantial work when request action=search found no match. Include observable acceptance criteria; do not use for trivial local fixes. A request always belongs to a project: the session's own, or the one you pass as project. Add explanation, a short plain-language note for the people who will read the ledger.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRequestCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_start_work", Description: "Associate a Ghosttree session with an existing request as its primary task or as related work. Repeating the same association is safe.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleRequestStartWork)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_finish_work", Description: "End a session's work association with a paused, completed, or abandoned outcome and a concise handoff. This does not complete the request.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleRequestFinishWork)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_record_progress", Description: "Record evidenced request progress: add or satisfy criteria, reopen a met or waived criterion that its evidence does not hold up (criterion_reopen, needs a reason), complete or drop the request, add or remove a relation, or correct what the request says. Completion without evidence or with open criteria is rejected. A correction and a relation removal each need a reason, which goes into the activity list — nothing changes silently. Answers with what changed and how many criteria remain — call request action=get for the full picture. To move a request to another project, use correct with project and a reason.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRequestProgress)
	s.registerPainTools(srv)
	s.registerHumanTaskTools(srv)
	s.registerPackageTools(srv)
	s.registerQueryTools(srv)
	s.registerRunbookTools(srv)
	s.registerLinkTools(srv)
	s.registerFeatureTools(srv)
	s.registerMachineTools(srv)
}

func Run(ctx context.Context, s *Server, version string) error {
	return s.NewMCP(version).Run(ctx, &mcp.StdioTransport{})
}

// NewMCP is the MCP server with ghosttree's tools registered and the
// instructions the profile calls for.
func (s *Server) NewMCP(version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghosttree", Version: version},
		&mcp.ServerOptions{Instructions: s.instructions()})
	s.Register(srv)
	return srv
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

// maxSearchLimit caps what one search may ask for per area.
const maxSearchLimit = 50

func (in SearchInput) resultLimit() int {
	switch {
	case in.Limit <= 0:
		return searchLimit
	case in.Limit > maxSearchLimit:
		return maxSearchLimit
	}
	return in.Limit
}

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
		return s.withCard(textResult(renderKnowledgeFull(k)), s.builder().KnowledgeCard(k)), nil, nil
	}
	// Neither an id nor words: nothing to answer, and saying so beats returning
	// the whole archive or an empty list that reads like "there is nothing".
	if strings.TrimSpace(in.Query) == "" {
		return nil, nil, fmt.Errorf("give a query to search for, or a knowledge_id to read one entry in full")
	}
	anchor := client.AnchorFilter{Commit: gitanchor.Normalize(in.Commit), PR: in.PR}
	if anchor.Commit != "" && !gitanchor.ValidPrefix(anchor.Commit) {
		return nil, nil, fmt.Errorf("commit needs at least %d hexadecimal characters", gitanchor.MinPrefix)
	}
	if in.PR < 0 {
		return nil, nil, fmt.Errorf("pr must be a positive pull request number")
	}
	anchor.Origin = client.OriginFilter{Since: in.Since, Until: in.Until, Areas: in.Area}
	if anchor.Origin.Active() {
		// Gleiche Prüfung wie am Server, damit der Fehler vor dem Aufruf kommt.
		if _, err := anchor.Origin.Resolve(time.Now()); err != nil {
			return nil, nil, err
		}
	}
	parsed, err := query.Parse(in.Query, time.Now())
	if err != nil {
		return nil, nil, err
	}
	if parsed.Qualified() {
		return s.qualifiedSearch(in, parsed, anchor)
	}
	anchored := anchor.Commit != "" || anchor.PR != 0 || anchor.Origin.Active()
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
	limit := in.resultLimit()
	b := s.builder()
	var areas []searchArea
	if kind == "knowledge" || kind == "all" {
		areas = append(areas, searchArea{name: "knowledge", run: func(ctx context.Context) (string, error) {
			// The scope union is what a session reads here. Across projects that
			// union is meaningless, so the axes are matched as given instead.
			res, err := s.client.SearchContext(ctx, in.Query, "knowledge", ax, "", limit, !crossProject, anchor)
			if err != nil || len(res.Knowledge) == 0 {
				return "", err
			}
			var out strings.Builder
			out.WriteString("## knowledge\n")
			shortened := false
			for _, k := range res.Knowledge {
				out.WriteString(hitLink(renderKnowledge(k), b.WithProject(k.Scope.Project).Knowledge(k.ID)))
				shortened = shortened || len([]rune(oneLine(k.Body))) > snippetChars
			}
			// Only said when something was actually held back. A pointer to the
			// rest on a hit that already showed all of it is noise, and a
			// shortened hit that says nothing is the entry lying about its size.
			if shortened {
				out.WriteString("(an entry ending in … is shortened; call context_search with knowledge_id=<id> for its full text)\n")
			}
			out.WriteString(limitNote(len(res.Knowledge), limit))
			return out.String(), nil
		}})
	}
	// The default search (all) leaves sessions out (decision #4447); kind=sessions asks for them.
	if kind == "sessions" && !anchored {
		areas = append(areas, searchArea{name: "sessions", run: func(ctx context.Context) (string, error) {
			// Sessions carry all three axes, so an exact filter on the full context
			// would only ever match this very session; project is the useful scope.
			// An empty project means every project, which is what all_projects asks for.
			filter := scope.Axes{Project: ax.Project, Machine: in.Machine}
			res, err := s.client.SearchContext(ctx, in.Query, "sessions", filter, s.sessionRef, limit, false, client.AnchorFilter{})
			if err != nil {
				return "", err
			}
			res.Sessions = withoutSession(res.Sessions, s.sessionRef)
			if len(res.Sessions) == 0 {
				return "", nil
			}
			var out strings.Builder
			out.WriteString("## sessions\n")
			for _, h := range res.Sessions {
				out.WriteString(hitLink(renderHit(h), s.sessionURL(h.Session)))
			}
			out.WriteString(limitNote(len(res.Sessions), limit))
			return out.String(), nil
		}})
	}
	if kind == "requests" || kind == "all" {
		areas = append(areas, searchArea{name: "requests", run: func(ctx context.Context) (string, error) {
			filter := scope.Axes{Project: s.ctxAxes.Project}
			res, err := s.client.SearchContext(ctx, in.Query, "requests", filter, "", limit, false, anchor)
			if err != nil || len(res.Requests) == 0 {
				return "", err
			}
			var out strings.Builder
			out.WriteString("## requests\n")
			for _, h := range res.Requests {
				out.WriteString(hitLink(fmt.Sprintf("- [request|status=%s|type=%s|source=ledger%s] REQ-%d %s — %s\n", h.Request.State, h.Request.Type, originctx.Label(h.OriginContext, h.Request.CreatedAt), h.Request.ID, h.Request.Title, h.MatchReason),
					b.WithProject(h.Request.Scope.Project).Issue(h.Request.ID)))
			}
			out.WriteString(limitNote(len(res.Requests), limit))
			return out.String(), nil
		}})
	}
	if kind == "files" || kind == "all" {
		areas = append(areas, searchArea{name: "files", run: func(ctx context.Context) (string, error) {
			// Der Ghost-Baum ist projektgebunden: ein Pfad ist nur im Repo
			// eindeutig, zu dem er gehört.
			res, err := s.client.SearchGhostsContext(ctx, in.Query, ax.Project, limit, anchor)
			if err != nil || len(res) == 0 {
				return "", err
			}
			var out strings.Builder
			out.WriteString("## files\n")
			for _, g := range res {
				out.WriteString(renderGhostHit(g))
			}
			return out.String(), nil
		}})
	}
	text, err := runSearchAreas(ctx, areas, s.areaBudget())
	if err != nil {
		return nil, nil, err
	}
	return textResult(text), nil, nil
}

func (s *Server) handleGet(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, any, error) {
	actx := s.baseActivation
	actx.Paths = append([]string(nil), in.Paths...)
	normalized, err := activation.NormalizeContext(actx)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Paths) > 0 {
		ghosts, err := s.client.GhostTreeAt(s.ctxAxes.Project, "", s.currentAxes())
		if err != nil {
			return nil, nil, err
		}
		text := renderGhostContext(ghosts, normalized.Paths)
		// Die Features, in deren Bereichen diese Pfade liegen (REQ-612), kurz und
		// mit Budget. Ein Fehler kostet nur diesen Abschnitt.
		if fc, err := s.client.FeatureContext(s.ctxAxes.Project, normalized.Paths, "", 0); err == nil && fc.Text != "" {
			text += fc.Text
		}
		return textResult(text), nil, nil
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

// rememberMachine resolves the machine named for a project entry: "here" is the
// session's own machine. An empty result means there is none to name.
func (s *Server) rememberMachine(name string) string {
	if strings.TrimSpace(name) == "here" {
		return s.ctxAxes.Machine
	}
	return scope.CanonicalAxes(scope.Axes{Machine: name}).Machine
}

func (s *Server) handleRemember(ctx context.Context, _ *mcp.CallToolRequest, in RememberInput) (*mcp.CallToolResult, any, error) {
	if in.Type == "" || in.Title == "" {
		return nil, nil, fmt.Errorf("type and title are required")
	}
	k := store.Knowledge{Type: in.Type, Title: in.Title, Body: in.Body, Harness: "mcp", SessionRef: s.sessionRef, Explanation: in.Explanation}
	autoCtx := s.ctxAxes
	// Placement is asked for rather than defaulted. A default put everything on
	// the branch and stranded 127 entries; the correction put everything on the
	// project, which is right more often and still nobody's decision. Where a
	// thing belongs is a judgement about how long it stays true, and only the
	// writer is in a position to make it.
	//
	// Neither is there a fallback for a missing project: an entry that cannot
	// say where it belongs is refused, because the old fallback (the machine)
	// filed 120 project notes under mainex.
	switch in.ScopeHint {
	case "project":
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, nil, err
		}
		k.Scope = scope.Axes{Project: project}
		if in.Machine != "" {
			// Knowledge about a repository on a host: the machine is where it applies,
			// the project decides who reads it.
			machine := s.rememberMachine(in.Machine)
			if machine == "" {
				return nil, nil, fmt.Errorf("this session has no machine identity; pass machine=<name>")
			}
			k.Scope.Machine = machine
		}
	case "branch":
		if in.Machine != "" {
			return nil, nil, fmt.Errorf("machine goes with scope_hint project (knowledge about a repository on a host) or machine (a fact about the host alone), not with branch")
		}
		if s.ctxAxes.Branch == "" {
			return nil, nil, fmt.Errorf("scope_hint branch, but this session is not on a named branch; use project or machine")
		}
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, nil, err
		}
		if project != s.ctxAxes.Project {
			return nil, nil, fmt.Errorf("a branch entry belongs to this session's branch of %s; for %s use scope_hint project", s.ctxAxes.Project, project)
		}
		k.Scope = scope.Axes{Project: project, Branch: s.ctxAxes.Branch}
	case "machine":
		if in.Project != "" {
			return nil, nil, fmt.Errorf("a machine entry is not tied to a project; drop project. Knowledge about a repository on a machine is a project entry that names the machine: scope_hint project with machine")
		}
		machine := scope.CanonicalAxes(scope.Axes{Machine: in.Machine}).Machine
		if machine == "" {
			machine = s.ctxAxes.Machine
		}
		if machine == "" {
			return nil, nil, fmt.Errorf("this session has no machine identity; pass machine=<name>")
		}
		k.Scope = scope.Axes{Machine: machine}
	case "global":
		if in.Project != "" || in.Machine != "" {
			return nil, nil, fmt.Errorf("a global entry applies everywhere; drop project and machine, or use scope_hint project or machine")
		}
		// Empty scope plus empty context: the server's write defaults resolve
		// to global rather than filling anything in.
		autoCtx = scope.Axes{}
	case "":
		return nil, nil, fmt.Errorf("scope_hint is required: project, branch, machine or global. " +
			"Ask whether this stops being true once the branch is merged or abandoned — if yes it is branch, otherwise project")
	default:
		return nil, nil, fmt.Errorf("unknown scope_hint %q; use project, branch, machine or global", in.ScopeHint)
	}
	if len(in.Supersedes) > 0 && strings.TrimSpace(in.SupersedeReason) == "" {
		return nil, nil, fmt.Errorf("supersede_reason is required with supersedes: say what changed")
	}
	if len(in.Supersedes) == 0 && in.SupersedeReason != "" {
		return nil, nil, fmt.Errorf("supersede_reason only goes with supersedes")
	}
	if len(in.ShareWithOrgs) > 0 && in.ScopeHint != "machine" {
		return nil, nil, fmt.Errorf("share_with_orgs only goes with scope_hint machine, an entry without a project; an entry with a project is seen through the project")
	}
	saved, err := s.client.RememberShared(k, autoCtx, in.Supersedes, in.SupersedeReason, in.ShareWithOrgs)
	if err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("stored %s [%s|%s]", knref.Format(saved.ID), saved.Type, scopeLabel(saved.Scope))
	if len(in.Supersedes) > 0 {
		msg += fmt.Sprintf("; superseded %s", idList(in.Supersedes))
	}
	if hint := reasoningHint(saved); hint != "" {
		msg += "\n" + hint
	}
	return s.withCard(textResult(msg), s.builder().KnowledgeCard(saved)), nil, nil
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
	if in.SessionID != 0 || in.SessionRef != "" {
		chunks, err := s.client.ReadSessionRef(store.SessionRef{ID: in.SessionID, PublicID: in.SessionRef}, 0, limit)
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
		return s.withSessionCard(textResult(out.String()), in), nil, nil
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
	source := sessionSource(k.SessionRef)
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

// publicAddressPattern: zwölf Zeichen aus dem Alphabet der Session-Adressen
// (ohne l, o, 0, 1).
var publicAddressPattern = regexp.MustCompile(`^[a-km-np-z2-9]{12}$`)

var sessionSourcePattern = regexp.MustCompile(`^session:([A-Za-z0-9]+)#(\d+)$`)

// sessionSource writes a session reference so its parts paste straight into
// context_sessions: session_ref takes the bare address, session_id the number.
func sessionSource(ref string) string {
	m := sessionSourcePattern.FindStringSubmatch(ref)
	if m == nil {
		return ref
	}
	key := "session_ref"
	// Am Format erkennen: eine Adresse, die zufällig nur aus Ziffern besteht,
	// bleibt eine Adresse.
	if _, err := strconv.ParseInt(m[1], 10, 64); err == nil && !publicAddressPattern.MatchString(m[1]) {
		key = "session_id"
	}
	return "session|" + key + ":" + m[1] + "|chunk:" + m[2]
}

func renderKnowledge(k store.Knowledge) string {
	body := oneLine(k.Body)
	if len([]rune(body)) > snippetChars {
		body = string([]rune(body)[:snippetChars]) + "…"
	}
	label := knowledgeLabel(k)
	if k.Match != "" {
		// Found by meaning, not by the words of the query: say so, the reader
		// should weigh it differently from an exact word hit.
		label += "|match:" + k.Match
	}
	return fmt.Sprintf("- %s [%s%s%s] %s — %s\n", knref.Format(k.ID), label, anchorLabel(k.GitAnchor), originctx.Label(k.OriginContext, k.CreatedAt), k.Title, body)
}

// anchorLabel nennt den Git-Stand eines Treffers kurz: "|at:<commit>" mit
// Branch, "+dirty" bei unsauberem Arbeitsverzeichnis und #PR, wenn bekannt.
// Leer ohne Anker, damit Einträge von früher unverändert aussehen.
func anchorLabel(a *gitanchor.Anchor) string {
	if a == nil || a.IsZero() {
		return ""
	}
	out := "|at:" + a.Short()
	if a.Dirty {
		out += "+dirty"
	}
	if a.Branch != "" {
		out += "@" + a.Branch
	}
	if a.PR != nil {
		out += fmt.Sprintf(" #%d", *a.PR)
	}
	return out
}

// renderKnowledgeFull answers the other question: not "which entries are there"
// but "what does this one say". The body goes out untouched — the newlines,
// bullets and fenced blocks it was written with are the entry, not decoration
// around it.
func renderKnowledgeFull(k store.Knowledge) string {
	header := fmt.Sprintf("# %s %s\n[%s%s%s]", knref.Format(k.ID), k.Title, knowledgeLabel(k), anchorLabel(k.GitAnchor), originctx.Label(k.OriginContext, k.CreatedAt))
	if k.ObservedAt != "" {
		header += " observed:" + k.ObservedAt
	}
	if k.Closure != nil {
		header += "\n" + k.Closure.Summary()
	}
	return header + "\n\n" + k.Body + "\n"
}

// sessionLabel nennt die Session, wie man sie wieder anspricht: Mitglieder mit
// der Nummer, Gäste (der Server nennt ihnen keine) mit der Adresse.
func sessionLabel(se store.Session) string {
	if se.ID == 0 && se.PublicID != "" {
		return "@" + se.PublicID
	}
	return fmt.Sprintf("#%d", se.ID)
}

func renderSession(se store.Session) string {
	return fmt.Sprintf("- %s %s %s %s (%s)\n", sessionLabel(se), se.Harness, se.Scope.Project, se.Scope.Branch, se.LastSeenAt)
}

func renderHit(h store.SessionHit) string {
	meta := fmt.Sprintf("%s %s %s %s (%s)", sessionLabel(h.Session), h.Session.Harness,
		h.Session.Scope.Project, h.Session.Scope.Branch, h.Session.LastSeenAt)
	if h.Match != "" {
		// Found by meaning, not by the words of the query: the snippet is the text of
		// the session that came closest, and the reader should weigh the hit that way.
		meta += " [match:" + h.Match + "]"
	}
	return fmt.Sprintf("- %s — %s\n", meta, oneLine(h.Snippet))
}

func scopeLabel(ax scope.Axes) string { return ax.Label() }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
