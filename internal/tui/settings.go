package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type settingsState struct {
	cursor     int
	doctor     *DoctorReport
	doctorErr  error
	doctorBusy bool
	busy       string // what runs right now, for the selected row
	confirm    bool   // sign-out asked, waiting for y or n
	note       string
	noteBad    bool
}

func (s *settingsState) move(d, n int) {
	if n == 0 {
		return
	}
	s.cursor = max(0, min(n-1, s.cursor+d))
	s.note = ""
}

type settingRow struct {
	kind   string // server, add-server, channel, harness, collector, machine, github
	server int
}

func (m Model) settingsRows() []settingRow {
	var rows []settingRow
	for i := range m.local.Servers {
		rows = append(rows, settingRow{kind: "server", server: i})
	}
	rows = append(rows,
		settingRow{kind: "add-server"},
		settingRow{kind: "channel"},
		settingRow{kind: "harness"},
		settingRow{kind: "collector"},
		settingRow{kind: "machine"},
		settingRow{kind: "github"},
	)
	return rows
}

type (
	doctorMsg struct {
		rep DoctorReport
		err error
	}
	collectorMsg struct {
		text string
		err  error
	}
	logoutMsg  struct{ err error }
	channelMsg struct{ err error }
)

// enterSettings refreshes what the settings show: local state, each server
// and the doctor (read only).
func (m *Model) enterSettings() tea.Cmd {
	m.local = m.be.Local()
	m.settings.note = ""
	cmds := []tea.Cmd{m.runDoctor(false)}
	for _, s := range m.local.Servers {
		url := s.URL
		cmds = append(cmds, call(quickTimeout, func(ctx context.Context) tea.Msg {
			return serverMsg{m.be.CheckServer(ctx, url)}
		}))
	}
	return tea.Batch(cmds...)
}

func (m *Model) runDoctor(fix bool) tea.Cmd {
	m.settings.doctorBusy = true
	if fix {
		m.settings.busy = "harness"
	}
	return tea.Batch(m.ensureSpin(), call(slowTimeout, func(ctx context.Context) tea.Msg {
		rep, err := m.be.Doctor(ctx, fix)
		return doctorMsg{rep, err}
	}))
}

func (m Model) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doctorMsg:
		m.settings.doctorBusy = false
		m.settings.busy = ""
		if msg.err != nil {
			m.settings.doctorErr = msg.err
		} else {
			rep := msg.rep
			m.settings.doctor, m.settings.doctorErr = &rep, nil
		}
	case collectorMsg:
		m.settings.busy = ""
		m.local = m.be.Local()
		if msg.err != nil {
			m.settings.note, m.settings.noteBad = errText(msg.err), true
		} else {
			m.settings.note, m.settings.noteBad = msg.text, false
		}
	case logoutMsg:
		m.settings.busy = ""
		m.local = m.be.Local()
		if s, ok := m.local.DefaultServer(); ok {
			m.server = s
		}
		if msg.err != nil {
			m.settings.note, m.settings.noteBad = errText(msg.err), true
		} else {
			m.settings.note, m.settings.noteBad = m.lang.t("signed.out"), false
		}
	case channelMsg:
		m.local = m.be.Local()
		if msg.err != nil {
			m.settings.note, m.settings.noteBad = errText(msg.err), true
		}
	}
	return m, nil
}

func (m Model) keySettings(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.settingsRows()
	if m.settings.confirm {
		m.settings.confirm = false
		m.settings.note = ""
		if k := msg.String(); k != "y" && k != "j" {
			return m, nil
		}
		if m.settings.cursor >= len(rows) || rows[m.settings.cursor].kind != "server" {
			return m, nil
		}
		m.settings.busy = "server"
		url := m.local.Servers[rows[m.settings.cursor].server].URL
		return m, tea.Batch(m.ensureSpin(), call(quickTimeout, func(ctx context.Context) tea.Msg {
			return logoutMsg{m.be.Logout(ctx, url)}
		}))
	}
	switch msg.String() {
	case "esc", "backspace", "q":
		return m, m.goBack()
	case "up", "k", "ctrl+p":
		return m.move(-1)
	case "down", "j", "ctrl+n":
		return m.move(1)
	case "enter", " ":
		return m.activateSetting()
	case "left", "right", "h", "l":
		if m.settings.cursor < len(rows) && rows[m.settings.cursor].kind == "channel" {
			return m.activateSetting()
		}
	case "f":
		if m.settings.cursor < len(rows) && rows[m.settings.cursor].kind == "harness" && !m.settings.doctorBusy {
			return m, m.runDoctor(true)
		}
	case "x":
		if m.settings.cursor < len(rows) && rows[m.settings.cursor].kind == "server" {
			s := m.local.Servers[rows[m.settings.cursor].server]
			if s.Token == TokenNone {
				return m, nil
			}
			m.settings.confirm = true
			return m, nil
		}
	case "r", "ctrl+r":
		return m, m.enterSettings()
	}
	return m, nil
}

// activateSetting does what enter does on the selected row.
func (m Model) activateSetting() (tea.Model, tea.Cmd) {
	rows := m.settingsRows()
	if m.settings.cursor >= len(rows) {
		return m, nil
	}
	row := rows[m.settings.cursor]
	switch row.kind {
	case "server":
		return m, m.openLogin(m.local.Servers[row.server].URL, false)
	case "add-server":
		return m, m.openLogin("", false)
	case "machine":
		return m, m.openLogin(m.server.URL, true)
	case "channel":
		next := "dev"
		if m.local.Channel == "dev" {
			next = "stable"
		}
		if err := m.be.SetChannel(next); err != nil {
			m.settings.note, m.settings.noteBad = errText(err), true
			return m, nil
		}
		m.local = m.be.Local()
		return m, nil
	case "harness":
		if m.settings.doctorBusy {
			return m, nil
		}
		return m, m.runDoctor(false)
	case "collector":
		m.settings.busy = "collector"
		return m, tea.Batch(m.ensureSpin(), call(slowTimeout, func(ctx context.Context) tea.Msg {
			text, err := m.be.RestartCollector(ctx)
			return collectorMsg{text, err}
		}))
	case "github":
		return m, m.openURL(m.local.ProfileURL)
	}
	return m, nil
}

// equivalent is the non-interactive command that does what the row does.
func (m Model) equivalent(row settingRow) string {
	switch row.kind {
	case "server":
		s := m.local.Servers[row.server]
		if s.Token != TokenNone {
			return "ctx login --server " + s.URL + "   ·   ctx logout"
		}
		return "ctx login --server " + s.URL
	case "add-server":
		return "ctx login --server <url>"
	case "channel":
		next := "dev"
		if m.local.Channel == "dev" {
			next = "stable"
		}
		return "ctx config set channel " + next
	case "harness":
		return "ctx doctor   ·   ctx doctor --fix"
	case "collector":
		return "ctx install watch --restart"
	case "machine":
		return "ctx login --machine <name>"
	case "github":
		return m.local.ProfileURL
	}
	return ""
}

func (m Model) serverStatus(s Server) string {
	sep := m.th.Subtle.Render(" · ")
	var parts []string
	switch {
	case !s.Checked:
		parts = append(parts, m.th.Subtle.Render(m.lang.t("checking")))
	case !s.Reachable:
		parts = append(parts, m.th.Caution.Render(m.lang.t("unreachable")))
	default:
		parts = append(parts, m.th.Good.Render(m.lang.t("reachable")))
		if s.Version != "" {
			v := clean(s.Version, 40)
			if s.Channel != "" {
				v += " " + clean(s.Channel, 16)
			}
			parts = append(parts, m.th.Dim.Render(v))
		}
	}
	return strings.Join(parts, sep)
}

func (m Model) tokenStatus(s Server) string {
	sep := m.th.Subtle.Render(" · ")
	var who []string
	if s.Account != "" {
		who = append(who, m.th.Text.Render(clean(s.Account, 60)))
	}
	if s.Machine != "" {
		who = append(who, m.th.Dim.Render(clean(s.Machine, 64)))
	}
	switch s.Token {
	case TokenNone:
		who = append(who, m.th.Caution.Render(m.lang.t("signed.out")))
	case TokenStored:
		who = append(who, m.th.Dim.Render(m.lang.t("token.stored")))
	case TokenValid:
		who = append(who, m.th.Good.Render(m.lang.t("token.valid")))
	case TokenRejected:
		who = append(who, m.th.Bad.Render(m.lang.t("token.rejected")))
	}
	return strings.Join(who, sep)
}

func (m Model) harnessStatus() string {
	st := m.settings
	switch {
	case st.doctorBusy && st.doctor == nil:
		return m.th.Subtle.Render(m.lang.t("checking"))
	case st.doctorErr != nil:
		return m.th.Caution.Render(errText(st.doctorErr))
	case st.doctor == nil:
		return m.th.Subtle.Render(m.lang.t("doctor.unrun"))
	}
	ok, fail, unv := st.doctor.Counts()
	if fail == 0 && unv == 0 {
		return m.th.Good.Render(m.lang.t("doctor.ok")) + m.th.Subtle.Render(fmt.Sprintf("  %d", ok))
	}
	parts := []string{m.th.Good.Render(fmt.Sprintf("%d OK", ok))}
	if fail > 0 {
		parts = append(parts, m.th.Bad.Render(fmt.Sprintf("%d FAIL", fail)))
	}
	if unv > 0 {
		parts = append(parts, m.th.Caution.Render(fmt.Sprintf("%d ?", unv)))
	}
	return strings.Join(parts, m.th.Subtle.Render(" · "))
}

func (m Model) collectorStatus() string {
	c := m.local.Collector
	var s string
	switch c.State {
	case "running":
		s = m.th.Good.Render(m.lang.t("running"))
	case "stopped":
		s = m.th.Bad.Render(m.lang.t("stopped"))
	case "missing":
		s = m.th.Caution.Render(m.lang.t("missing"))
	default:
		s = m.th.Subtle.Render(m.lang.t("unknown"))
	}
	if c.Detail != "" {
		s += "  " + m.th.Dim.Render(clean(c.Detail, 80))
	}
	return s
}

func (m Model) channelStatus() string {
	opt := func(name string) string {
		if m.local.Channel == name {
			return m.th.Selected.Render(" " + name + " ")
		}
		return m.th.Dim.Render(" " + name + " ")
	}
	return opt("stable") + " " + opt("dev")
}

func (m Model) viewSettings() block {
	b := m.smallHeader(m.lang.t("settings"))
	rows := m.settingsRows()
	width := m.contentWidth()
	labelW := 18
	sel := func(i int) string {
		if i == m.settings.cursor {
			return m.th.Key.Render("▌ ")
		}
		return "  "
	}
	label := func(i int, s string) string {
		st := m.th.Text
		if i == m.settings.cursor {
			st = m.th.Title
		}
		return st.Render(fmt.Sprintf("%-*s", labelW, s))
	}
	busy := func(kind string) string {
		if m.settings.busy == kind {
			return "  " + m.th.Subtle.Render(spinner(m.spin))
		}
		return ""
	}
	section := func(s string) {
		b.blank(1)
		b.add("  " + m.th.Dim.Render(strings.ToUpper(s)))
	}
	section(m.lang.t("servers"))
	for i, row := range rows {
		switch row.kind {
		case "server":
			s := m.local.Servers[row.server]
			name := clean(host(s.URL), 80)
			if s.Name != "" && s.Name != name {
				name = clean(s.Name, 40) + "  " + m.th.Dim.Render(name)
			}
			def := ""
			if s.Default {
				def = "  " + m.th.Subtle.Render(m.lang.t("default"))
			}
			b.addHit(sel(i)+m.th.Ref.Render(name)+def+"   "+m.serverStatus(s)+busy("server"), action{kind: "setting", a: i})
			b.add("    " + m.tokenStatus(s))
		case "add-server":
			b.addHit(sel(i)+m.th.Dim.Render("+ "+m.lang.t("other.server")), action{kind: "setting", a: i})
			section(m.lang.t("this.machine"))
		case "channel":
			b.addHit(sel(i)+label(i, m.lang.t("channel"))+m.channelStatus(), action{kind: "setting", a: i})
		case "harness":
			b.addHit(sel(i)+label(i, m.lang.t("harness"))+m.harnessStatus()+busy("harness"), action{kind: "setting", a: i})
		case "collector":
			b.addHit(sel(i)+label(i, m.lang.t("collector"))+m.collectorStatus()+busy("collector"), action{kind: "setting", a: i})
		case "machine":
			b.addHit(sel(i)+label(i, m.lang.t("machine"))+m.th.Text.Render(clean(m.local.Machine, 64)), action{kind: "setting", a: i})
		case "github":
			section(m.lang.t("account"))
			link := m.th.Ref.Render(m.lang.t("github.web"))
			if webURL(m.local.ProfileURL) {
				link = m.th.Ref.Underline(true).Hyperlink(m.local.ProfileURL).Render(m.lang.t("github.web") + " ↗")
			}
			b.addHit(sel(i)+label(i, m.lang.t("github"))+link, action{kind: "setting", a: i})
		}
	}
	b.blank(1)
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", width)))
	if m.settings.cursor < len(rows) {
		row := rows[m.settings.cursor]
		if eq := m.equivalent(row); eq != "" {
			b.add("  " + m.th.Subtle.Render(clean(eq, 200)))
		}
		if m.settings.confirm && row.kind == "server" {
			b.add("  " + m.th.Caution.Render(fmt.Sprintf(m.lang.t("logout.confirm"), clean(host(m.local.Servers[row.server].URL), 80))))
		} else if m.settings.note != "" {
			st := m.th.Good
			if m.settings.noteBad {
				st = m.th.Caution
			}
			b.add("  " + st.Render(clean(m.settings.note, 300)))
		}
		if row.kind == "harness" && m.settings.doctor != nil {
			b.blank(1)
			b.append(m.doctorLines(width, max(3, m.h-len(b.lines)-4)))
		}
	}
	return m.page(b, m.settingsFooter(rows))
}

func (m Model) doctorLines(width, rows int) block {
	var b block
	nameW := 0
	for _, l := range m.settings.doctor.Lines {
		nameW = max(nameW, min(28, lipgloss.Width(l.Name)))
	}
	// Failures first: they are what the person came for.
	var lines []DoctorLine
	for _, pass := range []bool{false, true} {
		for _, l := range m.settings.doctor.Lines {
			if (l.Status == "OK") == pass {
				lines = append(lines, l)
			}
		}
	}
	for _, l := range lines {
		if len(b.lines) >= rows {
			break
		}
		var mark string
		switch l.Status {
		case "OK":
			mark = m.th.Good.Render("●")
		case "FAIL":
			mark = m.th.Bad.Render("●")
		case "UNVERIFIED":
			mark = m.th.Caution.Render("○")
		default:
			mark = m.th.Subtle.Render("·")
		}
		name := ansi.Truncate(clean(l.Name, 80), nameW, "…")
		detail := ansi.Truncate(clean(l.Detail, 300), max(10, width-nameW-6), "…")
		b.add("  " + mark + " " + m.th.Text.Render(fmt.Sprintf("%-*s", nameW, name)) + "  " + m.th.Dim.Render(detail))
		if l.Status == "FAIL" && l.Fix != "" && len(b.lines) < rows {
			b.add("    " + strings.Repeat(" ", nameW) + "  " + m.th.Subtle.Render(ansi.Truncate(clean(l.Fix, 300), max(10, width-nameW-6), "…")))
		}
	}
	return b
}

func (m Model) settingsFooter(rows []settingRow) string {
	if m.settings.confirm {
		return m.keys("y", m.lang.t("key.logout"), "n", m.lang.t("key.cancel"))
	}
	pairs := []string{}
	if m.settings.cursor < len(rows) {
		switch rows[m.settings.cursor].kind {
		case "server":
			pairs = append(pairs, "enter", m.lang.t("key.login"))
			if m.local.Servers[rows[m.settings.cursor].server].Token != TokenNone {
				pairs = append(pairs, "x", m.lang.t("key.logout"))
			}
		case "add-server", "machine":
			pairs = append(pairs, "enter", m.lang.t("key.login"))
		case "channel":
			pairs = append(pairs, "enter", m.lang.t("key.select"))
		case "harness":
			pairs = append(pairs, "enter", m.lang.t("key.run"), "f", m.lang.t("key.fix"))
		case "collector":
			pairs = append(pairs, "enter", m.lang.t("restart"))
		case "github":
			pairs = append(pairs, "enter", m.lang.t("key.browser"))
		}
	}
	pairs = append(pairs, "↑↓", m.lang.t("key.select"), "esc", m.lang.t("key.back"))
	return m.keys(pairs...)
}

var spinFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

func spinner(i int) string { return spinFrames[i%len(spinFrames)] }
