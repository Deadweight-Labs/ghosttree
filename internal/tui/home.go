package tui

import (
	"fmt"
	"net/url"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// keys renders key hints: pairs of key and word.
func (m Model) keys(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, m.th.Key.Render(pairs[i])+" "+m.th.Dim.Render(pairs[i+1]))
	}
	return strings.Join(parts, "   ")
}

// page puts the content at the top, the footer on the last line and the
// flash message above it.
func (m Model) page(content block, footer string) block {
	limit := max(1, m.h-2)
	if len(content.lines) > limit {
		content.lines = content.lines[:limit]
	}
	for len(content.lines) < limit {
		content.lines = append(content.lines, "")
	}
	flash := ""
	if m.flash != "" {
		if m.flashBad {
			flash = m.th.Caution.Render(clean(m.flash, 200))
		} else {
			flash = m.th.Good.Render(clean(m.flash, 200))
		}
	}
	content.add("  " + flash)
	content.add("  " + footer)
	return content
}

func (m Model) wordmark() string {
	return m.th.Title.Render("ghosttree")
}

func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// accountLine is who, where and which machine, in one dim line.
func (m Model) accountLine() string {
	if !m.local.Configured {
		return m.th.Caution.Render(m.lang.t("no.config")) + m.th.Dim.Render("   ctrl+l "+m.lang.t("key.login"))
	}
	s := m.server
	var parts []string
	switch {
	case s.Account != "":
		parts = append(parts, m.th.Text.Render(clean(s.Account, 60)))
	case s.Checked && s.Token == TokenRejected:
		parts = append(parts, m.th.Bad.Render(m.lang.t("token.rejected")))
	case s.Checked && !s.Reachable:
		parts = append(parts, m.th.Caution.Render(m.lang.t("unreachable")))
	case s.Token == TokenNone:
		parts = append(parts, m.th.Caution.Render(m.lang.t("signed.out")))
	}
	if m.local.Machine != "" {
		parts = append(parts, m.th.Dim.Render(clean(m.local.Machine, 64)))
	}
	if s.URL != "" {
		parts = append(parts, m.th.Dim.Render(clean(host(s.URL), 80)))
	}
	if m.local.Project != "" {
		parts = append(parts, m.th.Dim.Render(clean(projectShort(m.local.Project), 80)))
	}
	return strings.Join(parts, m.th.Subtle.Render(" · "))
}

func projectShort(p string) string {
	p = strings.TrimPrefix(p, "github.com/")
	return p
}

var sectionKeys = [listCount]string{"tasks", "requests", "messages"}

// tabs renders the three list names; the active one stands out.
func (m Model) tabs() block {
	var b block
	var parts []string
	var hits []hit
	x := 0
	for i := 0; i < listCount; i++ {
		label := m.lang.t(sectionKeys[i])
		count := ""
		if st := m.lists[i]; st.loaded && st.err == nil {
			count = fmt.Sprintf(" %d", len(st.items))
		}
		var s string
		if i == m.section {
			s = m.th.Title.Render(label) + m.th.Key.Render(count)
		} else {
			s = m.th.Dim.Render(label + count)
		}
		w := lipgloss.Width(s)
		hits = append(hits, hit{row: 0, x0: x, x1: x + w, act: action{kind: "section", a: i}})
		parts = append(parts, s)
		x += w + 4
	}
	b.add(strings.Join(parts, "    "))
	b.hits = hits
	return b
}

// itemLine renders one list row in width columns.
func (m Model) itemLine(it Item, selected bool, width int) string {
	ref := links.Clean(it.Ref, 14)
	title := links.Clean(it.Title, 200)
	meta := links.Clean(it.Meta, 40)
	refW := 10
	if lipgloss.Width(ref) > refW {
		refW = lipgloss.Width(ref)
	}
	avail := width - refW - 4
	if meta != "" {
		avail -= lipgloss.Width(meta) + 2
	}
	title = ansi.Truncate(title, max(8, avail), "…")
	marker := "  "
	refStyle, titleStyle := m.th.Ref, m.th.Text
	if selected {
		marker = m.th.Key.Render("▌ ")
		titleStyle = m.th.Title
	}
	line := marker + refStyle.Render(fmt.Sprintf("%-*s", refW, ref)) + "  " + titleStyle.Render(title)
	if meta != "" {
		pad := width - lipgloss.Width(line) - lipgloss.Width(meta)
		line += strings.Repeat(" ", max(2, pad)) + m.th.Dim.Render(meta)
	}
	return line
}

// listBlock renders a list with a window around the cursor.
func (m Model) listBlock(st listState, cursor, rows, width int, kind string, section int) block {
	var b block
	switch {
	case st.loading && !st.loaded:
		b.add("  " + m.th.Subtle.Render(m.lang.t("loading")))
		return b
	case st.err != nil:
		b.add("  " + m.th.Caution.Render(errText(st.err)))
		return b
	case len(st.items) == 0:
		b.add("  " + m.th.Subtle.Render(m.lang.t("nothing")))
		return b
	}
	start := 0
	if rows > 0 && cursor >= rows {
		start = cursor - rows + 1
	}
	for i := start; i < len(st.items) && (rows <= 0 || i < start+rows); i++ {
		act := action{kind: kind, a: section, b: i}
		if kind == "result" {
			act = action{kind: kind, a: i}
		}
		b.addHit(m.itemLine(st.items[i], i == cursor, width), act)
	}
	if rows > 0 && len(st.items) > start+rows {
		b.add("  " + m.th.Subtle.Render(fmt.Sprintf("+%d", len(st.items)-start-rows)))
	}
	return b
}

func (m Model) homeFooter() string {
	pairs := []string{"enter", m.lang.t("key.open"), "tab", m.lang.t("key.section"), "↑↓", m.lang.t("key.select"), "ctrl+s", m.lang.t("key.settings")}
	if m.w >= 110 {
		pairs = append(pairs, "f2", m.lang.t("key.look"))
	}
	return m.keys(append(pairs, "esc", m.lang.t("key.quit"))...)
}

func (m Model) viewHome() block {
	switch m.opt.Look {
	case LogoTree:
		return m.page(m.homeSplit(), m.homeFooter())
	case LogoLines:
		return m.page(m.homeHeader(), m.homeFooter())
	}
	return m.page(m.homeCentered(), m.homeFooter())
}

func (m Model) contentWidth() int { return max(30, min(96, m.w-6)) }

// homeCentered: the logo and the search in the middle, the lists below as
// tabs. The look of `ctx` with the web logo.
func (m Model) homeCentered() block {
	var b block
	width := m.contentWidth()
	lg := m.logo.View()
	logoH := lipgloss.Height(lg)
	listRows := max(3, m.h-logoH-14)
	showLogo := m.h >= logoH+16
	if showLogo {
		b.blank(1)
		var head block
		head.add(lg)
		b.append(head.center(m.w))
		b.blank(1)
	} else {
		b.blank(1)
	}
	var mark block
	mark.add(m.wordmark())
	b.append(mark.center(m.w))
	var acc block
	acc.add(m.accountLine())
	b.append(acc.center(m.w))
	b.blank(1)

	var body block
	body.add(m.search.View())
	body.add(m.th.Rule.Render(strings.Repeat("─", width)))
	body.blank(1)
	body.append(m.tabs().indent(2))
	body.blank(1)
	if !showLogo {
		listRows = max(3, m.h-12)
	}
	body.append(m.listBlock(m.lists[m.section], m.cursor, listRows, width, "item", m.section))
	b.append(body.indent(max(2, (m.w-width)/2)))
	return b
}

// homeSplit: a narrow column with the tree logo and the account on the
// left, search and all three lists on the right.
func (m Model) homeSplit() block {
	var left block
	left.blank(1)
	left.add(m.logo.View())
	left.blank(1)
	left.add(m.wordmark())
	left.blank(1)
	s := m.server
	if m.local.Configured {
		if s.Account != "" {
			left.add(m.th.Text.Render(clean(s.Account, 30)))
		} else {
			left.add(m.th.Caution.Render(m.lang.t("signed.out")))
		}
		left.add(m.th.Dim.Render(clean(m.local.Machine, 30)))
		left.add(m.th.Dim.Render(clean(host(s.URL), 30)))
		left.add(m.th.Dim.Render(clean(m.local.Channel, 16)))
	} else {
		left.add(m.th.Caution.Render(m.lang.t("no.config")))
	}
	left.blank(1)
	left.addHit(m.th.Key.Render("ctrl+s")+" "+m.th.Dim.Render(m.lang.t("settings")), action{kind: "settings"})

	rightW := max(30, min(90, m.w-left.width()-10))
	var right block
	right.blank(1)
	right.add(m.search.View())
	right.add(m.th.Rule.Render(strings.Repeat("─", rightW)))
	per := max(2, (m.h-10)/3-2)
	for i := 0; i < listCount; i++ {
		right.blank(1)
		label := m.lang.t(sectionKeys[i])
		var head string
		if i == m.section {
			head = m.th.Title.Render(label)
		} else {
			head = m.th.Dim.Render(label)
		}
		if st := m.lists[i]; st.loaded && st.err == nil {
			head += m.th.Subtle.Render(fmt.Sprintf("  %d", len(st.items)))
		}
		right.addHit(head, action{kind: "section", a: i})
		cur := -1
		if i == m.section {
			cur = m.cursor
		}
		lb := m.listBlock(m.lists[i], cur, per, rightW, "item", i)
		if cur < 0 && len(lb.lines) > per {
			lb.lines = lb.lines[:per]
		}
		right.append(lb)
	}
	return beside(4, left.indent(3), right)
}

// homeHeader: a framed header in the manner of a terminal app, braille
// logo inside, then search and the active list.
func (m Model) homeHeader() block {
	var info block
	info.blank(1)
	info.add(m.wordmark() + "  " + m.th.Subtle.Render(clean(m.local.ClientVersion, 40)))
	info.blank(1)
	s := m.server
	row := func(k, v string) { info.add(m.th.Dim.Render(fmt.Sprintf("%-9s", k)) + v) }
	acct := m.th.Caution.Render(m.lang.t("signed.out"))
	if s.Account != "" {
		acct = m.th.Text.Render(clean(s.Account, 60))
	}
	row(strings.ToLower(m.lang.t("account")), acct)
	row("server", m.th.Text.Render(clean(host(s.URL), 80)))
	row(strings.ToLower(m.lang.t("machine")), m.th.Text.Render(clean(m.local.Machine, 64)))
	if m.local.Project != "" {
		repo := clean(projectShort(m.local.Project), 80)
		if m.local.Branch != "" {
			repo += m.th.Subtle.Render(" @ ") + clean(m.local.Branch, 60)
		}
		row("repo", m.th.Text.Render(repo))
	}
	var lg block
	lg.add(m.logo.View())
	inner := beside(3, lg, info)
	boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.th.Faint).Padding(0, 2)
	if m.th.Plain {
		boxStyle = boxStyle.UnsetBorderForeground()
	}
	box := boxStyle.Render(inner.String())
	var b block
	b.blank(1)
	var hb block
	hb.add(box)
	b.append(hb.indent(2))
	b.blank(1)
	width := m.contentWidth()
	var body block
	body.add(m.search.View())
	body.blank(1)
	body.append(m.tabs().indent(2))
	body.add(m.th.Rule.Render(strings.Repeat("─", width)))
	body.append(m.listBlock(m.lists[m.section], m.cursor, max(3, m.h-len(b.lines)-9), width, "item", m.section))
	b.append(body.indent(3))
	return b
}

func (m Model) smallHeader(title string) block {
	var b block
	b.blank(1)
	head := m.wordmark()
	if title != "" {
		head += m.th.Subtle.Render("  /  ") + m.th.Text.Render(title)
	}
	b.add("  " + head)
	b.blank(1)
	return b
}

func (m Model) viewResults() block {
	b := m.smallHeader(m.lang.t("results"))
	b.add("  " + m.th.Key.Render("› ") + m.th.Text.Render(links.Clean(m.resultsQuery, 200)))
	b.blank(1)
	width := m.contentWidth()
	b.append(m.listBlock(m.results, m.resultsCursor, max(3, m.h-9), width, "result", 0))
	return m.page(b, m.keys("enter", m.lang.t("key.open"), "↑↓", m.lang.t("key.select"), "esc", m.lang.t("key.back")))
}

func (m Model) viewCard() block {
	b := m.smallHeader("")
	c := m.card
	switch {
	case m.cardLoading:
		b.add("  " + m.th.Ref.Render(links.Clean(c.Ref, 40)) + "  " + m.th.Subtle.Render(m.lang.t("loading")))
		return m.page(b, m.keys("esc", m.lang.t("key.back")))
	case m.cardErr != nil:
		b.add("  " + m.th.Ref.Render(links.Clean(c.Ref, 40)))
		b.blank(1)
		for _, l := range strings.Split(lipgloss.Wrap(errText(m.cardErr), m.contentWidth(), " "), "\n") {
			b.add("  " + m.th.Caution.Render(l))
		}
		return m.page(b, m.keys("esc", m.lang.t("key.back")))
	}
	c = c.Sanitized()
	width := m.contentWidth()
	b.add("  " + m.th.Dim.Render(c.Kind) + "  " + m.th.Key.Render(c.Ref))
	for _, l := range strings.Split(lipgloss.Wrap(c.Title, width, " "), "\n") {
		b.add("  " + m.th.Title.Render(l))
	}
	b.blank(1)
	field := func(k, v string) {
		if v == "" {
			return
		}
		lines := strings.Split(lipgloss.Wrap(v, width-10, " "), "\n")
		for i, l := range lines {
			key := ""
			if i == 0 {
				key = k
			}
			b.add("  " + m.th.Dim.Render(fmt.Sprintf("%-8s", key)) + m.th.Text.Render(l))
		}
	}
	de := m.lang == langDE
	pick := func(en, dee string) string {
		if de {
			return dee
		}
		return en
	}
	field(pick("state", "Stand"), c.Status)
	field(pick("next", "Weiter"), c.Next)
	field(pick("with", "Mit"), c.Who)
	if c.URL != "" {
		b.blank(1)
		text := ansi.Truncate(c.URL, width, "…")
		link := m.th.Dim.Render(text)
		if webURL(c.URL) {
			link = m.th.Ref.Underline(true).Hyperlink(c.URL).Render(text)
		}
		b.addHit("  "+link, action{kind: "link"})
	}
	return m.page(b, m.keys("enter", m.lang.t("key.browser"), "c", m.lang.t("key.copy"), "esc", m.lang.t("key.back")))
}
