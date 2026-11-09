package tui

import (
	"image/color"
	"math"

	"github.com/lucasb-eyer/go-colorful"
)

type pt struct{ x, y float64 }

func dist(a, b pt) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

// segDist is the distance from p to the segment a–b.
func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return dist(p, a)
	}
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return dist(p, pt{a.x + t*dx, a.y + t*dy})
}

// polyline is a path with its cumulative length, so a drawing animation can
// show a prefix of it.
type polyline struct {
	pts []pt
	cum []float64
}

func newPolyline(pts []pt) polyline {
	cum := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		cum[i] = cum[i-1] + dist(pts[i-1], pts[i])
	}
	return polyline{pts: pts, cum: cum}
}

func (p polyline) length() float64 {
	if len(p.cum) == 0 {
		return 0
	}
	return p.cum[len(p.cum)-1]
}

// prefix returns the first frac (0..1) of the path.
func (p polyline) prefix(frac float64) polyline {
	if frac >= 1 {
		return p
	}
	if frac <= 0 || len(p.pts) < 2 {
		return polyline{}
	}
	want := frac * p.length()
	out := []pt{p.pts[0]}
	for i := 1; i < len(p.pts); i++ {
		if p.cum[i] >= want {
			seg := p.cum[i] - p.cum[i-1]
			t := 0.0
			if seg > 0 {
				t = (want - p.cum[i-1]) / seg
			}
			a, b := p.pts[i-1], p.pts[i]
			out = append(out, pt{a.x + t*(b.x-a.x), a.y + t*(b.y-a.y)})
			break
		}
		out = append(out, p.pts[i])
	}
	return newPolyline(out)
}

func (p polyline) distTo(q pt) float64 {
	best := math.Inf(1)
	if len(p.pts) == 1 {
		return dist(q, p.pts[0])
	}
	for i := 1; i < len(p.pts); i++ {
		best = math.Min(best, segDist(q, p.pts[i-1], p.pts[i]))
	}
	return best
}

// cubic samples a cubic Bézier curve into n segments.
func cubic(p0, p1, p2, p3 pt, n int) []pt {
	out := make([]pt, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		out = append(out, pt{
			u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
			u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
		})
	}
	return out
}

// mix blends two colours in Lab space; t=0 is a, t=1 is b.
func mix(a, b color.Color, t float64) color.Color {
	t = clamp01(t)
	ca, _ := colorful.MakeColor(a)
	cb, _ := colorful.MakeColor(b)
	return ca.BlendLab(cb, t).Clamped()
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// easeOut is a cubic ease-out on 0..1.
func easeOut(t float64) float64 {
	t = clamp01(t)
	return 1 - math.Pow(1-t, 3)
}

// hash01 is a fixed pseudo-random value in 0..1 for a pixel, so every run of
// the animation looks the same.
func hash01(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h%10000) / 10000
}
