package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// action is what a click on a row does.
type action struct {
	kind string // item, section, setting, key
	a, b int
}

type hit struct {
	row, x0, x1 int
	act         action
}

// block is rendered lines plus the clickable areas in them, so the mouse
// handler knows what lies under a click without parsing the screen.
type block struct {
	lines []string
	hits  []hit
}

func (b *block) add(s string) {
	b.lines = append(b.lines, strings.Split(s, "\n")...)
}

// addHit adds one line that reacts to a click over its full width.
func (b *block) addHit(s string, act action) {
	b.hits = append(b.hits, hit{row: len(b.lines), x0: 0, x1: lipgloss.Width(s), act: act})
	b.add(s)
}

func (b *block) blank(n int) {
	for i := 0; i < n; i++ {
		b.lines = append(b.lines, "")
	}
}

func (b block) width() int {
	w := 0
	for _, l := range b.lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return w
}

// append places o below b.
func (b *block) append(o block) {
	off := len(b.lines)
	for _, h := range o.hits {
		h.row += off
		b.hits = append(b.hits, h)
	}
	b.lines = append(b.lines, o.lines...)
}

// indent shifts the block right by n columns.
func (b block) indent(n int) block {
	if n <= 0 {
		return b
	}
	pad := strings.Repeat(" ", n)
	out := block{lines: make([]string, len(b.lines))}
	for i, l := range b.lines {
		if l != "" {
			l = pad + l
		}
		out.lines[i] = l
	}
	for _, h := range b.hits {
		h.x0 += n
		h.x1 += n
		out.hits = append(out.hits, h)
	}
	return out
}

// center indents the block so it sits in the middle of width.
func (b block) center(width int) block {
	return b.indent((width - b.width()) / 2)
}

// beside puts columns next to each other with gap blanks between them.
func beside(gap int, cols ...block) block {
	var out block
	x := 0
	height := 0
	for _, c := range cols {
		if len(c.lines) > height {
			height = len(c.lines)
		}
	}
	out.lines = make([]string, height)
	for ci, c := range cols {
		w := c.width()
		for row := 0; row < height; row++ {
			l := ""
			if row < len(c.lines) {
				l = c.lines[row]
			}
			if ci < len(cols)-1 {
				l += strings.Repeat(" ", max(0, w-lipgloss.Width(l))+gap)
			}
			out.lines[row] += l
		}
		for _, h := range c.hits {
			h.x0 += x
			h.x1 += x
			out.hits = append(out.hits, h)
		}
		x += w + gap
	}
	return out
}

// hitAt returns the action under a cell.
func (b block) hitAt(x, y int) (action, bool) {
	for _, h := range b.hits {
		if h.row == y && x >= h.x0 && x < h.x1 {
			return h.act, true
		}
	}
	return action{}, false
}

func (b block) String() string { return strings.Join(b.lines, "\n") }
