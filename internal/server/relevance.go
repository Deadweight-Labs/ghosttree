package server

import (
	"fmt"
	"net/http"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// maxRelevantEntries caps what one prompt may pull in. Three is enough to
// answer a sentence and few enough that a wrong guess stays cheap.
const maxRelevantEntries = 3

// maxHintTitle keeps the title of a request or session in a hint to what lets
// the reader recognise it. A hint is delivered unasked, so it carries the least
// text that still says where to look.
const maxHintTitle = 70

// maxHintSources is how many requests, and separately how many sessions, may
// name a word before it stops being a codename. A codename that never became an
// entry came up in a session or two; a word that the reader's sessions or the
// project's requests use again and again is vocabulary, whatever the knowledge
// says, and pointing at one of its many places says nothing. Only what the
// reader may read is counted, so the verdict tells them nothing about the rest.
const maxHintSources = 3

// hintSessionScan is how many matching chunks are read to count the sessions
// behind them. Hits are chunks, several from one session; the scan is what lets
// a word said often in one session still count as one session.
const hintSessionScan = 40

// relevant answers with knowledge the text gives a reason to deliver, or with
// nothing at all — which is the usual case, and is an empty body rather than an
// error.
//
// With a session id it also remembers what it delivered, and what the start-up
// briefing delivered, so the same entry arrives once per session however often
// its word is said. The memory is claimed only after the visibility rules have
// run: an entry the reader may not see is never recorded as shown to them.
func (a *api) relevant(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", maxRelevantEntries)
	if limit > maxRelevantEntries {
		limit = maxRelevantEntries
	}
	pa := a.access(r)
	pa.Filtered()
	axes := axesFromQuery(r)
	account := principalOf(r).ID
	session := store.CleanSessionKey(r.URL.Query().Get("session"))
	text := r.URL.Query().Get("q")
	// The hint searches the sessions, so it looks there only for a prompt that asks
	// about earlier work (PAIN-70, PAIN-76). The pointer to a request is cheap
	// (requests are few and small) and stays for every prompt.
	inSessions := asksAboutEarlierWork(text)
	res, err := a.st.RelevantFor(store.RelevanceQuery{
		Text:    text,
		Axes:    axes,
		Limit:   a.overfetch(limit),
		Viewer:  []store.SessionViewer{pa.Viewer()},
		Account: account,
		Session: session,
		Record:  true,
	})
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	hits := filterTo(res.Hits, limit, func(h store.RelevantHit) bool {
		return pa.CanSeeKnowledge(h.Knowledge) && pa.CanDeliverKnowledge(h.Knowledge)
	})
	// Claiming comes before answering, so two prompts of the same session
	// handled at once cannot both deliver an entry. When the claim cannot be
	// made the answer is silence: better to miss an entry than to repeat it. The
	// same trade runs the other way when the hook gives up on a slow server after
	// the claim went through: the entry is then recorded and never seen, which is
	// what a missed entry always was. A caller that already hung up claims nothing.
	if len(hits) > 0 && session != "" && r.Context().Err() == nil {
		items := make([]store.ContextDelivery, len(hits))
		for i, h := range hits {
			items[i] = store.ContextDelivery{KnowledgeID: h.ID}
		}
		fresh, err := a.st.ClaimContextDeliveries(account, session, store.DeliveredByRelevance, items)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		won := make(map[int64]bool, len(fresh))
		for _, f := range fresh {
			won[f.KnowledgeID] = true
		}
		hits = filterTo(hits, 0, func(h store.RelevantHit) bool { return won[h.ID] })
	}
	hint, err := a.relevanceHint(r, pa, res.Hints, axes, account, session, inSessions)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	if len(hits) == 0 && hint == "" {
		return
	}
	// A different heading from the bootstrap on purpose. This arrived because of
	// what was just said, and a reader who cannot tell the two apart cannot judge
	// why it is in front of them. Each line names the word that brought it, for
	// the same reason: a reader who sees it can tell a good reason from a bad one.
	fmt.Fprint(w, "## Possibly relevant to what you just said (ghosttree)\n\n")
	for _, h := range hits {
		k := h.Knowledge
		fmt.Fprintf(w, "- [%s|%s] %s — %s (triggered by %q)\n", k.Type, scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), 400), h.Term)
	}
	if hint != "" {
		fmt.Fprint(w, hint)
	}
}

// rememberDelivered records entries as delivered to the session named in the
// request. Without a session id there is nothing to remember. A failed write is
// dropped on purpose: the cost is that an entry may arrive once more later,
// which is what delivery did before it had a memory.
func (a *api) rememberDelivered(r *http.Request, via string, ids []int64) {
	session := store.CleanSessionKey(r.URL.Query().Get("session"))
	if session == "" || len(ids) == 0 {
		return
	}
	items := make([]store.ContextDelivery, len(ids))
	for i, id := range ids {
		items[i] = store.ContextDelivery{KnowledgeID: id}
	}
	_, _ = a.st.ClaimContextDeliveries(principalOf(r).ID, session, via, items)
}

// relevanceHint is the one line that says where a very rare word came up before,
// for a word the knowledge has no answer to. It exists for the codename that was
// said in a session or written into a request and never became an entry.
//
// It points, it does not quote: the word, one request or session, a date, and
// the tool that finds more. It is built from what the reader may see and
// nothing else. Requests and sessions go through the same filters, views and
// scope the search endpoint uses, so a hint can never name an object that
// context_search would not show this reader, and a word that occurs only in
// material they cannot read produces no line at all — the same silence as a
// word that occurs nowhere.
//
// At most one line per prompt and, through the delivery memory, one per word
// and session. Without inSessions the sessions are neither searched nor counted:
// only a request can be named then, and a word that names none gets no line.
func (a *api) relevanceHint(r *http.Request, pa *store.ProjectAccess, terms []store.HintTerm, axes scope.Axes, account, session string, inSessions bool) (string, error) {
	where := scope.Axes{Project: axes.Project}
	for _, t := range terms {
		if answered(pa, t.Known) {
			continue
		}
		line, err := a.hintLine(r, pa, t.Term, where, session, inSessions)
		if err != nil {
			return "", err
		}
		if line == "" {
			continue
		}
		if session != "" {
			fresh, err := a.st.ClaimContextDeliveries(account, session, store.DeliveredByHint, []store.ContextDelivery{{Term: t.Term}})
			if err != nil {
				return "", err
			}
			if len(fresh) == 0 {
				continue
			}
		}
		return line, nil
	}
	return "", nil
}

// answered says the word already has an entry the reader may be given. Entries
// the reader may not see do not count: for them the archive is silent.
func answered(pa *store.ProjectAccess, known []store.Knowledge) bool {
	for _, k := range known {
		if pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) {
			return true
		}
	}
	return false
}

func (a *api) hintLine(r *http.Request, pa *store.ProjectAccess, term string, where scope.Axes, session string, inSessions bool) (string, error) {
	page, err := a.st.SearchRequests(a.requestFilter(r, requestdomain.SearchFilter{Query: term, Scope: where, Limit: maxHintSources + 1}))
	if err != nil {
		return "", err
	}
	if len(page.Results) > maxHintSources || (!inSessions && len(page.Results) == 0) {
		return "", nil
	}
	if !inSessions {
		// The prompt asks nothing about earlier work: the request is named, and
		// the sessions that would confirm the word as a codename are not read.
		return requestHintLine(pa, term, page.Results[0]), nil
	}
	// The session the prompt is in is left out: it contains the word because it
	// was just typed, which says nothing. Sessions are counted even when a
	// request names the word, so that a word of everyday language that one
	// request happens to contain is still recognised as one.
	hits, err := a.searchSessions(a.st, pa, term, where, session, hintSessionScan, a.overfetch(hintSessionScan), store.SearchFullTextOnly)
	if err != nil {
		return "", err
	}
	if distinctSessions(hits) > maxHintSources {
		return "", nil
	}
	if len(page.Results) > 0 {
		return requestHintLine(pa, term, page.Results[0]), nil
	}
	if len(hits) == 0 {
		return "", nil
	}
	se := hits[0].Session
	label := sessionLabel(se)
	if title := truncate(oneLine(se.Title), maxHintTitle); title != "" {
		label += fmt.Sprintf(" %q", title)
	}
	date := se.LastSeenAt
	if len(date) > len("2006-01-02") {
		date = date[:len("2006-01-02")]
	}
	return fmt.Sprintf("- [hint] %q also came up in session %s on %s. context_search finds more.\n", term, label, date), nil
}

// requestHintLine points at the request that names the word.
func requestHintLine(pa *store.ProjectAccess, term string, hit requestdomain.SearchHit) string {
	h := pa.RequestHitView(hit)
	return fmt.Sprintf("- [hint] %q also came up in REQ-%d %q (%s). context_search finds more.\n",
		term, h.Request.ID, truncate(oneLine(h.Request.Title), maxHintTitle), h.Request.State)
}

// distinctSessions counts the sessions behind a list of hits, by the label a
// reader addresses them with (a guest's view carries no number).
func distinctSessions(hits []store.SessionHit) int {
	seen := map[string]bool{}
	for _, h := range hits {
		seen[sessionLabel(h.Session)] = true
	}
	return len(seen)
}

// sessionLabel names a session the way a reader can address it again: members by
// number, guests, to whom the server gives none, by address.
func sessionLabel(se store.Session) string {
	if se.ID == 0 && se.PublicID != "" {
		return "@" + se.PublicID
	}
	return fmt.Sprintf("#%d", se.ID)
}
