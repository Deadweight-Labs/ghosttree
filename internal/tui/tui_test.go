package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

func plainView(m Model) string { return ansi.Strip(m.View().Content) }

func TestIsRefSeparatesObjectsFromSearchLines(t *testing.T) {
	for _, in := range []string{"REQ-610", "pain-66", "TASK-124", "IDEA-3", "FEAT-7", "#4354", "pr:187", "internal/store", "AC-619.2"} {
		if !isRef(in) {
			t.Errorf("%q should open an object", in)
		}
	}
	for _, in := range []string{"login bug", "is:open label:bug", "preset:fuer-mich", "tui", "#abc"} {
		if isRef(in) {
			t.Errorf("%q should be searched", in)
		}
	}
}

func TestQuickSearchJumpsToTheCardAndOpensTheLink(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	m = typeText(m, "REQ-619")
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || len(f.resolved) != 1 || f.resolved[0] != "REQ-619" {
		t.Fatalf("screen %v resolved %v", m.screen, f.resolved)
	}
	v := plainView(m)
	for _, want := range []string{"Issue", "REQ-619", "ctx als Terminal-Zuhause", "8 of 8 criteria open", "/issues/619"} {
		if !strings.Contains(v, want) {
			t.Errorf("card misses %q:\n%s", want, v)
		}
	}
	if !strings.Contains(m.View().Content, ansi.SetHyperlink(m.card.URL)) {
		t.Error("the link is not an OSC 8 hyperlink")
	}
	m = drive(m, key("enter"))
	if len(f.opened) != 1 || f.opened[0] != m.card.URL {
		t.Fatalf("opened %v", f.opened)
	}
	if !strings.Contains(m.flash, "nicht geöffnet") {
		t.Fatalf("flash %q", m.flash)
	}
	next, cmd := m.Update(key("c"))
	m = next.(Model)
	if cmd == nil || m.flash != "Link kopiert" {
		t.Fatalf("copy: cmd %v flash %q", cmd, m.flash)
	}
	m = drive(m, key("esc"))
	if m.screen != ScreenHome || m.search.Value() != "REQ-619" {
		t.Fatalf("back: screen %v value %q", m.screen, m.search.Value())
	}
}

func TestUnknownObjectSaysSoOnTheCard(t *testing.T) {
	m := ready(newFake(), Options{}, 100, 32, darkBG)
	m = typeText(m, "REQ-404")
	m = drive(m, key("enter"))
	if m.cardErr == nil || !strings.Contains(plainView(m), "not found") {
		t.Fatalf("err %v view %s", m.cardErr, plainView(m))
	}
}

func TestFreeTextAndPresetsGoThroughTheSearchLanguage(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	m = typeText(m, "is:open label:bug")
	m = drive(m, key("enter"))
	if m.screen != ScreenResults || len(m.results.items) != 2 {
		t.Fatalf("screen %v results %+v", m.screen, m.results)
	}
	if !strings.Contains(plainView(m), "#4354") {
		t.Fatalf("results view:\n%s", plainView(m))
	}
	m = drive(m, key("down"))
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || f.resolved[len(f.resolved)-1] != "REQ-619" {
		t.Fatalf("screen %v resolved %v", m.screen, f.resolved)
	}
	m = drive(m, key("esc"))
	m = drive(m, key("esc"))
	m.search.SetValue("")
	m = typeText(m, "preset:fuer-mich")
	m = drive(m, key("enter"))
	if got := f.searched[len(f.searched)-1]; got != "preset:fuer-mich" {
		t.Fatalf("searched %v", f.searched)
	}
}

func TestProjectModeShowsOneListAndTabSwitchesIt(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	if m.mode != modeProject {
		t.Fatalf("start mode %d", m.mode)
	}
	v := plainView(m)
	for _, want := range []string{"Projekt", "Inbox 5", "deadweight-labs/ghosttree", "Requests 5", "Pains 2", "Tasks 2", "Nachrichten 2", "REQ-619"} {
		if !strings.Contains(v, want) {
			t.Errorf("home misses %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "PAIN-66") || strings.Contains(v, "Fragen") {
		t.Fatalf("more than one list, or inbox content, on the project mode:\n%s", v)
	}
	m = drive(m, key("down"))
	m = drive(m, key("enter"))
	if f.resolved[len(f.resolved)-1] != "REQ-610" {
		t.Fatalf("resolved %v", f.resolved)
	}
	m = drive(m, key("esc"))
	m = drive(m, key("tab"))
	if m.currentKind() != ListPains || !strings.Contains(plainView(m), "PAIN-66") {
		t.Fatalf("tab: kind %v view %s", m.currentKind(), plainView(m))
	}
	m = drive(m, key("tab"))
	m = drive(m, key("tab"))
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || f.resolved[len(f.resolved)-1] != "room:12" {
		t.Fatalf("room row: screen %v resolved %v", m.screen, f.resolved)
	}
}

func TestShiftTabSwitchesToTheInbox(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.mode != modeInbox {
		t.Fatalf("mode %d", m.mode)
	}
	v := plainView(m)
	for _, want := range []string{"Tasks 2", "Fragen 2", "Nachrichten 1", "TASK-12", "design-system", "shift+tab Projekt"} {
		if !strings.Contains(v, want) {
			t.Errorf("inbox misses %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Vorschläge") || strings.Contains(v, "REQ-619") {
		t.Fatalf("empty suggestions tab or project content in the inbox:\n%s", v)
	}
	m = drive(m, key("tab"))
	if m.currentKind() != ListQuestions || !strings.Contains(plainView(m), "Rollout von #200 freigeben?") {
		t.Fatalf("questions: %v %s", m.currentKind(), plainView(m))
	}
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.mode != modeProject || m.currentKind() != ListRequests {
		t.Fatalf("back: mode %d kind %v", m.mode, m.currentKind())
	}
}

func TestInboxNumberOnlyWhenSomethingWaits(t *testing.T) {
	f := newFake()
	f.inboxEmpty = true
	m := ready(f, Options{}, 100, 32, darkBG)
	v := plainView(m)
	if !strings.Contains(v, "Inbox") || strings.Contains(v, "Inbox 0") {
		t.Fatalf("view:\n%s", v)
	}
	f2 := newFake()
	f2.suggestions = []Item{{Ref: "#4400", Title: "Neue Notiz zum Rollout"}}
	m = ready(f2, Options{}, 100, 32, darkBG)
	m.setMode(modeInbox)
	if v := plainView(m); !strings.Contains(v, "Vorschläge 1") || !strings.Contains(v, "Inbox 5") {
		t.Fatalf("suggestions tab, or count includes them:\n%s", v)
	}
}

func TestMessageRowsShowTheirOwnCard(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	card := links.Card{Kind: "Message", Ref: "#9", Title: "hallo", URL: "https://ghosttree.example.com/ui/room"}
	m.lists[ListRoom] = listState{items: []Item{{Ref: "#9", Title: "hallo", Card: &card}}, loaded: true}
	m.tab[modeProject] = 3
	before := len(f.resolved)
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || m.card.Ref != "#9" || len(f.resolved) != before {
		t.Fatalf("screen %v card %+v resolved %v", m.screen, m.card, f.resolved)
	}
}

func TestMouseClickOnARowOpensIt(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	v := m.View()
	lines := strings.Split(ansi.Strip(v.Content), "\n")
	row := -1
	for i, l := range lines {
		if strings.Contains(l, "REQ-610") {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("row not found")
	}
	x := len([]rune(lines[row][:strings.Index(lines[row], "REQ-610")]))
	cmd := v.OnMouse(tea.MouseClickMsg{X: x, Y: row, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("click hit nothing")
	}
	m = runCmd(m, cmd, 0)
	if f.resolved[len(f.resolved)-1] != "REQ-610" {
		t.Fatalf("resolved %v", f.resolved)
	}
}

func TestViewUsesTerminalFeatures(t *testing.T) {
	m := ready(newFake(), Options{}, 100, 32, darkBG)
	v := m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion || v.WindowTitle != "ghosttree" {
		t.Fatalf("view %+v", v)
	}
	m = drive(m, key("ctrl+s"))
	if m.View().WindowTitle != "ghosttree · Einstellungen" {
		t.Fatalf("title %q", m.View().WindowTitle)
	}
}

func TestNoColorDrawsWithoutColours(t *testing.T) {
	for _, look := range []LogoStyle{LogoMark, LogoTree, LogoLines} {
		m := ready(newFake(), Options{Plain: true, Look: look}, 100, 40, darkBG)
		if c := m.View().Content; strings.Contains(c, "\x1b[38;") || strings.Contains(c, "\x1b[48;") {
			t.Fatalf("%s: colour escapes with NO_COLOR", look)
		}
	}
}

func TestLightBackgroundSwitchesTheTheme(t *testing.T) {
	m := ready(newFake(), Options{}, 100, 32, lightBG)
	if m.th.Dark {
		t.Fatal("light background not detected")
	}
	m = drive(m, darkBG)
	if !m.th.Dark {
		t.Fatal("dark background not detected")
	}
}

func TestLogoAnimatesForAboutASecondThenStandsStill(t *testing.T) {
	for _, look := range []LogoStyle{LogoMark, LogoTree, LogoLines} {
		th := NewTheme(true, false)
		first := logo{style: look, theme: th, frame: 1}.View()
		last := logo{style: look, theme: th, frame: logoFrames}.View()
		later := logo{style: look, theme: th, frame: logoFrames + 40}.View()
		if first == last {
			t.Errorf("%s: no animation", look)
		}
		if last != later {
			t.Errorf("%s: the logo still moves after the animation", look)
		}
		if lw, lh := len(strings.Split(first, "\n")), len(strings.Split(last, "\n")); lw != lh {
			t.Errorf("%s: frames change height (%d, %d)", look, lw, lh)
		}
	}
	if logoFrames*1000/logoFPS > 1300 {
		t.Fatalf("animation runs %d ms", logoFrames*1000/logoFPS)
	}
	m := New(newFake(), Options{Still: true, tick: testTick, Getenv: func(string) string { return "" }})
	if !m.logo.done() {
		t.Fatal("Still should start at rest")
	}
	m = New(newFake(), Options{tick: testTick, Getenv: func(string) string { return "" }})
	if !m.splash {
		t.Fatal("no start picture")
	}
	for i := 0; i < logoFrames+splashHold+2; i++ {
		next, _ := m.Update(logoTickMsg{})
		m = next.(Model)
	}
	if !m.logo.done() || m.splash {
		t.Fatalf("logo done %v, splash %v", m.logo.done(), m.splash)
	}
	if _, cmd := m.Update(logoTickMsg{}); cmd != nil {
		t.Fatal("ticks continue after the start picture")
	}
}

func TestStartPictureThenCompactHome(t *testing.T) {
	f := newFake()
	m := New(f, Options{tick: testTick, now: func() time.Time { return testNow }, Getenv: func(string) string { return "" }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = next.(Model)
	m = runCmd(m, tea.Batch(m.loadHome()...), 0)
	if v := plainView(m); strings.Contains(v, "Requests") || !strings.Contains(v, "ghosttree") {
		t.Fatalf("start picture shows lists:\n%s", v)
	}
	// A key ends the picture at once and still counts.
	m = drive(m, key("R"))
	if m.splash || m.search.Value() != "R" {
		t.Fatalf("splash %v search %q", m.splash, m.search.Value())
	}
	v := plainView(m)
	if !strings.Contains(v, "Requests") {
		t.Fatalf("home after the picture:\n%s", v)
	}
	// Compact: the big logo is gone, the name stands in the header line.
	if strings.Contains(v, "▀▀▀") || strings.Count(v, "\n") > 32 {
		t.Fatalf("big logo after the start:\n%s", v)
	}
}

func TestSwitchStyles(t *testing.T) {
	for in, want := range map[string]SwitchStyle{"": SwitchRail, "rail": SwitchRail, "bar": SwitchBar} {
		if got, ok := ParseSwitchStyle(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := ParseSwitchStyle("tabs"); ok {
		t.Error("tabs accepted")
	}
	m := ready(newFake(), Options{}, 100, 32, darkBG)
	rail := plainView(m)
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyF3})
	if m.opt.Switch != SwitchBar {
		t.Fatalf("f3: %v", m.opt.Switch)
	}
	bar := plainView(m)
	// Rail: the modes stand below each other; bar: in the header line.
	firstLine := func(v string) string { return strings.Split(strings.TrimLeft(v, "\n"), "\n")[0] }
	if strings.Contains(firstLine(rail), "Projekt") || !strings.Contains(firstLine(bar), "Projekt") {
		t.Fatalf("rail header %q, bar header %q", firstLine(rail), firstLine(bar))
	}
	// Clicking a mode switches to it, in both styles.
	for _, sw := range []SwitchStyle{SwitchRail, SwitchBar} {
		m := ready(newFake(), Options{Switch: sw}, 100, 32, darkBG)
		v := m.View()
		lines := strings.Split(ansi.Strip(v.Content), "\n")
		for y, l := range lines {
			if x := strings.Index(l, "Inbox"); x >= 0 {
				if cmd := v.OnMouse(tea.MouseClickMsg{X: len([]rune(l[:x])), Y: y, Button: tea.MouseLeft}); cmd != nil {
					m = runCmd(m, cmd, 0)
				}
				break
			}
		}
		if m.mode != modeInbox {
			t.Fatalf("%v: click on Inbox did not switch", sw)
		}
	}
}

func TestParseLogoStyle(t *testing.T) {
	for in, want := range map[string]LogoStyle{"": LogoMark, "mark": LogoMark, "B": LogoTree, "lines": LogoLines} {
		if got, ok := ParseLogoStyle(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := ParseLogoStyle("neon"); ok {
		t.Error("neon accepted")
	}
}

func TestF2CyclesTheLook(t *testing.T) {
	m := ready(newFake(), Options{}, 100, 32, darkBG)
	for _, want := range []LogoStyle{LogoTree, LogoLines, LogoMark} {
		m = drive(m, tea.KeyPressMsg{Code: tea.KeyF2})
		if m.opt.Look != want || m.logo.style != want {
			t.Fatalf("look %v, want %v", m.opt.Look, want)
		}
	}
}

func TestEnglishWithoutGermanLocale(t *testing.T) {
	m := ready(newFake(), Options{Getenv: func(k string) string {
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}}, 100, 32, darkBG)
	if v := plainView(m); !strings.Contains(v, "Project") || !strings.Contains(v, "Inbox") || !strings.Contains(v, "settings") || !strings.Contains(v, "Messages") {
		t.Fatalf("english view:\n%s", v)
	}
}

func settingsAt(t *testing.T, m Model, kind string) Model {
	t.Helper()
	m = drive(m, key("ctrl+s"))
	for i, r := range m.settingsRows() {
		if r.kind == kind {
			m.settings.cursor = i
			return m
		}
	}
	t.Fatalf("no row %s", kind)
	return m
}

func TestSettingsShowLoginStateWithoutToken(t *testing.T) {
	m := ready(newFake(), Options{}, 100, 40, darkBG)
	m = drive(m, key("ctrl+s"))
	v := plainView(m)
	for _, want := range []string{"ghosttree.example.com", "Standard", "erreichbar", "robin", "Anmeldung gültig", "stable", "dev", "läuft", "workstation", "im Web-Profil", "1 FAIL"} {
		if !strings.Contains(v, want) {
			t.Errorf("settings miss %q:\n%s", want, v)
		}
	}
}

func TestSettingsShowSinceWhenTheLoginExistsAndUntilWhenItHolds(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 120, 40, darkBG)
	v := plainView(drive(m, key("ctrl+s")))
	for _, want := range []string{"Anmeldung gültig", "seit 2026-10-12", "läuft nicht ab"} {
		if !strings.Contains(v, want) {
			t.Errorf("login without expiry misses %q:\n%s", want, v)
		}
	}

	f = newFake()
	f.expires = time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
	m = ready(f, Options{}, 120, 40, darkBG)
	v = plainView(drive(m, key("ctrl+s")))
	for _, want := range []string{"seit 2026-10-12", "gültig bis 2026-12-31"} {
		if !strings.Contains(v, want) {
			t.Errorf("login with expiry misses %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "läuft nicht ab") {
		t.Errorf("an expiring login says it does not expire:\n%s", v)
	}

	// English words for an English locale.
	m = ready(f, Options{}, 120, 40, darkBG)
	m.lang = langEN
	v = plainView(drive(m, key("ctrl+s")))
	if !strings.Contains(v, "since 2026-10-12") || !strings.Contains(v, "valid until 2026-12-31") {
		t.Errorf("english settings:\n%s", v)
	}
}

func TestSettingsNameNoTimeForAnOlderServer(t *testing.T) {
	// An older server says neither since when nor until when: the row then
	// shows what it knows and no invented dates.
	f := newFake()
	f.oldServer = true
	m := ready(f, Options{}, 120, 40, darkBG)
	v := plainView(drive(m, key("ctrl+s")))
	if !strings.Contains(v, "Anmeldung gültig") {
		t.Errorf("the login state vanished:\n%s", v)
	}
	if strings.Contains(v, "seit ") || strings.Contains(v, "läuft nicht ab") || strings.Contains(v, "gültig bis") {
		t.Errorf("dates shown without data:\n%s", v)
	}
}

// Every row of the settings names a command a script can run instead; a web
// address is not one (REQ-619 AC-6).
func TestEverySettingRowNamesAnEquivalentCommand(t *testing.T) {
	f := newFake()
	f.local.Servers = append(f.local.Servers, Server{Name: "Firma", URL: "https://ghosttree.firma.example", Token: TokenNone})
	m := ready(f, Options{}, 100, 40, darkBG)
	seen := map[string]bool{}
	for _, row := range m.settingsRows() {
		eq := m.equivalent(row)
		seen[row.kind] = true
		if !strings.HasPrefix(eq, "ctx ") || strings.Contains(eq, "://") && !strings.Contains(eq, "--server") {
			t.Errorf("row %s: equivalent %q is not a command", row.kind, eq)
		}
	}
	for _, kind := range []string{"server", "add-server", "channel", "harness", "collector", "machine", "github"} {
		if !seen[kind] {
			t.Errorf("settings have no row %s", kind)
		}
	}
	m = settingsAt(t, m, "github")
	if v := plainView(m); !strings.Contains(v, "ctx account github link") {
		t.Fatalf("github row shows no command:\n%s", v)
	}
}

func TestServerSectionIsAList(t *testing.T) {
	f := newFake()
	f.local.Servers = append(f.local.Servers, Server{Name: "Firma", URL: "https://ghosttree.firma.example", Token: TokenNone})
	m := ready(f, Options{}, 100, 40, darkBG)
	m = drive(m, key("ctrl+s"))
	v := plainView(m)
	if !strings.Contains(v, "ghosttree.example.com") || !strings.Contains(v, "Firma") || !strings.Contains(v, "ghosttree.firma.example") {
		t.Fatalf("server list:\n%s", v)
	}
	if n := len(m.settingsRows()); n != 8 {
		t.Fatalf("rows %d", n)
	}
}

func TestSettingsActions(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 40, darkBG)

	m = settingsAt(t, m, "channel")
	if !strings.Contains(plainView(m), "ctx config set channel dev") {
		t.Fatalf("no equivalent command:\n%s", plainView(m))
	}
	m = drive(m, key("enter"))
	if f.channel != "dev" || m.local.Channel != "dev" {
		t.Fatalf("channel %s / %s", f.channel, m.local.Channel)
	}
	m = drive(m, key("esc"))

	m = settingsAt(t, m, "harness")
	m = drive(m, key("f"))
	if len(f.doctorFix) == 0 || !f.doctorFix[len(f.doctorFix)-1] || m.settings.doctor == nil || !m.settings.doctor.OK {
		t.Fatalf("doctor fix %v rep %+v", f.doctorFix, m.settings.doctor)
	}
	m = drive(m, key("esc"))

	m = settingsAt(t, m, "collector")
	m = drive(m, key("enter"))
	if f.restarted != 1 || !strings.Contains(m.settings.note, "restarted") {
		t.Fatalf("restart %d note %q", f.restarted, m.settings.note)
	}
	m = drive(m, key("esc"))

	m = settingsAt(t, m, "github")
	m = drive(m, key("enter"))
	if f.opened[len(f.opened)-1] != "https://ghosttree.example.com/ui/profile" {
		t.Fatalf("opened %v", f.opened)
	}
	m = drive(m, key("esc"))

	m = settingsAt(t, m, "server")
	m = drive(m, key("x"))
	m = drive(m, key("y"))
	if len(f.loggedOut) != 1 || m.local.Servers[0].Token != TokenNone {
		t.Fatalf("logout %v servers %+v", f.loggedOut, m.local.Servers)
	}
}

func TestLoginFlowFromSettings(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 40, darkBG)
	m = settingsAt(t, m, "server")
	m = drive(m, key("enter"))
	if m.screen != ScreenLogin || m.login.server.Value() != "https://ghosttree.example.com" || m.login.machine.Value() != "workstation" {
		t.Fatalf("form: screen %v server %q machine %q", m.screen, m.login.server.Value(), m.login.machine.Value())
	}
	f.waitBlock = true
	m = drive(m, key("enter"))
	if m.login.step != loginWaiting {
		t.Fatalf("step %v err %v", m.login.step, m.login.err)
	}
	v := plainView(m)
	for _, want := range []string{"WDJB-MJHT", "https://ghosttree.example.com/device", "wartet auf den Browser"} {
		if !strings.Contains(v, want) {
			t.Errorf("waiting view misses %q:\n%s", want, v)
		}
	}
	if f.opened[len(f.opened)-1] != "https://ghosttree.example.com/device?code=WDJB-MJHT" {
		t.Fatalf("browser not offered the login page: %v", f.opened)
	}
	m = drive(m, key("esc"))
	if m.login.step != loginForm {
		t.Fatalf("esc: step %v", m.login.step)
	}
	f.waitBlock = false
	m = drive(m, key("enter"))
	if m.login.step != loginDone || !m.LoggedIn || !strings.Contains(plainView(m), "Angemeldet als robin") {
		t.Fatalf("done: step %v view %s", m.login.step, plainView(m))
	}
	m = drive(m, key("enter"))
	if m.screen != ScreenHome {
		t.Fatalf("after login: screen %v", m.screen)
	}
}

func TestLoginTakenMachineSuggestsAnotherName(t *testing.T) {
	f := newFake()
	f.waitErr = &MachineTakenError{Machine: "workstation", Suggestion: "workstation-2"}
	m := ready(f, Options{Start: ScreenLogin, LoginServer: "https://ghosttree.example.com", LoginMachine: "workstation"}, 100, 40, darkBG)
	m = drive(m, key("enter"))
	if m.login.step != loginFailed || !strings.Contains(plainView(m), "workstation-2") {
		t.Fatalf("step %v view %s", m.login.step, plainView(m))
	}
	m = drive(m, key("enter"))
	if m.login.step != loginForm || m.login.machine.Value() != "workstation-2" {
		t.Fatalf("retry: step %v machine %q", m.login.step, m.login.machine.Value())
	}
}

func TestLoginStartErrorIsShown(t *testing.T) {
	f := newFake()
	f.loginErr = errors.New("connection refused")
	m := ready(f, Options{Start: ScreenLogin, LoginServer: "ghosttree.example.com", LoginMachine: "m"}, 100, 40, darkBG)
	m = drive(m, key("enter"))
	if m.login.step != loginFailed || !strings.Contains(plainView(m), "connection refused") {
		t.Fatalf("step %v view %s", m.login.step, plainView(m))
	}
	if m.login.server.Value() != "https://ghosttree.example.com" {
		t.Fatalf("scheme not added: %q", m.login.server.Value())
	}
}

func TestStandaloneLoginQuitsAfterSuccess(t *testing.T) {
	f := newFake()
	m := ready(f, Options{Start: ScreenLogin, LoginServer: "https://ghosttree.example.com", LoginMachine: "m", LoginDirect: true}, 100, 40, darkBG)
	m = runCmd(m, m.Init(), 0)
	if m.login.step != loginDone || !m.LoggedIn {
		t.Fatalf("step %v", m.login.step)
	}
	_, cmd := m.Update(loginQuitMsg{})
	if cmd == nil {
		t.Fatal("no quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not a quit")
	}
}

func TestNotConfiguredInvitesToSignIn(t *testing.T) {
	f := newFake()
	f.local.Configured = false
	f.local.Servers = nil
	m := ready(f, Options{}, 100, 32, darkBG)
	if v := plainView(m); !strings.Contains(v, "nicht verbunden") || !strings.Contains(v, "ctrl+l") {
		t.Fatalf("view:\n%s", v)
	}
	m = drive(m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.screen != ScreenLogin || m.login.server.Value() != "" {
		t.Fatalf("screen %v server %q", m.screen, m.login.server.Value())
	}
}

func TestHomeShowsLoadingBeforeTheListsArrive(t *testing.T) {
	m := New(newFake(), Options{Still: true, tick: testTick, Getenv: func(string) string { return "LANG=de" }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	if v := plainView(next.(Model)); !strings.Contains(v, "lädt") && !strings.Contains(v, "loading") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestSmallTerminalStillRenders(t *testing.T) {
	for _, look := range []LogoStyle{LogoMark, LogoTree, LogoLines} {
		m := ready(newFake(), Options{Look: look}, 50, 14, darkBG)
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > 14 {
			t.Errorf("%s: %d lines for 14 rows", look, len(lines))
		}
	}
}
