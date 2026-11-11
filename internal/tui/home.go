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
	if m.screen != ScreenLogin {
		footer = m.keys("ctrl+k", m.lang.t("key.commands")) + "   " + footer
	}
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

func projectShort(p string) string {
	p = strings.TrimPrefix(p, "github.com/")
	return p
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
		b.add("  " + m.th.Caution.Render(m.problemText(st.err)))
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

func (m Model) contentWidth() int { return max(30, min(96, m.w-6)) }

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
		for _, l := range strings.Split(lipgloss.Wrap(m.problemText(m.cardErr), m.contentWidth(), " "), "\n") {
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
