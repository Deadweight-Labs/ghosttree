package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// projectState is what the TUI knows about the project of its folder.
type projectState struct {
	checked bool
	known   bool
	err     error
}

// ClaimError is a refused "create project": Code is the server's code
// (not_org_owner, not_org_member, project_claimed, org_not_found, no_org).
type ClaimError struct{ Code string }

func (e *ClaimError) Error() string { return "claim refused: " + e.Code }

type (
	projectCheckMsg struct {
		project string
		known   bool
		err     error
	}
	projectsMsg struct {
		list []ProjectInfo
		err  error
	}
	previewMsg struct {
		project  string
		requests []Item
		tasks    []Item
		err      error
	}
	claimPlanMsg struct {
		plan ClaimPlan
		err  error
	}
	claimDoneMsg struct {
		p   ProjectInfo
		err error
	}
	recentDirsMsg struct {
		dirs []string
		err  error
	}
)

// The buttons of the "no project" state.
const (
	btnClaim = iota
	btnDir
	btnProjects
	btnCount
)

// noProject says whether the home shows the "no project" state.
func (m Model) noProject() bool {
	if !m.local.Configured {
		return false
	}
	if m.local.Project == "" {
		return true
	}
	return m.proj.checked && !m.proj.known && m.proj.err == nil
}

// canClaim says whether the folder could become a project: a git remote the
// server does not know yet.
func (m Model) canClaim() bool {
	return m.local.Remote != "" && m.proj.checked && !m.proj.known
}

func (m Model) checkProject() tea.Cmd {
	project := m.local.Project
	if project == "" || !m.local.Configured {
		return nil
	}
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		known, err := m.be.CheckProject(ctx, project)
		return projectCheckMsg{project: project, known: known, err: err}
	})
}

// problemText turns a load error into the words the screen shows.
func (m Model) problemText(err error) string {
	var pe *ProblemError
	if errors.As(err, &pe) {
		switch pe.Kind {
		case ProblemNoProject:
			return m.lang.t("problem.noproject")
		case ProblemForbidden:
			return m.lang.t("problem.forbidden")
		case ProblemOffline:
			return m.lang.t("problem.offline")
		case ProblemSignedOut:
			return m.lang.t("problem.signedout")
		}
		if pe.Detail != "" {
			return m.lang.t("problem.other") + " (" + clean(pe.Detail, 80) + ")"
		}
		return m.lang.t("problem.other")
	}
	var ce *ClaimError
	if errors.As(err, &ce) {
		if t := m.lang.t("claim." + ce.Code); t != "claim."+ce.Code {
			return t
		}
		return m.lang.t("problem.other") + " (" + clean(ce.Code, 40) + ")"
	}
	return errText(err)
}

// folderReason is why the folder has no project.
func (m Model) folderReason() string {
	switch {
	case m.local.Project != "":
		return fmt.Sprintf(m.lang.t("project.unknown"), clean(projectShort(m.local.Project), 80))
	case !m.local.Git:
		return m.lang.t("no.git")
	default:
		return m.lang.t("no.remote")
	}
}

func (m Model) buttonEnabled(i int) bool {
	return i != btnClaim || m.canClaim()
}

// buttonEquivalent is the command line behind a button.
func (m Model) buttonEquivalent(i int) string {
	switch i {
	case btnClaim:
		return "ctx project claim " + clean(m.local.Remote, 120)
	case btnDir:
		return "cd <" + strings.ToLower(m.lang.t("folder")) + "> && ctx"
	case btnProjects:
		return "ctx project list"
	}
	return ""
}

// projectPanel is the "no project" state with its three actions, or an empty
// block when the folder has a project.
func (m Model) projectPanel(width int) block {
	var b block
	if !m.noProject() {
		return b
	}
	b.add(m.th.Title.Render(m.lang.t("no.project")))
	b.add(m.th.Dim.Render(ansi.Truncate(clean(m.local.Dir, 300), width, "…")))
	b.add(m.th.Dim.Render(ansi.Truncate(m.folderReason(), width, "…")))
	b.blank(1)
	var parts []string
	var hits []hit
	x := 0
	labels := [btnCount]string{m.lang.t("btn.claim"), m.lang.t("btn.dir"), m.lang.t("btn.projects")}
	for i := 0; i < btnCount; i++ {
		var s string
		switch {
		case !m.buttonEnabled(i):
			continue
		case m.button == i:
			s = m.th.Selected.Render(" " + labels[i] + " ")
		default:
			s = m.th.Ref.Render(" " + labels[i] + " ")
		}
		w := lipgloss.Width(s)
		hits = append(hits, hit{row: len(b.lines), x0: x, x1: x + w, act: action{kind: "button", a: i}})
		parts = append(parts, s)
		x += w + 3
	}
	row := len(b.lines)
	b.add(strings.Join(parts, "   "))
	for _, h := range hits {
		h.row = row
		b.hits = append(b.hits, h)
	}
	b.add(m.th.Subtle.Render(ansi.Truncate(m.buttonEquivalent(m.button), width, "…")))
	b.blank(1)
	return b
}

// moveButton moves the focus by d buttons, skipping the missing ones; d=0
// only leaves a missing button.
func (m *Model) moveButton(d int) {
	if d == 0 {
		if m.buttonEnabled(m.button) {
			return
		}
		d = 1
	}
	for i := 0; i < btnCount; i++ {
		m.button = (m.button + d + btnCount) % btnCount
		if m.buttonEnabled(m.button) {
			return
		}
	}
}

func (m *Model) activateButton(i int) tea.Cmd {
	switch i {
	case btnClaim:
		if !m.canClaim() {
			return nil
		}
		m.go_(ScreenClaim)
		m.search.Blur()
		m.claim = claimState{loading: true}
		return call(quickTimeout, func(ctx context.Context) tea.Msg {
			plan, err := m.be.ClaimPlan(ctx)
			return claimPlanMsg{plan, err}
		})
	case btnDir:
		return m.openDirs()
	case btnProjects:
		return m.openProjects()
	}
	return nil
}

// ---------------------------------------------------------------- claim

type claimState struct {
	loading bool
	busy    bool
	plan    ClaimPlan
	org     int
	err     error
}

func (m Model) updateClaim(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case claimPlanMsg:
		m.claim.loading, m.claim.plan, m.claim.err = false, msg.plan, msg.err
		m.claim.org = 0
		for i, o := range msg.plan.Orgs {
			if o.Default {
				m.claim.org = i
			}
		}
	case claimDoneMsg:
		m.claim.busy = false
		if msg.err != nil {
			m.claim.err = msg.err
			return m, nil
		}
		m.back = nil
		m.screen = ScreenHome
		m.mode, m.cursor = modeProject, 0
		m.local = m.be.Local()
		cmd := m.setFlash(fmt.Sprintf(m.lang.t("claim.done"), clean(msg.p.ID, 100), clean(msg.p.Org, 40)), false)
		return m, tea.Batch(append(m.reloadHome(), cmd, m.search.Focus())...)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "n":
			return m, m.goBack()
		case "left", "h", "shift+tab":
			if n := len(m.claim.plan.Orgs); n > 0 {
				m.claim.org = (m.claim.org + n - 1) % n
			}
		case "right", "l", "tab":
			if n := len(m.claim.plan.Orgs); n > 0 {
				m.claim.org = (m.claim.org + 1) % n
			}
		case "enter", "y", "j":
			if m.claim.loading || m.claim.busy || m.claim.plan.Remote == "" || len(m.claim.plan.Orgs) == 0 {
				return m, nil
			}
			m.claim.busy, m.claim.err = true, nil
			remote, org := m.claim.plan.Remote, m.claim.plan.Orgs[m.claim.org].Slug
			return m, tea.Batch(m.ensureSpin(), call(quickTimeout, func(ctx context.Context) tea.Msg {
				p, err := m.be.Claim(ctx, remote, org)
				return claimDoneMsg{p, err}
			}))
		}
	}
	return m, nil
}

func (m Model) claimOrg() string {
	if len(m.claim.plan.Orgs) == 0 {
		return ""
	}
	return m.claim.plan.Orgs[m.claim.org].Slug
}

func (m Model) viewClaim() block {
	b := m.smallHeader(m.lang.t("btn.claim"))
	width := m.contentWidth()
	c := m.claim
	switch {
	case c.loading:
		b.add("  " + m.th.Subtle.Render(m.lang.t("loading")))
		return m.page(b, m.keys("esc", m.lang.t("key.back")))
	case c.err != nil && c.plan.Remote == "":
		b.add("  " + m.th.Caution.Render(m.problemText(c.err)))
		return m.page(b, m.keys("esc", m.lang.t("key.back")))
	}
	b.add("  " + m.th.Dim.Render(fmt.Sprintf("%-14s", m.lang.t("claim.project"))) + m.th.Key.Render(clean(c.plan.Remote, 120)))
	if len(c.plan.Orgs) == 0 {
		b.blank(1)
		b.add("  " + m.th.Caution.Render(m.lang.t("claim.no_org")))
		return m.page(b, m.keys("esc", m.lang.t("key.back")))
	}
	var orgs []string
	for i, o := range c.plan.Orgs {
		label := clean(o.Name, 40)
		if label == "" {
			label = clean(o.Slug, 40)
		}
		if i == c.org {
			orgs = append(orgs, m.th.Selected.Render(" "+label+" "))
		} else {
			orgs = append(orgs, m.th.Dim.Render(" "+label+" "))
		}
	}
	b.add("  " + m.th.Dim.Render(fmt.Sprintf("%-14s", m.lang.t("claim.org"))) + strings.Join(orgs, " "))
	if o := c.plan.Orgs[c.org]; o.Role != "" && o.Role != "owner" {
		b.add("  " + strings.Repeat(" ", 14) + m.th.Caution.Render(fmt.Sprintf(m.lang.t("claim.role"), clean(o.Role, 20))))
	}
	b.blank(1)
	b.add("  " + m.th.Title.Render(fmt.Sprintf(m.lang.t("claim.question"), clean(c.plan.Remote, 120), clean(m.claimOrg(), 40))))
	if c.busy {
		b.add("  " + m.th.Key.Render(spinner(m.spin)))
	}
	if c.err != nil {
		b.blank(1)
		for _, l := range strings.Split(lipgloss.Wrap(m.problemText(c.err), width, " "), "\n") {
			b.add("  " + m.th.Caution.Render(l))
		}
	}
	b.blank(1)
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", width)))
	b.add("  " + m.th.Subtle.Render("ctx project claim "+clean(c.plan.Remote, 120)+" --org "+clean(m.claimOrg(), 40)))
	pairs := []string{"enter", m.lang.t("key.create")}
	if len(c.plan.Orgs) > 1 {
		pairs = append(pairs, "←→", m.lang.t("claim.org"))
	}
	return m.page(b, m.keys(append(pairs, "esc", m.lang.t("key.back"))...))
}

// ---------------------------------------------------------------- folders

type dirsState struct {
	input   textinput.Model
	recent  listState
	cursor  int
	matches []string
	err     error
}

func (m *Model) openDirs() tea.Cmd {
	m.go_(ScreenDirs)
	m.search.Blur()
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = m.lang.t("dirs.placeholder")
	in.CharLimit = 1024
	in.SetWidth(min(70, max(20, m.w-10)))
	m.dirs = dirsState{input: in, recent: listState{loading: true}}
	m.applyInputStyles()
	return tea.Batch(m.dirs.input.Focus(), call(quickTimeout, func(ctx context.Context) tea.Msg {
		dirs, err := m.be.RecentDirs(ctx)
		return recentDirsMsg{dirs, err}
	}))
}

func (m Model) updateDirs(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case recentDirsMsg:
		items := make([]Item, 0, len(msg.dirs))
		for _, d := range msg.dirs {
			items = append(items, Item{Ref: clean(d, 300)})
		}
		m.dirs.recent = listState{items: items, err: msg.err, loaded: true}
		return m, nil
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.dirs.input, cmd = m.dirs.input.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, m.goBack()
		case "tab":
			return m.completeDir(), nil
		case "up", "ctrl+p":
			m.dirs.cursor = max(0, m.dirs.cursor-1)
			return m, nil
		case "down", "ctrl+n":
			m.dirs.cursor = min(max(0, len(m.dirs.recent.items)-1), m.dirs.cursor+1)
			return m, nil
		case "enter":
			dir := strings.TrimSpace(m.dirs.input.Value())
			if dir == "" && m.dirs.cursor < len(m.dirs.recent.items) {
				dir = m.dirs.recent.items[m.dirs.cursor].Ref
			}
			if dir == "" {
				return m, nil
			}
			return m.changeDir(dir)
		}
		var cmd tea.Cmd
		m.dirs.input, cmd = m.dirs.input.Update(msg)
		m.dirs.matches = nil
		return m, cmd
	}
	return m, nil
}

// completeDir extends the typed path to the longest common completion and
// keeps the candidates for display.
func (m Model) completeDir() Model {
	matches := m.be.CompleteDir(m.dirs.input.Value())
	for i := range matches {
		matches[i] = clean(matches[i], 300)
	}
	m.dirs.matches = matches
	if len(matches) == 0 {
		return m
	}
	common := matches[0]
	for _, s := range matches[1:] {
		for !strings.HasPrefix(s, common) {
			common = common[:len(common)-1]
		}
	}
	if len(common) > len(m.dirs.input.Value()) {
		m.dirs.input.SetValue(common)
		m.dirs.input.CursorEnd()
	}
	if len(matches) == 1 {
		m.dirs.matches = nil
	}
	return m
}

func (m Model) changeDir(dir string) (tea.Model, tea.Cmd) {
	if err := m.be.ChangeDir(dir); err != nil {
		m.dirs.err = err
		return m, nil
	}
	m.back = nil
	m.screen = ScreenHome
	m.mode, m.tab, m.cursor = modeProject, [2]int{}, 0
	m.local = m.be.Local()
	if s, ok := m.local.DefaultServer(); ok {
		s.Account, s.Checked, s.Reachable, s.Token = m.server.Account, m.server.Checked, m.server.Reachable, m.server.Token
		m.server = s
	}
	return m, tea.Batch(append(m.reloadHome(), m.search.Focus())...)
}

func (m Model) viewDirs() block {
	b := m.smallHeader(m.lang.t("btn.dir"))
	width := m.contentWidth()
	b.add("  " + m.dirs.input.View())
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", width)))
	if m.dirs.err != nil {
		b.add("  " + m.th.Caution.Render(errText(m.dirs.err)))
	}
	if len(m.dirs.matches) > 0 {
		for i, d := range m.dirs.matches {
			if i >= 6 {
				b.add("  " + m.th.Subtle.Render(fmt.Sprintf("+%d", len(m.dirs.matches)-i)))
				break
			}
			b.add("  " + m.th.Dim.Render(ansi.Truncate(d, width, "…")))
		}
	}
	b.blank(1)
	b.add("  " + m.th.Dim.Render(strings.ToUpper(m.lang.t("dirs.recent"))))
	rows := max(3, m.h-len(b.lines)-6)
	st := m.dirs.recent
	switch {
	case st.loading && !st.loaded:
		b.add("  " + m.th.Subtle.Render(m.lang.t("loading")))
	case st.err != nil:
		b.add("  " + m.th.Caution.Render(m.problemText(st.err)))
	case len(st.items) == 0:
		b.add("  " + m.th.Subtle.Render(m.lang.t("nothing")))
	default:
		start := 0
		if m.dirs.cursor >= rows {
			start = m.dirs.cursor - rows + 1
		}
		for i := start; i < len(st.items) && i < start+rows; i++ {
			line := "  " + m.th.Text.Render(ansi.Truncate(st.items[i].Ref, width, "…"))
			if i == m.dirs.cursor && strings.TrimSpace(m.dirs.input.Value()) == "" {
				line = m.th.Key.Render("▌ ") + m.th.Title.Render(ansi.Truncate(st.items[i].Ref, width, "…"))
			}
			b.addHit(line, action{kind: "dir", a: i})
		}
	}
	b.blank(1)
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", width)))
	target := strings.TrimSpace(m.dirs.input.Value())
	if target == "" && m.dirs.cursor < len(st.items) {
		target = st.items[m.dirs.cursor].Ref
	}
	if target == "" {
		target = "<" + strings.ToLower(m.lang.t("folder")) + ">"
	}
	b.add("  " + m.th.Subtle.Render(ansi.Truncate("cd "+clean(target, 300)+" && ctx", width, "…")))
	return m.page(b, m.keys("enter", m.lang.t("key.select"), "tab", m.lang.t("key.complete"), "↑↓", m.lang.t("dirs.recent"), "esc", m.lang.t("key.back")))
}

// ---------------------------------------------------------------- projects

type projectsState struct {
	list    listState
	cursor  int
	preview map[string]previewMsg
}

func (m *Model) openProjects() tea.Cmd {
	m.go_(ScreenProjects)
	m.search.Blur()
	m.projects = projectsState{list: listState{loading: true}, preview: map[string]previewMsg{}}
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		list, err := m.be.Projects(ctx)
		return projectsMsg{list, err}
	})
}

func (m Model) selectedProject() (ProjectInfo, bool) {
	items := m.projects.list.items
	if m.projects.cursor >= len(items) {
		return ProjectInfo{}, false
	}
	it := items[m.projects.cursor]
	return ProjectInfo{ID: it.Ref, Org: it.Meta}, true
}

func (m *Model) loadPreview() tea.Cmd {
	p, ok := m.selectedProject()
	if !ok {
		return nil
	}
	if _, seen := m.projects.preview[p.ID]; seen {
		return nil
	}
	m.projects.preview[p.ID] = previewMsg{project: p.ID}
	id := p.ID
	return call(quickTimeout, func(ctx context.Context) tea.Msg {
		reqs, tasks, err := m.be.ProjectPreview(ctx, id)
		return previewMsg{project: id, requests: reqs, tasks: tasks, err: err}
	})
}

func (m Model) projectURL(id string) string {
	return links.New(m.server.URL, id).Repo()
}

func (m Model) updateProjects(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case projectsMsg:
		items := make([]Item, 0, len(msg.list))
		for _, p := range msg.list {
			items = append(items, Item{Ref: clean(p.ID, 200), Meta: clean(p.Org, 40)})
		}
		m.projects.list = listState{items: items, err: msg.err, loaded: true}
		m.projects.cursor = 0
		for i, it := range items {
			if it.Ref == m.local.Project {
				m.projects.cursor = i
			}
		}
		return m, m.loadPreview()
	case previewMsg:
		if msg.err == nil {
			// nil marks "still loading"; an empty answer is an empty list.
			if msg.requests == nil {
				msg.requests = []Item{}
			}
			if msg.tasks == nil {
				msg.tasks = []Item{}
			}
		}
		m.projects.preview[msg.project] = previewMsg{project: msg.project, requests: msg.requests, tasks: msg.tasks, err: msg.err}
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "backspace", "q":
			return m, m.goBack()
		case "up", "k", "ctrl+p":
			m.projects.cursor = max(0, m.projects.cursor-1)
			return m, m.loadPreview()
		case "down", "j", "ctrl+n":
			m.projects.cursor = min(max(0, len(m.projects.list.items)-1), m.projects.cursor+1)
			return m, m.loadPreview()
		case "enter", "o":
			if p, ok := m.selectedProject(); ok {
				return m, m.openURL(m.projectURL(p.ID))
			}
		}
	}
	return m, nil
}

func (m Model) viewProjects() block {
	b := m.smallHeader(m.lang.t("projects"))
	width := m.contentWidth()
	st := m.projects.list
	listW := min(44, width/2)
	var left block
	switch {
	case st.loading && !st.loaded:
		left.add("  " + m.th.Subtle.Render(m.lang.t("loading")))
	case st.err != nil:
		left.add("  " + m.th.Caution.Render(m.problemText(st.err)))
	case len(st.items) == 0:
		left.add("  " + m.th.Subtle.Render(m.lang.t("nothing")))
	default:
		rows := max(3, m.h-10)
		start := 0
		if m.projects.cursor >= rows {
			start = m.projects.cursor - rows + 1
		}
		for i := start; i < len(st.items) && i < start+rows; i++ {
			it := st.items[i]
			name := ansi.Truncate(projectShort(it.Ref), listW-4, "…")
			line := "  " + m.th.Text.Render(name)
			if i == m.projects.cursor {
				line = m.th.Key.Render("▌ ") + m.th.Title.Render(name)
			}
			left.addHit(line, action{kind: "project", a: i})
		}
	}
	var right block
	if p, ok := m.selectedProject(); ok {
		right.add(m.th.Title.Render(clean(projectShort(p.ID), 80)) + "  " + m.th.Subtle.Render(clean(p.Org, 40)))
		pv := m.projects.preview[p.ID]
		previewW := max(20, width-listW-4)
		section := func(label string, items []Item) {
			right.blank(1)
			right.add(m.th.Dim.Render(label) + m.th.Subtle.Render(fmt.Sprintf("  %d", len(items))))
			for i, it := range items {
				if i >= 6 {
					right.add(m.th.Subtle.Render(fmt.Sprintf("+%d", len(items)-i)))
					break
				}
				right.add(m.th.Ref.Render(fmt.Sprintf("%-9s", clean(it.Ref, 12))) + " " + m.th.Text.Render(ansi.Truncate(clean(it.Title, 200), previewW-11, "…")))
			}
		}
		switch {
		case pv.err != nil:
			right.blank(1)
			right.add(m.th.Caution.Render(m.problemText(pv.err)))
		case pv.requests == nil && pv.tasks == nil:
			right.blank(1)
			right.add(m.th.Subtle.Render(m.lang.t("loading")))
		default:
			section(m.lang.t("requests"), pv.requests)
			section(m.lang.t("tasks"), pv.tasks)
		}
	}
	b.append(beside(2, left.indent(0), right))
	b.blank(1)
	b.add("  " + m.th.Rule.Render(strings.Repeat("─", width)))
	eq := "ctx project list"
	if p, ok := m.selectedProject(); ok {
		eq += "   ·   ctx search --project " + clean(p.ID, 120) + " is:open"
	}
	b.add("  " + m.th.Subtle.Render(ansi.Truncate(eq, width, "…")))
	return m.page(b, m.keys("enter", m.lang.t("key.browser"), "↑↓", m.lang.t("key.select"), "esc", m.lang.t("key.back")))
}
