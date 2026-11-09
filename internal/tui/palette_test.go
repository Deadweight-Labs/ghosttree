package tui

import (
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var ctrlK = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
var ctrlF = tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}

// openPal is a ready home with the palette open and its sources loaded.
func openPal(t *testing.T, f *fakeBackend) Model {
	t.Helper()
	m := ready(f, Options{}, 110, 40, darkBG)
	m = drive(m, ctrlK)
	if !m.pal.open {
		t.Fatal("ctrl+k did not open the palette")
	}
	return m
}

func palTitles(m Model) []string {
	var out []string
	for _, it := range m.palVisible() {
		out = append(out, it.title)
	}
	return out
}

func TestFuzzyCountsOnlyWordStartsAndRuns(t *testing.T) {
	for _, tc := range []struct {
		query, text string
		ok          bool
	}{
		{"einst", "Einstellungen", true},                // prefix
		{"kanal", "Update-Kanal auf dev stellen", true}, // word start
		{"lungen", "Einstellungen", true},               // a run inside a word
		{"ucs", "Update channel set", true},             // pieces at word starts
		{"upch", "Update channel", true},                // runs at word starts
		{"des", "Harnesses prüfen doctor", false},       // scattered letters
		{"des", "abmelden logout sign out", false},      // scattered letters
		{"ezt", "Einstellungen", false},
	} {
		if _, ok := fuzzy(tc.query, tc.text); ok != tc.ok {
			t.Errorf("fuzzy(%q, %q) ok=%v, want %v", tc.query, tc.text, ok, tc.ok)
		}
	}
	prefix, _ := fuzzy("col", "Collector neu starten")
	head, _ := fuzzy("col", "Restart collector")
	inside, _ := fuzzy("col", "Protocol")
	if !(prefix > head && head > inside) {
		t.Fatalf("prefix %d > word start %d > inside %d", prefix, head, inside)
	}
	if s, ok := fuzzy("", "x"); !ok || s <= 0 {
		t.Fatal("empty query matches everything")
	}
}

func TestPaletteHitsForDesDokLog(t *testing.T) {
	m := openPal(t, newFake())
	m = typeText(m, "des")
	got := palTitles(m)
	for _, bad := range []string{"Harnesses prüfen", "Harnesses reparieren", "Abmelden"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("\"des\" hits %q by scattered letters: %v", bad, got)
			}
		}
	}
	if len(got) == 0 || got[0] != "Projekt deadweight-labs/design-system" {
		t.Fatalf("\"des\": %v", got)
	}

	m = openPal(t, newFake())
	m = typeText(m, "dok")
	if got := palTitles(m); len(got) < 2 || got[0] != "Harnesses prüfen" || got[1] != "Harnesses reparieren" {
		t.Fatalf("\"dok\": %v", got)
	}

	m = openPal(t, newFake())
	m = typeText(m, "log")
	got = palTitles(m)
	if len(got) == 0 || got[0] != "Abmelden" {
		t.Fatalf("\"log\": %v", got)
	}
	for _, g := range got {
		if g == "Harnesses prüfen" || g == "Projekte anzeigen" {
			t.Errorf("\"log\" also hits %q: %v", g, got)
		}
	}
}

func TestEveryActionTitleStartsUpperCase(t *testing.T) {
	m := openPal(t, newFake().inFolder("unknown"))
	for _, it := range m.palAll("") {
		if it.group != palAction {
			continue
		}
		if r := []rune(it.title)[0]; r != unicode.ToUpper(r) {
			t.Errorf("action %q starts lower case", it.title)
		}
	}
}

func TestPaletteOpensOverTheScreenAndEscCloses(t *testing.T) {
	m := openPal(t, newFake())
	v := plainView(m)
	for _, want := range []string{"Befehl, REQ-12, Projekt, Ordner oder Suche", "AKTIONEN", "Einstellungen", "ctrl+s", "Update-Kanal auf dev stellen", "ctx config set channel dev", "shift+tab", "Harnesses prüfen", "ctx doctor", "╭"} {
		if !strings.Contains(v, want) {
			t.Errorf("palette misses %q:\n%s", want, v)
		}
	}
	// The screen stays visible behind it.
	if !strings.Contains(v, "robin · ghosttree.example.com") {
		t.Fatalf("background gone:\n%s", v)
	}
	m = drive(m, key("esc"))
	if m.pal.open || m.quitting {
		t.Fatalf("esc: open %v quitting %v", m.pal.open, m.quitting)
	}
}

func TestPaletteDimsTheBackgroundOrFramesIt(t *testing.T) {
	m := openPal(t, newFake())
	// With colours the rows behind the palette lose their own colours.
	for _, l := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(ansi.Strip(l), "REQ-610") && !strings.Contains(l, "╭") && !strings.Contains(l, "│") {
			if strings.Contains(l, "\x1b[38;2;169;198;255m") {
				t.Fatalf("background row keeps its accent colour: %q", l)
			}
		}
	}
	m = ready(newFake(), Options{Plain: true}, 110, 40, darkBG)
	m = drive(m, ctrlK)
	if v := m.View().Content; !strings.Contains(v, "╭") || strings.Contains(v, "\x1b[38;") {
		t.Fatal("without colours the palette should be framed and colourless")
	}
}

func TestPaletteRunsActions(t *testing.T) {
	f := newFake()
	m := openPal(t, f)
	m = typeText(m, "einst")
	if got := palTitles(m); len(got) == 0 || got[0] != "Einstellungen" {
		t.Fatalf("first hit %v", got)
	}
	m = drive(m, key("enter"))
	if m.pal.open || m.screen != ScreenSettings {
		t.Fatalf("open %v screen %v", m.pal.open, m.screen)
	}

	m = drive(m, ctrlK)
	m = typeText(m, "kanal")
	m = drive(m, key("enter"))
	if f.channel != "dev" || !strings.Contains(m.flash, "dev") {
		t.Fatalf("channel %q flash %q", f.channel, m.flash)
	}

	m = drive(m, ctrlK)
	m = typeText(m, "abmelden")
	m = drive(m, key("enter"))
	if m.screen != ScreenSettings || !m.settings.confirm || len(f.loggedOut) != 0 {
		t.Fatalf("logout must ask first: screen %v confirm %v out %v", m.screen, m.settings.confirm, f.loggedOut)
	}
	m = drive(m, key("n"))

	m = drive(m, ctrlK)
	m = typeText(m, "inbox")
	m = drive(m, key("enter"))
	if m.screen != ScreenHome || m.mode != modeInbox {
		t.Fatalf("mode: screen %v mode %d", m.screen, m.mode)
	}

	m = drive(m, ctrlK)
	m = typeText(m, "collector")
	m = drive(m, key("enter"))
	if f.restarted != 1 {
		t.Fatalf("collector restarted %d", f.restarted)
	}
}

func TestPaletteJumpsToRefsProjectsAndFolders(t *testing.T) {
	f := newFake()
	home := f.local
	f.recent = []string{"/home/robin/ghosttree"}
	f.dirs = map[string]Local{"/home/robin/ghosttree": home}
	m := openPal(t, f)
	m = typeText(m, "REQ-610")
	if got := palTitles(m); len(got) == 0 || got[0] != "REQ-610 öffnen" {
		t.Fatalf("hits %v", got)
	}
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || f.resolved[len(f.resolved)-1] != "REQ-610" {
		t.Fatalf("screen %v resolved %v", m.screen, f.resolved)
	}

	m = drive(m, ctrlK)
	m = typeText(m, "design")
	if got := palTitles(m); len(got) == 0 || got[0] != "Projekt deadweight-labs/design-system" {
		t.Fatalf("project hits %v", got)
	}
	m = drive(m, key("enter"))
	if p, ok := m.selectedProject(); m.screen != ScreenProjects || !ok || p.ID != "github.com/deadweight-labs/design-system" {
		t.Fatalf("screen %v selected %v", m.screen, p)
	}

	m = drive(m, ctrlK)
	m = typeText(m, "robin/ghost")
	m = drive(m, key("enter"))
	if len(f.changedTo) != 1 || f.changedTo[0] != "/home/robin/ghosttree" || m.screen != ScreenHome {
		t.Fatalf("changed %v screen %v", f.changedTo, m.screen)
	}
}

func TestPaletteSearchIsTheLastHit(t *testing.T) {
	f := newFake()
	m := openPal(t, f)
	m = typeText(m, "zzqx")
	got := palTitles(m)
	if len(got) != 1 || got[0] != "„zzqx“ suchen" {
		t.Fatalf("hits %v", got)
	}
	m = drive(m, key("enter"))
	if m.screen != ScreenResults || f.searched[len(f.searched)-1] != "zzqx" {
		t.Fatalf("screen %v searched %v", m.screen, f.searched)
	}
}

func TestPaletteRemembersRecentlyUsed(t *testing.T) {
	m := openPal(t, newFake())
	m = typeText(m, "harnesses pr")
	m = drive(m, key("enter"))
	m = drive(m, key("esc"))
	m = drive(m, ctrlK)
	v := plainView(m)
	if !strings.Contains(v, "ZULETZT") {
		t.Fatalf("no recent group:\n%s", v)
	}
	if items := m.palVisible(); items[0].group != palRecent || items[0].title != "Harnesses prüfen" {
		t.Fatalf("first %+v", items[0])
	}
}

func TestPaletteContextActions(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 110, 40, darkBG)
	m = typeText(m, "REQ-619")
	m = drive(m, key("enter"))
	m = drive(m, ctrlK)
	m = typeText(m, "browser")
	m = drive(m, key("enter"))
	if got := f.opened[len(f.opened)-1]; got != m.card.URL {
		t.Fatalf("opened %q", got)
	}
	m = drive(m, ctrlK)
	m = typeText(m, "link kop")
	next, cmd := m.palRun(0)
	if cmd == nil || next.(Model).flash != "Link kopiert" {
		t.Fatal("copy link did nothing")
	}
	// Claim only shows where it can work.
	if hasTitle(openPal(t, newFake()), "Ordner als Projekt anlegen") {
		t.Fatal("claim offered for a known project")
	}
	if !hasTitle(openPal(t, newFake().inFolder("unknown")), "Ordner als Projekt anlegen") {
		t.Fatal("claim missing for an unknown remote")
	}
}

func hasTitle(m Model, title string) bool {
	for _, it := range m.palAll("") {
		if it.title == title {
			return true
		}
	}
	return false
}

func TestPaletteClickRunsAndTheBackgroundIgnoresClicks(t *testing.T) {
	f := newFake()
	m := openPal(t, f)
	m = typeText(m, "einst")
	v := m.View()
	lines := strings.Split(ansi.Strip(v.Content), "\n")
	for y, l := range lines {
		if x := strings.Index(l, "Einstellungen"); x >= 0 && strings.Contains(l, "│") {
			cmd := v.OnMouse(tea.MouseClickMsg{X: len([]rune(l[:x])), Y: y, Button: tea.MouseLeft})
			if cmd == nil {
				t.Fatal("click on the palette row hit nothing")
			}
			m = runCmd(m, cmd, 0)
			break
		}
	}
	if m.screen != ScreenSettings {
		t.Fatalf("screen %v", m.screen)
	}
	m = drive(m, key("esc"))
	m = drive(m, ctrlK)
	v = m.View()
	if cmd := v.OnMouse(tea.MouseClickMsg{X: 1, Y: len(lines) - 3, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("a click behind the palette reached the screen")
	}
}

func TestPaletteFiltersServerText(t *testing.T) {
	f := newFake()
	h := hostileFolder{hostileBackend{fakeBackend: f}}
	m := ready(f, Options{}, 110, 40, darkBG)
	m.be = h
	m = runCmd(m, m.openPalette(), 0)
	noForeignControls(t, "palette", m.View().Content)
	m = typeText(m, "a")
	noForeignControls(t, "palette hits", m.View().Content)
}

func TestCtrlFJumpsIntoTheSearchAndEscLeaves(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 110, 40, darkBG)
	m = typeText(m, "REQ-619")
	m = drive(m, key("enter"))
	if m.screen != ScreenCard {
		t.Fatal("no card")
	}
	m = drive(m, ctrlF)
	if m.screen != ScreenHome || !m.search.Focused() {
		t.Fatalf("screen %v focused %v", m.screen, m.search.Focused())
	}
	m = typeText(m, "x")
	m = drive(m, key("esc"))
	if m.quitting || m.search.Focused() || m.search.Value() != "" {
		t.Fatalf("esc: quitting %v focused %v value %q", m.quitting, m.search.Focused(), m.search.Value())
	}
	m = drive(m, key("esc"))
	if !m.quitting {
		t.Fatal("second esc should quit")
	}
	// Forms keep their fields.
	m = ready(newFake(), Options{Start: ScreenLogin}, 110, 40, darkBG)
	m = drive(m, ctrlF)
	if m.screen != ScreenLogin {
		t.Fatal("ctrl+f left the login form")
	}
	m = drive(m, ctrlK)
	if m.pal.open {
		t.Fatal("palette over the login form")
	}
}

func TestFooterNamesTheCommands(t *testing.T) {
	m := ready(newFake(), Options{}, 110, 40, darkBG)
	if !strings.Contains(plainView(m), "ctrl+k Befehle") {
		t.Fatalf("footer:\n%s", plainView(m))
	}
	m.setMode(modeInbox)
	if !strings.Contains(plainView(m), "ctrl+k Befehle") {
		t.Fatal("inbox footer")
	}
}
