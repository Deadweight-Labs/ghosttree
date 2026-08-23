// Command ctx is the ghosttree CLI: server, session collector,
// MCP server and harness installer in one binary.
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "0.1.0-dev"

func run(args []string, stdout io.Writer) int {
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
	case "install":
		return cmdInstall(rest, stdout)
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
	case "request":
		return cmdRequest(rest, stdout)
	case "migrate":
		return cmdMigrate(rest, stdout)
	case "upgrade-schema":
		return cmdUpgradeSchema(rest, stdout)
	case "person":
		return cmdPerson(rest, stdout)
	case "setup":
		return cmdSetup(rest, stdout)
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
  install  set up a harness (claude|codex)
  setup    write client config (server URL + token)
  person   manage persons/tokens (server-side)
  status   show local setup state
  doctor   check the harness wiring for drift (--fix to repair)
  export   write a session's original transcript as JSONL
  review   approve or reject distilled knowledge
  request  search and manage the work ledger
  migrate  move repository agent artifacts into ghosttree
  upgrade-schema  one-off knowledge table rebuild for trust tiers
  version  print version`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
