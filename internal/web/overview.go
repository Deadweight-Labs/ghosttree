package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Startseite. Jede Liste und jede Zahl hier entsteht aus der Menge, die der
// Betrachter ohnehin sehen darf: Projekte aus seinen Rollen (ProjectAccess),
// Anfragen und Wissen mit der Projektgrenze in der Abfrage, Agenten nur aus
// Projekten, in denen er Agenten lesen darf, Anliegen über CoordAccess. Es gibt
// keine globalen Zähler, keine IDs aus fremden Reihen und keine Fenstergrenzen
// über verborgene Zeilen (#2447). Ein Gast bekommt nur Requests und Wissen; die
// Seite sieht für ihn gleich aus, ob es verborgene Daten gibt oder nicht.

const (
	// agentActiveWithin: so frisch muss das letzte Lebenszeichen sein.
	agentActiveWithin = 8 * time.Minute
	// agentOfflineAfter: danach gilt ein Agent als offline.
	agentOfflineAfter = 24 * time.Hour
	// learnedWindow ist das Fenster von "Learned this week".
	learnedWindow = 7 * 24 * time.Hour
	// connectedShownFor: so lange zeigt Getting started "connected", bis ein
	// Agent in einem Projekt erscheint.
	connectedShownFor = 15 * time.Minute

	overviewMaxNext = 6
	// overviewMaxProposals: how far the count of proposals waiting for review
	// is taken; beyond it the row says "100+".
	overviewMaxProposals = 100
	overviewMaxAgents    = 10
	overviewMaxRequests  = 6
	overviewMaxLearned   = 5
	setupRefreshSeconds  = 5
	// knowledgeMaxPages: so viele Seiten zu je 100 liest knowledgeKeep höchstens.
	knowledgeMaxPages = 10
)

// overviewNow ist die Uhr der Startseite; Tests setzen sie.
var overviewNow = time.Now

type overviewView struct {
	Setup     *setupView
	Guest     bool
	ForYou    *forYouView
	Run       *runView
	Week      *weekView
	Machines  []machineRow
	Requests  []requestRow
	Learned   []learnedRow
	Connect   string
	Example   string
	CSRFToken string
	NoContent bool
}

type agentRow struct {
	Name, Project, Activity, State, StateText, Age string
	// Flag is "waiting_user" or "stuck" when the agent's work state asks for
	// attention, from the same presence the room reads; "" otherwise.
	Flag string
	// Work is what the agent is working on: the title of its request, else its
	// own work note, else its branch. BareName is Name without the short tag.
	Work, BareName string
	// Sub is the second line of the row: project, with the agent's name before it
	// when Work is the headline.
	Sub string
	// The rest feeds the agents page; the overview ignores it.
	ExternalID, RoomKey, Machine, Branch, Provider string
	// WorkNote is the agent's own "working on" line, self-reported.
	WorkNote string
	// ProjectTitle is the full remote, for the title attribute.
	ProjectTitle string
	// Owner is the account name of whoever runs the agent, as far as the viewer
	// may know it. Presence is the one state the row shows, text and dot alike:
	// work, wait_you, wait_other, stuck, idle or off. PresenceText is its words.
	Owner, Presence, PresenceText string
	// RequestID and RequestRef name the request the agent works on, only where
	// the viewer may read it (AgentWorkFor).
	RequestRef string
}

type requestRow struct {
	ID, Title, Href string
	Done, Total     int
	Percent         int
	Progress        string
}

type learnedRow struct{ Title, Age string }

// setupView ist die Seite "Connect your first agent". Offene Geräte-Logins der
// Instanz erscheinen hier nie: wer den Code vom Terminal kennt, gibt ihn ein, und
// erst die Device-Seite nennt danach die Maschine.
type setupView struct {
	// State: waiting oder connected.
	State   string
	Command string
	Machine string
	Title   string
	// Code: die Sitzung darf Geräte freigeben, also zeigt die Seite das Code-Feld.
	Code bool
	// Example is the command that starts an agent on a connected machine.
	Example string
	// Guided: the install command with a pairing code, the same one an invited
	// person gets, instead of the bare login command. Needs an interactive
	// session and a server reachable over https (or loopback).
	Guided bool
	// PairCommand is that command once the account has a pairing code; empty
	// while there is none and the page offers to create one.
	PairCommand string
	// PairOpen links to the pairing page once a machine reported itself and
	// waits for approval there.
	PairOpen          bool
	Person, CSRFToken string
}

// agentState ordnet ein Lebenszeichen ein. Ein fehlendes Signal gilt als offline.
func agentState(now, signal time.Time) string {
	switch {
	case signal.IsZero():
		return "offline"
	case now.Sub(signal) <= agentActiveWithin:
		return "active"
	case now.Sub(signal) > agentOfflineAfter:
		return "offline"
	}
	return "idle"
}

// shortAge schreibt ein Alter knapp: now, 5m, 3h, 2d.
func shortAge(l lang, now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return l.msg("age.now")
	case d < time.Hour:
		return l.msg("age.short.min", int(d/time.Minute))
	case d < 48*time.Hour:
		return l.msg("age.short.hours", int(d/time.Hour))
	}
	return l.msg("age.short.days", int(d/(24*time.Hour)))
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// selectedProject ist das gewählte Projekt der Hülle, oder "" bei "All projects".
func selectedProject(sh shellView) string {
	if sh.AllSelected {
		return ""
	}
	for _, p := range sh.Projects {
		if p.Selected {
			return p.Remote
		}
	}
	return ""
}

func (a *app) overviewPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	now := overviewNow().UTC()
	pa := a.access(r)
	who := a.shellBaseFor(r).who
	shell := a.shellFor(r, "overview")
	project := selectedProject(shell)
	view := overviewView{Guest: who.kind == viewerGuest, CSRFToken: csrfOf(r)}
	data := pageData{Title: l.msg("overview.title")}

	var agents []agentRow
	var err error
	if !view.Guest {
		if agents, err = a.overviewAgents(r, pa, project, now); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if view.Requests, err = a.overviewRequests(pa, project); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view.Learned, err = a.overviewLearned(l, pa, project, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !view.Guest {
		hasContent := len(agents) > 0 || len(view.Requests) > 0 || len(view.Learned) > 0
		if !hasContent && r.URL.Query().Get("connect") == "" {
			if hasContent, err = a.overviewHasKnowledge(pa, project); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		setup, err := a.setupFor(r, who, hasContent, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if setup != nil {
			view.Setup = setup
			if r.URL.Query().Get("connect") == "" {
				data.Refresh = setupRefreshSeconds
			}
			a.renderBrowser(w, r, "overview", data.withOverview(overviewView{Setup: setup, CSRFToken: view.CSRFToken}))
			return
		}
	}
	if view.ForYou, err = a.homeForYou(r, pa, who, project, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view.Guest {
		view.NoContent = len(view.Requests) == 0 && len(view.Learned) == 0
		a.renderBrowser(w, r, "overview", data.withOverview(view))
		return
	}
	if view.Run, err = a.homeRunning(r, pa, project, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view.Week, err = a.homeWeek(l, pa, project, now, view.Requests); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tokens, terr := a.deviceTokens(r); terr == nil {
		view.Machines = ownMachines(l, tokens, now)
		view.Example = l.msg("setup.example")
	}
	view.Connect = "/ui/overview?connect=1"
	a.renderBrowser(w, r, "overview", data.withOverview(view))
}

func (d pageData) withOverview(v overviewView) pageData {
	d.Overview = v
	return d
}

// overviewAgents listet die Agenten der Projekte, in denen der Betrachter
// Agenten lesen darf. project != "" schränkt auf dieses Projekt ein.
// Die Peers kommen über dieselbe Zugangsprüfung wie /ui/coord.
func (a *app) overviewAgents(r *http.Request, pa *store.ProjectAccess, project string, now time.Time) ([]agentRow, error) {
	rows, err := a.listAgents(r, pa, project, now, overviewMaxAgents)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ExternalID
	}
	work, err := a.store.AgentWorkFor(pa, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Work = agentWorkTitle(work[rows[i].ExternalID], rows[i])
		rows[i].Sub = rows[i].Project
		if rows[i].Work != "" {
			rows[i].Sub = rows[i].BareName + agentLabelSep + rows[i].Project
		}
	}
	return rows, nil
}

// agentWorkTitle is what an agent line says it is doing: the title of the open
// request it works on (only where the viewer may read it, AgentWorkFor decides),
// else the agent's own work note, else its branch. Nothing else: an id is no
// answer to "what is it doing".
func agentWorkTitle(w store.AgentWork, row agentRow) string {
	switch {
	case w.RequestTitle != "":
		return oneLine(w.RequestTitle, 90)
	case row.WorkNote != "":
		return oneLine(row.WorkNote, 90)
	}
	return row.Branch
}

// listAgents is overviewAgents with a limit, shared with the agents page.
// Besides the agents of every project the viewer may list, it always includes
// the agents the viewer started themselves: that needs no role and says nothing
// about anyone else.
func (a *app) listAgents(r *http.Request, pa *store.ProjectAccess, project string, now time.Time, limit int) ([]agentRow, error) {
	l := a.langFor(r)
	type ranked struct {
		row    agentRow
		signal time.Time
	}
	seen := map[string]bool{}
	var out []ranked
	// The presence of an agent says it waits for a person, not for whom. Only
	// the person asked is "waited on"; everyone else sees a neutral label.
	var asked map[string]bool
	if items, err := a.browserCoord(r).Attention(); err == nil {
		asked = askedViewer(items)
	}
	add := func(remote string, peer store.CoordAgent) {
		if peer.ParentExternalID != "" || seen[peer.ExternalID] {
			return
		}
		seen[peer.ExternalID] = true
		last := parseTime(peer.LastSeenAt)
		var life time.Time
		activity, flag, neutral := "", "", ""
		workState, workAt := "", time.Time{}
		if p := peer.Presence; p != nil {
			workState, workAt = p.WorkState.Value, parseTime(p.WorkState.At)
			// Only evidence that the agent itself is there counts as a sign of
			// life: a poll, or tool calls seen in its transcript. A message
			// sent to or from it, or a wait derived from one, does not.
			if p.Reachability.Value == store.ReachConnected {
				life = parseTime(p.Reachability.At)
			}
			if p.WorkState.Value == store.WorkWorking || p.WorkState.Value == store.WorkPaused {
				if t := parseTime(p.WorkState.At); t.After(life) {
					life = t
				}
			}
			activity = workLabel(l, p.WorkState.Value)
			switch p.WorkState.Value {
			case store.WorkWaitingUser:
				if asked[peer.ExternalID] {
					flag = p.WorkState.Value
				} else {
					neutral = l.msg("ov.work.waiting_person")
					activity = neutral
				}
			case store.WorkStuck:
				flag = p.WorkState.Value
			}
		}
		state := agentState(now, life)
		if state != "active" {
			// Not alive right now: seen at all within a day is idle, else offline.
			activity = ""
			state = agentState(now, last)
			if state == "active" {
				state = "idle"
			}
		}
		if neutral != "" {
			activity = neutral
		}
		if flag != "" {
			// An agent that waits for a person, or hangs mid-turn, says so even
			// when its last sign of life is old: "Idle" would hide it.
			activity = workLabel(l, flag)
		}
		signal := last
		if life.After(signal) {
			signal = life
		}
		presence := agentPresence(state, flag, workState)
		out = append(out, ranked{agentRow{Owner: store.NormalizeAccountName(peer.Owner), Presence: presence, PresenceText: agentStateText(l, presence, workState, now, workAt), Name: peerName(l, peer), BareName: peerName(l, peer), Flag: flag, Project: coordShortRoomName(store.RoomProject, remote), ProjectTitle: remote,
			Activity: activity, State: state, StateText: stateLabel(l, state), Age: shortAge(l, now, signal),
			ExternalID: peer.ExternalID, RoomKey: store.RoomKeyForProject(remote), Machine: hostUnlessNamed(peerName(l, peer), agentMachine(peer.ExternalID)), Branch: peer.Branch, Provider: peer.Provider, WorkNote: peer.WorkNote}, signal})
	}
	access := a.browserCoord(r)
	remotes := slices.Sorted(slices.Values(pa.Projects()))
	for _, remote := range remotes {
		if project != "" && remote != project {
			continue
		}
		if !pa.Allow(remote, store.ResAgents, store.ActRead, store.Object{}) {
			continue
		}
		peers, err := access.ProjectAgents(remote)
		if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, peer := range peers {
			add(remote, peer)
		}
	}
	own, err := a.store.OwnAgents(browserPrincipal(r).ID)
	if err != nil {
		return nil, err
	}
	for _, peer := range own {
		remote := strings.TrimPrefix(peer.RoomKey, "project:")
		if project != "" && remote != project {
			continue
		}
		add(remote, peer)
	}
	order := map[string]int{"active": 0, "idle": 1, "offline": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if fi, fj := out[i].row.Flag != "", out[j].row.Flag != ""; fi != fj {
			return fi
		}
		if oi, oj := order[out[i].row.State], order[out[j].row.State]; oi != oj {
			return oi < oj
		}
		if !out[i].signal.Equal(out[j].signal) {
			return out[i].signal.After(out[j].signal)
		}
		return out[i].row.Name < out[j].row.Name
	})
	rows := make([]agentRow, 0, len(out))
	for _, o := range out {
		rows = append(rows, o.row)
	}
	tellAgentRowsApart(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// tellAgentRowsApart gives rows that would read the same a short tag from the
// agent's own id ("#4821"), so three processes of one person on one machine are
// not three identical lines. Unique names stay as they are.
func tellAgentRowsApart(rows []agentRow) {
	count := map[string]int{}
	for _, r := range rows {
		count[r.Name]++
	}
	used := map[string]bool{}
	for _, r := range rows {
		if count[r.Name] == 1 {
			used[r.Name] = true
		}
	}
	for i, r := range rows {
		if count[r.Name] < 2 {
			continue
		}
		tag := agentShortTag(r.ExternalID)
		name := r.Name + " #" + tag
		for n := 2; tag == "" || used[name]; n++ {
			tag = strconv.Itoa(i+1) + "." + strconv.Itoa(n)
			name = r.Name + " #" + tag
		}
		used[name] = true
		rows[i].Name = name
	}
}

// agentShortTag is the short session name of an agent id: the last part of it,
// at most six characters, safe to show.
func agentShortTag(externalID string) string {
	parts := strings.Split(externalID, ":")
	tail := parts[len(parts)-1]
	var b strings.Builder
	for _, r := range tail {
		if b.Len() >= 6 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hostUnlessNamed drops the host when the name already carries it.
func hostUnlessNamed(name, host string) string {
	if strings.Contains(name, host) {
		return ""
	}
	return host
}

// peerName is how a person reads an agent: "Claude · Robin · mainex" (tool,
// owner, machine). A name the person chose stays; an ID or the bare tool name
// is replaced. The raw ID goes to the title of the row.
func peerName(l lang, peer store.CoordAgent) string {
	// A peer that reported its machine has a unique name: Person · Machine · Branch.
	if peer.Label != "" && peer.Machine != "" {
		return peer.Label
	}
	return agentLabel(l, peer.DisplayName, peer.Provider, peer.ExternalID, peer.Owner)
}

// agentLabel builds the readable name of an agent from what is known about it.
// owner is the account name of whoever runs it ("" when not known to the
// viewer). Session IDs are never a name, however they are spelled.
func agentLabel(l lang, displayName, provider, externalID, owner string) string {
	name := strings.TrimSpace(displayName)
	tool := providerLabel(l, provider, externalID)
	if name != "" && !looksLikeIdentifier(name, externalID) && !strings.EqualFold(name, provider) && !strings.EqualFold(name, tool) {
		return name
	}
	parts := []string{tool}
	if owner = store.NormalizeAccountName(owner); owner != "" {
		parts = append(parts, owner)
	}
	if host := agentMachine(externalID); host != "" {
		parts = append(parts, host)
	}
	return strings.Join(parts, agentLabelSep)
}

// agentLabelSep separates the parts of an agent label.
const agentLabelSep = " \u00b7 "

var uuidLikeRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// looksLikeIdentifier says whether a display name is really a machine
// identifier: the agent ID itself, anything with the ID separators, a UUID, or
// a long run of characters with digits in it and no space.
func looksLikeIdentifier(name, externalID string) bool {
	if name == externalID || strings.ContainsAny(name, ":/") || uuidLikeRE.MatchString(name) {
		return true
	}
	return len(name) >= 20 && !strings.ContainsAny(name, " \t") && strings.ContainsAny(name, "0123456789")
}

// neutralAgentLabel names an agent by its ID alone, for viewers who may not
// learn more (a guest): the tool when the ID says it, else "Agent".
func neutralAgentLabel(l lang, externalID string) string {
	if prefix, _, ok := strings.Cut(externalID, ":"); ok {
		switch strings.ToLower(prefix) {
		case "claude", "claude-code", "codex", "opencode", "cli":
			return providerLabel(l, prefix, externalID)
		}
	}
	return l.msg("agent.provider.unknown")
}

// providerLabel names the tool behind an agent.
func providerLabel(l lang, provider, externalID string) string {
	kind := strings.ToLower(provider)
	if prefix, _, ok := strings.Cut(externalID, ":"); ok && (kind == "" || kind == "ctx-cli" || kind == "unknown" || kind == "unidentified-harness") {
		kind = strings.ToLower(prefix)
	}
	switch kind {
	case "claude", "claude-code":
		return l.msg("agent.provider.claude")
	case "codex":
		return l.msg("agent.provider.codex")
	case "opencode":
		return l.msg("agent.provider.opencode")
	case "cli", "ctx-cli":
		return l.msg("agent.provider.cli")
	case "self-declared-subagent":
		return l.msg("agent.provider.subagent")
	}
	if kind == "" || kind == "unknown" || kind == "unidentified-harness" || kind == "derived" || looksLikeIdentifier(kind, "") {
		return l.msg("agent.provider.unknown")
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// agentPresence is the one state a home row shows. The dot and the words both
// come from it, so they cannot disagree: a row that says "Waiting on you" never
// carries the green dot of "working".
func agentPresence(state, flag, workState string) string {
	switch {
	case flag == store.WorkWaitingUser:
		return "wait_you"
	case flag == store.WorkStuck:
		return "stuck"
	case workState == store.WorkWaitingUser && state != "offline":
		// Waiting on somebody else than the viewer: not "on you", not working.
		return "wait_other"
	case state == "active":
		switch workState {
		case store.WorkWaitingUser, store.WorkWaitingPeer, store.WorkBlocked:
			return "wait_other"
		}
		return "work"
	case state == "idle":
		return "idle"
	}
	return "off"
}

// agentStateText says the state in words; a hanging agent says for how long.
func agentStateText(l lang, presence, workState string, now, at time.Time) string {
	switch presence {
	case "work":
		return l.msg("ov.work.working")
	case "wait_you":
		return l.msg("ov.work.waiting_user")
	case "wait_other":
		switch workState {
		case store.WorkWaitingPeer:
			return l.msg("home.state.wait_agent")
		case store.WorkBlocked:
			return l.msg("ov.work.blocked")
		}
		return l.msg("home.state.wait_person")
	case "stuck":
		if at.IsZero() || now.Sub(at) < time.Minute {
			return l.msg("ov.work.stuck")
		}
		return l.msg("home.state.stuck_since", shortAge(l, now, at))
	case "idle":
		return l.msg("ov.state.idle")
	}
	return l.msg("ov.state.offline")
}

func stateLabel(l lang, state string) string {
	switch state {
	case "active":
		return l.msg("ov.state.active")
	case "idle":
		return l.msg("ov.state.idle")
	}
	return l.msg("ov.state.offline")
}

// askedViewer is the set of agents that have an open question, approval or
// handoff addressed to the viewer. "Waiting on you" is only true for those.
func askedViewer(items []store.AttentionItem) map[string]bool {
	asked := map[string]bool{}
	for _, item := range items {
		if item.IsRecipient && item.State == store.AttentionOpen && item.SenderID != "" {
			asked[item.SenderID] = true
		}
	}
	return asked
}

func workLabel(l lang, v string) string {
	switch v {
	case store.WorkWorking:
		return l.msg("ov.work.working")
	case store.WorkWaitingUser:
		return l.msg("ov.work.waiting_user")
	case store.WorkWaitingPeer:
		return l.msg("ov.work.waiting_peer")
	case store.WorkBlocked:
		return l.msg("ov.work.blocked")
	case store.WorkPaused:
		return l.msg("ov.work.paused")
	case store.WorkStuck:
		return l.msg("ov.work.stuck")
	}
	return ""
}

func (a *app) overviewRequests(pa *store.ProjectAccess, project string) ([]requestRow, error) {
	filter := requestdomain.SearchFilter{State: "open", Limit: 25, Scope: scope.Axes{Project: project}}
	page, err := a.store.SearchRequests(pa.RequestFilter(filter))
	if err != nil {
		return nil, err
	}
	pa.NoteRequestHits(page.Results)
	hits := page.Results
	if len(hits) > overviewMaxRequests {
		hits = hits[:overviewMaxRequests]
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.Request.ID
	}
	progress, err := a.store.CriteriaProgress(ids)
	if err != nil {
		return nil, err
	}
	rows := make([]requestRow, 0, len(hits))
	for _, h := range hits {
		p := progress[h.Request.ID]
		row := requestRow{ID: h.Request.HumanID(), Title: h.Request.Title, Done: p.Done, Total: p.Total, Progress: fmt.Sprintf("%d/%d", p.Done, p.Total),
			Href: "/ui/requests/" + strconv.FormatInt(h.Request.ID, 10)}
		if p.Total > 0 {
			row.Percent = p.Done * 100 / p.Total
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *app) knowledgeWindowFor(pa *store.ProjectAccess, project string) store.KnowledgeWindow {
	w := store.KnowledgeWindow{}
	switch {
	case project != "":
		w.Restrict, w.Projects = true, []string{project}
	case a.store.AccessEnforced():
		w = pa.RestrictKnowledgeWindow(w)
	}
	return w
}

// knowledgeKeep liest die Wissensfenster seitenweise, bis limit Einträge den
// feinen Test bestehen oder nichts mehr kommt, höchstens knowledgeMaxPages
// Seiten. Ein fester Ausschnitt vor dem Test ließe verborgene Einträge sichtbare
// verdrängen (#2447); die Obergrenze hält eine Seite mit vielen verborgenen
// Einträgen bezahlbar.
func (a *app) knowledgeKeep(w store.KnowledgeWindow, limit int, allow func(store.Knowledge) bool) ([]store.Knowledge, error) {
	const page = 100
	w.Limit = page
	var out []store.Knowledge
	for pages := 0; pages < knowledgeMaxPages; pages++ {
		w.Offset = pages * page
		rows, err := a.store.KnowledgeWindow(w)
		if err != nil {
			return nil, err
		}
		for _, k := range rows {
			if allow(k) {
				out = append(out, k)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		if len(rows) < page {
			return out, nil
		}
	}
	return out, nil
}

// overviewHasKnowledge: gibt es in den sichtbaren Projekten irgendein Wissen,
// gleich wie alt. Es entscheidet mit, ob die Instanz noch leer ist.
func (a *app) overviewHasKnowledge(pa *store.ProjectAccess, project string) (bool, error) {
	pa.Filtered()
	found, err := a.knowledgeKeep(a.knowledgeWindowFor(pa, project), 1, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k)
	})
	return len(found) > 0, err
}

func (a *app) overviewLearned(l lang, pa *store.ProjectAccess, project string, now time.Time) ([]learnedRow, error) {
	w := a.knowledgeWindowFor(pa, project)
	w.Since = now.Add(-learnedWindow).Format(time.RFC3339)
	pa.Filtered()
	entries, err := a.knowledgeKeep(w, overviewMaxLearned, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k)
	})
	if err != nil {
		return nil, err
	}
	rows := make([]learnedRow, len(entries))
	for i, k := range entries {
		rows[i] = learnedRow{Title: k.Title, Age: shortAge(l, now, parseTime(k.CreatedAt))}
	}
	return rows, nil
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// shortWords is oneLine, but it cuts at a word boundary: "Backfill mit 'import'
// od…" says less than "Backfill mit 'import'…". A text without a space in its
// last fifth is cut hard, as oneLine does.
func shortWords(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	cut := limit
	for i := limit; i > limit*4/5; i-- {
		if rs[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(rs[:cut]), " ,;:.") + "…"
}

// setupFor entscheidet, ob statt der Übersicht "Connect your first agent"
// erscheint (nie für Gäste, der Aufrufer prüft das): auf Wunsch (?connect=1)
// für jeden, sonst nur für Owner und Admins, die keine Maschine haben (oder
// deren gerade erst verbundene bestätigt wird) und in deren sichtbaren Projekten
// weder Agenten noch Requests noch Wissen vorkommen. Eine laufende Instanz zeigt immer
// die Übersicht. Der Befehl nennt nur die Adresse dieses Servers.
func (a *app) setupFor(r *http.Request, who viewer, hasContent bool, now time.Time) (*setupView, error) {
	l := a.langFor(r)
	forced := r.URL.Query().Get("connect") != ""
	if !forced && (hasContent || who.kind != viewerOwner) {
		return nil, nil
	}
	view := &setupView{State: "waiting", Title: l.msg("setup.title_first"), Command: a.loginCommand(r), Code: interactive(r)}
	if forced {
		view.Title = l.msg("setup.title")
	}
	tokens, err := a.deviceTokens(r)
	if err != nil {
		return nil, err
	}
	view.Example = l.msg("setup.example")
	a.guideSetup(r, view)
	if machines := ownMachines(l, tokens, now); len(machines) > 0 {
		// A machine is there, so the status says so, however long ago it signed in.
		if _, recent := recentMachine(tokens, now); recent || forced {
			view.State, view.Machine = "connected", machines[0].Name
			return view, nil
		}
	}
	if !forced && len(tokens) > 0 {
		// Eine Maschine ist seit längerem verbunden: normale Übersicht mit
		// leerer Agentenliste statt einer Seite, die nie verschwindet.
		return nil, nil
	}
	return view, nil
}

func (a *app) deviceTokens(r *http.Request) ([]store.TokenInfo, error) {
	tokens, err := a.store.ListTokens(browserPrincipal(r).Label)
	if err != nil {
		return nil, err
	}
	nowText := overviewNow().UTC().Format(time.RFC3339)
	var out []store.TokenInfo
	for _, t := range tokens {
		if t.Kind == "device" && t.RevokedAt == "" && (t.ExpiresAt == "" || t.ExpiresAt > nowText) {
			out = append(out, t)
		}
	}
	return out, nil
}

// machineRow is one of the viewer's own machines: connected while it was heard
// from recently, otherwise the time it was last seen.
type machineRow struct {
	Name, State, StateText, Age string
}

// ownMachines folds the viewer's device tokens to one row per machine, newest
// contact first. Only tokens of the viewer's own account are ever passed in.
func ownMachines(l lang, tokens []store.TokenInfo, now time.Time) []machineRow {
	type seen struct {
		name string
		at   time.Time
	}
	byName := map[string]seen{}
	for _, t := range tokens {
		name := strings.TrimSpace(t.Machine)
		if name == "" {
			continue
		}
		at := parseTime(t.LastUsedAt)
		if c := parseTime(t.CreatedAt); c.After(at) {
			at = c
		}
		if cur, ok := byName[name]; !ok || at.After(cur.at) {
			byName[name] = seen{name, at}
		}
	}
	list := make([]seen, 0, len(byName))
	for _, v := range byName {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].at.Equal(list[j].at) {
			return list[i].at.After(list[j].at)
		}
		return list[i].name < list[j].name
	})
	rows := make([]machineRow, 0, len(list))
	for _, m := range list {
		row := machineRow{Name: m.name, State: "active", StateText: l.msg("machine.connected"), Age: shortAge(l, now, m.at)}
		if now.Sub(m.at) > agentActiveWithin {
			row.State, row.StateText = "idle", l.msg("machine.last_seen", shortAgo(l, now, m.at))
		}
		rows = append(rows, row)
	}
	return rows
}

// shortAgo writes a time as "2 min ago" for a sentence.
func shortAgo(l lang, now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return l.msg("age.just_now")
	case d < time.Hour:
		return l.msg("age.min_ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return l.msg("age.hours_ago", int(d/time.Hour))
	}
	return l.msg("age.days_ago", int(d/(24*time.Hour)))
}

func recentMachine(tokens []store.TokenInfo, now time.Time) (string, bool) {
	var newest store.TokenInfo
	var at time.Time
	for _, t := range tokens {
		if c := parseTime(t.CreatedAt); c.After(at) {
			newest, at = t, c
		}
	}
	if at.IsZero() || now.Sub(at) > connectedShownFor {
		return "", false
	}
	return newest.Machine, true
}

// loginCommand ist der Befehl, den der Owner auf seiner Maschine ausführt.
func (a *app) loginCommand(r *http.Request) string {
	origin, err := a.requestOrigin(r)
	if err != nil {
		origin = "https://" + a.publicHost(r)
	}
	if u, perr := url.Parse(origin); perr != nil || u.Host == "" {
		origin = "https://" + a.publicHost(r)
	}
	return fmt.Sprintf("ctx login --server %s", origin)
}

// guideSetup gives an owner without a connected machine the same guided install
// command an invited person gets (REQ-438): the pairing code lives on the
// pairing page's session, so the page either shows the command of a waiting
// session, links to the page where the machine is approved, or offers to create
// the code. Only an interactive session can create or approve one; otherwise,
// and for a server not reachable over https, the login command stays.
func (a *app) guideSetup(r *http.Request, view *setupView) {
	if !view.Code {
		return
	}
	p := browserPrincipal(r)
	v := a.store.Join().View(p.ID)
	if v.State == store.JoinWaiting {
		view.PairCommand = a.joinCommand(r, v.Pair)
		if view.PairCommand == "" {
			return
		}
	} else if a.joinCommand(r, "XXXX-XXXX") == "" {
		return
	}
	view.Guided = true
	view.PairOpen = v.State == store.JoinClaimed || v.State == store.JoinApproved
	view.Person, view.CSRFToken = p.Label, csrfOf(r)
}
