package tui

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// fakeBackend answers like a small ghosttree server with one project.
type fakeBackend struct {
	mu        sync.Mutex
	local     Local
	opened    []string
	resolved  []string
	searched  []string
	channel   string
	loggedOut []string
	doctorFix []bool
	restarted int
	loginErr  error
	waitErr   error
	waitBlock bool // WaitLogin waits until it is cancelled
}

func newFake() *fakeBackend {
	return &fakeBackend{channel: "stable", local: Local{
		Configured:    true,
		ConfigPath:    "/home/robin/.config/ghosttree/config.json",
		Servers:       []Server{{URL: "https://ghosttree.example.com", Default: true, Token: TokenStored}},
		Machine:       "workstation",
		ClientVersion: "0.2.0-dev",
		Project:       "github.com/deadweight-labs/ghosttree",
		Branch:        "feat/tui",
		Collector:     Collector{Running: true, State: "running", Detail: "pid 4242"},
		ProfileURL:    "https://ghosttree.example.com/ui/profile",
	}}
}

func (f *fakeBackend) Local() Local {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.local
	l.Channel = f.channel
	l.Servers = append([]Server(nil), f.local.Servers...)
	return l
}

func (f *fakeBackend) CheckServer(ctx context.Context, url string) Server {
	return Server{URL: url, Default: true, Token: TokenValid, Account: "robin", Machine: "workstation", Checked: true, Reachable: true, Version: "0.2.0", Channel: "dev"}
}

func (f *fakeBackend) Resolve(ctx context.Context, input string) (links.Card, error) {
	f.mu.Lock()
	f.resolved = append(f.resolved, input)
	f.mu.Unlock()
	if input == "REQ-404" {
		return links.Card{}, errors.New("REQ-404 not found, or you may not read it")
	}
	return links.Card{Kind: "Issue", Ref: input, Title: "ctx als Terminal-Zuhause: TUI mit Schnellsuche und lokalen Einstellungen", Status: "open", Next: "8 of 8 criteria open", Who: "agent", URL: "https://ghosttree.example.com/ui/deadweight-labs/ghosttree/issues/619"}, nil
}

func (f *fakeBackend) Search(ctx context.Context, line string) ([]Item, error) {
	f.mu.Lock()
	f.searched = append(f.searched, line)
	f.mu.Unlock()
	return []Item{
		{Ref: "#4354", Kind: "note", Title: "Rollout steht als Ablauf, ctx run zeigt die Schritte", Meta: "note"},
		{Ref: "REQ-619", Kind: "issue", Title: "ctx als Terminal-Zuhause", Meta: "open"},
	}, nil
}

func (f *fakeBackend) MyTasks(ctx context.Context) ([]Item, error) {
	return []Item{
		{Ref: "TASK-133", Title: "Mocks v2 der GitHub-artigen Oberfläche freigeben", Meta: "REQ-599"},
		{Ref: "TASK-124", Title: "Drei Entscheidungen zum Git-nativen ghosttree treffen", Meta: "REQ-602"},
		{Ref: "TASK-116", Title: "Release-PR dev → main für v0.1.0 freigeben", Meta: "REQ-580"},
	}, nil
}

func (f *fakeBackend) OpenRequests(ctx context.Context) ([]Item, error) {
	return []Item{
		{Ref: "REQ-619", Title: "ctx als Terminal-Zuhause: TUI mit Schnellsuche", Meta: "8 offen"},
		{Ref: "REQ-610", Title: "Mehrere ghosttree-Server gleichzeitig", Meta: "6 offen"},
		{Ref: "REQ-599", Title: "Web-Oberfläche nach dem Vorbild GitHub", Meta: "14 offen"},
		{Ref: "REQ-598", Title: "Öffentliches Lesen für Open-Source-Repos", Meta: "7 offen"},
		{Ref: "REQ-613", Title: "GitHub-Integration über das Lesen hinaus", Meta: "7 offen"},
	}, nil
}

func (f *fakeBackend) Messages(ctx context.Context) ([]Item, error) {
	return []Item{
		{Ref: "room:12", Title: "Rollout von #187 steht, bitte einmal gegenlesen", Meta: "orchestrator"},
		{Ref: "room:12", Title: "UI-Thread: Avatar aus GitHub übernehmen?", Meta: "robin"},
	}, nil
}

func (f *fakeBackend) Open(url string) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, url)
	return false, "ssh session"
}

func (f *fakeBackend) SetChannel(ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channel = ch
	return nil
}

func (f *fakeBackend) Doctor(ctx context.Context, fix bool) (DoctorReport, error) {
	f.mu.Lock()
	f.doctorFix = append(f.doctorFix, fix)
	f.mu.Unlock()
	status := "FAIL"
	if fix {
		status = "OK"
	}
	return DoctorReport{Lines: []DoctorLine{
		{Name: "claude mcp", Status: "OK", Detail: "~/.claude.json"},
		{Name: "claude hooks", Status: "OK", Detail: "6 events"},
		{Name: "codex hooks", Status: status, Detail: "approval missing", Fix: "ctx doctor --fix"},
		{Name: "collector active", Status: "OK", Detail: "watch process pid 4242"},
	}, OK: fix}, nil
}

func (f *fakeBackend) RestartCollector(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted++
	return "restarted ghosttree-watch.service", nil
}

func (f *fakeBackend) Logout(ctx context.Context, server string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedOut = append(f.loggedOut, server)
	for i := range f.local.Servers {
		if f.local.Servers[i].URL == server {
			f.local.Servers[i].Token = TokenNone
		}
	}
	return nil
}

func (f *fakeBackend) StartLogin(ctx context.Context, server, machine string) (LoginStart, error) {
	if f.loginErr != nil {
		return LoginStart{}, f.loginErr
	}
	return LoginStart{Server: server, Machine: machine, URL: server + "/device", CompleteURL: server + "/device?code=WDJB-MJHT", Code: "WDJB-MJHT", Expires: time.Date(2026, 11, 9, 14, 32, 0, 0, time.UTC)}, nil
}

func (f *fakeBackend) WaitLogin(ctx context.Context, start LoginStart) (LoginResult, error) {
	if f.waitBlock {
		<-ctx.Done()
		return LoginResult{}, ctx.Err()
	}
	if f.waitErr != nil {
		return LoginResult{}, f.waitErr
	}
	return LoginResult{Account: "robin", Machine: start.Machine}, nil
}

// drive feeds a message through Update and runs the commands it returns,
// recursively, the way the Bubble Tea loop would, but without ticks (they
// would wait for real time) and without terminal queries.
func drive(m Model, msg tea.Msg) Model {
	next, cmd := m.Update(msg)
	m = next.(Model)
	return runCmd(m, cmd, 0)
}

func runCmd(m Model, cmd tea.Cmd, depth int) Model {
	if cmd == nil || depth > 8 {
		return m
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(150 * time.Millisecond):
		return m // a tick: leave it
	}
	switch v := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range v {
			m = runCmd(m, c, depth+1)
		}
		return m
	case logoTickMsg, spinTickMsg, flashDoneMsg, loginQuitMsg:
		return m
	}
	if strings.HasPrefix(typeName(msg), "tea.") {
		return m // terminal commands: clipboard, background query, quit
	}
	next, c := m.Update(msg)
	return runCmd(next.(Model), c, depth+1)
}

func typeName(v any) string { return sprintType(v) }

// key builds a key press for a string like "enter", "tab" or "a".
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m = drive(m, key(string(r)))
	}
	return m
}

var darkBG = tea.BackgroundColorMsg{Color: color.RGBA{0x0d, 0x11, 0x17, 0xff}}
var lightBG = tea.BackgroundColorMsg{Color: color.RGBA{0xff, 0xff, 0xff, 0xff}}

// ready is a model at the given size and background with the home loaded
// and the logo at rest.
func ready(f *fakeBackend, opt Options, w, h int, bg tea.BackgroundColorMsg) Model {
	opt.Still = true
	if opt.Getenv == nil {
		opt.Getenv = func(k string) string {
			if k == "LANG" {
				return "de_DE.UTF-8"
			}
			return ""
		}
	}
	m := New(f, opt)
	m = drive(m, tea.WindowSizeMsg{Width: w, Height: h})
	m = drive(m, bg)
	m = runCmd(m, tea.Batch(m.loadHome()...), 0)
	return m
}
