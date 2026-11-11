package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

func TestAccountGitHubLinkPrintsTheProfileAddressWithoutADatabase(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(config.Config{ServerURL: "https://ghosttree.example.invalid/", Token: "secret-token", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	old := openLink
	t.Cleanup(func() { openLink = old })
	var started []string
	openLink = func(url string) error { started = append(started, url); return nil }

	var out bytes.Buffer
	if code := run([]string{"account", "github", "link", "--print"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "https://ghosttree.example.invalid/ui/profile") || len(started) != 0 {
		t.Fatalf("--print opened %v or missed the address: %s", started, out.String())
	}
	if strings.Contains(out.String(), "secret-token") {
		t.Fatalf("the token reached the output: %s", out.String())
	}
}

func TestAccountGitHubLinkOpensTheBrowserWhereOneCanOpen(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(config.Config{ServerURL: "https://ghosttree.example.invalid", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	oldLink, oldOS, oldEnv := openLink, openGOOS, openGetenv
	t.Cleanup(func() { openLink, openGOOS, openGetenv = oldLink, oldOS, oldEnv })
	var started []string
	openLink = func(url string) error { started = append(started, url); return nil }
	openGOOS, openGetenv = "darwin", func(string) string { return "" }

	var out bytes.Buffer
	if code := cmdAccountGitHub([]string{"link"}, &out); code != 0 || len(started) != 1 || started[0] != "https://ghosttree.example.invalid/ui/profile" {
		t.Fatalf("exit %d started %v: %s", code, started, out.String())
	}

	// Over ssh nothing opens on the remote machine; the address is printed.
	started = nil
	out.Reset()
	openGetenv = func(k string) string {
		if k == "SSH_CONNECTION" {
			return "1.2.3.4 1 5.6.7.8 22"
		}
		return ""
	}
	if code := cmdAccountGitHub([]string{"link"}, &out); code != 0 || len(started) != 0 || !strings.Contains(out.String(), "not opened (ssh session)") {
		t.Fatalf("ssh: exit %d started %v: %s", code, started, out.String())
	}
}

func TestAccountGitHubLinkNeedsAServerAndKnowsOnlyLink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if code := cmdAccountGitHub([]string{"link"}, &out); code != 1 || !strings.Contains(out.String(), "ctx login --server") {
		t.Fatalf("without a server: exit %d: %s", code, out.String())
	}
	out.Reset()
	for _, args := range [][]string{nil, {"unlink"}, {"link", "extra"}} {
		if code := cmdAccountGitHub(args, &out); code != 2 || !strings.Contains(out.String(), "usage: ctx account github link") {
			t.Fatalf("%v: exit %d: %s", args, code, out.String())
		}
		out.Reset()
	}
}

// Every command the TUI help names as the equivalent of a setting is a command
// ctx dispatches, and the account command's own help lists the GitHub step.
func TestTUIHelpNamesOnlyCommandsCtxKnows(t *testing.T) {
	usage := new(bytes.Buffer)
	run(nil, usage) // not a terminal: the command list
	listed := usage.String()
	if !strings.Contains(accountUsage, "ctx account github link") {
		t.Fatalf("account help misses the GitHub step:\n%s", accountUsage)
	}
	section := tuiUsage[strings.Index(tuiUsage, "Every setting it changes"):]
	cmds := regexp.MustCompile(`ctx ([a-z]+)`).FindAllStringSubmatch(section, -1)
	if len(cmds) < 7 {
		t.Fatalf("tui help names %d commands:\n%s", len(cmds), section)
	}
	for _, c := range cmds {
		if !regexp.MustCompile(`(?m)^\s+` + c[1] + `\s`).MatchString(listed) {
			t.Errorf("tui help names ctx %s, which the command list lacks:\n%s", c[1], listed)
		}
	}
}
