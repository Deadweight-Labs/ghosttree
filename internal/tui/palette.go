package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// The command palette (ctrl+k): one input over the current screen, the hits
// grouped into recently used, jump to, actions and search. Every action names
// its key or its command on the right.

type palGroup int

const (
	palRecent palGroup = iota
	palJump
	palAction
	palSearch
)

var palGroupKeys = map[palGroup]string{
	palRecent: "pal.recent",
	palJump:   "pal.jump",
	palAction: "pal.actions",
	palSearch: "pal.search",
}

type palItem struct {
	id       string
	group    palGroup
	title    string
	keywords string // more words the fuzzy match looks at
	hint     string // key or command, shown on the right
	score    int
	run      func(m Model) (Model, tea.Cmd)
}

type paletteState struct {
	open     bool
	input    textinput.Model
	cursor   int
	recent   []string // ids, most recent first
	asked    bool     // projects and folders were requested
	projects []ProjectInfo
	dirs     []string
}

type (
	palProjectsMsg struct{ list []ProjectInfo }
	palDirsMsg     struct{ dirs []string }
)

const (
	palRecentMax  = 5
	palGroupMax   = 6
	palEmptyJumps = 4
)

// openPalette shows the palette and, the first time, asks for the projects
// and recent folders it can jump to.
func (m *Model) openPalette() tea.Cmd {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = m.lang.t("pal.placeholder")
	in.CharLimit = 300
	in.SetWidth(m.paletteWidth() - 8)
	m.pal.input = in
	m.pal.open, m.pal.cursor = true, 0
	m.applyInputStyles()
	cmds := []tea.Cmd{m.pal.input.Focus()}
	if !m.pal.asked && m.local.Configured {
		m.pal.asked = true
		cmds = append(cmds,
			call(quickTimeout, func(ctx context.Context) tea.Msg {
				list, _ := m.be.Projects(ctx)
				return palProjectsMsg{list}
			}),
			call(quickTimeout, func(ctx context.Context) tea.Msg {
				dirs, _ := m.be.RecentDirs(ctx)
				return palDirsMsg{dirs}
			}))
	}
	return tea.Batch(cmds...)
}

func (m Model) paletteWidth() int { return max(36, min(76, m.w-8)) }

// Match classes of fuzzy, best first. A prefix of the text or of one of its
// words is a clear hit; the query as a run inside a word is good; pieces
// that each start at a word start (like "ups" for "update channel set") are
// acceptable. Letters scattered through words never count.
const (
	matchPieces   = 1
	matchInside   = 2
	matchWordHead = 3
	matchPrefix   = 4
)

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// fuzzy scores how well query matches text and says how (matchPrefix ...
// matchPieces); ok is false when it does not match at all. The score sorts
// by class first, then prefers earlier and shorter matches.
func fuzzy(query, text string) (score int, ok bool) {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return 1, true
	}
	t := []rune(strings.ToLower(text))
	class, at := fuzzyClass(q, t)
	if class == 0 {
		return 0, false
	}
	return class*1000 - min(at, 200) - min(len(t), 200)/10, true
}

// fuzzyClass is the best match class of q in t and where it starts.
func fuzzyClass(q, t []rune) (class, at int) {
	head := func(i int) bool { return i == 0 || !isWordRune(t[i-1]) }
	n := len(q)
	for i := 0; i+n <= len(t); i++ {
		if string(t[i:i+n]) != string(q) {
			continue
		}
		switch {
		case i == 0:
			return matchPrefix, 0
		case head(i):
			if class < matchWordHead {
				class, at = matchWordHead, i
			}
		case class < matchInside:
			class, at = matchInside, i
		}
	}
	if class > 0 {
		return class, at
	}
	if start, ok := piecesFrom(q, t, 0, head); ok {
		return matchPieces, start
	}
	return 0, 0
}

// piecesFrom matches q as consecutive pieces of t, each piece beginning at a
// word start and continuing letter by letter, searching from position from.
// It returns where the first piece begins.
func piecesFrom(q, t []rune, from int, head func(int) bool) (int, bool) {
	if len(q) == 0 {
		return from, true
	}
	for i := from; i < len(t); i++ {
		if !head(i) || t[i] != q[0] {
			continue
		}
		// Take the longest run that agrees, then try shorter ones, so that
		// "ups" can split as "up" + "s" when "ups" does not run on.
		run := 0
		for run < len(q) && i+run < len(t) && t[i+run] == q[run] {
			run++
		}
		for k := run; k >= 1; k-- {
			if _, ok := piecesFrom(q[k:], t, i+k, head); ok {
				return i, true
			}
		}
	}
	return 0, false
}

// palIsRef says whether the palette input names one object to open. Paths
// and "inbox" are left out: in the palette "robin/ghost" is more likely a
// folder or project than a file, and "inbox" the mode.
func palIsRef(input string) bool {
	if !isRef(input) {
		return false
	}
	ref, _ := links.ParseRef(input)
	return ref.Kind != links.KindPath && ref.Kind != links.KindInbox
}

// palAll is every candidate for the current input, before filtering.
func (m Model) palAll(query string) []palItem {
	var items []palItem
	q := strings.TrimSpace(query)
	if q != "" && palIsRef(q) {
		refs := []string{clean(q, 80)}
		if t, ok := links.ParseNumber(q); ok && t.Kind == "" {
			// #217 names no kind: one entry for each kind it can mean, in the order
			// the web search shows them (links.HashKinds).
			refs = refs[:0]
			for _, r := range t.Refs() {
				refs = append(refs, links.Short(r))
			}
		}
		for i, ref := range refs {
			items = append(items, palItem{id: "jump:ref:" + ref, group: palJump, title: fmt.Sprintf(m.lang.t("pal.open"), ref), hint: "ctx open " + ref, score: 1000 - i,
				run: func(m Model) (Model, tea.Cmd) { m.toHome(); cmd := m.openRef(ref); return m, cmd }})
		}
	}
	for _, p := range m.pal.projects {
		id := p.ID
		items = append(items, palItem{id: "jump:project:" + id, group: palJump, title: m.lang.t("pal.project") + " " + projectShort(id), keywords: id + " " + p.Org, hint: clean(p.Org, 30),
			run: func(m Model) (Model, tea.Cmd) { return m.jumpProject(id) }})
	}
	for _, d := range m.pal.dirs {
		dir := d
		items = append(items, palItem{id: "jump:dir:" + dir, group: palJump, title: m.lang.t("folder") + " " + dir, hint: "cd … && ctx",
			run: func(m Model) (Model, tea.Cmd) {
				m.dirs.err = nil
				next, cmd := m.changeDir(dir)
				return next.(Model), cmd
			}})
	}
	items = append(items, m.palActions()...)
	if q != "" && !palIsRef(q) {
		line := clean(q, 200)
		items = append(items, palItem{id: "search", group: palSearch, title: fmt.Sprintf(m.lang.t("pal.searchfor"), line), hint: "ctx search", score: -1,
			run: func(m Model) (Model, tea.Cmd) { m.toHome(); cmd := m.runSearch(line); return m, cmd }})
	}
	return items
}

// palActions are the actions that make sense right now.
func (m Model) palActions() []palItem {
	other := "mode.inbox"
	if m.mode == modeInbox {
		other = "mode.project"
	}
	next := "dev"
	if m.local.Channel == "dev" {
		next = "stable"
	}
	acts := []palItem{
		{id: "act:mode", title: fmt.Sprintf(m.lang.t("pal.mode"), m.lang.t(other)), keywords: "modus mode projekt project inbox", hint: "shift+tab",
			run: func(m Model) (Model, tea.Cmd) { m.toHome(); m.setMode(1 - m.mode); return m, nil }},
		{id: "act:search", title: m.lang.t("pal.focus"), keywords: "suche search find", hint: "ctrl+f",
			run: func(m Model) (Model, tea.Cmd) { return m.focusSearch() }},
		{id: "act:settings", title: m.lang.t("settings"), keywords: "settings einstellungen config konto account server", hint: "ctrl+s",
			run: func(m Model) (Model, tea.Cmd) { m.go_(ScreenSettings); m.search.Blur(); return m, m.enterSettings() }},
		{id: "act:channel", title: fmt.Sprintf(m.lang.t("pal.channel"), next), keywords: "kanal channel update stable dev", hint: "ctx config set channel " + next,
			run: func(m Model) (Model, tea.Cmd) { return m.palSetChannel(next) }},
		{id: "act:doctor", title: m.lang.t("pal.doctor"), keywords: "doctor doktor harness prüfen check", hint: "ctx doctor",
			run: func(m Model) (Model, tea.Cmd) {
				m, cmd := m.toSettingsRow("harness")
				return m, tea.Batch(cmd, m.runDoctor(false))
			}},
		{id: "act:fix", title: m.lang.t("pal.fix"), keywords: "doctor doktor fix reparieren repair harness", hint: "ctx doctor --fix",
			run: func(m Model) (Model, tea.Cmd) {
				m, cmd := m.toSettingsRow("harness")
				return m, tea.Batch(cmd, m.runDoctor(true))
			}},
		{id: "act:collector", title: m.lang.t("pal.collector"), keywords: "collector watch restart neu starten", hint: "ctx install watch --restart",
			run: func(m Model) (Model, tea.Cmd) {
				m, cmd := m.toSettingsRow("collector")
				next, c2 := m.activateSetting()
				return next.(Model), tea.Batch(cmd, c2)
			}},
		{id: "act:dirs", title: m.lang.t("btn.dir"), keywords: "ordner folder cd", hint: "cd … && ctx",
			run: func(m Model) (Model, tea.Cmd) { cmd := m.openDirs(); return m, cmd }},
	}
	if m.local.Configured {
		acts = append(acts, palItem{id: "act:projects", title: m.lang.t("btn.projects"), keywords: "projekte projects liste", hint: "ctx project list",
			run: func(m Model) (Model, tea.Cmd) { cmd := m.openProjects(); return m, cmd }})
	}
	if m.canClaim() {
		acts = append(acts, palItem{id: "act:claim", title: m.lang.t("btn.claim"), keywords: "projekt anlegen create claim", hint: "ctx project claim",
			run: func(m Model) (Model, tea.Cmd) { m.toHome(); cmd := m.activateButton(btnClaim); return m, cmd }})
	}
	if s, ok := m.local.DefaultServer(); ok && s.Token != TokenNone {
		acts = append(acts, palItem{id: "act:logout", title: m.lang.t("pal.logout"), keywords: "abmelden logout sign out", hint: "ctx logout",
			run: func(m Model) (Model, tea.Cmd) {
				m, cmd := m.toSettingsRow("server")
				m.settings.confirm = true
				return m, cmd
			}})
	}
	if u := m.contextURL(); u != "" {
		acts = append(acts,
			palItem{id: "act:browser", title: m.lang.t("pal.browser"), keywords: "browser öffnen open web", hint: "enter",
				run: func(m Model) (Model, tea.Cmd) { return m, m.openURL(u) }},
			palItem{id: "act:copy", title: m.lang.t("key.copy"), keywords: "link kopieren copy url", hint: "c",
				run: func(m Model) (Model, tea.Cmd) {
					return m, tea.Batch(tea.SetClipboard(u), m.setFlash(m.lang.t("copied"), false))
				}})
	}
	for i := range acts {
		acts[i].group = palAction
	}
	return acts
}

// palVisible is what the palette lists for its input: filtered, ranked and
// grouped, the recently used first when nothing is typed.
func (m Model) palVisible() []palItem {
	query := m.pal.input.Value()
	all := m.palAll(query)
	byID := map[string]palItem{}
	for _, it := range all {
		byID[it.id] = it
	}
	recentRank := map[string]int{}
	for i, id := range m.pal.recent {
		recentRank[id] = len(m.pal.recent) - i
	}
	var out []palItem
	if strings.TrimSpace(query) == "" {
		seen := map[string]bool{}
		for _, id := range m.pal.recent {
			if it, ok := byID[id]; ok {
				it.group = palRecent
				out = append(out, it)
				seen[id] = true
			}
		}
		jumps := 0
		for _, it := range all {
			if seen[it.id] {
				continue
			}
			// Without input only a few recent folders, no project list.
			if it.group == palJump {
				if strings.HasPrefix(it.id, "jump:project:") || jumps >= palEmptyJumps {
					continue
				}
				jumps++
			}
			out = append(out, it)
		}
		return out
	}
	for _, it := range all {
		if it.group == palSearch || strings.HasPrefix(it.id, "jump:ref:") {
			out = append(out, it)
			continue
		}
		s, ok := fuzzy(query, it.title+" "+it.keywords)
		if !ok {
			continue
		}
		it.score = s + 3*recentRank[it.id] // a small nudge, never a class jump
		out = append(out, it)
	}
	// A clear hit (prefix or word start) pushes the piecewise ones out.
	clear := false
	for _, it := range out {
		if it.group != palSearch && it.score >= matchWordHead*1000-400 {
			clear = true
		}
	}
	if clear {
		kept := out[:0]
		for _, it := range out {
			if it.group == palSearch || it.score >= matchInside*1000-400 {
				kept = append(kept, it)
			}
		}
		out = kept
	}
	// Groups come in the order of their best hit; the search stays last.
	groupBest := map[palGroup]int{}
	for _, it := range out {
		groupBest[it.group] = max(groupBest[it.group], it.score)
	}
	groupBest[palSearch] = -1 << 30
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := out[i].group, out[j].group
		if gi != gj {
			if groupBest[gi] != groupBest[gj] {
				return groupBest[gi] > groupBest[gj]
			}
			return gi < gj
		}
		return out[i].score > out[j].score
	})
	// At most palGroupMax per group, so one long list does not hide the rest.
	var capped []palItem
	count := map[palGroup]int{}
	for _, it := range out {
		if count[it.group] < palGroupMax {
			capped = append(capped, it)
			count[it.group]++
		}
	}
	return capped
}

func (m Model) updatePalette(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case palProjectsMsg:
		m.pal.projects = nil
		for _, p := range msg.list {
			m.pal.projects = append(m.pal.projects, ProjectInfo{ID: clean(p.ID, 200), Org: clean(p.Org, 40), Named: p.Named})
		}
		return m, nil
	case palDirsMsg:
		m.pal.dirs = nil
		for _, d := range msg.dirs {
			m.pal.dirs = append(m.pal.dirs, clean(d, 300))
		}
		return m, nil
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.pal.input, cmd = m.pal.input.Update(msg)
		m.pal.cursor = 0
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+k":
			m.pal.open = false
			return m, nil
		case "up", "ctrl+p", "shift+tab":
			m.pal.cursor = max(0, m.pal.cursor-1)
			return m, nil
		case "down", "ctrl+n", "tab":
			m.pal.cursor = min(max(0, len(m.palVisible())-1), m.pal.cursor+1)
			return m, nil
		case "enter":
			return m.palRun(m.pal.cursor)
		}
		var cmd tea.Cmd
		m.pal.input, cmd = m.pal.input.Update(msg)
		m.pal.cursor = 0
		return m, cmd
	}
	return m, nil
}

// palRun closes the palette, remembers the item and runs it.
func (m Model) palRun(i int) (tea.Model, tea.Cmd) {
	items := m.palVisible()
	if i >= len(items) {
		return m, nil
	}
	it := items[i]
	m.pal.open = false
	if it.id != "search" && !strings.HasPrefix(it.id, "jump:ref:") {
		rec := []string{it.id}
		for _, id := range m.pal.recent {
			if id != it.id && len(rec) < palRecentMax {
				rec = append(rec, id)
			}
		}
		m.pal.recent = rec
	}
	next, cmd := it.run(m)
	return next, cmd
}

// ---------------------------------------------------------------- helpers

// toHome leaves whatever screen is open for the home.
func (m *Model) toHome() {
	if m.screen != ScreenHome {
		m.back = nil
		m.screen = ScreenHome
	}
	m.flash = ""
}

// focusSearch is ctrl+f: the search line of the home, ready for typing.
// Forms keep their own fields.
func (m Model) focusSearch() (Model, tea.Cmd) {
	switch m.screen {
	case ScreenLogin, ScreenClaim, ScreenDirs:
		return m, nil
	}
	m.toHome()
	m.splash = false
	return m, m.search.Focus()
}

// toSettingsRow opens the settings with the row of kind selected.
func (m Model) toSettingsRow(kind string) (Model, tea.Cmd) {
	m.go_(ScreenSettings)
	m.search.Blur()
	cmd := m.enterSettings()
	for i, r := range m.settingsRows() {
		if r.kind == kind {
			m.settings.cursor = i
			break
		}
	}
	return m, cmd
}

func (m Model) palSetChannel(next string) (Model, tea.Cmd) {
	if err := m.be.SetChannel(next); err != nil {
		return m, m.setFlash(errText(err), true)
	}
	m.local = m.be.Local()
	return m, m.setFlash(m.lang.t("channel")+": "+next, false)
}

// jumpProject shows the project list with this project selected.
func (m Model) jumpProject(id string) (Model, tea.Cmd) {
	cmd := m.openProjects()
	m.projects.want = id
	return m, cmd
}

// contextURL is the web address of what is in front of the person: the open
// card, the selected row with a card of its own, the selected project, or the
// folder's project.
func (m Model) contextURL() string {
	var u string
	switch m.screen {
	case ScreenCard:
		u = m.card.URL
	case ScreenResults:
		if m.resultsCursor < len(m.results.items) && m.results.items[m.resultsCursor].Card != nil {
			u = m.results.items[m.resultsCursor].Card.URL
		}
	case ScreenProjects:
		if p, ok := m.selectedProject(); ok {
			u = m.projectURL(p.ID)
		}
	case ScreenHome:
		if items := m.currentList(); m.homeZone() == zoneList && m.cursor < len(items) && items[m.cursor].Card != nil {
			u = items[m.cursor].Card.URL
		} else if m.local.Project != "" && !m.noProject() && m.server.URL != "" {
			u = m.projectURL(m.local.Project)
		}
	}
	if !webURL(u) {
		return ""
	}
	return u
}

// ---------------------------------------------------------------- view

// paletteBlock is the modal itself.
func (m Model) paletteBlock() block {
	width := m.paletteWidth()
	inner := width - 4
	var b block
	b.add(m.pal.input.View())
	b.add(m.th.Rule.Render(strings.Repeat("─", inner)))
	items := m.palVisible()
	maxRows := max(4, m.h-12)
	// Keep the cursor in view: drop rows from the top when needed.
	type row struct {
		text string
		idx  int // item index, -1 for a group heading
	}
	var rows []row
	var last palGroup = -1
	for i, it := range items {
		if it.group != last {
			rows = append(rows, row{m.th.Dim.Render(strings.ToUpper(m.lang.t(palGroupKeys[it.group]))), -1})
			last = it.group
		}
		hint := clean(it.hint, 40)
		title := clean(it.title, 200)
		titleW := inner - 2 - lipgloss.Width(hint) - 2
		t := ansi.Truncate(title, max(8, titleW), "…")
		pad := max(1, inner-2-lipgloss.Width(t)-lipgloss.Width(hint))
		line := "  " + m.th.Text.Render(t) + strings.Repeat(" ", pad) + m.th.Subtle.Render(hint)
		if i == m.pal.cursor {
			line = m.th.Key.Render("▌ ") + m.th.Selected.Render(t+strings.Repeat(" ", pad)) + m.th.Dim.Render(hint)
		}
		rows = append(rows, row{line, i})
	}
	start := 0
	for i, r := range rows {
		if r.idx == m.pal.cursor && i >= maxRows {
			start = i - maxRows + 1
		}
	}
	if len(rows) == 0 {
		b.add(m.th.Subtle.Render(m.lang.t("nothing")))
	}
	for i := start; i < len(rows) && i < start+maxRows; i++ {
		if rows[i].idx >= 0 {
			b.addHit(rows[i].text, action{kind: "pal", a: rows[i].idx})
		} else {
			b.add(rows[i].text)
		}
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(width)
	if !m.th.Plain {
		box = box.BorderForeground(m.th.Accent)
	}
	rendered := box.Render(b.String())
	var out block
	out.add(rendered)
	// The box adds one border row and two columns (border, padding) in front.
	for _, h := range b.hits {
		h.row++
		h.x0 += 2
		h.x1 = 2 + inner
		out.hits = append(out.hits, h)
	}
	return out
}

// overlayPalette draws the palette over the screen. The screen behind it is
// dimmed where colours exist; without colours the frame alone sets it apart.
// Clicks only reach the palette.
func (m Model) overlayPalette(bg block) block {
	pal := m.paletteBlock()
	x := max(0, (m.w-pal.width())/2)
	y := max(1, min(4, m.h/8))
	lines := make([]string, max(len(bg.lines), y+len(pal.lines)))
	copy(lines, bg.lines)
	dim := func(s string) string {
		s = ansi.Strip(s)
		if m.th.Plain || s == "" {
			return s
		}
		return m.th.Subtle.Render(s)
	}
	for i := range lines {
		plain := ansi.Strip(lines[i])
		r := i - y
		if r < 0 || r >= len(pal.lines) {
			lines[i] = dim(plain)
			continue
		}
		plain += strings.Repeat(" ", max(0, x+pal.width()-ansi.StringWidth(plain)))
		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+ansi.StringWidth(pal.lines[r]), "")
		lines[i] = dim(left) + pal.lines[r] + dim(right)
	}
	out := block{lines: lines}
	for _, h := range pal.hits {
		h.row += y
		h.x0 += x
		h.x1 += x
		out.hits = append(out.hits, h)
	}
	return out
}
