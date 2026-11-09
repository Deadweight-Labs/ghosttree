package tui

import (
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// pixels is a small raster of optional colours. Half-block rendering maps two
// pixels onto one terminal cell, braille rendering maps eight dots onto one.
type pixels struct {
	w, h int
	px   []color.Color
}

func newPixels(w, h int) *pixels { return &pixels{w: w, h: h, px: make([]color.Color, w*h)} }

func (p *pixels) set(x, y int, c color.Color) {
	if x < 0 || y < 0 || x >= p.w || y >= p.h {
		return
	}
	p.px[y*p.w+x] = c
}

func (p *pixels) at(x, y int) color.Color {
	if x < 0 || y < 0 || x >= p.w || y >= p.h {
		return nil
	}
	return p.px[y*p.w+x]
}

// sgr writes the colour escape for one cell, or nothing in plain mode.
func sgr(fg, bg color.Color, plain bool) string {
	if plain || (fg == nil && bg == nil) {
		return ""
	}
	var s ansi.Style
	if fg != nil {
		s = s.ForegroundColor(fg)
	}
	if bg != nil {
		s = s.BackgroundColor(bg)
	}
	return s.String()
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	r1, g1, b1, _ := a.RGBA()
	r2, g2, b2, _ := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2
}

// halfBlocks renders the raster with ▀ ▄ █, two pixels per cell. A cell with
// two different colours uses the background colour for the lower pixel.
func (p *pixels) halfBlocks(plain bool) string {
	var b strings.Builder
	for y := 0; y < p.h; y += 2 {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < p.w; x++ {
			top, bot := p.at(x, y), p.at(x, y+1)
			switch {
			case top == nil && bot == nil:
				b.WriteByte(' ')
				continue
			case bot == nil:
				b.WriteString(sgr(top, nil, plain) + "▀")
			case top == nil:
				b.WriteString(sgr(bot, nil, plain) + "▄")
			case sameColor(top, bot) || plain:
				b.WriteString(sgr(top, nil, plain) + "█")
			default:
				b.WriteString(sgr(top, bot, plain) + "▀")
			}
			if !plain {
				b.WriteString(ansi.ResetStyle)
			}
		}
	}
	return b.String()
}

// braille renders the raster with braille dots, 2×4 per cell, in the colour
// of the cell's first lit dot.
func (p *pixels) braille(plain bool) string {
	bits := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	var b strings.Builder
	for y := 0; y < p.h; y += 4 {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < p.w; x += 2 {
			var r rune
			var c color.Color
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if v := p.at(x+dx, y+dy); v != nil {
						r |= bits[dy][dx]
						if c == nil {
							c = v
						}
					}
				}
			}
			if r == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(sgr(c, nil, plain) + string(0x2800+r))
			if !plain {
				b.WriteString(ansi.ResetStyle)
			}
		}
	}
	return b.String()
}
