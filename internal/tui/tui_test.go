package tui

import (
	"errors"
	"strings"
	"testing"

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

func TestListsSwitchWithTabAndOpenTheSelectedRow(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	v := plainView(m)
	for _, want := range []string{"Meine Tasks 3", "Offene Requests 5", "Nachrichten 2", "TASK-133"} {
		if !strings.Contains(v, want) {
			t.Errorf("home misses %q:\n%s", want, v)
		}
	}
	m = drive(m, key("tab"))
	m = drive(m, key("down"))
	if m.section != listRequests || m.cursor != 1 {
		t.Fatalf("section %d cursor %d", m.section, m.cursor)
	}
	m = drive(m, key("enter"))
	if f.resolved[len(f.resolved)-1] != "REQ-610" {
		t.Fatalf("resolved %v", f.resolved)
	}
	m = drive(m, key("esc"))
	m = drive(m, key("tab"))
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || f.resolved[len(f.resolved)-1] != "room:12" {
		t.Fatalf("message row: screen %v resolved %v", m.screen, f.resolved)
	}
}

func TestMessageRowsShowTheirOwnCard(t *testing.T) {
	f := newFake()
	m := ready(f, Options{}, 100, 32, darkBG)
	card := links.Card{Kind: "Message", Ref: "#9", Title: "hallo", URL: "https://ghosttree.example.com/ui/room"}
	m.lists[listMessages] = listState{items: []Item{{Ref: "#9", Title: "hallo", Card: &card}}, loaded: true}
	m.section = listMessages
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
		if strings.Contains(l, "TASK-124") {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("row not found")
	}
	x := strings.Index(lines[row], "TASK-124")
	cmd := v.OnMouse(tea.MouseClickMsg{X: x, Y: row, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("click hit nothing")
	}
	m = runCmd(m, cmd, 0)
	if f.resolved[len(f.resolved)-1] != "TASK-124" {
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
	for i := 0; i < logoFrames+5; i++ {
		next, _ := m.Update(logoTickMsg{})
		m = next.(Model)
	}
	if !m.logo.done() {
		t.Fatal("logo never came to rest")
	}
	if _, cmd := m.Update(logoTickMsg{}); cmd != nil {
		t.Fatal("ticks continue after the animation")
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
	if v := plainView(m); !strings.Contains(v, "My tasks") || !strings.Contains(v, "settings") {
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
