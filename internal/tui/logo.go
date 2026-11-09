package tui

import (
	"image/color"
	"math"
	"strings"

	"github.com/charmbracelet/harmonica"
)

// LogoStyle picks one of the three logo drawings. Mark is the web logo drawn
// in half blocks, Tree a pixel ghost that is also a tree, Lines a braille
// ghost outline with a tree growing inside.
type LogoStyle int

const (
	LogoMark LogoStyle = iota
	LogoTree
	LogoLines
)

var logoNames = []string{"mark", "tree", "lines"}

func (s LogoStyle) String() string {
	if int(s) < len(logoNames) {
		return logoNames[s]
	}
	return "mark"
}

// ParseLogoStyle reads mark|tree|lines (or a|b|c).
func ParseLogoStyle(v string) (LogoStyle, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "mark", "a", "":
		return LogoMark, true
	case "tree", "b":
		return LogoTree, true
	case "lines", "c":
		return LogoLines, true
	}
	return LogoMark, false
}

const (
	logoFPS    = 30
	logoFrames = 33 // about 1.1 s, then the logo stands still
)

// logo draws one frame of the start animation. Frame logoFrames and later
// is the still picture.
type logo struct {
	style LogoStyle
	theme Theme
	frame int
}

func (l logo) done() bool { return l.frame >= logoFrames }

func (l logo) t() float64 {
	if l.done() {
		return 1
	}
	return float64(l.frame) / float64(logoFrames)
}

// springAt runs a harmonica spring from 0 towards 1 for the frames since
// start and returns its position: it overshoots a little and settles, which
// gives the seed and the tree their bounce.
func springAt(frame, start int, freq, damping float64) float64 {
	n := frame - start
	if n <= 0 {
		return 0
	}
	if frame >= logoFrames {
		return 1
	}
	s := harmonica.NewSpring(harmonica.FPS(logoFPS), freq, damping)
	pos, vel := 0.0, 0.0
	for i := 0; i < n; i++ {
		pos, vel = s.Update(pos, vel, 1)
	}
	return pos
}

func (l logo) View() string {
	switch l.style {
	case LogoTree:
		return l.tree()
	case LogoLines:
		return l.lines()
	}
	return l.mark()
}

// The web logo, in its SVG coordinates (internal/web/static/logo-mark.svg):
// a ring with two eyes, a stem from its right side that bends down and left,
// and a teal seed at the end.
var (
	markCenter = pt{56, 58}
	markEyes   = []pt{{47, 58}, {64, 58}}
	markSeed   = pt{37, 104}
	markStem   = newPolyline(append([]pt{{80, 34}, {80, 96}}, cubic(pt{80, 96}, pt{80, 120}, pt{48, 124}, pt{38, 106}, 24)[1:]...))
)

const (
	markRing   = 24.0
	markStroke = 5.5
	markX0     = 25.0
	markX1     = 87.0
	markY0     = 27.0
	markY1     = 127.0
	markCols   = 15
)

func markSize() (w, h int) {
	w = markCols
	h = int(math.Round(float64(w) * (markY1 - markY0) / (markX1 - markX0)))
	if h%2 == 1 {
		h++
	}
	return w, h
}

func (l logo) mark() string {
	w, h := markSize()
	p := newPixels(w, h)
	t := l.t()
	ink, face, seed := l.theme.Ink, l.theme.Face, l.theme.Seed
	ringSweep := easeOut(t / 0.42)
	stem := markStem.prefix(easeOut((t - 0.28) / 0.38))
	faceOn := clamp01((t - 0.18) / 0.4)
	eyesOn := t >= 0.62
	seedR := 7.5 * springAt(l.frame, logoFrames*64/100, 9, 0.38)
	const ss = 4 // supersampling per axis
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nFace, nRing, nStem, nSeedFill int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					q := pt{
						markX0 + (float64(x)+(float64(sx)+0.5)/ss)/float64(w)*(markX1-markX0),
						markY0 + (float64(y)+(float64(sy)+0.5)/ss)/float64(h)*(markY1-markY0),
					}
					dc := dist(q, markCenter)
					switch {
					case seedR > 0 && dist(q, markSeed) <= seedR:
						nSeedFill++
					case len(stem.pts) > 0 && stem.distTo(q) <= markStroke:
						nStem++
					case math.Abs(dc-markRing) <= markStroke && ringAngle(q) <= ringSweep:
						nRing++
					case dc < markRing-markStroke && faceOn > 0:
						nFace++
					}
				}
			}
			n := ss * ss
			var c color.Color
			switch {
			case nSeedFill*2 >= n:
				c = seed
			case nStem*2 >= n || nRing*2 >= n || (nStem+nRing)*2 >= n:
				c = ink
			case nFace*2 >= n:
				c = mix(l.theme.Surface, face, faceOn)
			}
			if c != nil {
				p.set(x, y, c)
			}
		}
	}
	if eyesOn {
		// The eyes are smaller than two pixels at this size; a fixed 2×2
		// block each keeps them round enough and always visible.
		for _, e := range markEyes {
			ex := int(math.Round((e.x-markX0)/(markX1-markX0)*float64(w) - 1))
			ey := int(math.Round((e.y-markY0)/(markY1-markY0)*float64(h) - 1))
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					p.set(ex+dx, ey+dy, ink)
				}
			}
		}
	}
	return p.halfBlocks(l.theme.Plain)
}

// ringAngle is the share (0..1) of the ring drawn before q, clockwise from
// the top.
func ringAngle(q pt) float64 {
	a := math.Atan2(q.y-markCenter.y, q.x-markCenter.x) + math.Pi/2
	if a < 0 {
		a += 2 * math.Pi
	}
	return a / (2 * math.Pi)
}

// treeBitmap is a ghost whose head is a crown, with eyes, a wavy hem, a
// trunk and roots.
var treeBitmap = []string{
	".....####..####.....",
	"...##############...",
	"..################..",
	".##################.",
	".#####..####..#####.",
	".#####..####..#####.",
	".##################.",
	".##################.",
	"..###.########.###..",
	"........####........",
	"........####........",
	".......######.......",
	".....##..##..##.....",
	"....#....##....#....",
	"...#.....##.....#...",
	"..........#.........",
}

func (l logo) tree() string {
	h, w := len(treeBitmap), len(treeBitmap[0])
	p := newPixels(w, h)
	grow := springAt(l.frame, 0, 6.5, 0.62)
	for y, row := range treeBitmap {
		for x, ch := range row {
			if ch != '#' {
				continue
			}
			// Roots first, crown last, with some grain.
			th := 0.7*(float64(y)/float64(h-1)*-1+1) + 0.3*hash01(x, y)
			if th > grow*1.02 {
				continue
			}
			c := mix(l.theme.Ink, l.theme.Seed, math.Pow(float64(y)/float64(h-1), 1.6))
			if !l.done() && grow-th < 0.12 {
				c = mix(c, l.theme.Face, 0.55)
			}
			p.set(x, y, c)
		}
	}
	return p.halfBlocks(l.theme.Plain)
}

// branch is one stroke of the braille tree with the depth it grows at.
type branch struct {
	a, b  pt
	depth int
}

func treeBranches(root pt, angle, length float64, depth, max int, out *[]branch) {
	if depth > max {
		return
	}
	end := pt{root.x + length*math.Cos(angle), root.y + length*math.Sin(angle)}
	*out = append(*out, branch{root, end, depth})
	spread := 0.62 - 0.08*float64(depth)
	treeBranches(end, angle-spread, length*0.7, depth+1, max, out)
	treeBranches(end, angle+spread, length*0.7, depth+1, max, out)
}

const (
	linesW = 36
	linesH = 40
)

var (
	linesBranches = func() []branch {
		var out []branch
		treeBranches(pt{18, 35}, -math.Pi/2, 10, 0, 3, &out)
		return out
	}()
	linesOutline = func() polyline {
		var pts []pt
		// From the lower left, up, over the head, down the right side,
		// then the hem back with three waves.
		pts = append(pts, pt{4, 34}, pt{4, 16})
		for i := 0; i <= 32; i++ {
			a := math.Pi + math.Pi*float64(i)/32
			pts = append(pts, pt{18 + 14*math.Cos(a), 16 + 14*math.Sin(a)})
		}
		pts = append(pts, pt{32, 34})
		for i := 0; i <= 36; i++ {
			x := 32 - 28*float64(i)/36
			pts = append(pts, pt{x, 36 - 2.2*math.Cos(float64(i)/36*3*2*math.Pi)})
		}
		return newPolyline(pts)
	}()
)

func (l logo) lines() string {
	p := newPixels(linesW, linesH)
	t := l.t()
	grow := springAt(l.frame, 0, 7, 0.7) * 4.2
	for _, br := range linesBranches {
		share := clamp01(grow - float64(br.depth))
		if share <= 0 {
			continue
		}
		end := pt{br.a.x + share*(br.b.x-br.a.x), br.a.y + share*(br.b.y-br.a.y)}
		c := mix(l.theme.Seed, l.theme.Ink, float64(br.depth)/5)
		plotLine(p, br.a, end, c)
		if br.depth == 0 {
			plotLine(p, pt{br.a.x + 1, br.a.y}, pt{end.x + 1, end.y}, c)
		}
	}
	outline := linesOutline.prefix(easeOut((t - 0.3) / 0.55))
	for i := 1; i < len(outline.pts); i++ {
		plotLine(p, outline.pts[i-1], outline.pts[i], l.theme.Accent)
	}
	if t >= 0.85 {
		for _, e := range []pt{{12, 14}, {23, 14}} {
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 2; dx++ {
					p.set(int(e.x)+dx, int(e.y)+dy, l.theme.Ink)
				}
			}
		}
	}
	return p.braille(l.theme.Plain)
}

func plotLine(p *pixels, a, b pt, c color.Color) {
	n := int(math.Ceil(dist(a, b)*2)) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		p.set(int(math.Round(a.x+t*(b.x-a.x))), int(math.Round(a.y+t*(b.y-a.y))), c)
	}
}
