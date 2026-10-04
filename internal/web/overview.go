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

	overviewMaxNext     = 6
	overviewMaxReview   = 3
	overviewMaxAgents   = 10
	overviewMaxRequests = 6
	overviewMaxLearned  = 5
	setupRefreshSeconds = 5
	// knowledgeMaxPages: so viele Seiten zu je 100 liest knowledgeKeep höchstens.
	knowledgeMaxPages = 10
)

// overviewNow ist die Uhr der Startseite; Tests setzen sie.
var overviewNow = time.Now

type overviewView struct {
	Setup     *setupView
	Guest     bool
	Next      []nextRow
	Agents    []agentRow
	Machines  []machineRow
	Requests  []requestRow
	Learned   []learnedRow
	Connect   string
	Example   string
	CSRFToken string
	NoContent bool
}

// nextRow ist eine Entscheidung, die jetzt ansteht: Titel, Detail, Link zur Stelle.
type nextRow struct {
	Title, Detail, Href, HrefLabel string
}

type agentRow struct {
	Name, Project, Activity, State, StateText, Age string
	// The rest feeds the agents page; the overview ignores it.
	ExternalID, RoomKey, Machine, Branch, Provider string
	// ProjectTitle is the full remote, for the title attribute.
	ProjectTitle string
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
func shortAge(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return msg("age.now")
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
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
	w.Header().Set("Cache-Control", "no-store")
	now := overviewNow().UTC()
	pa := a.access(r)
	who := a.shellBaseFor(r).who
	shell := a.shellFor(r, "overview")
	project := selectedProject(shell)
	view := overviewView{Guest: who.kind == viewerGuest, CSRFToken: csrfOf(r)}
	data := pageData{Title: msg("overview.title")}

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
	if view.Learned, err = a.overviewLearned(pa, project, now); err != nil {
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
	if view.Guest {
		view.NoContent = len(view.Requests) == 0 && len(view.Learned) == 0
		a.renderBrowser(w, r, "overview", data.withOverview(view))
		return
	}
	view.Agents = agents
	if tokens, terr := a.deviceTokens(r); terr == nil {
		view.Machines = ownMachines(tokens, now)
		view.Example = msg("setup.example")
	}
	view.Connect = "/ui/overview?connect=1"
	if view.Next, err = a.overviewNext(r, pa, who, project, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	return a.listAgents(r, pa, project, now, overviewMaxAgents)
}

// listAgents is overviewAgents with a limit, shared with the agents page.
// Besides the agents of every project the viewer may list, it always includes
// the agents the viewer started themselves: that needs no role and says nothing
// about anyone else.
func (a *app) listAgents(r *http.Request, pa *store.ProjectAccess, project string, now time.Time, limit int) ([]agentRow, error) {
	type ranked struct {
		row    agentRow
		signal time.Time
	}
	seen := map[string]bool{}
	var out []ranked
	add := func(remote string, peer store.CoordAgent) {
		if peer.ParentExternalID != "" || seen[peer.ExternalID] {
			return
		}
		seen[peer.ExternalID] = true
		last := parseTime(peer.LastSeenAt)
		var life time.Time
		activity := ""
		if p := peer.Presence; p != nil {
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
			activity = workLabel(p.WorkState.Value)
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
		signal := last
		if life.After(signal) {
			signal = life
		}
		out = append(out, ranked{agentRow{Name: peerName(peer), Project: coordShortRoomName(store.RoomProject, remote), ProjectTitle: remote,
			Activity: activity, State: state, StateText: stateLabel(state), Age: shortAge(now, signal),
			ExternalID: peer.ExternalID, RoomKey: store.RoomKeyForProject(remote), Machine: hostUnlessNamed(peerName(peer), agentMachine(peer.ExternalID)), Branch: peer.Branch, Provider: peer.Provider}, signal})
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
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
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
func peerName(peer store.CoordAgent) string {
	return agentLabel(peer.DisplayName, peer.Provider, peer.ExternalID, peer.Owner)
}

// agentLabel builds the readable name of an agent from what is known about it.
// owner is the account name of whoever runs it ("" when not known to the
// viewer). Session IDs are never a name, however they are spelled.
func agentLabel(displayName, provider, externalID, owner string) string {
	name := strings.TrimSpace(displayName)
	tool := providerLabel(provider, externalID)
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
func neutralAgentLabel(externalID string) string {
	if prefix, _, ok := strings.Cut(externalID, ":"); ok {
		switch strings.ToLower(prefix) {
		case "claude", "claude-code", "codex", "opencode", "cli":
			return providerLabel(prefix, externalID)
		}
	}
	return msg("agent.provider.unknown")
}

// providerLabel names the tool behind an agent.
func providerLabel(provider, externalID string) string {
	kind := strings.ToLower(provider)
	if prefix, _, ok := strings.Cut(externalID, ":"); ok && (kind == "" || kind == "ctx-cli" || kind == "unknown" || kind == "unidentified-harness") {
		kind = strings.ToLower(prefix)
	}
	switch kind {
	case "claude", "claude-code":
		return msg("agent.provider.claude")
	case "codex":
		return msg("agent.provider.codex")
	case "opencode":
		return msg("agent.provider.opencode")
	case "cli", "ctx-cli":
		return msg("agent.provider.cli")
	case "self-declared-subagent":
		return msg("agent.provider.subagent")
	}
	if kind == "" || kind == "unknown" || kind == "unidentified-harness" || kind == "derived" || looksLikeIdentifier(kind, "") {
		return msg("agent.provider.unknown")
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func stateLabel(state string) string {
	switch state {
	case "active":
		return msg("ov.state.active")
	case "idle":
		return msg("ov.state.idle")
	}
	return msg("ov.state.offline")
}

func workLabel(v string) string {
	switch v {
	case store.WorkWorking:
		return msg("ov.work.working")
	case store.WorkWaitingUser:
		return msg("ov.work.waiting_user")
	case store.WorkWaitingPeer:
		return msg("ov.work.waiting_peer")
	case store.WorkBlocked:
		return msg("ov.work.blocked")
	case store.WorkPaused:
		return msg("ov.work.paused")
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
		w.Restrict, w.Projects = true, pa.Projects()
		w.UnclaimedAuthor, w.UnclaimedAll = pa.Principal().Label, pa.IsAdmin()
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

func (a *app) overviewLearned(pa *store.ProjectAccess, project string, now time.Time) ([]learnedRow, error) {
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
		rows[i] = learnedRow{Title: k.Title, Age: shortAge(now, parseTime(k.CreatedAt))}
	}
	return rows, nil
}

// overviewNext sammelt, was den Betrachter jetzt braucht: Wissen zur Prüfung
// (nur wer prüfen darf) und an ihn gerichtete Anliegen in Räumen, die er lesen
// darf. Offene Geräte-Logins gehören nicht dazu (siehe setupView).
func (a *app) overviewNext(r *http.Request, pa *store.ProjectAccess, who viewer, project string, now time.Time) ([]nextRow, error) {
	var rows []nextRow
	if who.reviewer {
		w := a.knowledgeWindowFor(pa, project)
		w.Pending = true
		pa.Filtered()
		pending, err := a.knowledgeKeep(w, overviewMaxReview, func(k store.Knowledge) bool {
			return pa.CanSeeKnowledge(k) && pa.CheckKnowledge(k, store.ActVerify) == nil
		})
		if err != nil {
			return nil, err
		}
		for _, k := range pending {
			detail := shortAge(now, parseTime(k.CreatedAt))
			if k.Person != "" {
				detail = msg("ov.proposed_by", k.Person) + " · " + detail
			}
			rows = append(rows, nextRow{Title: k.Title, Detail: detail, Href: "/ui/review", HrefLabel: msg("ov.review")})
		}
	}
	mentions, err := a.overviewAttention(r, now)
	if err != nil {
		return nil, err
	}
	rows = append(rows, mentions...)
	if len(rows) > overviewMaxNext {
		rows = rows[:overviewMaxNext]
	}
	return rows, nil
}

// overviewAttention nimmt nur offene Anliegen, deren Empfänger der Betrachter
// ist. CoordAccess.Attention lässt schon alles weg, was er nicht lesen darf.
func (a *app) overviewAttention(r *http.Request, now time.Time) ([]nextRow, error) {
	access := a.browserCoord(r)
	items, err := access.Attention()
	if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
		// Ohne Raumzugang (zum Beispiel ein Konto ohne Agenten) gibt es nichts.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current := browserPrincipal(r)
	labels := coordIdentityLabels(current, nil)
	seenRooms := map[string]bool{}
	for _, item := range items {
		if !item.IsRecipient || item.State != store.AttentionOpen || item.HomeRoomKey == "" || seenRooms[item.HomeRoomKey] {
			continue
		}
		seenRooms[item.HomeRoomKey] = true
		if peers, err := access.Peers(item.HomeRoomKey, ""); err == nil {
			for id, label := range coordIdentityLabels(current, nil, peers...) {
				labels[id] = label
			}
		}
	}
	views, _ := buildCoordAttentionViews(items, "", labels)
	byID := make(map[int64]store.AttentionItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	var rows []nextRow
	for _, v := range views {
		item := byID[v.ID]
		room := coordShortRoomName(coordRoomKindOf(item.HomeRoomKey), coordRoomLabel(store.CoordRoom{Key: item.HomeRoomKey}, "", labels))
		title := oneLine(v.Preview, 110)
		parts := []string{coordSenderLabel(item, labels), room, shortAge(now, parseTime(item.CreatedAt))}
		rows = append(rows, nextRow{Title: title, Detail: strings.Join(parts, " · "), Href: v.URL, HrefLabel: msg("ov.open_room")})
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

// setupFor entscheidet, ob statt der Übersicht "Connect your first agent"
// erscheint (nie für Gäste, der Aufrufer prüft das): auf Wunsch (?connect=1)
// für jeden, sonst nur für Owner und Admins, die keine Maschine haben (oder
// deren gerade erst verbundene bestätigt wird) und in deren sichtbaren Projekten
// weder Agenten noch Requests noch Wissen vorkommen. Eine laufende Instanz zeigt immer
// die Übersicht. Der Befehl nennt nur die Adresse dieses Servers.
func (a *app) setupFor(r *http.Request, who viewer, hasContent bool, now time.Time) (*setupView, error) {
	forced := r.URL.Query().Get("connect") != ""
	if !forced && (hasContent || who.kind != viewerOwner) {
		return nil, nil
	}
	view := &setupView{State: "waiting", Title: msg("setup.title_first"), Command: a.loginCommand(r), Code: interactive(r)}
	if forced {
		view.Title = msg("setup.title")
	}
	tokens, err := a.deviceTokens(r)
	if err != nil {
		return nil, err
	}
	view.Example = msg("setup.example")
	a.guideSetup(r, view)
	if machines := ownMachines(tokens, now); len(machines) > 0 {
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
func ownMachines(tokens []store.TokenInfo, now time.Time) []machineRow {
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
		row := machineRow{Name: m.name, State: "active", StateText: msg("machine.connected"), Age: shortAge(now, m.at)}
		if now.Sub(m.at) > agentActiveWithin {
			row.State, row.StateText = "idle", msg("machine.last_seen", shortAgo(now, m.at))
		}
		rows = append(rows, row)
	}
	return rows
}

// shortAgo writes a time as "2 min ago" for a sentence.
func shortAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return msg("age.just_now")
	case d < time.Hour:
		return msg("age.min_ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return msg("age.hours_ago", int(d/time.Hour))
	}
	return msg("age.days_ago", int(d/(24*time.Hour)))
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
