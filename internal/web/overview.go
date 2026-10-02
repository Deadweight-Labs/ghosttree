package web

import (
	"fmt"
	"net/http"
	"net/url"
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
)

// overviewNow ist die Uhr der Startseite; Tests setzen sie.
var overviewNow = time.Now

type overviewView struct {
	Setup     *setupView
	Guest     bool
	Next      []nextRow
	Agents    []agentRow
	Requests  []requestRow
	Learned   []learnedRow
	Connect   string
	CSRFToken string
	NoContent bool
}

// nextRow ist eine Entscheidung, die jetzt ansteht. Device gesetzt: Zeile mit
// Code-Feld (Freigabe), sonst Link zur Stelle.
type nextRow struct {
	Title, Detail, Href, HrefLabel string
	Device                         bool
}

type agentRow struct {
	Name, Project, Activity, State, StateText, Age string
}

type requestRow struct {
	ID, Title, Href string
	Done, Total     int
	Percent         int
	Progress        string
}

type learnedRow struct{ Title, Age string }

type setupView struct {
	// State: waiting, wants (ein Gerät wartet auf Freigabe), connected.
	State           string
	Command         string
	Machine, Remote string
	Detail          string
	Title           string
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
	now := overviewNow().UTC()
	pa := a.access(r)
	who := a.viewerOf(r, pa)
	shell := a.shellFor(r, "overview")
	project := selectedProject(shell)
	view := overviewView{Guest: who.kind == viewerGuest, CSRFToken: csrfOf(r)}
	data := pageData{Title: msg("overview.title")}

	var agents []agentRow
	if !view.Guest {
		agents = a.overviewAgents(pa, project, now)
	}
	if setup := a.setupFor(r, who, agents, now); setup != nil {
		view.Setup = setup
		if r.URL.Query().Get("connect") == "" && setup.State != "wants" {
			data.Refresh = setupRefreshSeconds
		}
		a.renderBrowser(w, r, "overview", data.withOverview(view))
		return
	}

	var err error
	if view.Requests, err = a.overviewRequests(pa, project); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view.Learned, err = a.overviewLearned(pa, project, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view.Guest {
		view.NoContent = len(view.Requests) == 0 && len(view.Learned) == 0
		a.renderBrowser(w, r, "overview", data.withOverview(view))
		return
	}
	view.Agents = agents
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
func (a *app) overviewAgents(pa *store.ProjectAccess, project string, now time.Time) []agentRow {
	type ranked struct {
		row    agentRow
		signal time.Time
	}
	seen := map[string]bool{}
	var out []ranked
	for _, remote := range pa.Projects() {
		if project != "" && remote != project {
			continue
		}
		if !pa.Allow(remote, store.ResAgents, store.ActRead, store.Object{}) {
			continue
		}
		peers, err := a.store.CoordPeers(store.RoomKeyForProject(remote), "")
		if err != nil {
			continue
		}
		for _, peer := range peers {
			if peer.ParentExternalID != "" || seen[peer.ExternalID] {
				continue
			}
			seen[peer.ExternalID] = true
			signal := parseTime(peer.LastSeenAt)
			activity := ""
			if p := peer.Presence; p != nil {
				for _, at := range []string{p.Reachability.At, p.WorkState.At} {
					if t := parseTime(at); t.After(signal) {
						signal = t
					}
				}
				activity = workLabel(p.WorkState.Value)
			}
			state := agentState(now, signal)
			if state != "active" {
				activity = ""
			}
			name := strings.TrimSpace(peer.DisplayName)
			if name == "" {
				name = peer.Provider
			}
			out = append(out, ranked{agentRow{Name: name, Project: coordShortRoomName(store.RoomProject, remote),
				Activity: activity, State: state, StateText: stateLabel(state), Age: shortAge(now, signal)}, signal})
		}
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
		if len(rows) == overviewMaxAgents {
			break
		}
	}
	return rows
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

func (a *app) overviewLearned(pa *store.ProjectAccess, project string, now time.Time) ([]learnedRow, error) {
	w := a.knowledgeWindowFor(pa, project)
	w.Since = now.Add(-learnedWindow).Format(time.RFC3339)
	entries, err := a.store.KnowledgeWindow(w)
	if err != nil {
		return nil, err
	}
	pa.Filtered()
	entries = keep(entries, overviewMaxLearned, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k)
	})
	rows := make([]learnedRow, len(entries))
	for i, k := range entries {
		rows[i] = learnedRow{Title: k.Title, Age: shortAge(now, parseTime(k.CreatedAt))}
	}
	return rows, nil
}

// overviewNext sammelt, was den Betrachter jetzt braucht: Geräte-Freigaben
// (nur Owner), Wissen zur Prüfung (nur wer prüfen darf) und an ihn gerichtete
// Anliegen in Räumen, die er lesen darf.
func (a *app) overviewNext(r *http.Request, pa *store.ProjectAccess, who viewer, project string, now time.Time) ([]nextRow, error) {
	var rows []nextRow
	if a.ownerLike(r, who) && interactive(r) {
		for _, d := range a.store.Device().Pending() {
			rows = append(rows, nextRow{Device: true, Title: msg("ov.wants", d.Machine),
				Detail: strings.Join([]string{d.Remote, shortAge(now, d.StartedAt)}, " · ")})
		}
	}
	if who.reviewer {
		w := a.knowledgeWindowFor(pa, project)
		w.Pending = true
		pending, err := a.store.KnowledgeWindow(w)
		if err != nil {
			return nil, err
		}
		pa.Filtered()
		pending = keep(pending, overviewMaxReview, func(k store.Knowledge) bool {
			return pa.CanSeeKnowledge(k) && pa.CheckKnowledge(k, store.ActVerify) == nil
		})
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
	if err != nil {
		// Ohne Raumzugang (zum Beispiel ein Konto ohne Agenten) gibt es nichts.
		return nil, nil
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
// erscheint: für einen Owner, solange kein Agent sichtbar ist und keine
// Maschine gerade erst verbunden wurde; für jeden Nicht-Gast auf Wunsch
// (?connect=1). Der Befehl nennt nur die Adresse dieses Servers.
func (a *app) setupFor(r *http.Request, who viewer, agents []agentRow, now time.Time) *setupView {
	if who.kind == viewerGuest {
		return nil
	}
	forced := r.URL.Query().Get("connect") != ""
	if !forced && (!a.ownerLike(r, who) || len(agents) > 0) {
		return nil
	}
	view := &setupView{State: "waiting", Title: msg("setup.title_first"), Command: a.loginCommand(r)}
	if forced {
		view.Title = msg("setup.title")
	}
	if a.ownerLike(r, who) && interactive(r) {
		if pending := a.store.Device().Pending(); len(pending) > 0 {
			view.State, view.Machine, view.Remote = "wants", pending[0].Machine, pending[0].Remote
			view.Detail = strings.Join([]string{pending[0].Remote, shortAge(now, pending[0].StartedAt)}, " · ")
			return view
		}
	}
	if machine, ok := a.recentMachine(r, now); ok {
		view.State, view.Machine = "connected", machine
		return view
	}
	if !forced && len(agents) == 0 && a.hasMachine(r) {
		// Eine Maschine ist seit längerem verbunden: normale Übersicht mit
		// leerer Agentenliste statt einer Seite, die nie verschwindet.
		return nil
	}
	return view
}

func (a *app) deviceTokens(r *http.Request) []store.TokenInfo {
	tokens, err := a.store.ListTokens(browserPrincipal(r).Label)
	if err != nil {
		return nil
	}
	nowText := overviewNow().UTC().Format(time.RFC3339)
	var out []store.TokenInfo
	for _, t := range tokens {
		if t.Kind == "device" && t.RevokedAt == "" && (t.ExpiresAt == "" || t.ExpiresAt > nowText) {
			out = append(out, t)
		}
	}
	return out
}

func (a *app) hasMachine(r *http.Request) bool { return len(a.deviceTokens(r)) > 0 }

func (a *app) recentMachine(r *http.Request, now time.Time) (string, bool) {
	var newest store.TokenInfo
	var at time.Time
	for _, t := range a.deviceTokens(r) {
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

// ownerLike: Owner im Sinne der Hülle, oder das älteste Konto der Instanz. Auf
// einer frischen Instanz hat es noch keine Organisation und gilt der Hülle als
// Member, ist aber die Person, die den ersten Agenten verbindet.
func (a *app) ownerLike(r *http.Request, who viewer) bool {
	if who.kind == viewerOwner {
		return true
	}
	id := store.AccountIDOf(browserPrincipal(r).ID)
	return id != 0 && id == a.store.OwnerAccountID()
}
