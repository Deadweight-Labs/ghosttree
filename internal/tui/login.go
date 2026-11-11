package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type loginStep int

const (
	loginForm loginStep = iota
	loginStarting
	loginWaiting
	loginDone
	loginFailed
)

type loginState struct {
	step    loginStep
	server  textinput.Model
	machine textinput.Model
	focus   int
	start   LoginStart
	result  LoginResult
	err     error
	opened  bool
	why     string
	cancel  context.CancelFunc
	seq     int // a newer attempt ignores answers of an older one
}

func newLoginState(l lang, server, machine string, local Local) loginState {
	s := loginState{server: textinput.New(), machine: textinput.New()}
	if server == "" {
		if d, ok := local.DefaultServer(); ok {
			server = d.URL
		}
	}
	if machine == "" {
		machine = local.Machine
	}
	s.server.Prompt = ""
	s.server.Placeholder = "https://ghosttree.example.com"
	s.server.SetValue(server)
	s.server.CharLimit = 300
	s.machine.Prompt = ""
	s.machine.SetValue(machine)
	s.machine.CharLimit = 64
	s.server.SetWidth(44)
	s.machine.SetWidth(44)
	s.server.Focus()
	return s
}

func (s *loginState) applyStyles(st textinput.Styles) {
	s.server.SetStyles(st)
	s.machine.SetStyles(st)
}

type (
	loginStartMsg struct {
		seq   int
		start LoginStart
		err   error
	}
	loginDoneMsg struct {
		seq int
		res LoginResult
		err error
	}
	loginQuitMsg struct{}
)

// openLogin shows the login screen with server (and machine) filled in.
func (m *Model) openLogin(server string, focusMachine bool) tea.Cmd {
	m.go_(ScreenLogin)
	m.search.Blur()
	keepMachine := m.login.machine.Value()
	m.login = newLoginState(m.lang, server, keepMachine, m.local)
	if server == "" {
		m.login.server.SetValue("")
	}
	m.applyInputStyles()
	if focusMachine {
		m.login.focus = 1
		m.login.server.Blur()
		m.login.machine.CursorEnd()
		return m.login.machine.Focus()
	}
	m.login.server.CursorEnd()
	return m.login.server.Focus()
}

func (m *Model) startLogin() tea.Cmd {
	if m.login.cancel != nil {
		m.login.cancel()
	}
	server := strings.TrimRight(strings.TrimSpace(m.login.server.Value()), "/")
	machine := strings.TrimSpace(m.login.machine.Value())
	if server == "" {
		m.login.server.Focus()
		return nil
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
		m.login.server.SetValue(server)
	}
	m.login.seq++
	seq := m.login.seq
	m.login.step = loginStarting
	m.login.err = nil
	m.login.server.Blur()
	m.login.machine.Blur()
	return tea.Batch(m.ensureSpin(), call(quickTimeout, func(ctx context.Context) tea.Msg {
		st, err := m.be.StartLogin(ctx, server, machine)
		return loginStartMsg{seq: seq, start: st, err: err}
	}))
}

func (m Model) updateLogin(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loginStartMsg:
		if msg.seq != m.login.seq {
			return m, nil
		}
		if msg.err != nil {
			m.login.step, m.login.err = loginFailed, msg.err
			return m, nil
		}
		// The addresses come from the server. Only an http(s) address on the
		// server the person confirmed is shown as a link or opened.
		st := msg.start
		if !webURL(st.CompleteURL) || !webURL(st.URL) || !sameHost(st.CompleteURL, st.Server) || !sameHost(st.URL, st.Server) {
			m.login.step, m.login.err = loginFailed, errors.New("the server answered with a login address on another host or of an unexpected kind")
			return m, nil
		}
		st.Code = LoginCode(st.Code)
		if st.Code == "" {
			m.login.step, m.login.err = loginFailed, errors.New("the server answered without a usable code")
			return m, nil
		}
		msg.start = st
		m.login.step = loginWaiting
		m.login.start = st
		m.login.opened, m.login.why = m.be.Open(st.CompleteURL)
		// The wait ends a little after the code expires, measured on the
		// model's clock, so a test clock does not make it end at once.
		ctx, cancel := context.WithCancel(context.Background())
		if !msg.start.Expires.IsZero() {
			ctx, cancel = context.WithTimeout(context.Background(), msg.start.Expires.Sub(m.opt.now())+5*time.Second)
		}
		m.login.cancel = cancel
		seq, start := msg.seq, msg.start
		return m, tea.Batch(m.ensureSpin(), func() tea.Msg {
			res, err := m.be.WaitLogin(ctx, start)
			return loginDoneMsg{seq: seq, res: res, err: err}
		})
	case loginDoneMsg:
		if msg.seq != m.login.seq {
			return m, nil
		}
		if m.login.cancel != nil {
			m.login.cancel()
			m.login.cancel = nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				return m, nil
			}
			m.login.step, m.login.err = loginFailed, msg.err
			return m, nil
		}
		m.login.step, m.login.result = loginDone, msg.res
		m.LoggedIn = true
		m.local = m.be.Local()
		if s, ok := m.local.DefaultServer(); ok {
			m.server = s
		}
		if m.opt.Start == ScreenLogin {
			return m, m.opt.tick(1600*time.Millisecond, loginQuitMsg{})
		}
		return m, nil
	case loginQuitMsg:
		m.quitting = true
		return m, tea.Quit
	case tea.PasteMsg:
		if m.login.step == loginForm {
			return m.loginInput(msg)
		}
	case tea.KeyPressMsg:
		return m.keyLogin(msg)
	}
	return m, nil
}

func (m Model) loginInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.login.focus == 0 {
		m.login.server, cmd = m.login.server.Update(msg)
	} else {
		m.login.machine, cmd = m.login.machine.Update(msg)
	}
	return m, cmd
}

func (m Model) keyLogin(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.login.step {
	case loginForm:
		switch key {
		case "esc":
			return m, m.goBack()
		case "tab", "shift+tab", "up", "down":
			m.login.focus = 1 - m.login.focus
			if m.login.focus == 0 {
				m.login.machine.Blur()
				return m, m.login.server.Focus()
			}
			m.login.server.Blur()
			return m, m.login.machine.Focus()
		case "enter":
			if m.login.focus == 0 && strings.TrimSpace(m.login.machine.Value()) == "" {
				m.login.focus = 1
				m.login.server.Blur()
				return m, m.login.machine.Focus()
			}
			return m, m.startLogin()
		}
		return m.loginInput(msg)
	case loginStarting, loginWaiting:
		switch key {
		case "esc":
			if m.login.cancel != nil {
				m.login.cancel()
				m.login.cancel = nil
			}
			m.login.seq++
			m.login.step = loginForm
			return m, m.login.server.Focus()
		case "o", "enter":
			if m.login.step == loginWaiting {
				return m, m.openURL(m.login.start.CompleteURL)
			}
		case "c", "y":
			if m.login.step == loginWaiting && m.login.start.Code != "" {
				return m, tea.Batch(tea.SetClipboard(m.login.start.Code), m.setFlash(m.lang.t("code.copied"), false))
			}
		}
	case loginDone:
		if m.opt.Start == ScreenLogin {
			m.quitting = true
			return m, tea.Quit
		}
		if key == "enter" || key == "esc" {
			m.back = nil
			m.screen = ScreenHome
			m.local = m.be.Local()
			return m, tea.Batch(append(m.loadHome(), m.search.Focus())...)
		}
	case loginFailed:
		switch key {
		case "enter", "r":
			var taken *MachineTakenError
			if errors.As(m.login.err, &taken) && taken.Suggestion != "" {
				m.login.machine.SetValue(taken.Suggestion)
			}
			m.login.step = loginForm
			m.login.focus = 0
			return m, m.login.server.Focus()
		case "esc":
			return m, m.goBack()
		}
	}
	return m, nil
}

func (m Model) viewLogin() block {
	var b block
	width := min(64, max(30, m.w-8))
	lg := m.logo.View()
	if m.h >= lipgloss.Height(lg)+18 {
		b.blank(1)
		var head block
		head.add(lg)
		b.append(head.center(m.w))
	}
	b.blank(1)
	var mark block
	mark.add(m.wordmark())
	b.append(mark.center(m.w))
	b.blank(2)

	var body block
	field := func(label string, in textinput.Model, focused bool) {
		st := m.th.Dim
		if focused {
			st = m.th.Key
		}
		body.add(st.Render(label))
		body.add(in.View())
		body.add(m.th.Rule.Render(strings.Repeat("─", min(width, 48))))
	}
	var footer string
	switch m.login.step {
	case loginForm:
		field(m.lang.t("login.server"), m.login.server, m.login.focus == 0)
		body.blank(1)
		field(m.lang.t("login.machine"), m.login.machine, m.login.focus == 1)
		footer = m.keys("enter", m.lang.t("key.continue"), "tab", m.lang.t("key.next"), "esc", m.lang.t("key.back"))
	case loginStarting:
		body.add(m.th.Dim.Render(clean(host(m.login.server.Value()), 80)))
		body.blank(1)
		body.add(m.th.Key.Render(spinner(m.spin)))
		footer = m.keys("esc", m.lang.t("key.back"))
	case loginWaiting:
		st := m.login.start
		body.add(m.th.Dim.Render(m.lang.t("login.code")))
		code := st.Code
		codeStyle := m.th.Title.Padding(0, 2)
		if !m.th.Plain {
			codeStyle = codeStyle.Background(m.th.Surface).Foreground(m.th.Accent)
		}
		body.add(codeStyle.Render(code))
		body.blank(1)
		link := m.th.Ref.Underline(true).Hyperlink(st.CompleteURL).Render(ansi.Truncate(clean(st.URL, 300), width, "…"))
		body.addHit(link, action{kind: "login.open"})
		if !m.login.opened && m.login.why != "" {
			body.add(m.th.Subtle.Render(m.lang.t("not.opened") + " (" + clean(m.login.why, 80) + ")"))
		}
		body.blank(1)
		wait := m.th.Key.Render(spinner(m.spin)) + " " + m.th.Dim.Render(m.lang.t("login.waiting"))
		if !st.Expires.IsZero() {
			wait += m.th.Subtle.Render(" · " + m.lang.t("login.expires") + " " + st.Expires.Local().Format("15:04"))
		}
		body.add(wait)
		if m.lang == langDE {
			footer = m.keys("o", m.lang.t("key.browser"), "c", "Code kopieren", "esc", m.lang.t("key.back"))
		} else {
			footer = m.keys("o", m.lang.t("key.browser"), "c", "copy code", "esc", m.lang.t("key.back"))
		}
	case loginDone:
		r := m.login.result
		body.add(m.th.Good.Render("●") + " " + m.th.Dim.Render(m.lang.t("login.done")) + " " + m.th.Title.Render(clean(r.Account, 60)))
		body.add("  " + m.th.Dim.Render(clean(r.Machine, 64)+" · "+clean(host(m.login.start.Server), 80)))
		if r.Note != "" {
			body.add("  " + m.th.Dim.Render(clean(r.Note, 160)))
		}
		if r.Warning != "" {
			body.blank(1)
			for _, l := range strings.Split(lipgloss.Wrap(clean(r.Warning, 400), width, " "), "\n") {
				body.add(m.th.Caution.Render(l))
			}
		}
		footer = m.keys("enter", m.lang.t("key.continue"))
	case loginFailed:
		var taken *MachineTakenError
		body.add(m.th.Bad.Render("●") + " " + m.th.Title.Render(m.lang.t("login.failed")))
		body.blank(1)
		if errors.As(m.login.err, &taken) {
			body.add(m.th.Text.Render(m.lang.t("login.taken")))
			body.add(m.th.Key.Render(clean(taken.Suggestion, 64)))
		} else {
			for _, l := range strings.Split(lipgloss.Wrap(errText(m.login.err), width, " "), "\n") {
				body.add(m.th.Caution.Render(l))
			}
		}
		footer = m.keys("enter", m.lang.t("key.retry"), "esc", m.lang.t("key.back"))
	}
	b.append(body.indent(max(2, (m.w-min(width, 48))/2)))
	return m.page(b, footer)
}
