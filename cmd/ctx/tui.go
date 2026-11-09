package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

// interactiveTerminal says whether ctx talks to a person: stdin and stdout
// are terminals and the output is the real stdout. Tests replace it.
var interactiveTerminal = func(stdout io.Writer) bool {
	f, ok := stdout.(*os.File)
	if !ok || f != os.Stdout {
		return false
	}
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// runTUI is a seam: tests check that the interface would start without
// taking over the terminal.
var runTUI = func(opt tui.Options) (tui.Model, error) {
	return tui.Run(newCLIBackend(), opt)
}

const tuiUsage = `usage: ctx tui [--look mark|tree|lines]

The terminal interface: quick search (REQ-n, PAIN-n, TASK-n, IDEA-n, FEAT-n,
#id, pr:n or a search line), your tasks, open requests and messages, and the
local settings. ctx without arguments opens it in a terminal.
--look (or GHOSTTREE_TUI_LOOK) picks the logo and layout; f2 cycles them.

Every setting it changes has a command of its own:
  sign in               ctx login [--server URL] [--machine NAME]
  sign out              ctx logout
  update channel        ctx config set channel stable|dev
  harness check/repair  ctx doctor / ctx doctor --fix
  collector restart     ctx install watch --restart`

func tuiOptions(look string) (tui.Options, error) {
	if look == "" {
		look = os.Getenv("GHOSTTREE_TUI_LOOK")
	}
	style, ok := tui.ParseLogoStyle(look)
	if !ok {
		return tui.Options{}, fmt.Errorf("unknown look %q: use mark, tree or lines", look)
	}
	return tui.Options{
		Look:  style,
		Plain: os.Getenv("NO_COLOR") != "",
		Still: os.Getenv("GHOSTTREE_TUI_STILL") != "",
	}, nil
}

func cmdTUI(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stdout)
	look := fs.String("look", "", "mark, tree or lines")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, tuiUsage)
		return 2
	}
	if !interactiveTerminal(stdout) {
		fmt.Fprintln(stdout, "ctx tui needs a terminal")
		return 2
	}
	opt, err := tuiOptions(*look)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if _, err := runTUI(opt); err != nil {
		fmt.Fprintf(stdout, "tui: %v\n", err)
		return 1
	}
	return 0
}

// runTUILogin is ctx login in a terminal: the login screen of the TUI, which
// quits after a successful login. A server named on the command line skips
// the form.
func runTUILogin(server, machine string, stdout io.Writer) int {
	opt, err := tuiOptions("")
	if err != nil {
		opt = tui.Options{}
	}
	opt.Start = tui.ScreenLogin
	opt.LoginServer, opt.LoginMachine = server, machine
	opt.LoginDirect = server != ""
	m, err := runTUI(opt)
	if err != nil {
		fmt.Fprintf(stdout, "login: %v\n", err)
		return 1
	}
	if !m.LoggedIn {
		return 1
	}
	if cfg, err := config.Load(); err == nil {
		fmt.Fprintf(stdout, "logged in; wrote %s (machine %s)\n", config.Path(), cfg.Machine)
	}
	return 0
}

const logoutUsage = `usage: ctx logout [--local]

Signs this machine out: the server revokes the token and frees the machine
name, then the token is removed from the client config. Server, machine name
and update channel stay. --local only removes the token here.

Exit codes: 0 signed out, 1 nothing done, 3 removed here but the server did
not confirm the revocation (revoke the device in the web interface).`

func cmdLogout(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(stdout)
	local := fs.Bool("local", false, "remove the token here without asking the server")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, logoutUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := logout(ctx, *local)
	var warn *logoutWarning
	switch {
	case errors.As(err, &warn):
		fmt.Fprintf(stdout, "signed out here; %s\n", links.Clean(warn.Error(), 400))
		return 3
	case err != nil:
		fmt.Fprintf(stdout, "logout: %s\n", links.Clean(err.Error(), 300))
		return 1
	}
	fmt.Fprintln(stdout, "signed out")
	return 0
}

// logoutWarning is a logout that removed the token locally but could not
// confirm the revocation on the server.
type logoutWarning struct{ err error }

func (w *logoutWarning) Error() string {
	return "the server did not confirm the revocation (" + links.Clean(w.err.Error(), 160) + "); revoke the device in the web interface under account, devices"
}

func logout(ctx context.Context, localOnly bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config missing (%s)", config.Path())
	}
	if cfg.Token == "" {
		return errors.New("not signed in")
	}
	var warn error
	if !localOnly {
		if err := client.New(cfg).RevokeSelf(ctx); err != nil {
			// 401: the token was already revoked or expired. Anything else,
			// 403 included, leaves the revocation unconfirmed.
			var se *client.StatusError
			if !(errors.As(err, &se) && se.Status == http.StatusUnauthorized) {
				warn = &logoutWarning{err: err}
			}
		}
	}
	cfg.Token = ""
	if err := config.Save(cfg); err != nil {
		return err
	}
	return warn
}

// cliBackend is the TUI's view of ctx: the same calls the subcommands make.
type cliBackend struct{}

func newCLIBackend() cliBackend { return cliBackend{} }

func (cliBackend) client(cfg config.Config) *client.Client {
	return client.NewWithTimeout(cfg, 10*time.Second)
}

func (b cliBackend) Local() tui.Local {
	l := tui.Local{ConfigPath: config.Path(), ClientVersion: versionLabel(version), Channel: config.ChannelStable}
	cfg, err := config.Load()
	if err == nil {
		l.Configured = cfg.ServerURL != ""
		l.Machine = cfg.Machine
		l.Channel = cfg.UpdateChannel()
		token := tui.TokenNone
		if cfg.Token != "" {
			token = tui.TokenStored
		}
		if cfg.ServerURL != "" {
			l.Servers = []tui.Server{{URL: cfg.ServerURL, Default: true, Token: token, Machine: cfg.Machine}}
			l.ProfileURL = strings.TrimRight(cfg.ServerURL, "/") + "/ui/profile"
		}
		g := currentGitContext(cfg.Machine)
		l.Project, l.Branch = g.axes.Project, g.axes.Branch
		l.Dir, l.Git, l.Remote, l.Linked = folderInfo()
	} else if h, err := os.Hostname(); err == nil {
		l.Machine = h
	}
	l.Collector = collectorState()
	l.Machine, l.Project, l.Branch = links.Clean(l.Machine, 64), links.Clean(l.Project, 200), links.Clean(l.Branch, 120)
	l.Dir, l.Remote = links.Clean(l.Dir, 300), links.Clean(l.Remote, 200)
	l.Collector.Detail = links.Clean(l.Collector.Detail, 160)
	for i := range l.Servers {
		l.Servers[i].Machine = links.Clean(l.Servers[i].Machine, 64)
	}
	return l
}

func collectorState() tui.Collector {
	if pid, running := watchProcess(); running {
		return tui.Collector{Running: true, State: "running", Detail: fmt.Sprintf("pid %d", pid)}
	}
	state, detail := installer.WatchServiceState()
	switch state {
	case installer.ServiceActive:
		return tui.Collector{Running: true, State: "running", Detail: detail}
	case installer.ServiceMissing:
		return tui.Collector{State: "missing", Detail: detail}
	case installer.ServiceInactive:
		return tui.Collector{State: "stopped", Detail: detail}
	}
	return tui.Collector{State: "unknown", Detail: detail}
}

func (b cliBackend) CheckServer(ctx context.Context, url string) tui.Server {
	s := tui.Server{URL: url, Default: true, Checked: true}
	cfg, err := config.Load()
	if err != nil || cfg.ServerURL != url {
		cfg = config.Config{ServerURL: url}
	}
	s.Machine = links.Clean(cfg.Machine, 64)
	if cfg.Token != "" {
		s.Token = tui.TokenStored
	}
	c := b.client(cfg)
	if err := c.Health(); err != nil {
		s.Err = links.Clean(err.Error(), 160)
		return s
	}
	s.Reachable = true
	if info, err := c.Version(); err == nil {
		s.Version, s.Channel = links.Clean(versionLabel(info.Version), 40), links.Clean(info.Channel, 16)
	}
	if cfg.Token == "" {
		return s
	}
	who, err := c.WhoAmIContext(ctx)
	if err != nil {
		var se *client.StatusError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			s.Token = tui.TokenRejected
		} else {
			s.Err = links.Clean(err.Error(), 160)
		}
		return s
	}
	s.Token, s.Account = tui.TokenValid, links.Clean(who.Label, 60)
	if m := links.Clean(who.Machine, 64); m != "" {
		s.Machine = m
	}
	return s
}

func (b cliBackend) Resolve(ctx context.Context, input string) (links.Card, error) {
	cfg, err := config.Load()
	if err != nil {
		return links.Card{}, classifyErr(err)
	}
	ref, err := links.ParseRef(input)
	if err != nil {
		return links.Card{}, friendlyErr(err)
	}
	hctx := currentGitContext(cfg.Machine)
	env := links.Env{Builder: cliBuilder(cfg, hctx.axes.Project), API: b.client(cfg), Project: hctx.axes.Project, Branch: hctx.axes.Branch, RepoRoot: hctx.root}
	card, err := env.Resolve(ref)
	return card.Sanitized(), friendlyErr(err)
}

// Search runs a search line of ctx search, or a saved search written as
// preset:NAME value...
func (b cliBackend) Search(ctx context.Context, line string) ([]tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	c := b.client(cfg)
	ax := requestProjectAxes(cfg.Machine)
	if ref, err := links.ParseRef(line); err == nil && ref.Kind == links.KindPreset {
		run, err := c.RunPreset(ax.Project, ref.Text, presetArgs(c, ax.Project, ref.Text, ref.Args), 15)
		if err != nil {
			return nil, friendlyErr(err)
		}
		return searchItems(cliBuilder(cfg, ax.Project), run.Result), nil
	}
	if _, err := query.Parse(line, time.Now()); err != nil {
		return nil, friendlyErr(err)
	}
	res, err := c.SearchAnchored(line, "all", ax, 15, false, client.AnchorFilter{Agent: os.Getenv(agentIDEnv)})
	if err != nil {
		return nil, friendlyErr(err)
	}
	return searchItems(cliBuilder(cfg, ax.Project), res), nil
}

// presetArgs maps the positional values of preset:NAME v1 v2 onto the
// placeholders the preset declares, in their order.
func presetArgs(c *client.Client, project, name string, values []string) map[string]string {
	out := map[string]string{}
	if len(values) == 0 {
		return out
	}
	list, err := c.ListPresets(project)
	if err != nil {
		return out
	}
	for _, p := range list {
		if p.Name != name {
			continue
		}
		for i, a := range p.Args {
			if i < len(values) {
				out[a] = values[i]
			}
		}
	}
	return out
}

func searchItems(b links.Builder, r client.SearchResult) []tui.Item {
	var out []tui.Item
	for _, h := range r.Requests {
		out = append(out, tui.Item{Ref: fmt.Sprintf("REQ-%d", h.Request.ID), Kind: "issue", Title: h.Request.Title, Meta: h.Request.State})
	}
	for _, t := range r.Tasks {
		out = append(out, tui.Item{Ref: t.HumanID(), Kind: "task", Title: t.Title, Meta: t.State})
	}
	for _, p := range r.Pains {
		out = append(out, tui.Item{Ref: fmt.Sprintf("PAIN-%d", p.ID), Kind: "pain", Title: p.Title, Meta: p.State})
	}
	for _, i := range r.Ideas {
		out = append(out, tui.Item{Ref: i.HumanID(), Kind: "idea", Title: i.Title, Meta: i.State})
	}
	for _, f := range r.Features {
		out = append(out, tui.Item{Ref: fmt.Sprintf("FEAT-%d", f.ID), Kind: "feature", Title: f.Name, Meta: f.State})
	}
	for _, c := range r.Criteria {
		out = append(out, tui.Item{Ref: fmt.Sprintf("AC-%d.%d", c.RequestID, c.Number), Kind: "criterion", Title: c.Description, Meta: c.State})
	}
	for _, k := range r.Knowledge {
		out = append(out, tui.Item{Ref: fmt.Sprintf("#%d", k.ID), Kind: k.Type, Title: k.Title, Meta: k.Type})
	}
	for _, f := range r.Files {
		out = append(out, tui.Item{Ref: f.Path, Kind: "file", Title: firstLine(f.Description), Meta: f.Kind})
	}
	for _, m := range r.Messages {
		card := b.MessageCard(m)
		out = append(out, tui.Item{Ref: card.Ref, Kind: "message", Title: m.Body, Meta: m.Sender, Card: &card})
	}
	for _, s := range r.Sessions {
		card := b.SessionCard(s.Session)
		out = append(out, tui.Item{Ref: card.Ref, Kind: "session", Title: firstLine(s.Snippet), Meta: s.Session.Harness, Card: &card})
	}
	return cleanItems(out)
}

// cleanItems makes every field of a list row one printable line before it
// leaves for the interface; the texts come from the server.
func cleanItems(items []tui.Item) []tui.Item {
	for i := range items {
		it := &items[i]
		it.Ref, it.Kind, it.Title, it.Meta = links.Clean(it.Ref, 40), links.Clean(it.Kind, 24), links.Clean(it.Title, 200), links.Clean(it.Meta, 40)
		if it.Card != nil {
			c := it.Card.Sanitized()
			it.Card = &c
		}
	}
	return items
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// MyTasks are the open tasks for the caller in the current project, or in
// every visible project when the folder has none.
func (b cliBackend) MyTasks(ctx context.Context) ([]tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	c := b.client(cfg)
	project := requestProjectAxes(cfg.Machine).Project
	if project == "" {
		return b.myTasksEverywhere(c)
	}
	list, err := c.ListHumanTasks(project, "open", true, 0)
	if err != nil {
		return nil, classifyErr(err)
	}
	out := make([]tui.Item, 0, len(list))
	for _, t := range list {
		out = append(out, tui.Item{Ref: t.HumanID(), Kind: "task", Title: t.Title, Meta: fmt.Sprintf("REQ-%d", t.RequestID)})
	}
	return cleanItems(out), nil
}

// OpenRequests are the open requests of the current project. Without a
// project the answer is "no project", never the requests of every project.
func (b cliBackend) OpenRequests(ctx context.Context) ([]tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	ax := requestProjectAxes(cfg.Machine)
	if ax.Project == "" {
		return nil, &tui.ProblemError{Kind: tui.ProblemNoProject}
	}
	page, err := b.client(cfg).SearchRequests(requestdomain.SearchFilter{Scope: ax, State: "open", Limit: 50})
	if err != nil {
		return nil, classifyErr(err)
	}
	out := make([]tui.Item, 0, len(page.Results))
	for _, h := range page.Results {
		meta := ""
		if h.OpenCriteria > 0 {
			meta = fmt.Sprintf("%d open", h.OpenCriteria)
		}
		out = append(out, tui.Item{Ref: fmt.Sprintf("REQ-%d", h.Request.ID), Kind: "issue", Title: h.Request.Title, Meta: meta})
	}
	return cleanItems(out), nil
}

// Messages lists what is unread in the room of the current repository for
// this terminal, without moving the read mark: reading the list is not
// reading the messages.
func (b cliBackend) Messages(ctx context.Context) ([]tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	room, code := coordRoomKey(false, nil, cfg, io.Discard)
	if code != 0 {
		room = store.RoomKeyForMachine(cfg.Machine)
	}
	c := b.client(cfg)
	me := coordSession(cfg)
	after, _ := c.CoordCursor(me, store.DestinationRoom, room)
	msgs, err := c.CoordInbox(store.DestinationRoom, room, me, after, 30)
	if messagesNotFound(err) {
		return []tui.Item{}, nil // no room yet: no messages
	}
	if err != nil {
		return nil, classifyErr(err)
	}
	builder := clientBuilder(c, "")
	out := make([]tui.Item, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Expired {
			continue
		}
		card := builder.MessageCard(store.MessageHit{ID: m.ID, RoomKey: room, Sequence: m.Sequence, Sender: m.SenderExternalID, Body: m.Body})
		out = append(out, tui.Item{Ref: fmt.Sprintf("#%d", m.Sequence), Kind: "message", Title: m.Body, Meta: m.SenderExternalID, Card: &card})
	}
	return cleanItems(out), nil
}

func (cliBackend) Open(url string) (bool, string) {
	if !tui.WebURL(url) {
		return false, "not a web address"
	}
	if ok, why := browserAllowed(); !ok {
		return false, why
	}
	if err := openLink(url); err != nil {
		return false, links.Clean(err.Error(), 120)
	}
	return true, ""
}

func (cliBackend) SetChannel(channel string) error {
	if !config.ValidChannel(channel) {
		return fmt.Errorf("unknown channel %q", channel)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Channel = channel
	return config.Save(cfg)
}

func (cliBackend) Doctor(ctx context.Context, fix bool) (tui.DoctorReport, error) {
	args := []string{}
	if fix {
		args = append(args, "--fix")
	}
	var buf bytes.Buffer
	code := cmdDoctor(args, &buf)
	rep := parseDoctor(buf.String())
	rep.OK = code == 0
	if code == 2 {
		return rep, errors.New(strings.TrimSpace(buf.String()))
	}
	return rep, nil
}

// parseDoctor reads the lines printDoctorChecks writes: name, two blanks,
// status, two blanks, detail, and an indented "fix:" line under a failure.
func parseDoctor(out string) tui.DoctorReport {
	var rep tui.DoctorReport
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "fix: ") && len(rep.Lines) > 0 {
			rep.Lines[len(rep.Lines)-1].Fix = strings.TrimPrefix(trim, "fix: ")
			continue
		}
		matched := false
		for _, st := range []string{"OK", "FAIL", "UNVERIFIED"} {
			sep := "  " + st + "  "
			if i := strings.Index(line, sep); i > 0 {
				rep.Lines = append(rep.Lines, tui.DoctorLine{Name: strings.TrimSpace(line[:i]), Status: st, Detail: strings.TrimSpace(line[i+len(sep):])})
				matched = true
				break
			}
			if strings.HasSuffix(line, "  "+st) {
				rep.Lines = append(rep.Lines, tui.DoctorLine{Name: strings.TrimSpace(strings.TrimSuffix(line, "  "+st)), Status: st})
				matched = true
				break
			}
		}
		if !matched {
			name, detail, _ := strings.Cut(trim, ": ")
			rep.Lines = append(rep.Lines, tui.DoctorLine{Name: name, Status: "note", Detail: detail})
		}
	}
	for i := range rep.Lines {
		l := &rep.Lines[i]
		l.Name, l.Detail, l.Fix = links.Clean(l.Name, 80), links.Clean(l.Detail, 300), links.Clean(l.Fix, 300)
	}
	return rep
}

func (cliBackend) RestartCollector(ctx context.Context) (string, error) {
	if _, err := config.Load(); err != nil {
		return "", errors.New("this machine is not connected yet")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	changes, err := installer.RestartWatchService(home)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	return links.Clean(changes[len(changes)-1].Action, 200), nil
}

func (cliBackend) Logout(ctx context.Context, server string) error {
	err := logout(ctx, false)
	var warn *logoutWarning
	if errors.As(err, &warn) {
		return warn
	}
	return err
}

// loginHandle is what the TUI carries from StartLogin to WaitLogin.
type loginHandle struct {
	cfg      config.Config
	previous config.Config
	auth     client.DeviceAuth
}

func (cliBackend) StartLogin(ctx context.Context, server, machine string) (tui.LoginStart, error) {
	previous, _ := config.Load()
	cfg, err := loginConfig(server, machine)
	if err != nil {
		return tui.LoginStart{}, err
	}
	auth, err := client.New(cfg).StartDeviceLogin(ctx, cfg.Machine)
	if err != nil {
		return tui.LoginStart{}, err
	}
	return tui.LoginStart{
		Server: cfg.ServerURL, Machine: cfg.Machine,
		URL: auth.VerificationURI, CompleteURL: auth.VerificationURIComplete, Code: tui.LoginCode(auth.UserCode),
		Expires: time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second),
		Handle:  loginHandle{cfg: cfg, previous: previous, auth: auth},
	}, nil
}

func (cliBackend) WaitLogin(ctx context.Context, start tui.LoginStart) (tui.LoginResult, error) {
	h, ok := start.Handle.(loginHandle)
	if !ok {
		return tui.LoginResult{}, errors.New("no login in progress")
	}
	token, err := pollDeviceToken(ctx, client.New(h.cfg), h.auth)
	if err != nil {
		var taken *machineTakenError
		if errors.As(err, &taken) {
			return tui.LoginResult{}, &tui.MachineTakenError{Machine: h.cfg.Machine, Suggestion: machineSuggestion(h.cfg.Machine)}
		}
		return tui.LoginResult{}, err
	}
	cfg, err := saveLogin(h.cfg, token)
	if err != nil {
		return tui.LoginResult{}, err
	}
	res := tui.LoginResult{Machine: links.Clean(cfg.Machine, 64)}
	if who, err := client.NewWithTimeout(cfg, 10*time.Second).WhoAmIContext(ctx); err == nil {
		res.Account = links.Clean(who.Label, 60)
	}
	res.Note, res.Warning = retireOldLogin(context.WithoutCancel(ctx), h.previous, cfg)
	return res, nil
}
