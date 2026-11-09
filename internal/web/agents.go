package web

import (
	"errors"
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
	Sections  agentSections
	Cmd       cmdBlock
	Machines  []machineRow
	Example   string
	Connect   string
	CanAdd    bool
	Guest     bool
	CSRFToken string
}

type agentCard struct {
	agentRow
	RoomHref, SessionHref string
	// MachineHref is the machine page, empty when the viewer cannot reach it.
	MachineHref string
	// CSRFToken and Back belong to the check "seen" of a row that waits on the
	// viewer: the token of the form and the page it returns to.
	CSRFToken, Back          string
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
	l := a.langFor(r)
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
		view.Machines, view.Example = ownMachines(l, tokens, now), l.msg("setup.example")
		view.Sections = sectionAgents(view.Cards, csrfOf(r), r.URL.RequestURI())
		view.Cmd = cmdBlock{ID: "agents-cmd", Mac: view.Example, Linux: view.Example}
	}
	a.renderBrowser(w, r, "agents", pageData{Title: l.msg("agents.title"), Agents: view})
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
	machineLink := a.machineLinker(r)
	for i, row := range rows {
		c := agentCard{agentRow: row, RoomHref: "/ui/coord?room=" + url.QueryEscape(row.RoomKey)}
		if c.Machine == "" {
			c.Machine = agentMachine(row.ExternalID)
		}
		c.MachineHref = machineLink(c.Machine)
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
	l := a.langFor(r)
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
			out[m.SenderExternalID] = agentPost{shortWords(m.Body, agentPostLength), shortAge(l, now, parseTime(m.CreatedAt))}
		}
	}
	return out
}

// agentSections is the list of agents the way a person reads it: first those
// that wait on the viewer, then those at work, and the dormant ones apart (the
// page folds them), so thousands of resting agents do not bury the few that
// matter.
type agentSections struct {
	Waiting, Active, Inactive []agentCard
}

// sectionAgents sorts cards (already ordered by listAgents) into the sections:
// "wait_you" is waiting on the viewer, "work", "wait_other" and "stuck" are
// active, "idle" and "off" are inactive. Rows that wait carry the form of the
// check "seen" (token and the page to return to).
func sectionAgents(cards []agentCard, token, back string) agentSections {
	var s agentSections
	for _, c := range cards {
		switch c.Presence {
		case "wait_you":
			c.CSRFToken, c.Back = token, back
			s.Waiting = append(s.Waiting, c)
		case "idle", "off":
			s.Inactive = append(s.Inactive, c)
		default:
			s.Active = append(s.Active, c)
		}
	}
	return s
}

// Any says whether the page has an agent to show.
func (s agentSections) Any() bool {
	return len(s.Waiting)+len(s.Active)+len(s.Inactive) > 0
}

// agentSeen quits the wait of an agent on the viewer without an answer. It
// closes the viewer's open questions, approvals and blockers from that agent as
// dismissed, the same step as the check in the inbox (ResolveAttention); the
// agent's wait ends with them. Nothing but the viewer's own items is touched:
// Attention lists only those, and the store checks the recipient again.
func (a *app) agentSeen(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	access := a.browserCoord(r)
	items, err := access.Attention()
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	for _, item := range items {
		if !item.IsRecipient || item.State != store.AttentionOpen || item.SenderID != id {
			continue
		}
		if err := access.ResolveAttention(item.ID, store.AttentionActionDismiss); err != nil && !errors.Is(err, store.ErrAttentionClosed) {
			coordHTTPError(l, w, err)
			return
		}
	}
	http.Redirect(w, r, a.agentSeenBack(r, r.FormValue("back")), http.StatusSeeOther)
}

// agentSeenBack is the agent list the check came from: the list of all agents
// or the agents tab of an organisation the viewer belongs to. The address is
// built here from known values, never taken over from the form.
func (a *app) agentSeenBack(r *http.Request, back string) string {
	const all = "/ui/agents"
	slug, ok := strings.CutPrefix(back, "/ui/")
	if !ok {
		return all
	}
	slug, ok = strings.CutSuffix(strings.SplitN(slug, "?", 2)[0], "/agents")
	if !ok || slug == "" {
		return all
	}
	orgs, err := a.store.ListOrgs(browserPrincipal(r).ID)
	if err != nil {
		return all
	}
	for _, o := range orgs {
		if o.Slug == slug {
			return "/ui/" + url.PathEscape(o.Slug) + "/agents"
		}
	}
	return all
}
