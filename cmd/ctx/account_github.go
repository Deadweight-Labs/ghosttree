package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

const accountGitHubUsage = `usage: ctx account github link [--print]

Links your GitHub account to your ghosttree account. GitHub itself says who you
are, so this happens in the web profile of the server you are signed in to: the
command opens that page in the browser of this machine, or prints its address
when no browser can be opened (over ssh, without a display) or with --print.
No database access and no token on the command line are needed. It is the same
step as the GitHub row in the settings of ctx tui.`

// profileURL is the web profile of the configured server, where a person links
// and unlinks their GitHub account. It is "" without a server.
func profileURL(cfg config.Config) string {
	if cfg.ServerURL == "" {
		return ""
	}
	return strings.TrimRight(cfg.ServerURL, "/") + "/ui/profile"
}

// cmdAccountGitHub routes ctx account github <verb>; link is the only one.
func cmdAccountGitHub(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "link" {
		fmt.Fprintln(stdout, accountGitHubUsage)
		return 2
	}
	fs := flag.NewFlagSet("account github link", flag.ContinueOnError)
	fs.SetOutput(stdout)
	printOnly := fs.Bool("print", false, "print the address, never open a browser")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, accountGitHubUsage)
		return 2
	}
	cfg, err := config.Load()
	url := profileURL(cfg)
	if err != nil || url == "" || !tui.WebURL(url) {
		fmt.Fprintf(stdout, "account github link: this machine is not connected to a server yet; run 'ctx login --server <url>' first (config %s)\n", config.Path())
		return 1
	}
	fmt.Fprintf(stdout, "link your GitHub account in your web profile:\n  %s\n", links.Clean(url, 300))
	why := "--print"
	if !*printOnly {
		if ok, reason := browserAllowed(); !ok {
			why = reason
		} else if err := openLink(url); err != nil {
			why = "could not start the browser: " + links.Clean(err.Error(), 120)
		} else {
			fmt.Fprintln(stdout, "opened in your browser")
			return 0
		}
	}
	fmt.Fprintf(stdout, "not opened (%s): open the address on a machine with a browser\n", why)
	return 0
}
