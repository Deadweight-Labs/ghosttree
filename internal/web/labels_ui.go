package web

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// labelPalette are the fills ui-labels.css knows. A label keeps its own colour
// in the store; the page cannot carry it as a style attribute (CSP), so
// labelClass snaps it to the nearest entry here.
var labelPalette = []string{"0075ca", "0969da", "0e8a16", "1a7f37", "1d76db", "5319e7", "7057ff", "8250df", "008672", "a2eeef", "b60205", "bfd4f2", "bfdadc", "c2e0c6", "c5def5", "cfd3d7", "d4c5f9", "d73a4a", "d876e3", "e4e669", "e99695", "ededed", "f9d0c4", "fbca04", "fef2c0"}

func hexRGB(h string) (r, g, b float64, ok bool) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "#")
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n & 255), true
}

// labelClass is the CSS class of a label colour: "lc-<nearest palette entry>",
// "lc-none" for a colour that is not a hex value.
func labelClass(color string) string {
	r, g, b, ok := hexRGB(color)
	if !ok {
		return "lc-none"
	}
	best, bestD := "", math.MaxFloat64
	for _, p := range labelPalette {
		pr, pg, pb, _ := hexRGB(p)
		if d := (r-pr)*(r-pr) + (g-pg)*(g-pg) + (b-pb)*(b-pb); d < bestD {
			best, bestD = p, d
		}
	}
	return "lc-" + best
}

// rowLabels turns the labels of an object into the pills of a list row; each pill
// links to the list filtered by it (base is the list's address).
func rowLabels(labels []store.Label, base string) []rowLabel {
	out := make([]rowLabel, 0, len(labels))
	for _, l := range labels {
		out = append(out, rowLabel{Text: l.Name, Class: labelClass(l.Color), Href: base + "?q=" + url.QueryEscape("label:"+l.Name)})
	}
	return out
}
