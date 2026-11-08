package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

const openUsage = `usage: ctx open <what> [--print] [--json]

Opens the page of one object in the browser of this machine, or prints its link
(and a compact card) when no browser can be opened: over ssh, without a display.
The link carries no login; a browser that is not signed in returns to exactly
this page after signing in.

what is one of
  REQ-596          an issue          AC-596.2 or REQ-596#2   one requirement
  TASK-124         a human task      PAIN-57   a pain        IDEA-3  an idea
  FEAT-2           a feature
  #4295            a knowledge entry PKG-1     a work package (checklist)
  pr:155           a pull request    commit:2d950bf          a commit
  room:12          a room thread     session:ID              a session
  doc:NAME         a document        runbook:NAME            a runbook
  branch:NAME      the checklist of a branch
  preset:pr-ergebnis 155             a saved search with its values
  internal/store   a file or folder on the current branch
  is:open label:bug  a search line   inbox   what waits for you

--print never opens a browser. --json answers with the card as JSON.`

// The seams for tests: the operating system, the environment and the program
// that opens an address.
var (
	openGOOS   = runtime.GOOS
	openGetenv = os.Getenv
	openLink   = func(url string) error {
		if !canOpenBrowser() {
			return fmt.Errorf("no program to open an address found")
		}
		return openBrowser(url)
	}
)

// browserAllowed says whether a browser may be opened on this machine: never
// over ssh (it would open on the remote machine, where nobody sits), on macOS
// always, elsewhere only with a display.
func browserAllowed() (bool, string) {
	if openGetenv("SSH_CONNECTION") != "" || openGetenv("SSH_TTY") != "" {
		return false, "ssh session"
	}
	switch openGOOS {
	case "darwin":
		return true, ""
	case "linux", "freebsd", "openbsd", "netbsd":
		if openGetenv("DISPLAY") != "" || openGetenv("WAYLAND_DISPLAY") != "" {
			return true, ""
		}
		return false, "no display"
	}
	return false, "no browser opener on " + openGOOS
}

// cliBuilder makes the web addresses of the configured server for a project.
func cliBuilder(cfg config.Config, project string) links.Builder {
	return links.New(cfg.ServerURL, project)
}

// clientBuilder is cliBuilder for code that holds the client but not the config.
func clientBuilder(c *client.Client, project string) links.Builder {
	return links.New(c.ServerURL(), project)
}

// cliCard prints the card of an object after its normal output.
func cliCard(stdout io.Writer, c links.Card) {
	if c.URL == "" {
		return
	}
	fmt.Fprintln(stdout, c.String())
}

func cmdOpen(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	args, printOnly := removeBoolArg(args, "--print")
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, openUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	ref, err := links.ParseRef(strings.Join(args, " "))
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 2, err)
	}
	hctx := currentGitContext(cfg.Machine)
	c := client.New(cfg)
	env := links.Env{Builder: cliBuilder(cfg, hctx.axes.Project), API: c, Project: hctx.axes.Project, Branch: hctx.axes.Branch, RepoRoot: hctx.root}
	card, err := env.Resolve(ref)
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	opened, why := false, ""
	if printOnly {
		why = "--print"
	} else if ok, reason := browserAllowed(); !ok {
		why = reason
	} else if err := openLink(card.URL); err != nil {
		why = "could not start the browser: " + links.Clean(err.Error(), 120)
	} else {
		opened = true
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(struct {
			OK     bool       `json:"ok"`
			Opened bool       `json:"opened"`
			Card   links.Card `json:"card"`
		}{true, opened, card})
		return 0
	}
	fmt.Fprintln(stdout, card.String())
	if opened {
		fmt.Fprintln(stdout, "opened in your browser")
	} else {
		fmt.Fprintf(stdout, "not opened (%s): pass the link on\n", why)
	}
	return 0
}
