package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/releasekey"
	"github.com/Deadweight-Labs/ghosttree/internal/serverinstall"
)

const serverUsage = `usage: ctx server install [options]
       ctx server update [--version TAG] [--force] [--wait 90s]
       ctx server doctor

  install  set up a ghosttree server on this machine with systemd: the server key,
           the settings, the service, a nightly database backup, the first
           administrator and the gardener's account, and print a login link
  update   move the server to another release: check its signature and checksum,
           copy the database, replace ctx, restart, and go back if the new
           release does not come up; it only goes up unless --force is given
  doctor   check the server of this machine (service, database, backup, key)

ctx server install asks what it needs when it has a terminal; every answer
can also be given as an option, and --yes (or no terminal) takes the defaults:

  --domain NAME     public name of the server (ghosttree.example.com); without
                    it the server stays on this machine (http://127.0.0.1:8474)
  --tls proxy       (default with a domain) the server speaks http on loopback
                    behind a proxy of yours; configuration for Caddy and nginx is
                    written to /etc/ghosttree/proxy to copy
  --tls builtin     the server fetches its own certificate from Let's Encrypt
                    (the name must point here; ports 80 and 443 must be free)
  --email ADDRESS   contact address for Let's Encrypt, optional
  --listen ADDR     listen address, default 127.0.0.1:8474
  --admin NAME      name of the first administrator (default: admin, also with --yes)
  --language de|en  language of the interface (empty: each person's browser)
  --ai anthropic|openai --ai-key-file FILE [--ai-model M] [--ai-base-url URL]
                    connect a model provider; the key is read from a file
  --force           on a machine that has a server already: write settings and
                    units again from these answers, after copying the database,
                    replacing ctx and bringing the schema up to date (without
                    it, install stops and points to ctx server update)
  --yes             do not ask
  --wait 90s        how long the server may take to come up

It needs root (sudo) and a machine that runs systemd. On a machine with a server
set up, use ctx server update.`

// serverRelease and serverSystem are seams for the tests of the commands.
var (
	serverSystem  serverinstall.System = serverinstall.ExecSystem{}
	serverRelease                      = func() (serverinstall.Release, error) {
		key, err := releasekey.Public()
		if err != nil {
			return serverinstall.Release{}, err
		}
		return serverinstall.Release{Key: key, APIBase: os.Getenv("GHOSTTREE_API_URL"), DLBase: os.Getenv("GHOSTTREE_RELEASE_BASE")}, nil
	}
	serverPrefix = "" // tests only: where installation writes
)

func cmdServer(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, serverUsage)
		return 2
	}
	switch args[0] {
	case "install":
		return cmdServerInstall(args[1:], stdout)
	case "update":
		return cmdServerUpdate(args[1:], stdout)
	case "doctor":
		return cmdServerDoctor(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, serverUsage)
		return 0
	}
	fmt.Fprintf(stdout, "unknown server command %q\n%s\n", args[0], serverUsage)
	return 2
}

func serverEnv(stdout io.Writer, wait time.Duration) serverinstall.Env {
	return serverinstall.Env{
		Sys: serverSystem, Out: stdout, RequireRoot: serverPrefix == "",
		Systemd: serverinstall.SystemdRunning, HealthTimeout: wait,
	}
}

const waitUsage = "how long the server may take to answer after a start (default 90s)"

func cmdServerInstall(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("server install", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() { fmt.Fprintln(stdout, serverUsage) }
	var o serverinstall.Options
	fs.StringVar(&o.Domain, "domain", "", "")
	fs.StringVar(&o.TLS, "tls", "", "")
	fs.StringVar(&o.Email, "email", "", "")
	fs.StringVar(&o.Listen, "listen", "", "")
	fs.StringVar(&o.Admin, "admin", "", "")
	fs.StringVar(&o.Language, "language", "", "")
	fs.StringVar(&o.AI.Format, "ai", "", "")
	fs.StringVar(&o.AI.KeyFile, "ai-key-file", "", "")
	fs.StringVar(&o.AI.Model, "ai-model", "", "")
	fs.StringVar(&o.AI.BaseURL, "ai-base-url", "", "")
	fs.BoolVar(&o.Force, "force", false, "")
	yes := fs.Bool("yes", false, "")
	wait := fs.Duration("wait", 0, waitUsage)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stdout, serverUsage)
		return 2
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	o.Prefix = serverPrefix
	// Before a question is asked: a server that is set up is updated, not
	// installed over.
	if !o.Force && serverinstall.Existing(o) {
		fmt.Fprintf(stdout, "server install: %v (%s or %s exists).\nUpdate it with: ctx server update. To set it up again from other answers, add --force: the database is copied first and its schema brought up to date.\n", serverinstall.ErrInstalled, serverinstall.ServiceID, serverinstall.StateDir)
		return 1
	}

	if !*yes {
		if t, err := installTTY(); err == nil {
			askServerQuestions(t, &o, given)
			t.Close()
		}
	}
	if err := o.Validate(); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := serverinstall.Install(ctx, o, serverEnv(stdout, *wait))
	if err != nil {
		fmt.Fprintf(stdout, "server install: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nghosttree %s is running as %s.\n\n", version, serverinstall.ServiceID)
	printServerChecks(stdout, res.Checks)
	fmt.Fprintf(stdout, "\nSign in as %s (one use, valid 15 minutes):\n  %s\n", res.Admin, res.LoginURL)
	fmt.Fprintln(stdout, "\nNext:")
	if o.Domain != "" && o.TLS != "builtin" {
		fmt.Fprintf(stdout, "  - Put a proxy in front: copy %s/Caddyfile or %s/nginx.conf (they forward to %s).\n", serverinstall.ProxyDir, serverinstall.ProxyDir, o.ListenAddr())
	}
	if o.Domain != "" && o.TLS == "builtin" {
		fmt.Fprintf(stdout, "  - %s must point at this machine and ports 80 and 443 must be open; the certificate is fetched on the first visit.\n", o.Domain)
	}
	fmt.Fprintf(stdout, "  - Connect a machine: invite it from the web interface, or run: ctx join --server %s\n", res.PublicURL)
	fmt.Fprintf(stdout, "  - Keep a copy of %s with your backups (nightly copies of the database: %s).\n", serverinstall.SecretKey, serverinstall.BackupDir)
	fmt.Fprintln(stdout, "  - Update later with: ctx server update")
	if !serverinstall.Healthy(res.Checks) {
		return 1
	}
	return 0
}

// flagExit is the exit status for a flag parsing error: asking for help is not one.
func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func printServerChecks(stdout io.Writer, checks []serverinstall.Check) {
	var out []installer.Check
	for _, c := range checks {
		out = append(out, installer.Check{Name: c.Name, OK: c.OK, Unverified: c.Unverified, Detail: c.Detail, Fix: c.Fix})
	}
	printDoctorChecks(stdout, out)
}

// askServerQuestions asks, on the terminal, for every answer not given as an
// option. An empty answer takes the default.
func askServerQuestions(t terminal, o *serverinstall.Options, given map[string]bool) {
	ask := func(prompt string) string {
		s, _ := t.Ask(prompt)
		return strings.TrimSpace(s)
	}
	if !given["domain"] {
		o.Domain = ask("Public name of this server, like ghosttree.example.com (empty: keep it on this machine only): ")
	}
	if o.Domain != "" && !given["tls"] {
		switch ask("TLS: 1) behind a proxy of mine (Caddy/nginx configuration is written), 2) the server fetches its own certificate (Let's Encrypt) [1]: ") {
		case "2":
			o.TLS = "builtin"
		default:
			o.TLS = "proxy"
		}
	}
	if o.TLS == "builtin" && !given["email"] {
		o.Email = ask("Contact address for Let's Encrypt (optional): ")
	}
	if !given["admin"] {
		o.Admin = ask("Name of the first administrator [admin]: ")
	}
	if !given["language"] {
		o.Language = ask("Language of the interface, de or en (empty: each person's browser decides): ")
	}
	if !given["ai"] {
		if f := ask("File with the API key of a model provider (empty: connect one later in the settings): "); f != "" {
			o.AI.KeyFile = f
			if ask("Provider: 1) Anthropic, 2) OpenAI-compatible [1]: ") == "2" {
				o.AI.Format = "openai"
				o.AI.BaseURL = ask("Base URL of the provider (https://.../v1): ")
			} else {
				o.AI.Format = "anthropic"
			}
			o.AI.Model = ask("Model (empty: the provider's default): ")
		}
	}
}

func cmdServerUpdate(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("server update", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() { fmt.Fprintln(stdout, serverUsage) }
	tag := fs.String("version", "", "release tag to move to, such as v0.2.0 (default: the newest stable release)")
	force := fs.Bool("force", false, "install the release even if that version runs already")
	wait := fs.Duration("wait", 0, waitUsage)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stdout, serverUsage)
		return 2
	}
	rel, err := serverRelease()
	if err != nil {
		fmt.Fprintf(stdout, "server update: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := serverinstall.Update(ctx, serverinstall.UpdateOptions{Prefix: serverPrefix, Version: *tag, Force: *force}, rel, serverEnv(stdout, *wait))
	if res != nil && len(res.Checks) > 0 {
		fmt.Fprintln(stdout)
		printServerChecks(stdout, res.Checks)
	}
	if err != nil {
		fmt.Fprintf(stdout, "server update: %v\n", err)
		return 1
	}
	if !res.Unchanged {
		fmt.Fprintf(stdout, "\nUpdated ghosttree %s -> %s. A copy of the database from before is %s;\nrestart running agents on connected machines and update them with: ctx update\n", res.From, res.To, res.Backup)
	}
	if res.Checks != nil && !serverinstall.Healthy(res.Checks) {
		return 1
	}
	return 0
}

func cmdServerDoctor(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("server doctor", flag.ContinueOnError)
	fs.SetOutput(stdout)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, serverUsage)
		return 2
	}
	if _, err := os.Stat(serverPrefix + serverinstall.ServerEnv); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "server doctor: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	checks := serverinstall.Doctor(ctx, serverinstall.OptionsFromDisk(serverPrefix), serverEnv(stdout, 0))
	printServerChecks(stdout, checks)
	if !serverinstall.Healthy(checks) {
		return 1
	}
	return 0
}
