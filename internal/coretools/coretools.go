// Package coretools is the table of ghosttree's core MCP tools: at most twelve
// tools an agent sees, each with sub-actions that name the older, finer tools.
package coretools

import (
	"sort"
	"strings"
)

// Action is one sub-action of a core tool. Tool is the registered tool that
// serves it; Inner, when set, is the value its own action field gets. A Rare
// action adds no fields to the listed schema: its line names its parameters,
// and the registered tool still checks them exactly.
type Action struct {
	Name  string
	Tool  string
	Inner string
	Rare  bool
	Line  string
}

// Core is one tool in the list an agent sees.
type Core struct {
	Name string
	Lead string
	// Default is the action used when the call names none; empty means the
	// action is required.
	Default string
	Actions []Action
}

// MaxCore is the ceiling the list must stay under (REQ-436).
const MaxCore = 12

// Table returns the core tools in the order they are listed.
func Table() []Core { return table }

var table = []Core{
	{
		Name:    "context_search",
		Lead:    "Search what ghosttree knows before you write or re-derive it: knowledge (pitfalls, decisions, notes, plans), past session transcripts, file descriptions and the ledger. Defaults to this project, branch and machine; project or all_projects looks further.",
		Default: "search",
		Actions: []Action{
			{Name: "search", Tool: "context_search", Line: "words and/or qualifiers in query; knowledge_id reads one entry in full (plans, specs); kind narrows to knowledge, requests, sessions, files"},
			{Name: "sessions", Tool: "context_sessions", Line: "list or search past sessions (query), or read one transcript (session_id or session_ref)"},
			{Name: "file_history", Tool: "context_file_history", Line: "how the description of a path changed, as a diff (path; full for old wording)"},
			{Name: "preset_list", Tool: "search_preset", Inner: "list", Line: "saved searches"},
			{Name: "preset_run", Tool: "search_preset", Inner: "run", Line: "run one by name: pr-ergebnis (pr), branch-offen (branch), ordner (area), seit (since), fuer-mich"},
			{Name: "preset_save", Rare: true, Tool: "search_preset", Inner: "save", Line: "store a saved search (name, queries, scope)"},
			{Name: "preset_delete", Rare: true, Tool: "search_preset", Inner: "delete", Line: "remove a saved search (name)"},
		},
	},
	{
		Name:    "context_get",
		Lead:    "Read one thing directly: the context package for paths you work on, an object by its number, a commit, a pull request, a machine or a snapshot.",
		Default: "context",
		Actions: []Action{
			{Name: "context", Tool: "context_get", Line: "with paths: the descriptions of those paths and their directories; without: the briefing of this project, branch and machine"},
			{Name: "link", Tool: "open_link", Line: "web link and short card for REQ-n, TASK-n, PAIN-n, IDEA-n, #id, pr:n or preset:name (ref), to hand to the human"},
			{Name: "commit", Tool: "commit_get", Line: "what was said about a commit (commit: hash or 7+ characters)"},
			{Name: "pull", Tool: "pull_get", Line: "one pull request with its package, feature checklist and anchored knowledge (pr)"},
			{Name: "pulls", Rare: true, Tool: "pull_list", Line: "pull requests GitHub reported (state, branch, author)"},
			{Name: "machine", Rare: true, Tool: "machine_get", Line: "a machine, its owner and sharing (name; none lists yours)"},
			{Name: "snapshot_list", Rare: true, Tool: "context_snapshot_list", Line: "named context snapshots of this project"},
			{Name: "snapshot_get", Rare: true, Tool: "context_snapshot_get", Line: "one snapshot (name; domain and key for details)"},
		},
	},
	{
		Name:    "context_remember",
		Lead:    "Keep what you learned in ghosttree instead of in source comments: a pitfall, decision, note or plan, a file's description, or a correction of what is stored. Search first; when a known state changed, supersede the old entry instead of adding a follow-up.",
		Default: "remember",
		Actions: []Action{
			{Name: "remember", Tool: "context_remember", Line: "store an entry (type, title, body, scope_hint; explanation for reviewers; supersedes with supersede_reason replaces older ones). A decision says why, which alternatives were rejected, the tradeoffs. Repo knowledge about a host gets scope_hint project plus machine"},
			{Name: "describe_file", Tool: "context_describe_file", Line: "what a file or directory does, instead of an explanatory comment (path, description; nothing_to_say when the code says it all). Do it for every file you create"},
			{Name: "retire", Tool: "context_retire", Line: "an entry whose subject no longer exists (id, reason)"},
			{Name: "supersede", Tool: "context_supersede", Line: "mark an existing entry as the replacement of older ones (id, supersedes, reason)"},
			{Name: "move", Rare: true, Tool: "context_move", Line: "move an entry to another project, machine or global (id)"},
			{Name: "regression_cover", Rare: true, Tool: "context_regression_cover", Line: "which test keeps a fixed pitfall from returning (knowledge_id, state covered|uncovered|not_applicable, test); no arguments lists unguarded fixes"},
			{Name: "snapshot_create", Rare: true, Tool: "context_snapshot_create", Line: "immutable named snapshot of the project's context (name)"},
			{Name: "share_entry", Rare: true, Tool: "machine_share_entry", Line: "share a machine-scoped entry or runbook with an organization, or take it back (kind, id, org, unshare)"},
		},
	},
	{
		Name: "request",
		Lead: "The work ledger (issues): look for a matching request before substantial work, attach your session to it, and record progress with evidence. Trivial local fixes need no request.",
		Actions: []Action{
			{Name: "search", Tool: "request_search", Line: "list or search requests (query; state, type)"},
			{Name: "get", Tool: "request_get", Line: "one request in full (request_id; response_format detailed for history and evidence)"},
			{Name: "create", Tool: "request_create", Line: "new request when none matches (type, title, description, criteria; explanation)"},
			{Name: "start_work", Tool: "request_start_work", Line: "attach this session to a request (request_id)"},
			{Name: "finish_work", Tool: "request_finish_work", Line: "end the work with a handoff (work_id, state paused|completed|abandoned, summary); does not complete the request"},
			{Name: "criterion_met", Tool: "request_record_progress", Inner: "criterion_met", Line: "a criterion is met (request_id, criterion_id, evidence_kind commit|test|file|decision|session|url, evidence_ref)"},
			{Name: "criterion_add", Tool: "request_record_progress", Inner: "criterion_add", Line: "add a criterion (request_id, description)"},
			{Name: "criterion_waive", Tool: "request_record_progress", Inner: "criterion_waive", Line: "waive a criterion (request_id, criterion_id, reason)"},
			{Name: "criterion_reopen", Tool: "request_record_progress", Inner: "criterion_reopen", Line: "reopen a criterion whose evidence does not hold (request_id, criterion_id, reason)"},
			{Name: "complete", Tool: "request_record_progress", Inner: "complete", Line: "finish a request whose criteria are all met (request_id, evidence)"},
			{Name: "drop", Tool: "request_record_progress", Inner: "drop", Line: "drop a request (request_id, reason)"},
			{Name: "relation_add", Tool: "request_record_progress", Inner: "relation_add", Line: "relate requests: request_id <relation_kind> other_request_id (parent, related, blocks, duplicates, supersedes, derived, knowledge, external)"},
			{Name: "relation_remove", Tool: "request_record_progress", Inner: "relation_remove", Line: "remove a relation (request_id, relation_id, reason)"},
			{Name: "correct", Tool: "request_record_progress", Inner: "correct", Line: "correct title, description, type, priority or project (request_id, reason)"},
		},
	},
	{
		Name: "pain",
		Lead: "A problem without a solution yet (with a solution, write a request). A pain is mitigated or resolved with evidence, never by a finished request alone.",
		Actions: []Action{
			{Name: "record", Tool: "pain_record", Line: "record a problem (title; body, explanation)"},
			{Name: "search", Tool: "pain_search", Line: "list pains (query, state)"},
			{Name: "get", Tool: "pain_get", Line: "one pain (id)"},
			{Name: "update", Tool: "pain_update", Line: "move state or correct it (id; state mitigated needs remaining and evidence, resolved needs evidence, accepted/obsolete a note)"},
		},
	},
	{
		Name: "idea",
		Lead: "A wish voiced in passing, without working it out; later it is promoted to a request or worked out into a concept with open decisions. Look in search first.",
		Actions: []Action{
			{Name: "record", Tool: "idea_record", Line: "record a wish (title; body, raw, summary, concept, motivated_by_pain)"},
			{Name: "search", Tool: "idea_search", Line: "list ideas (query, state)"},
			{Name: "get", Tool: "idea_get", Line: "one idea with concept, decisions and attachments (id)"},
			{Name: "promote", Tool: "idea_update", Inner: "promote", Line: "make a request of it (id; title, description, type, criteria)"},
			{Name: "link_request", Tool: "idea_update", Inner: "link_request", Line: "tie an existing request to it (id, request_id)"},
			{Name: "dismiss", Tool: "idea_update", Inner: "dismiss", Line: "put it aside (id, reason)"},
			{Name: "reopen", Tool: "idea_update", Inner: "reopen", Line: "take it up again (id)"},
			{Name: "phase", Rare: true, Tool: "idea_update", Inner: "phase", Line: "move a worked-out idea (id, phase concept|discussion|done)"},
			{Name: "discuss", Tool: "idea_update", Inner: "discuss", Line: "mark it as under discussion (id)"},
			{Name: "issues", Rare: true, Tool: "idea_update", Inner: "issues", Line: "create the issues of an approved idea (id; issues, branch, package_name)"},
			{Name: "requirement_waive", Rare: true, Tool: "idea_update", Inner: "requirement_waive", Line: "waive a concept requirement, a person only (id, requirement, reason)"},
			{Name: "text", Rare: true, Tool: "idea_text", Line: "read or add the raw text or summary (id, part; body adds a version)"},
			{Name: "concept", Rare: true, Tool: "idea_concept", Line: "read or write the concept sections (id; about, what, how, not, questions, requirements)"},
			{Name: "attach", Rare: true, Tool: "idea_attach", Line: "attach an image to an idea, a decision variant or a reply (path or data_base64)"},
			{Name: "decision_open", Rare: true, Tool: "decision_open", Line: "put a question as an open decision instead of asking in chat (question; idea or request_id, variants, way)"},
			{Name: "decision_list", Rare: true, Tool: "decision_list", Line: "read decisions (id, idea, request_id, state, waiting_for_a_person)"},
			{Name: "decision_vote", Rare: true, Tool: "decision_update", Inner: "vote", Line: "your pick among the variants (id, variant, reason)"},
			{Name: "decision_variant", Rare: true, Tool: "decision_update", Inner: "variant", Line: "add an option (id, title, text, pro, contra)"},
			{Name: "decision_close", Rare: true, Tool: "decision_update", Inner: "close", Line: "decide, defer (trigger) or drop it (id, outcome, reason)"},
			{Name: "decision_reopen", Rare: true, Tool: "decision_update", Inner: "reopen", Line: "take a closed decision back (id, reason)"},
			{Name: "decision_way", Rare: true, Tool: "decision_update", Inner: "way", Line: "who decides, a person only (id, way)"},
		},
	},
	{
		Name: "coord",
		Lead: "Talk to the other agents and people in this project's room and on this machine, and keep lasting discussions (threads). Messages for you also arrive by themselves in hook context.",
		Actions: []Action{
			{Name: "status", Tool: "coord_status", Line: "say in one sentence what you work on (note; empty clears it). Set it when you start on a PR or branch"},
			{Name: "send", Tool: "coord_send", Line: "message the room (body; mention, intent question with explanation, reply_to)"},
			{Name: "inbox", Tool: "coord_inbox", Line: "what others said since you last looked"},
			{Name: "peers", Tool: "coord_peers", Line: "who else works here"},
			{Name: "touched", Rare: true, Tool: "coord_touched", Line: "has anyone worked on this path lately (path)"},
			{Name: "dm", Rare: true, Tool: "coord_dm", Line: "private message to one peer or a small group (to, body)"},
			{Name: "dm_read", Rare: true, Tool: "coord_dm_read", Line: "read private conversations (room, with)"},
			{Name: "thread_open", Tool: "thread_open", Line: "start a lasting discussion on one question (title; link_kind, link_id, first_post)"},
			{Name: "thread_read", Tool: "thread_read", Line: "where a discussion stands (id)"},
			{Name: "thread_reply", Tool: "thread_reply", Line: "add to a discussion (id, body)"},
			{Name: "thread_find", Tool: "thread_find", Line: "find discussions by words or object (query, object_kind, object_id)"},
			{Name: "thread_resolve", Tool: "thread_resolve", Line: "close, defer or reopen a discussion (id, state, note)"},
			{Name: "thread_propose", Rare: true, Tool: "thread_propose", Line: "propose a decision, pitfall or request from a discussion (id, kind, note)"},
		},
	},
	{
		Name: "human_task",
		Lead: "What only the human can provide (a key, a file, a decision, a manual step): ask with a task instead of in chat, with where it lives and how to check it, never a secret. You never mark one done; you confirm or reopen what the human marked done.",
		Actions: []Action{
			{Name: "create", Tool: "human_task_create", Line: "ask (kind, title; location, check_hint, steps, explanation, request_id)"},
			{Name: "list", Tool: "human_task_list", Line: "open tasks and those awaiting your check (state, mine, request_id)"},
			{Name: "confirm", Tool: "human_task_resolve", Inner: "confirm", Line: "the human's done holds after your check (id, note)"},
			{Name: "reopen", Tool: "human_task_resolve", Inner: "reopen", Line: "it is not what was asked (id, note)"},
			{Name: "drop", Tool: "human_task_resolve", Inner: "drop", Line: "no longer needed (id, note)"},
		},
	},
	{
		Name: "package",
		Lead: "Work packages: the requests a branch or pull request carries as its requirements. Without package_id the current branch's package is meant.",
		Actions: []Action{
			{Name: "get", Tool: "package_get", Line: "the package with open criteria and waiting human tasks (branch, pr, package_id)"},
			{Name: "review", Tool: "package_review", Line: "every criterion with its evidence and a verdict, for a reviewer"},
			{Name: "create", Tool: "package_update", Inner: "create", Line: "new package for a branch (branch, name, requests with role pflicht|optional, pr)"},
			{Name: "add", Tool: "package_update", Inner: "add", Line: "put requests into it (requests)"},
			{Name: "remove", Tool: "package_update", Inner: "remove", Line: "take one out (request_id, reason)"},
			{Name: "set", Tool: "package_update", Inner: "set", Line: "change name, bind a pr (pr, unlink_pr) or close it (state)"},
			{Name: "import_relations", Tool: "package_update", Inner: "import_relations", Line: "turn old branch relations into packages (dry_run)"},
		},
	},
	{
		Name: "feature",
		Lead: "Feature pages: what the product does now, across files (a wish is an idea). Look here before describing the same thing twice; after a change, write the new state.",
		Actions: []Action{
			{Name: "search", Tool: "feature_search", Line: "list features, or those living in a path (query, path)"},
			{Name: "get", Tool: "feature_get", Line: "one feature page (id; versions)"},
			{Name: "create", Rare: true, Tool: "feature_create", Line: "new page (name; summary, behavior, limits, cli, mcp, api, ui, areas, from_idea)"},
			{Name: "update", Tool: "feature_update", Line: "how it is NOW after your change (id and changed fields; pr, idea, requests)"},
			{Name: "state", Rare: true, Tool: "feature_state", Line: "retire it or take it up again (id, state, reason)"},
			{Name: "link", Rare: true, Tool: "feature_link", Inner: "link", Line: "tie it to an idea or issue (id, kind, ref, rel origin|affects)"},
			{Name: "unlink", Rare: true, Tool: "feature_link", Inner: "unlink", Line: "take a tie off (id, kind, ref)"},
			{Name: "pr_paths", Rare: true, Tool: "feature_pr_paths", Line: "report the paths a pull request changes and read its feature checklist (pr, paths)"},
			{Name: "check", Rare: true, Tool: "feature_check", Line: "the description still holds for a pull request (id, pr, note)"},
		},
	},
	{
		Name: "runbook",
		Lead: "Runbooks: named procedures that must be done the same way every time (build, roll out, check). Follow them exactly and report the run.",
		Actions: []Action{
			{Name: "get", Tool: "runbook_get", Line: "read one with parameters filled in (name, params); without name lists those that apply"},
			{Name: "write", Rare: true, Tool: "runbook_write", Line: "new runbook or version, a proposal until approved (name, spec)"},
			{Name: "report", Tool: "run_report", Line: "outcome of a run (name, result ok|failed|aborted; deviations)"},
		},
	},
	{
		Name: "comment",
		Lead: "Comments, reactions, labels and topics on requests, ideas, pains and knowledge entries.",
		Actions: []Action{
			{Name: "add", Tool: "comment_add", Line: "comment on REQ-n, IDEA-n or PAIN-n (ref, body)"},
			{Name: "list", Tool: "comment_list", Line: "read the comments (ref)"},
			{Name: "react", Tool: "reaction_set", Line: "emoji reaction on comment:n, IDEA-n or REQ-n (ref, reaction; remove)"},
			{Name: "reactions", Tool: "reaction_list", Line: "the reactions on an object (ref)"},
			{Name: "label_list", Rare: true, Tool: "label", Inner: "list", Line: "labels of the project"},
			{Name: "label_create", Rare: true, Tool: "label", Inner: "create", Line: "new label (name, color, description)"},
			{Name: "label_update", Rare: true, Tool: "label", Inner: "update", Line: "change a label (name, new_name, color, description)"},
			{Name: "label_delete", Rare: true, Tool: "label", Inner: "delete", Line: "delete a label (name)"},
			{Name: "label_add", Rare: true, Tool: "label", Inner: "add", Line: "put labels on an object (kind, id, labels)"},
			{Name: "label_remove", Rare: true, Tool: "label", Inner: "remove", Line: "take labels off (kind, id, labels)"},
			{Name: "topic_list", Rare: true, Tool: "topic", Inner: "list", Line: "named areas of the repository"},
			{Name: "topic_save", Rare: true, Tool: "topic", Inner: "save", Line: "name a set of path prefixes (name, prefixes)"},
			{Name: "topic_delete", Rare: true, Tool: "topic", Inner: "delete", Line: "remove a topic (name)"},
		},
	},
}

// IsCore reports whether name is a core tool.
func IsCore(name string) bool {
	_, ok := byCore[name]
	return ok
}

// Find returns the core tool of that name.
func Find(name string) (Core, bool) {
	c, ok := byCore[name]
	return c, ok
}

// Route resolves a core tool and an action (empty means the default) to the
// registered tool and the inner action it takes.
func Route(core, action string) (Action, bool) {
	c, ok := byCore[core]
	if !ok {
		return Action{}, false
	}
	if action == "" {
		action = c.Default
	}
	for _, a := range c.Actions {
		if a.Name == action {
			return a, true
		}
	}
	return Action{}, false
}

// ActionNames lists the actions of a core tool in table order.
func ActionNames(core string) []string {
	c := byCore[core]
	out := make([]string, 0, len(c.Actions))
	for _, a := range c.Actions {
		out = append(out, a.Name)
	}
	return out
}

// Ref is how a text names a registered tool for an agent: "request
// action=get" for request_get, the core name alone for a default action.
func Ref(tool string) string {
	l, ok := byTool[tool]
	if !ok || len(l) == 0 {
		return tool
	}
	r := l[0]
	if c := byCore[r.core]; c.Default == r.action.Name && r.action.Inner == "" {
		return r.core
	}
	if r.action.Inner != "" && len(l) > 1 {
		// A tool spread over several actions (package_update): name the tool
		// and say that its own action picks the sub-action.
		return r.core + " action=" + strings.Join(innerNames(l), "|")
	}
	return r.core + " action=" + r.action.Name
}

func innerNames(l []route) []string {
	var out []string
	for _, r := range l {
		out = append(out, r.action.Name)
	}
	return out
}

// Classify names the core tool and action of a call as a harness reports it:
// "mcp__ghosttree__request" with action "get", or an older name such as
// "mcp__ghosttree__request_get". ok is false for a tool that is not
// ghosttree's.
func Classify(toolName, action string) (core, act string, ok bool) {
	name := toolName
	if i := strings.LastIndex(name, "__"); i >= 0 {
		if !strings.HasPrefix(name, "mcp__") || !strings.Contains(strings.ToLower(name[:i]), "ghosttree") {
			return "", "", false
		}
		name = name[i+2:]
	}
	if c, found := byCore[name]; found {
		if action == "" {
			action = c.Default
		}
		if _, known := Route(name, action); !known {
			action = "unknown"
		}
		return name, action, true
	}
	if l, found := byTool[name]; found {
		r := l[0]
		if len(l) > 1 && action != "" {
			for _, x := range l {
				if x.action.Inner == action {
					r = x
				}
			}
		}
		return r.core, r.action.Name, true
	}
	return "", "", false
}

// Tools lists every registered tool the table routes to, sorted.
func Tools() []string {
	out := make([]string, 0, len(byTool))
	for t := range byTool {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

type route struct {
	core   string
	action Action
}

var (
	byCore = map[string]Core{}
	byTool = map[string][]route{}
)

func init() {
	for _, c := range table {
		byCore[c.Name] = c
		for _, a := range c.Actions {
			byTool[a.Tool] = append(byTool[a.Tool], route{core: c.Name, action: a})
		}
	}
}
