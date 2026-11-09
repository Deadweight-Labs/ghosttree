package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Live work on an issue (REQ-599 criterion 15). An agent "works on" an issue
// when its work on it is active (request_start_work) AND the session shows a
// sign of life in the last two minutes (store.ActivityFreshTTL): a tool call
// that touched a file (path_activity) or a fresh upload of the transcript. The
// same rule as coord_peers uses for "working". With older signs the work is
// still listed, but the page no longer says that someone works there right now.
//
// The issue page, the lists and the inbox render the state once; the stream
// /ui/live sends it again whenever it changes, so nothing has to be reloaded.

// liveAgent is one agent working on an issue.
type liveAgent struct {
	Handle, Kind, Owner, Href string
	// Live: a sign of life is fresh. Since is how long the work has run, Last what
	// the agent did last ("" when unknown), External its session id.
	Live        bool
	Since, Last string
	At          time.Time
	External    string
}

var liveStreamEvery = 3 * time.Second

// liveFresh is the span a sign of life counts.
const liveFresh = store.ActivityFreshTTL

// liveAgents lists the agents with active work among the work lines of an
// issue, in the order of the lines. A session the viewer may not read has no
// address and is left out: the page would say that someone works there.
func (a *app) liveAgents(pa *store.ProjectAccess, work []workLine, now time.Time, l lang) []liveAgent {
	var out []liveAgent
	for _, w := range work {
		if !w.Active || w.Href == "" {
			continue
		}
		if la, ok := a.liveOf(pa, strings.TrimPrefix(w.Href, "/ui/sessions/"), parseTime(w.Started), now, l); ok {
			out = append(out, la)
		}
	}
	return out
}

// liveOf reads one session: who it is and whether it shows a fresh sign of
// life. ok is false for a session the viewer cannot read.
func (a *app) liveOf(pa *store.ProjectAccess, publicID string, started, now time.Time, l lang) (liveAgent, bool) {
	sess, err := a.store.SessionByPublicID(publicID)
	if err != nil || !pa.CanSeeTranscript(sess) {
		return liveAgent{}, false
	}
	la := liveAgent{Handle: sessionHandle(pa, sess), Kind: harnessKind(sess.Harness), Href: "/ui/sessions/" + sess.PublicID, External: sess.ExternalID}
	if acct, err := a.store.AccountByPrincipalID("person:" + strconv.FormatInt(sess.AccountID, 10)); err == nil {
		la.Owner = acct.Name
	}
	if !started.IsZero() {
		la.Since = shortAge(l, now, started)
	}
	fresh := now.Add(-liveFresh)
	if seen := parseTime(sess.LastSeenAt); !seen.IsZero() && seen.After(fresh) && !seen.After(now.Add(time.Minute)) {
		la.Live, la.At = true, seen
	}
	// The last file the session touched, within the last day, however old.
	if acts, err := a.store.SessionPathActivity(sess.ExternalID, sess.AccountID, now.Add(-24*time.Hour).Format(time.RFC3339), 1); err == nil && len(acts) > 0 {
		at := parseTime(acts[0].At)
		if !at.IsZero() {
			if at.After(fresh) && !at.After(now.Add(time.Minute)) {
				la.Live = true
			}
			la.At = at
			la.Last = l.msg("live.last_file", ellipsize(acts[0].Path, 80), shortAge(l, now, at))
		}
	}
	return la, true
}

// liveByRequest is the live agents of several issues, one per issue (the first
// that is live, else none: an issue whose agent is silent shows nothing).
func (a *app) liveByRequest(pa *store.ProjectAccess, ids []int64, now time.Time, l lang) map[int64]liveAgent {
	out := map[int64]liveAgent{}
	work, err := a.store.ActiveWorkOnRequests(pa, ids)
	if err != nil {
		return out
	}
	for id, list := range work {
		for _, w := range list {
			if la, ok := a.liveOf(pa, w.SessionPublicID, time.Time{}, now, l); ok && la.Live {
				out[id] = la
				break
			}
		}
	}
	return out
}

// liveBoxHTML is the box of the issue page: who works on it, for how long, what
// was done last and the way to the session.
func liveBoxHTML(l lang, la liveAgent) template.HTML {
	if !la.Live {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="live-box" data-live-handle="` + html.EscapeString(la.Handle) + `"><span class="live-av">` + string(avatarMarkup(l, avatarSpec{Kind: la.Kind, Name: la.Handle, Size: "lg", Owner: la.Owner, Live: true})) + `</span><div class="live-tx"><span><b>` + html.EscapeString(la.Handle) + `</b> ` + html.EscapeString(l.msg("live.working")))
	if la.Since != "" {
		b.WriteString(` <span class="muted">· ` + html.EscapeString(l.msg("live.since", la.Since)) + `</span>`)
	}
	b.WriteString(`</span>`)
	if la.Last != "" {
		b.WriteString(`<small class="muted">` + html.EscapeString(la.Last) + `</small>`)
	}
	b.WriteString(`</div><a class="gbtn sm" href="` + html.EscapeString(la.Href) + `">` + string(mockIcon("term")) + html.EscapeString(l.msg("live.jump")) + `</a></div>`)
	return template.HTML(b.String())
}

// liveDotHTML is the avatar of the working agent with the pulse, for rows of
// lists and the inbox.
func liveDotHTML(l lang, la liveAgent) template.HTML {
	if !la.Live {
		return ""
	}
	return avatarMarkup(l, avatarSpec{Kind: la.Kind, Name: la.Handle, Owner: la.Owner, Live: true, Title: l.msg("live.row", la.Handle)})
}

type liveSnapshot struct {
	Box string `json:"box"`
	Dot string `json:"dot"`
}

// liveEvents is /ui/live?ids=1,2,3: a stream that says, for the issues named,
// who works on them now. The first message and every change send the whole
// state of the issues (an empty box means: nobody). Only issues the viewer may
// read are looked at.
func (a *app) liveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "browser session missing", http.StatusUnauthorized)
		return
	}
	l := a.langFor(r)
	var asked []int64
	for _, f := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && n > 0 && !slices.Contains(asked, n) && len(asked) < 100 {
			asked = append(asked, n)
		}
	}
	// What was readable once and is not any more: the box is emptied, nothing else is said.
	wasReadable := map[int64]bool{}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	principal := browserPrincipal(r)
	last := ""
	lastRecheck := time.Now()
	tick := time.NewTicker(liveStreamEvery)
	defer tick.Stop()
	for {
		// Every round starts from the rights of that moment: a member who was
		// removed from the project in the meantime learns nothing more.
		pa := a.access(r)
		var ids []int64
		for _, n := range asked {
			if a.requestReadable(pa, n) {
				ids = append(ids, n)
				wasReadable[n] = true
			}
		}
		snap := map[string]liveSnapshot{}
		now := overviewNow().UTC()
		byReq := a.liveByRequest(pa, ids, now, l)
		for _, id := range ids {
			la := byReq[id]
			snap[strconv.FormatInt(id, 10)] = liveSnapshot{Box: string(liveBoxHTML(l, la)), Dot: string(liveDotHTML(l, la))}
		}
		for _, n := range asked {
			if wasReadable[n] && !slices.Contains(ids, n) {
				snap[strconv.FormatInt(n, 10)] = liveSnapshot{}
			}
		}
		raw, _ := json.Marshal(snap)
		if string(raw) != last {
			last = string(raw)
			if _, err := fmt.Fprintf(w, "event: live\ndata: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		_, live := a.sessions.get(cookie.Value)
		if live && time.Since(lastRecheck) >= sessionRecheckInterval {
			lastRecheck = time.Now()
			if !a.store.PrincipalValid(principal) {
				a.sessions.remove(cookie.Value)
				live = false
			}
		}
		if !live {
			_, _ = fmt.Fprint(w, "event: session-ended\ndata: {}\n\n")
			flusher.Flush()
			return
		}
	}
}
