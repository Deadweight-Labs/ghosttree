package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The agents page. The list comes from listAgents: only projects in which the
// viewer may read agents, each room through CoordAccess.Peers. Request, session
// and last room post are looked up per visible agent and checked again against
// the viewer's rights, so a hidden request or transcript leaves a blank, not a
// hint (#2447). A guest gets the page without agents and without the connect
// action.

const (
	agentsMax = 50
	// agentPostWindow: how many recent room messages are searched for the last
	// post of each agent.
	agentPostWindow = 100
	agentPostLength = 120
)

type agentsView struct {
	Cards     []agentCard
	Machines  []machineRow
	Example   string
	Connect   string
	CanAdd    bool
	Guest     bool
	CSRFToken string
}

type agentCard struct {
	agentRow
	RoomHref, SessionHref    string
	ReqID, ReqTitle, ReqHref string
	Post, PostAge            string
}

// agentMachine is the host in an agent ID of the form claude:<host>:<uuid> or
// cli:<host>.
func agentMachine(externalID string) string {
	parts := strings.Split(externalID, ":")
	if len(parts) == 3 || (len(parts) == 2 && parts[0] == "cli") {
		return parts[1]
	}
	return ""
}

func (a *app) agentsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	now := overviewNow().UTC()
	pa := a.access(r)
	who := a.shellBaseFor(r).who
	shell := a.shellFor(r, "agents")
	view := agentsView{CSRFToken: csrfOf(r)}
	view.Guest = who.kind == viewerGuest
	if !view.Guest {
		view.CanAdd, view.Connect = true, "/ui/overview?connect=1"
		rows, err := a.listAgents(r, pa, selectedProject(shell), now, agentsMax)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if view.Cards, err = a.agentCards(r, pa, rows, now); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tokens, err := a.deviceTokens(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		view.Machines, view.Example = ownMachines(tokens, now), msg("setup.example")
	}
	a.renderBrowser(w, r, "agents", pageData{Title: msg("agents.title"), Agents: view})
}

// agentCards adds request, session link and last room post to each row.
func (a *app) agentCards(r *http.Request, pa *store.ProjectAccess, rows []agentRow, now time.Time) ([]agentCard, error) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ExternalID
	}
	work, err := a.store.AgentWorkFor(pa, ids)
	if err != nil {
		return nil, err
	}
	posts := a.lastPosts(r, rows, now)
	cards := make([]agentCard, len(rows))
	for i, row := range rows {
		c := agentCard{agentRow: row, RoomHref: "/ui/coord?room=" + url.QueryEscape(row.RoomKey)}
		if wk, ok := work[row.ExternalID]; ok {
			if wk.RequestID != 0 {
				c.ReqID = "REQ-" + strconv.FormatInt(wk.RequestID, 10)
				c.ReqTitle = wk.RequestTitle
				c.ReqHref = "/ui/requests/" + strconv.FormatInt(wk.RequestID, 10)
			}
			if wk.SessionPublicID != "" {
				c.SessionHref = "/ui/sessions/" + wk.SessionPublicID
			}
		}
		if p, ok := posts[row.ExternalID]; ok {
			c.Post, c.PostAge = p.text, p.age
		}
		cards[i] = c
	}
	return cards, nil
}

type agentPost struct{ text, age string }

// lastPosts reads the newest room messages once per room (through the viewer's
// CoordAccess) and keeps the latest of each agent.
func (a *app) lastPosts(r *http.Request, rows []agentRow, now time.Time) map[string]agentPost {
	out := map[string]agentPost{}
	access := a.browserCoord(r)
	rooms := map[string]bool{}
	for _, row := range rows {
		if rooms[row.RoomKey] {
			continue
		}
		rooms[row.RoomKey] = true
		page, err := access.MessageWindow(store.DestinationRoom, row.RoomKey, store.LatestWindow(agentPostWindow))
		if err != nil {
			continue
		}
		for _, m := range page.Messages {
			if m.AuthorKind != store.AuthorAgent || m.SenderExternalID == "" || strings.TrimSpace(m.Body) == "" {
				continue
			}
			out[m.SenderExternalID] = agentPost{oneLine(m.Body, agentPostLength), shortAge(now, parseTime(m.CreatedAt))}
		}
	}
	return out
}
