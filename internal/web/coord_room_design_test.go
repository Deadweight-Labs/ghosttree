package web

import (
	"regexp"
	"strings"
	"testing"
)

// The room follows the Clay rules of the v5 acceptance: shadows are never
// clipped, dots keep their shape, class names are not shared between meanings.
func TestCoordRoomStylesFollowTheClayRules(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	rules := parseCSSRules(css)
	byName := func(sel string) string {
		var out []string
		for _, r := range rules {
			if strings.Join(strings.Fields(r.selector), " ") == sel {
				out = append(out, r.body)
			}
		}
		if len(out) == 0 {
			t.Fatalf("CSS rule %q missing", sel)
		}
		return strings.Join(out, "\n")
	}
	// Scroll containers pad by the shadow depth and give the layout the room back.
	for sel, wants := range map[string][]string{
		".coord-messages": {"overflow-y: auto", "var(--shadow-room-y)", "var(--shadow-room-x)"},
		".coord-context":  {"overflow-y: auto", "var(--shadow-room-y)", "var(--shadow-room-x)"},
		".rtabs-scroll":   {"overflow-x: auto", "var(--shadow-room-y)", "var(--shadow-room-x)"},
		".coord-composer": {"var(--shadow-room-y)"},
	} {
		body := byName(sel)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q (shadows would be clipped): %s", sel, want, body)
			}
		}
	}
	// Dots: fixed size, square aspect, never stretched; the modifier is "pulse".
	dot := byName(".coord-dot")
	for _, want := range []string{"inline-size: 9px", "block-size: 9px", "aspect-ratio: 1", "flex: none", "border-radius: 50%"} {
		if !strings.Contains(dot, want) {
			t.Errorf(".coord-dot: missing %q", want)
		}
	}
	if !strings.Contains(byName(".coord-dot.pulse"), "box-shadow") {
		t.Error("the live dot pulses through the modifier .pulse")
	}
	// A dot never shares a class with a layout rule (the old ".live" bug).
	for _, r := range rules {
		if regexp.MustCompile(`(^|[\s,>])\.live\b`).MatchString(r.selector) {
			t.Errorf("%s: the bare class .live is taken by nothing in the room; dots use .pulse", r.selector)
		}
	}
	if regexp.MustCompile(`var\(--coord-`).MatchString(css) {
		t.Error("the room reads the palette tokens, not private --coord- values")
	}
	// The drawer, toggles and backdrop keep winning over component display rules.
	for _, want := range []string{"position: fixed", "display: none !important"} {
		if !strings.Contains(css, want) {
			t.Errorf("room CSS missing %q", want)
		}
	}
}

// The policy forbids inline styles, inline script and event handlers.
func TestCoordTemplateHasNoInlineStyleScriptOrHandlers(t *testing.T) {
	tpl := coordTemplate(t)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)\sstyle\s*=`),
		regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
		regexp.MustCompile(`(?i)<script`),
	} {
		if re.MatchString(tpl) {
			t.Errorf("room template contains %s", re)
		}
	}
}

func TestCoordScriptSendsOnEnterAndKeepsShiftEnterForNewLines(t *testing.T) {
	js := string(mustReadEmbedded(t, "static/app.js"))
	for _, want := range []string{
		`event.key !== "Enter"`, "event.shiftKey", "event.isComposing", "form.requestSubmit(button)",
		`=== "directive"`, `submitter?.hasAttribute("formaction")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("composer script missing %q", want)
		}
	}
}
