package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme holds the few colours and styles the screens share. It follows the
// web logo: ink blue and a pale face on light terminals, a light blue on dark
// ones, teal for the seed and for what went well.
type Theme struct {
	Dark    bool
	Plain   bool // NO_COLOR or no colour profile: no colours, only weight
	Ink     color.Color
	Face    color.Color
	Seed    color.Color
	Accent  color.Color
	Muted   color.Color
	Faint   color.Color
	Warn    color.Color
	Danger  color.Color
	Surface color.Color
	Fg      color.Color

	Title    lipgloss.Style
	Text     lipgloss.Style
	Dim      lipgloss.Style
	Subtle   lipgloss.Style
	Key      lipgloss.Style
	Selected lipgloss.Style
	Good     lipgloss.Style
	Bad      lipgloss.Style
	Caution  lipgloss.Style
	Ref      lipgloss.Style
	Rule     lipgloss.Style
}

// NewTheme builds the theme for a dark or light background. plain drops every
// colour (NO_COLOR); bold and faint still carry the hierarchy.
func NewTheme(dark, plain bool) Theme {
	ld := lipgloss.LightDark(dark)
	t := Theme{
		Dark:    dark,
		Plain:   plain,
		Ink:     ld(lipgloss.Color("#24305E"), lipgloss.Color("#8DB3FF")),
		Face:    ld(lipgloss.Color("#E9EDFA"), lipgloss.Color("#1D2430")),
		Seed:    lipgloss.Color("#5CC8B8"),
		Accent:  ld(lipgloss.Color("#2F4BA8"), lipgloss.Color("#A9C6FF")),
		Muted:   ld(lipgloss.Color("#59636E"), lipgloss.Color("#9198A1")),
		Faint:   ld(lipgloss.Color("#AEB6C2"), lipgloss.Color("#4A515C")),
		Warn:    ld(lipgloss.Color("#9A6700"), lipgloss.Color("#D29922")),
		Danger:  ld(lipgloss.Color("#CF222E"), lipgloss.Color("#F85149")),
		Surface: ld(lipgloss.Color("#EEF1FA"), lipgloss.Color("#182030")),
		Fg:      ld(lipgloss.Color("#1F2328"), lipgloss.Color("#E6EDF3")),
	}
	s := lipgloss.NewStyle()
	if plain {
		t.Title = s.Bold(true)
		t.Text = s
		t.Dim = s.Faint(true)
		t.Subtle = s.Faint(true)
		t.Key = s.Bold(true)
		t.Selected = s.Reverse(true)
		t.Good = s
		t.Bad = s.Bold(true)
		t.Caution = s.Bold(true)
		t.Ref = s.Bold(true)
		t.Rule = s.Faint(true)
		return t
	}
	t.Title = s.Bold(true).Foreground(t.Ink)
	t.Text = s.Foreground(t.Fg)
	t.Dim = s.Foreground(t.Muted)
	t.Subtle = s.Foreground(t.Faint)
	t.Key = s.Foreground(t.Accent).Bold(true)
	t.Selected = s.Background(t.Surface).Foreground(t.Accent).Bold(true)
	t.Good = s.Foreground(ld(lipgloss.Color("#1A7F72"), t.Seed))
	t.Bad = s.Foreground(t.Danger)
	t.Caution = s.Foreground(t.Warn)
	t.Ref = s.Foreground(t.Accent)
	t.Rule = s.Foreground(t.Faint)
	return t
}
