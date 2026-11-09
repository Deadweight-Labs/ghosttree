// Package tui is the terminal home of ctx: quick search, a few lists, local
// settings and the device login, drawn with Bubble Tea.
package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// Screen names a page of the TUI.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenResults
	ScreenCard
	ScreenSettings
	ScreenLogin
	ScreenClaim
	ScreenDirs
	ScreenProjects
)

// Focus zones of the home screen: the lists, or the buttons of the "no
// project" state.
const (
	zoneList = iota
	zoneButtons
)

// Options set up one run.
type Options struct {
	Look   LogoStyle
	Switch SwitchStyle // how the mode switch is drawn
	Still  bool        // draw the logo without animation
	Plain  bool        // NO_COLOR: no colours at all
	Lang   string
	// Start is ScreenHome for `ctx`, ScreenLogin for `ctx login`.
	Start Screen
	// LoginServer and LoginMachine fill the login form; LoginDirect skips it.
	LoginServer  string
	LoginMachine string
	LoginDirect  bool
	// Getenv is os.Getenv unless a test replaces it.
	Getenv func(string) string

	// tick and now are the clock: timers for animation, spinner and flash
	// messages, and the current time. Tests replace both so that nothing
	// waits for real time.
	tick func(d time.Duration, msg tea.Msg) tea.Cmd
	now  func() time.Time
}

func realTick(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

// Timeouts for calls a person waits on: a slow server must look like an
// absent one instead of freezing the screen.
const (
	quickTimeout = 8 * time.Second
	slowTimeout  = 90 * time.Second
)

type listState struct {
	items   []Item
	err     error
	loading bool
	loaded  bool
}

// Model is the root Bubble Tea model.
type Model struct {
	be   Backend
	opt  Options
	lang lang
	th   Theme
	w, h int

	logo   logo
	screen Screen
	back   []Screen

	local  Local
	server Server // the checked default server

	search textinput.Model
	lists  [listKindCount]listState
	mode   int    // modeProject or modeInbox
	tab    [2]int // the open list of each mode
	cursor int
	splash bool // the big logo of the start, before the home

	results       listState
	resultsQuery  string
	resultsCursor int

	card        links.Card
	cardErr     error
	cardLoading bool

	settings settingsState
	login    loginState

	proj     projectState
	zone     int
	button   int
	claim    claimState
	dirs     dirsState
	projects projectsState

	flash    string
	flashBad bool
	flashID  int
	spin     int
	spinOn   bool

	// initCmd is work New prepared for Init, which cannot change the model.
	initCmd tea.Cmd

	// LoggedIn is set when a login finished; `ctx login` reads it for its
	// exit code.
	LoggedIn bool
	quitting bool
}

// New builds the model. It reads only local state; everything from the
// server arrives as messages.
func New(be Backend, opt Options) Model {
	if opt.Getenv == nil {
		opt.Getenv = os.Getenv
	}
	if opt.tick == nil {
		opt.tick = realTick
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	m := Model{be: be, opt: opt, w: 100, h: 34}
	if opt.Lang == "de" {
		m.lang = langDE
	} else if opt.Lang == "en" {
		m.lang = langEN
	} else {
		m.lang = detectLang(opt.Getenv)
	}
	m.th = NewTheme(true, opt.Plain)
	m.logo = logo{style: opt.Look, theme: m.th}
	if opt.Still {
		m.logo.frame = logoFrames
	}
	m.local = be.Local()
	if s, ok := m.local.DefaultServer(); ok {
		m.server = s
	}
	m.search = textinput.New()
	m.search.Prompt = "› "
	m.search.Placeholder = m.lang.t("search.placeholder")
	m.search.CharLimit = 400
	m.search.Focus()
	m.applyInputStyles()
	m.screen = opt.Start
	m.login = newLoginState(m.lang, opt.LoginServer, opt.LoginMachine, m.local)
	if m.screen != ScreenLogin && m.local.Configured {
		for k := range m.lists {
			if ListKind(k) >= ListMyTasks || m.local.Project != "" {
				m.lists[k].loading = true
			}
		}
	}
	m.splash = m.screen == ScreenHome && !opt.Still
	m.setMode(modeProject)
	if m.screen == ScreenLogin && opt.LoginDirect {
		m.initCmd = m.startLogin()
	}
	return m
}

func (m *Model) applyInputStyles() {
	st := textinput.DefaultStyles(m.th.Dark)
	if m.th.Plain {
		st = textinput.DefaultStyles(true)
		st.Focused.Prompt = m.th.Key
		st.Focused.Placeholder = m.th.Dim
		st.Blurred.Placeholder = m.th.Dim
	} else {
		st.Focused.Prompt = m.th.Key
		st.Blurred.Prompt = m.th.Dim
		st.Focused.Placeholder = m.th.Subtle
		st.Blurred.Placeholder = m.th.Subtle
		st.Cursor.Color = m.th.Accent
	}
	st.Cursor.Blink = false
	m.search.SetStyles(st)
	m.login.applyStyles(st)
	m.dirs.input.SetStyles(st)
}

// Init asks for the background colour, starts the logo and the first loads.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if !m.logo.done() {
		cmds = append(cmds, m.logoTick())
	}
	if m.screen == ScreenLogin {
		cmds = append(cmds, m.initCmd)
	} else {
		cmds = append(cmds, m.homeCmds()...)
	}
	return tea.Batch(cmds...)
}

type (
	logoTickMsg  struct{}
	spinTickMsg  struct{}
	flashDoneMsg struct{ id int }
	serverMsg    struct{ s Server }
	listMsg      struct {
		which ListKind
		items []Item
		err   error
	}
	cardMsg struct {
		card links.Card
		err  error
	}
	resultsMsg struct {
		query string
		items []Item
		err   error
	}
	clickMsg struct{ act action }
	wheelMsg struct{ up bool }
)

func (m Model) logoTick() tea.Cmd { return m.opt.tick(time.Second/logoFPS, logoTickMsg{}) }

func (m Model) spinTick() tea.Cmd { return m.opt.tick(90*time.Millisecond, spinTickMsg{}) }

// call runs f with a timeout in a command.
func call(d time.Duration, f func(ctx context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return f(ctx)
	}
}

// spinning says whether something the person waits for is running.
func (m Model) spinning() bool {
	if m.screen == ScreenLogin {
		return m.login.step == loginStarting || m.login.step == loginWaiting
	}
	return m.settings.busy != "" || m.settings.doctorBusy
}

// ensureSpin starts the spinner ticks unless they run already.
func (m *Model) ensureSpin() tea.Cmd {
	if m.spinOn {
		return nil
	}
	m.spinOn = true
	return m.spinTick()
}

// homeCmds are the loads of the home screen, without touching the model;
// loadHome also marks the lists as loading.
func (m Model) homeCmds() []tea.Cmd {
	return m.loadHome()
}

func (m *Model) loadHome() []tea.Cmd {
	var cmds []tea.Cmd
	if s, ok := m.local.DefaultServer(); ok {
		url := s.URL
		cmds = append(cmds, call(quickTimeout, func(ctx context.Context) tea.Msg {
			return serverMsg{m.be.CheckServer(ctx, url)}
		}))
	}
	if !m.local.Configured {
		return cmds
	}
	if c := m.checkProject(); c != nil {
		cmds = append(cmds, c)
	}
	for k := ListKind(0); k < listKindCount; k++ {
		if k < ListMyTasks && m.local.Project == "" {
			// Without a project the project mode shows only the "no
			// project" state; asking anyway would list every project.
			m.lists[k] = listState{err: &ProblemError{Kind: ProblemNoProject}, loaded: true}
			continue
		}
		cmds = append(cmds, m.loadList(k))
	}
	return cmds
}

// reloadHome forgets what was known about the folder's project and loads the
// home again, after a folder change or a new project.
func (m *Model) reloadHome() []tea.Cmd {
	m.proj = projectState{}
	m.button = 0
	cmds := m.loadHome()
	m.setMode(m.mode)
	return cmds
}

func (m *Model) setFlash(s string, bad bool) tea.Cmd {
	m.flash, m.flashBad = s, bad
	m.flashID++
	id := m.flashID
	return m.opt.tick(4*time.Second, flashDoneMsg{id})
}

func (m *Model) go_(s Screen) {
	if m.screen != s {
		m.back = append(m.back, m.screen)
	}
	m.screen = s
	m.flash = ""
}

func (m *Model) goBack() tea.Cmd {
	if len(m.back) == 0 {
		if m.opt.Start == ScreenLogin {
			m.quitting = true
			return tea.Quit
		}
		m.screen = ScreenHome
		return nil
	}
	m.screen = m.back[len(m.back)-1]
	m.back = m.back[:len(m.back)-1]
	m.flash = ""
	if m.screen == ScreenHome {
		return m.search.Focus()
	}
	return nil
}

// Update handles one message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.search.SetWidth(min(60, max(20, m.w-12)))
		return m, nil
	case tea.BackgroundColorMsg:
		m.th = NewTheme(msg.IsDark(), m.opt.Plain)
		m.logo.theme = m.th
		m.applyInputStyles()
		return m, nil
	case tea.ColorProfileMsg:
		if msg.Profile == colorprofile.Ascii || msg.Profile == colorprofile.NoTTY {
			m.opt.Plain = true
			m.th = NewTheme(m.th.Dark, true)
			m.logo.theme = m.th
			m.applyInputStyles()
		}
		return m, nil
	case logoTickMsg:
		if m.splash && m.logo.frame >= logoFrames+splashHold {
			m.splash = false
			return m, nil
		}
		if !m.splash && m.logo.done() {
			return m, nil
		}
		m.logo.frame++
		return m, m.logoTick()
	case spinTickMsg:
		if !m.spinning() {
			m.spinOn = false
			return m, nil
		}
		m.spin++
		return m, m.spinTick()
	case flashDoneMsg:
		if msg.id == m.flashID {
			m.flash = ""
		}
		return m, nil
	case serverMsg:
		// The check answers for the connection; name and default mark
		// belong to the local list.
		for i := range m.local.Servers {
			if old := m.local.Servers[i]; old.URL == msg.s.URL {
				s := msg.s
				s.Name, s.Default = old.Name, old.Default
				m.local.Servers[i] = s
				if s.Default || len(m.local.Servers) == 1 {
					m.server = s
				}
			}
		}
		if len(m.local.Servers) == 0 {
			m.server = msg.s
		}
		return m, nil
	case listMsg:
		m.lists[msg.which] = listState{items: msg.items, err: msg.err, loaded: true}
		return m, nil
	case projectCheckMsg:
		if msg.project != m.local.Project {
			return m, nil
		}
		m.proj = projectState{checked: true, known: msg.known, err: msg.err}
		m.setMode(m.mode)
		return m, nil
	case claimPlanMsg, claimDoneMsg:
		return m.updateClaim(msg)
	case recentDirsMsg:
		return m.updateDirs(msg)
	case projectsMsg, previewMsg:
		return m.updateProjects(msg)
	case cardMsg:
		m.cardLoading = false
		m.card, m.cardErr = msg.card.Sanitized(), msg.err
		return m, nil
	case resultsMsg:
		if msg.query == m.resultsQuery {
			m.results = listState{items: msg.items, err: msg.err, loaded: true}
			m.resultsCursor = 0
		}
		return m, nil
	case wheelMsg:
		return m.move(map[bool]int{true: -1, false: 1}[msg.up])
	case clickMsg:
		return m.click(msg.act)
	case tea.PasteMsg:
		if m.screen == ScreenHome {
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			return m, cmd
		}
		if m.screen == ScreenLogin {
			return m.updateLogin(msg)
		}
		if m.screen == ScreenDirs {
			return m.updateDirs(msg)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.splash {
			// A key ends the start picture and then counts as usual.
			m.splash = false
		}
		switch msg.String() {
		case "f2":
			m.opt.Look = (m.opt.Look + 1) % 3
			m.logo.style = m.opt.Look
			return m, nil
		case "f3":
			m.opt.Switch = (m.opt.Switch + 1) % 2
			return m, nil
		}
		switch m.screen {
		case ScreenHome:
			return m.keyHome(msg)
		case ScreenResults:
			return m.keyResults(msg)
		case ScreenCard:
			return m.keyCard(msg)
		case ScreenSettings:
			return m.keySettings(msg)
		case ScreenLogin:
			return m.updateLogin(msg)
		case ScreenClaim:
			return m.updateClaim(msg)
		case ScreenDirs:
			return m.updateDirs(msg)
		case ScreenProjects:
			return m.updateProjects(msg)
		}
	}
	switch m.screen {
	case ScreenSettings:
		return m.updateSettings(msg)
	case ScreenLogin:
		return m.updateLogin(msg)
	}
	return m, nil
}

// isRef says whether the quick search input names one object (REQ-12,
// #4295, pr:187, a path) rather than a search line.
func isRef(input string) bool {
	ref, err := links.ParseRef(input)
	if err != nil {
		return false
	}
	switch ref.Kind {
	case links.KindQuery, links.KindPreset:
		return false
	}
	return true
}

func (m *Model) openRef(ref string) tea.Cmd {
	m.go_(ScreenCard)
	m.card, m.cardErr, m.cardLoading = links.Card{Ref: ref}, nil, true
	m.search.Blur()
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		card, err := m.be.Resolve(ctx, ref)
		return cardMsg{card, err}
	})
}

// openItem shows the card of a list row.
func (m *Model) openItem(it Item) tea.Cmd {
	if it.Card != nil {
		m.go_(ScreenCard)
		m.card, m.cardErr, m.cardLoading = it.Card.Sanitized(), nil, false
		m.search.Blur()
		return nil
	}
	return m.openRef(it.Ref)
}

func (m *Model) runSearch(line string) tea.Cmd {
	m.go_(ScreenResults)
	m.resultsQuery = line
	m.results = listState{loading: true}
	m.search.Blur()
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		items, err := m.be.Search(ctx, line)
		return resultsMsg{query: line, items: items, err: err}
	})
}

func (m Model) keyHome(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	empty := strings.TrimSpace(m.search.Value()) == ""
	buttons := m.homeZone() == zoneButtons
	switch msg.String() {
	case "shift+tab":
		m.setMode(1 - m.mode)
		return m, nil
	case "tab":
		if buttons {
			m.moveButton(1)
			return m, nil
		}
		n := len(m.tabs())
		m.tab[m.mode] = (m.tab[m.mode] + 1) % max(1, n)
		m.cursor = 0
		return m, nil
	case "left", "right":
		if !empty {
			break
		}
		d := 1
		if msg.String() == "left" {
			d = -1
		}
		if buttons {
			m.moveButton(d)
			return m, nil
		}
		n := max(1, len(m.tabs()))
		m.tab[m.mode] = (m.tab[m.mode] + d + n) % n
		m.cursor = 0
		return m, nil
	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "enter":
		v := strings.TrimSpace(m.search.Value())
		if v != "" {
			if isRef(v) {
				return m, m.openRef(v)
			}
			return m, m.runSearch(v)
		}
		if buttons {
			return m, m.activateButton(m.button)
		}
		if items := m.currentList(); m.cursor < len(items) {
			return m, m.openItem(items[m.cursor])
		}
		return m, nil
	case "up", "ctrl+p":
		return m.move(-1)
	case "down", "ctrl+n":
		return m.move(1)
	case "ctrl+s", "f10":
		m.go_(ScreenSettings)
		m.search.Blur()
		return m, m.enterSettings()
	case "ctrl+l":
		return m, m.openLogin(m.server.URL, false)
	case "ctrl+r":
		m.local = m.be.Local()
		return m, tea.Batch(m.reloadHome()...)
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	return m, cmd
}

func (m Model) move(d int) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenHome:
		n := len(m.currentList())
		if n > 0 {
			m.cursor = (m.cursor + d + n) % n
		}
	case ScreenResults:
		n := len(m.results.items)
		if n > 0 {
			m.resultsCursor = max(0, min(n-1, m.resultsCursor+d))
		}
	case ScreenSettings:
		m.settings.move(d, len(m.settingsRows()))
	}
	return m, nil
}

func (m Model) keyResults(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		return m, m.goBack()
	case "up", "k", "ctrl+p":
		return m.move(-1)
	case "down", "j", "ctrl+n":
		return m.move(1)
	case "enter":
		if m.resultsCursor < len(m.results.items) {
			return m, m.openItem(m.results.items[m.resultsCursor])
		}
	}
	return m, nil
}

func (m Model) keyCard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace", "q":
		return m, m.goBack()
	case "enter", "o":
		return m, m.openURL(m.card.URL)
	case "c", "y":
		if !webURL(m.card.URL) {
			return m, nil
		}
		return m, tea.Batch(tea.SetClipboard(m.card.URL), m.setFlash(m.lang.t("copied"), false))
	}
	return m, nil
}

func (m *Model) openURL(url string) tea.Cmd {
	if url == "" {
		return nil
	}
	if !webURL(url) {
		return m.setFlash(m.lang.t("not.opened"), true)
	}
	opened, why := m.be.Open(url)
	if opened {
		return m.setFlash(m.lang.t("opened"), false)
	}
	return m.setFlash(m.lang.t("not.opened")+" ("+clean(why, 80)+")", true)
}

func (m Model) click(a action) (tea.Model, tea.Cmd) {
	switch a.kind {
	case "mode":
		m.setMode(a.a)
		return m, nil
	case "tab":
		m.tab[m.mode], m.cursor = a.a, 0
		return m, nil
	case "item":
		m.tab[m.mode], m.cursor = a.a, a.b
		if items := m.currentList(); a.b < len(items) {
			return m, m.openItem(items[a.b])
		}
	case "result":
		m.resultsCursor = a.a
		if a.a < len(m.results.items) {
			return m, m.openItem(m.results.items[a.a])
		}
	case "link":
		return m, m.openURL(m.card.URL)
	case "setting":
		m.settings.cursor = a.a
		return m.activateSetting()
	case "settings":
		m.go_(ScreenSettings)
		m.search.Blur()
		return m, m.enterSettings()
	case "back":
		return m, m.goBack()
	case "login.open":
		return m, m.openURL(m.login.start.CompleteURL)
	case "button":
		m.button = a.a
		return m, m.activateButton(a.a)
	case "dir":
		if a.a < len(m.dirs.recent.items) {
			return m.changeDir(m.dirs.recent.items[a.a].Ref)
		}
	case "project":
		m.projects.cursor = a.a
		return m, m.loadPreview()
	}
	return m, nil
}

// View renders the current screen.
func (m Model) View() tea.View {
	var b block
	switch m.screen {
	case ScreenResults:
		b = m.viewResults()
	case ScreenCard:
		b = m.viewCard()
	case ScreenSettings:
		b = m.viewSettings()
	case ScreenLogin:
		b = m.viewLogin()
	case ScreenClaim:
		b = m.viewClaim()
	case ScreenDirs:
		b = m.viewDirs()
	case ScreenProjects:
		b = m.viewProjects()
	default:
		b = m.viewHome()
	}
	if m.quitting {
		return tea.NewView("")
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = m.title()
	v.OnMouse = func(msg tea.MouseMsg) tea.Cmd {
		switch e := msg.(type) {
		case tea.MouseClickMsg:
			if e.Button != tea.MouseLeft {
				return nil
			}
			if a, ok := b.hitAt(e.X, e.Y); ok {
				return func() tea.Msg { return clickMsg{a} }
			}
		case tea.MouseWheelMsg:
			up := e.Button == tea.MouseWheelUp
			return func() tea.Msg { return wheelMsg{up} }
		}
		return nil
	}
	return v
}

func (m Model) title() string {
	switch m.screen {
	case ScreenCard:
		if m.card.Ref != "" {
			return "ghosttree · " + links.Clean(m.card.Ref, 40)
		}
	case ScreenSettings:
		return "ghosttree · " + m.lang.t("settings")
	case ScreenLogin:
		return "ghosttree · login"
	case ScreenProjects:
		return "ghosttree · " + m.lang.t("projects")
	}
	return "ghosttree"
}

// errText makes an error one short line for the screen.
func errText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return links.Clean(err.Error(), 160)
}

// Run starts the program on the terminal and returns the final model.
func Run(be Backend, opt Options) (Model, error) {
	p := tea.NewProgram(New(be, opt))
	final, err := p.Run()
	if fm, ok := final.(Model); ok {
		return fm, err
	}
	return Model{}, err
}
