// Command ctx is the ghosttree CLI: server, session collector,
// MCP server and harness installer in one binary.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
)

var version = "0.1.0-dev"

func run(args []string, stdout io.Writer) int {
	client.ClientVersion = version
	if len(args) == 0 {
		fmt.Fprintln(stdout, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(rest, stdout)
	case "watch":
		return cmdWatch(rest, stdout)
	case "mcp":
		return cmdMCP(rest, stdout)
	case "channel":
		return cmdChannel(rest, stdout)
	case "claude":
		return cmdClaude(rest, stdout)
	case "install":
		return cmdInstall(rest, stdout)
	case "uninstall":
		return cmdUninstall(rest, stdout)
	case "hook":
		return cmdHook(rest, stdout)
	case "status":
		return cmdStatus(rest, stdout)
	case "export":
		return cmdExport(rest, stdout)
	case "doctor":
		return cmdDoctor(rest, stdout)
	case "review":
		return cmdReview(rest, stdout)
	case "regression":
		return cmdRegression(rest, stdout)
	case "knowledge":
		return cmdKnowledge(rest, stdout)
	case "request":
		return cmdRequest(rest, stdout)
	case "pr":
		return cmdPR(rest, stdout)
	case "comment":
		return cmdComment(rest, stdout)
	case "react":
		return cmdReact(rest, stdout)
	case "package":
		return cmdPackage(rest, stdout)
	case "runbook":
		return cmdRunbook(rest, stdout)
	case "github":
		return cmdGithub(rest, stdout)
	case "run":
		return cmdRun(rest, stdout)
	case "ghost":
		return cmdGhost(rest, stdout)
	case "doc":
		return cmdDoc(rest, stdout)
	case "migrate":
		return cmdMigrate(rest, stdout)
	case "coord":
		return cmdCoord(rest, stdout)
	case "mirror":
		return cmdMirror(rest, stdout)
	case "snapshot":
		return cmdSnapshot(rest, stdout, os.Stderr)
	case "garden":
		return cmdGarden(rest, stdout)
	case "explain":
		return cmdExplain(rest, stdout)
	case "distill-sessions":
		return cmdDistillSessions(rest, stdout)
	case "usage":
		return cmdUsage(rest, stdout)
	case "cost":
		return cmdCost(rest, stdout)
	case "upgrade-schema":
		return cmdUpgradeSchema(rest, stdout)
	case "origin":
		return cmdOrigin(rest, stdout)
	case "canonicalize-scopes":
		return cmdCanonicalizeScopes(rest, stdout)
	case "unbind-branch":
		return cmdUnbindBranch(rest, stdout)
	case "strip-images":
		return cmdStripImages(rest, stdout)
	case "db":
		return cmdDB(rest, stdout)
	case "embed":
		return cmdEmbed(rest, stdout)
	case "eval":
		return cmdEval(rest, stdout)
	case "redact-sessions":
		return cmdRedactSessions(rest, stdout)
	case "person":
		return cmdPerson(rest, stdout)
	case "account":
		return cmdAccount(rest, stdout)
	case "machine":
		return cmdMachine(rest, stdout)
	case "session":
		return cmdSession(rest, stdout)
	case "org":
		return cmdOrg(rest, stdout)
	case "project":
		return cmdProject(rest, stdout)
	case "setup":
		return cmdSetup(rest, stdout)
	case "config":
		return cmdConfig(rest, stdout)
	case "update":
		return cmdUpdate(rest, stdout)
	case "login":
		return cmdLogin(rest, stdout)
	case "join":
		return cmdJoin(rest, stdout)
	case "pain":
		return cmdPain(rest, stdout)
	case "idea":
		return cmdIdea(rest, stdout)
	case "decision":
		return cmdDecision(rest, stdout)
	case "feature":
		return cmdFeature(rest, stdout)
	case "search":
		return cmdSearch(rest, stdout)
	case "label":
		return cmdLabel(rest, stdout)
	case "topic":
		return cmdTopic(rest, stdout)
	case "task":
		return cmdTask(rest, stdout)
	case "open":
		return cmdOpen(rest, stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usage)
		return 0
	case "version":
		fmt.Fprintf(stdout, "ctx %s\n", version)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown command %q\n%s\n", cmd, usage)
		return 2
	}
}

const usage = `usage: ctx <command>

  serve    run the ghosttree server
  watch    run the session collector daemon
  mcp      run the MCP server (stdio)
  channel  serve coordination messages as a Claude Code channel (stdio, opt-in at session start)
  claude   start Claude Code with the ghosttree channel (opt-in launcher)
  ghclaude  not a command: a link to ctx that starts claude with the channel, all arguments unchanged
  install  set up a harness (claude|codex|opencode) or ghclaude
  uninstall  remove ghclaude
  setup    write client config (server URL + token)
  config   get or set client settings (channel stable|dev)
  update   update ctx from the stable release or the dev build (--check shows only)
  login    sign this machine in through the browser (device login)
  join     pair this machine with a pairing code from an invitation page
  person   manage persons/tokens (server-side; deprecated alias, see account)
  account  manage accounts and their tokens (server-side)
  machine  list, release and transfer machine names (server-side)
  session  share a session's transcript with the members of its project
  org      organizations: members, invitations, default (needs ctx login)
  project  list, claim and move projects between organizations
  status   show local setup state
  doctor   check the harness wiring for drift (--fix to repair)
  export   write a session's transcript as JSONL (images as placeholders)
  review   approve or reject distilled knowledge
  regression  say which test guards a fixed defect, and list the fixes none does
  request  search and manage the work ledger (create, move, ...)
  comment  comments on an issue, idea or pain (add, list, edit, delete)
  react    reactions on an issue, idea or comment (+1, heart, ...; --remove takes one back)
  pr       pull requests of a project: list, show (checklist, package, what was said), commits
  package  work packages: the requests that belong to a branch or pull request (create, add, remove, show, import-relations)
  runbook  named procedures with steps, parameters and triggers (list, show, write, import, approve, allow, report)
  github   ctx github sync-prs --db PATH: operator, fetches PR numbers for existing branches from GitHub
  run      ctx run NAME [param=value...] prints a runbook's checklist; --target T --run executes an approved, locally allowed target
  pain     record a problem without a solution, follow it to mitigated or resolved
  idea     record a wish without working it out, later make a request of it; or work it out (raw text, concept, images, issues)
  decision open questions on an idea or issue with variants, votes and a way to close them
  feature  the living description of what the product does: write how a feature is now, see which pull request touches which (list, show, update, paths, export, import)
  search   search with qualifiers (is:open label:bug area:internal/store), saved searches (--preset)
  label    labels for requests, pains, ideas and knowledge, per project or org
  topic    named areas of the repository (several path prefixes under one name)
  task     ask the human for a key, file, decision or action, and check it afterwards
  open     open an object (REQ-n, TASK-n, PAIN-n, #id, pr:n, preset:..., a path) in the browser, or print its link and card
  knowledge  add, show, supersede, retire or move a knowledge entry
  ghost    read description history or archive confirmed-deleted paths
  doc      write, publish, and read long-form documents
  migrate  move repository agent artifacts into ghosttree
  coord    talk to the other agents in this repo or on this machine
  mirror   write .ghosttree/ for a repository (harnesses without hooks)
  snapshot create, inspect, export, verify, and mirror immutable context marks
  garden   check knowledge against the code (opt-in, needs an API key)
  explain  write the "In short" notes that are missing (needs a model)
  distill-sessions  extract quarantined knowledge from idle sessions
                    (--submit/--collect for the half-price batch path,
                     --reprocess-version to redo work of an older prompt)
  usage    list knowledge nobody has used within a window
  cost     report LLM spend and forecast the remaining backlog
  upgrade-schema  one-off knowledge table rebuild for trust tiers
  origin               backfill the origin context (time, areas) of older entries
  canonicalize-scopes  one-off project name backfill and duplicate merge
  unbind-branch  lift knowledge filed against a branch to its project
  strip-images  replace stored base64 images and files with placeholders (--dry-run, runs beside the server)
  db       vacuum (server stopped) and check the database file; sqlite3 cannot, the schema needs ctx's SQL functions
  embed    status of the embeddings behind hybrid search (vectors, due entries, job, server)
  eval     measure search quality on stored evidence quotes (eval search: full text, vector, hybrid)
  redact-sessions  replace passwords and other secrets in stored transcripts and their search indexes (--dry-run, runs beside the server; --literals-file names known values)
  version  print version`

func main() {
	if asGhclaude(os.Args) {
		os.Exit(runGhclaude(os.Args[0], os.Args[1:], os.Stdout))
	}
	os.Exit(run(os.Args[1:], os.Stdout))
}
