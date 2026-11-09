package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// bad carries what a compromised server could send: ESC sequences (OSC 52
// clipboard write, clear screen), a bare BEL and C1 controls (CSI U+009B,
// OSC U+009D, ST U+009C) that some terminals act on without an ESC.
const bad = "\x1b]52;c;ZXZpbA==\x07\x1b[2J\x07\u009b2J\u009d0;x\u009c"

func evil(s string) string { return s + bad }

// hostileBackend puts bad into every string field the interface shows.
type hostileBackend struct {
	*fakeBackend
	completeURL string
}

func (h hostileBackend) Local() Local {
	l := h.fakeBackend.Local()
	l.ConfigPath, l.Machine, l.ClientVersion = evil(l.ConfigPath), evil(l.Machine), evil(l.ClientVersion)
	l.Project, l.Branch, l.ProfileURL = evil(l.Project), evil(l.Branch), evil(l.ProfileURL)
	l.Collector.Detail = evil(l.Collector.Detail)
	for i := range l.Servers {
		l.Servers[i].Name, l.Servers[i].Machine, l.Servers[i].Account = evil("Firma"), evil("m"), evil("a")
	}
	return l
}

func (h hostileBackend) CheckServer(ctx context.Context, url string) Server {
	s := h.fakeBackend.CheckServer(ctx, url)
	s.Account, s.Machine, s.Version, s.Channel, s.Err = evil("robin"), evil("box"), evil("1.0"), evil("dev"), evil("err")
	return s
}

func evilItems(items []Item) []Item {
	for i := range items {
		items[i].Ref, items[i].Title, items[i].Meta, items[i].Kind = evil(items[i].Ref), evil(items[i].Title), evil(items[i].Meta), evil("k")
	}
	return items
}

func (h hostileBackend) MyTasks(ctx context.Context) ([]Item, error) {
	items, err := h.fakeBackend.MyTasks(ctx)
	return evilItems(items), err
}

func (h hostileBackend) OpenRequests(ctx context.Context) ([]Item, error) {
	items, err := h.fakeBackend.OpenRequests(ctx)
	return evilItems(items), err
}

func (h hostileBackend) Messages(ctx context.Context) ([]Item, error) {
	c := links.Card{Kind: evil("Message"), Ref: evil("#1"), Title: evil("t"), Status: evil("s"), Next: evil("n"), Who: evil("w"), URL: evil("https://ghosttree.example.com/ui/room")}
	return []Item{{Ref: evil("#1"), Title: evil("hallo"), Meta: evil("orchestrator"), Card: &c}}, errors.New(bad)
}

func (h hostileBackend) Search(ctx context.Context, line string) ([]Item, error) {
	items, err := h.fakeBackend.Search(ctx, line)
	return evilItems(items), err
}

func (h hostileBackend) Resolve(ctx context.Context, input string) (links.Card, error) {
	c, err := h.fakeBackend.Resolve(ctx, input)
	c.Kind, c.Ref, c.Title, c.Status, c.Next, c.Who, c.URL = evil(c.Kind), evil(c.Ref), evil(c.Title), evil(c.Status), evil(c.Next), evil(c.Who), evil(c.URL)
	return c, err
}

func (h hostileBackend) Open(url string) (bool, string) {
	h.fakeBackend.Open(url)
	return false, evil("why")
}

func (h hostileBackend) Doctor(ctx context.Context, fix bool) (DoctorReport, error) {
	rep, err := h.fakeBackend.Doctor(ctx, fix)
	for i := range rep.Lines {
		rep.Lines[i].Name, rep.Lines[i].Detail, rep.Lines[i].Fix = evil(rep.Lines[i].Name), evil(rep.Lines[i].Detail), evil("fix")
	}
	return rep, err
}

func (h hostileBackend) RestartCollector(ctx context.Context) (string, error) {
	return evil("restarted"), nil
}

func (h hostileBackend) Logout(ctx context.Context, server string) error {
	h.fakeBackend.Logout(ctx, server)
	return errors.New(bad)
}

func (h hostileBackend) StartLogin(ctx context.Context, server, machine string) (LoginStart, error) {
	st, err := h.fakeBackend.StartLogin(ctx, server, machine)
	st.Code += bad
	if h.completeURL != "" {
		st.CompleteURL = h.completeURL
	}
	return st, err
}

func (h hostileBackend) WaitLogin(ctx context.Context, start LoginStart) (LoginResult, error) {
	if h.waitBlock {
		<-ctx.Done()
		return LoginResult{}, ctx.Err()
	}
	if h.waitErr != nil {
		return LoginResult{}, h.waitErr
	}
	return LoginResult{Account: evil("mallory"), Machine: evil("m"), Note: evil("note"), Warning: evil("warning")}, nil
}

var (
	ownLink = regexp.MustCompile("\x1b]8;[^\x07\x1b]*;[^\x07\x1b]*(\x07|\x1b\\\\)")
	ownSGR  = regexp.MustCompile("\x1b\\[[0-9;:]*m")
)

// noForeignControls fails when the view holds any control character other
// than newline, after removing the interface's own colour sequences and
// OSC 8 links (which end in BEL).
func noForeignControls(t *testing.T, where, view string) {
	t.Helper()
	rest := ownSGR.ReplaceAllString(ownLink.ReplaceAllString(view, ""), "")
	for _, r := range rest {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("%s: control character %U reached the terminal: %q", where, r, rest)
		}
	}
}

func TestNoServerStringReachesTheTerminalUnfiltered(t *testing.T) {
	f := newFake()
	f.local.Servers = append(f.local.Servers, Server{URL: "https://second.example.com", Token: TokenStored})
	h := hostileBackend{fakeBackend: f}
	for _, look := range []LogoStyle{LogoMark, LogoTree, LogoLines} {
		m := ready(f, Options{Look: look}, 110, 44, darkBG)
		m.be = h
		m.local = h.Local()
		m = runCmd(m, tea.Batch(m.loadHome()...), 0)
		for i := 0; i < listCount; i++ {
			m.section = i
			noForeignControls(t, "home "+look.String(), m.View().Content)
		}
	}

	m := ready(f, Options{}, 110, 44, darkBG)
	m.be = h
	m.local = h.Local()
	m = runCmd(m, tea.Batch(m.loadHome()...), 0)

	// a row with its own card, a resolved card, results
	m.section = listMessages
	m = drive(m, key("enter"))
	noForeignControls(t, "message card", m.View().Content)
	m = drive(m, key("enter"))
	noForeignControls(t, "flash after open", m.View().Content)
	m = drive(m, key("esc"))
	m.section = listTasks
	m = drive(m, key("enter"))
	noForeignControls(t, "card", m.View().Content)
	m = drive(m, key("esc"))
	m.search.SetValue("")
	m = typeText(m, "tui suche")
	m = drive(m, key("enter"))
	noForeignControls(t, "results", m.View().Content)
	m = drive(m, key("esc"))

	// every settings row, then a sign-out with its confirmation and error
	m = drive(m, key("ctrl+s"))
	for i := range m.settingsRows() {
		m.settings.cursor = i
		noForeignControls(t, "settings", m.View().Content)
	}
	m = settingsAt(t, m, "collector")
	m = drive(m, key("enter"))
	noForeignControls(t, "collector note", m.View().Content)
	m = settingsAt(t, m, "server")
	m = drive(m, key("x"))
	noForeignControls(t, "logout question", m.View().Content)
	m = drive(m, key("y"))
	noForeignControls(t, "logout error", m.View().Content)

	// login: waiting, done with note and warning, failed
	f.local.Servers[0].Token = TokenStored
	m = settingsAt(t, m, "server")
	m = drive(m, key("enter"))
	f.waitBlock = true
	m = drive(m, key("enter"))
	if m.login.step != loginWaiting {
		t.Fatalf("step %v err %v", m.login.step, m.login.err)
	}
	if c := m.login.start.Code; !strings.HasPrefix(c, "WDJB-MJHT") || strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
		t.Fatalf("code %q", c)
	}
	noForeignControls(t, "login waiting", m.View().Content)
	f.waitBlock = false
	m = drive(m, key("esc"))
	m = drive(m, key("enter"))
	if m.login.step != loginDone {
		t.Fatalf("step %v", m.login.step)
	}
	noForeignControls(t, "login done", m.View().Content)
	f.waitErr = errors.New(bad)
	m.login.step = loginForm
	m = drive(m, key("enter"))
	noForeignControls(t, "login failed", m.View().Content)
	f.waitErr = &MachineTakenError{Machine: "m", Suggestion: evil("m-2")}
	m.login.step = loginForm
	m = drive(m, key("enter"))
	noForeignControls(t, "login taken", m.View().Content)
}

func TestLoginAddressOnAnotherHostIsNeitherShownNorOpened(t *testing.T) {
	for _, addr := range []string{
		"https://evil.example.net/device?code=WDJB-MJHT",
		"file:///etc/passwd",
		"-oProxyCommand=x",
		"https://ghosttree.example.com/device?code=A\x1b]8;;x",
		"https://ghosttree.example.com/device?code=A\u009b2J",
	} {
		f := newFake()
		h := hostileBackend{fakeBackend: f, completeURL: addr}
		m := ready(f, Options{Start: ScreenLogin, LoginServer: "https://ghosttree.example.com", LoginMachine: "m"}, 100, 40, darkBG)
		m.be = h
		m = drive(m, key("enter"))
		if m.login.step != loginFailed {
			t.Fatalf("%q: step %v", addr, m.login.step)
		}
		if len(f.opened) != 0 {
			t.Fatalf("%q: opened %v", addr, f.opened)
		}
	}
}

func TestLoginCodeKeepsOnlyCodeCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"WDJB-MJHT":             "WDJB-MJHT",
		"wdjb-mjht":             "WDJB-MJHT",
		"WDJB\x1b]52;c;x\x07":   "WDJB52CX",
		"AB\u009b2J":            "AB2J",
		"":                      "",
		strings.Repeat("A", 99): strings.Repeat("A", 32),
	} {
		if got := LoginCode(in); got != want {
			t.Errorf("LoginCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnlyWebAddressesAreOpenedOrCopied(t *testing.T) {
	for in, want := range map[string]bool{
		"https://ghosttree.example.com/ui/x": true,
		"http://localhost:8474/ui":           true,
		"javascript:alert(1)":                false,
		"file:///etc/passwd":                 false,
		"-a Calculator":                      false,
		"https://":                           false,
		"https://h/\x1b[2J":                  false,
		"https://h/\u009b2J":                 false,
		"https://h/a b":                      false,
	} {
		if got := webURL(in); got != want {
			t.Errorf("webURL(%q) = %v", in, got)
		}
	}
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	m.screen = ScreenCard
	m.card.URL = "javascript:alert(1)"
	m = drive(m, key("enter"))
	if len(f.opened) != 0 {
		t.Fatalf("opened %v", f.opened)
	}
	if _, cmd := m.Update(key("c")); cmd != nil {
		t.Fatal("copied a non-web address")
	}
}

func TestSignOutAsksFirst(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 40, darkBG)
	m = settingsAt(t, m, "server")
	m = drive(m, key("x"))
	if !m.settings.confirm || len(f.loggedOut) != 0 || !strings.Contains(plainView(m), "abmelden?") {
		t.Fatalf("confirm %v loggedOut %v view %s", m.settings.confirm, f.loggedOut, plainView(m))
	}
	m = drive(m, key("n"))
	if m.settings.confirm || len(f.loggedOut) != 0 {
		t.Fatalf("n: confirm %v loggedOut %v", m.settings.confirm, f.loggedOut)
	}
	m = drive(m, key("x"))
	m = drive(m, key("y"))
	if len(f.loggedOut) != 1 {
		t.Fatalf("y: loggedOut %v", f.loggedOut)
	}
}
