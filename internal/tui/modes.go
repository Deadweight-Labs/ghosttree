package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The two working modes of the home screen. Project is everything about the
// folder's project, Inbox everything that waits for the person, across
// projects. Shift+Tab switches.
const (
	modeProject = iota
	modeInbox
)

// SwitchStyle is how the mode switch is drawn: as a rail on the left (the
// default, Robin's pick in the design round) or in the header line.
type SwitchStyle int

const (
	SwitchRail SwitchStyle = iota
	SwitchBar
)

// ParseSwitchStyle reads rail|bar.
func ParseSwitchStyle(v string) (SwitchStyle, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "rail":
		return SwitchRail, true
	case "bar":
		return SwitchBar, true
	}
	return SwitchRail, false
}

func (s SwitchStyle) String() string {
	if s == SwitchRail {
		return "rail"
	}
	return "bar"
}

// splashHold is how long the big logo stays after its animation, in frames,
// before the home takes its place.
const splashHold = 18

var (
	projectTabs = []ListKind{ListRequests, ListPains, ListProjectTasks, ListRoom}
	inboxTabs   = []ListKind{ListMyTasks, ListQuestions, ListUnread, ListSuggestions}
	// inboxCounted are the lists whose entries make the number at "Inbox".
	inboxCounted = []ListKind{ListMyTasks, ListQuestions, ListUnread}
)

var tabKeys = map[ListKind]string{
	ListRequests:     "tab.requests",
	ListPains:        "tab.pains",
	ListProjectTasks: "tab.tasks",
	ListRoom:         "tab.messages",
	ListMyTasks:      "tab.tasks",
	ListQuestions:    "tab.questions",
	ListUnread:       "tab.messages",
	ListSuggestions:  "tab.suggestions",
}

// tabs are the lists of the current mode. Suggestions only show when there
// are some: most people never review.
func (m Model) tabs() []ListKind {
	if m.mode == modeProject {
		return projectTabs
	}
	var out []ListKind
	for _, k := range inboxTabs {
		if k == ListSuggestions {
			if st := m.lists[k]; !st.loaded || st.err != nil || len(st.items) == 0 {
				continue
			}
		}
		out = append(out, k)
	}
	return out
}

func (m Model) currentKind() ListKind {
	tabs := m.tabs()
	i := m.tab[m.mode]
	if i >= len(tabs) {
		i = len(tabs) - 1
	}
	return tabs[max(0, i)]
}

func (m Model) currentList() []Item { return m.lists[m.currentKind()].items }

// inboxCount is the number at "Inbox": what waits, once it is known.
func (m Model) inboxCount() int {
	n := 0
	for _, k := range inboxCounted {
		if st := m.lists[k]; st.loaded && st.err == nil {
			n += len(st.items)
		}
	}
	return n
}

// homeZone is where the keys go on the home screen: the buttons of the
// "no project" state when that is all the project mode shows.
func (m Model) homeZone() int {
	if m.mode == modeProject && m.noProject() {
		return zoneButtons
	}
	return zoneList
}

func (m *Model) setMode(mode int) {
	m.mode = mode
	m.cursor = 0
	m.zone = m.homeZone()
	if m.zone == zoneButtons {
		m.moveButton(0)
	}
}

func (m *Model) loadList(kind ListKind) tea.Cmd {
	m.lists[kind].loading = true
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		items, err := m.be.List(ctx, kind)
		return listMsg{which: kind, items: items, err: err}
	})
}

// ---------------------------------------------------------------- view

func (m Model) modeLabel(mode int) string {
	if mode == modeProject {
		return m.lang.t("mode.project")
	}
	label := m.lang.t("mode.inbox")
	if n := m.inboxCount(); n > 0 {
		label += fmt.Sprintf(" %d", n)
	}
	return label
}

// switchBar is the mode switch for the header line: the active mode stands
// on a surface, the other is quiet.
func (m Model) switchBar(x0 int) (string, []hit) {
	var parts []string
	var hits []hit
	x := x0
	for _, mode := range []int{modeProject, modeInbox} {
		label := " " + m.modeLabel(mode) + " "
		var s string
		if mode == m.mode {
			s = m.th.Selected.Render(label)
		} else {
			s = m.th.Dim.Render(label)
		}
		w := lipgloss.Width(s)
		hits = append(hits, hit{x0: x, x1: x + w, act: action{kind: "mode", a: mode}})
		parts = append(parts, s)
		x += w + 1
	}
	return strings.Join(parts, " "), hits
}

// header is the compact top line: the name, the switch (bar style) and who
// and where on the right.
func (m Model) header() block {
	var b block
	left := "  " + m.wordmark()
	var hits []hit
	if m.opt.Switch == SwitchBar {
		sw, h := m.switchBar(lipgloss.Width(left) + 4)
		left += "    " + sw
		hits = h
	}
	right := m.headerAccount()
	room := m.w - lipgloss.Width(left) - 2
	if room > 10 {
		right = ansi.Truncate(right, room-2, "…")
		left += strings.Repeat(" ", max(2, room-lipgloss.Width(right))) + right
	}
	b.add(left)
	for _, h := range hits {
		b.hits = append(b.hits, h)
	}
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", max(10, m.w-4))))
	return b
}

// headerAccount is who is signed in where, quietly; machine and project
// have their own places (settings, the project line).
func (m Model) headerAccount() string {
	if !m.local.Configured {
		return m.th.Caution.Render(m.lang.t("no.config")) + m.th.Dim.Render("   ctrl+l "+m.lang.t("key.login"))
	}
	s := m.server
	var who string
	switch {
	case s.Account != "":
		who = m.th.Text.Render(clean(s.Account, 60))
	case s.Checked && s.Token == TokenRejected:
		who = m.th.Bad.Render(m.lang.t("token.rejected"))
	case s.Checked && !s.Reachable:
		who = m.th.Caution.Render(m.lang.t("unreachable"))
	case s.Token == TokenNone:
		who = m.th.Caution.Render(m.lang.t("signed.out"))
	}
	if h := clean(host(s.URL), 80); h != "" {
		if who != "" {
			who += m.th.Subtle.Render(" · ")
		}
		who += m.th.Dim.Render(h)
	}
	return who
}

// rail is the mode switch as a column on the left (rail style).
func (m Model) rail() block {
	var b block
	b.blank(1)
	for _, mode := range []int{modeProject, modeInbox} {
		label := m.modeLabel(mode)
		if mode == m.mode {
			b.addHit(m.th.Key.Render("▌ ")+m.th.Title.Render(label), action{kind: "mode", a: mode})
		} else {
			b.addHit("  "+m.th.Dim.Render(label), action{kind: "mode", a: mode})
		}
		b.blank(1)
	}
	return b
}

// tabRow renders the lists of the current mode; the active one stands out.
func (m Model) tabRow() block {
	var b block
	var parts []string
	x := 0
	for i, k := range m.tabs() {
		label := m.lang.t(tabKeys[k])
		count := ""
		if st := m.lists[k]; st.loaded && st.err == nil {
			count = fmt.Sprintf(" %d", len(st.items))
		}
		var s string
		if k == m.currentKind() {
			s = m.th.Title.Render(label) + m.th.Key.Render(count)
		} else {
			s = m.th.Dim.Render(label + count)
		}
		w := lipgloss.Width(s)
		b.hits = append(b.hits, hit{row: 0, x0: x, x1: x + w, act: action{kind: "tab", a: i}})
		parts = append(parts, s)
		x += w + 4
	}
	b.add(strings.Join(parts, "    "))
	return b
}

// projectLine names the project the project mode is about.
func (m Model) projectLine() string {
	p := clean(projectShort(m.local.Project), 80)
	if m.local.Branch != "" {
		p += m.th.Subtle.Render(" @ ") + m.th.Dim.Render(clean(m.local.Branch, 60))
	}
	return m.th.Title.Render(p)
}

// modeBody is the content of the current mode: search, then either the
// "no project" state alone or the tabs with one list.
func (m Model) modeBody(width, height int) block {
	var b block
	b.add(m.search.View())
	b.blank(1)
	if !m.local.Configured {
		return b // the header says "not connected" and names ctrl+l
	}
	if m.mode == modeProject {
		if m.noProject() {
			b.append(m.projectPanel(width))
			return b
		}
		b.add(m.projectLine())
		b.blank(1)
	}
	b.append(m.tabRow())
	b.add(m.th.Rule.Render(strings.Repeat("─", width)))
	rows := max(3, height-len(b.lines)-1)
	kind := m.currentKind()
	b.append(m.listBlock(m.lists[kind], m.cursor, rows, width, "item", m.tab[m.mode]))
	return b
}

func (m Model) viewHome() block {
	if m.splash {
		return m.viewSplash()
	}
	b := block{}
	b.blank(1)
	b.append(m.header())
	b.blank(1)
	height := m.h - len(b.lines) - 3
	if m.opt.Switch == SwitchRail {
		rail := m.rail()
		width := max(30, min(96, m.w-6-rail.width()-4))
		b.append(beside(4, rail.indent(2), m.modeBody(width, height)))
	} else {
		width := m.contentWidth()
		b.append(m.modeBody(width, height).indent(4))
	}
	return m.page(b, m.homeFooter())
}

// viewSplash is the start: the big logo in the middle while it animates,
// before the home takes its place.
func (m Model) viewSplash() block {
	var content block
	content.add(m.logo.View())
	content.blank(1)
	var mark block
	mark.add(m.wordmark())
	content.append(mark.center(content.width()))
	top := max(0, (m.h-len(content.lines))/2-1)
	var b block
	b.blank(top)
	b.append(content.center(m.w))
	return b
}

func (m Model) homeFooter() string {
	other := modeInbox
	if m.mode == modeInbox {
		other = modeProject
	}
	pairs := []string{"shift+tab", m.lang.t(map[int]string{modeProject: "mode.project", modeInbox: "mode.inbox"}[other])}
	if m.homeZone() == zoneButtons {
		pairs = append(pairs, "←→", m.lang.t("key.select"), "enter", m.lang.t("key.run"))
	} else {
		pairs = append(pairs, "tab", m.lang.t("key.section"), "enter", m.lang.t("key.open"))
	}
	pairs = append(pairs, "ctrl+s", m.lang.t("key.settings"), "esc", m.lang.t("key.quit"))
	return m.keys(pairs...)
}
