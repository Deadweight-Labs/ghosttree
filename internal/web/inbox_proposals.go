package web

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Proposed answers in the inbox (REQ-599, IDEA-2). An agent that asks something
// can hand along two to six short answers; the row shows them as buttons, and a
// click answers with that text through POST /ui/inbox/answer (inbox_answer.go),
// which is the one way from the browser to ProjectAccess.QuickAnswer. "Own
// answer" opens the reply field of the row as before. What a row offers comes
// from ProjectAccess.Inbox, so a person sees proposals only on entries they may
// answer: the store attaches them after its own visibility checks.

// inboxProposals sets the proposals of the store on the waiting rows: a task by
// its number, a question in a room by its attention number, a decision by its
// number (its variants). A row without a
// proposal stays as it is.
func (a *app) inboxProposals(r *http.Request, pa *store.ProjectAccess, project string, rows []inboxRow) {
	if len(rows) == 0 {
		return
	}
	entries, err := pa.Inbox(a.browserCoord(r), store.InboxFilter{State: store.InboxOpen, Project: project, Mine: true, Limit: 200})
	if err != nil {
		return
	}
	type key struct {
		kind string
		id   int64
	}
	byEntry := map[key]store.InboxEntry{}
	for _, e := range entries {
		if len(e.Options) > 0 && (e.Kind == store.InboxTask || e.Kind == store.InboxAttention || e.Kind == store.InboxDecision) {
			byEntry[key{e.Kind, e.ID}] = e
		}
	}
	for i := range rows {
		k := key{}
		switch {
		case rows[i].taskID != 0:
			k = key{store.InboxTask, rows[i].taskID}
		case rows[i].AttentionID != 0:
			k = key{store.InboxAttention, rows[i].AttentionID}
		case rows[i].DecisionN != 0:
			k = key{store.InboxDecision, rows[i].DecisionN}
		default:
			continue
		}
		e, ok := byEntry[k]
		if !ok {
			continue
		}
		rows[i].QuickKind, rows[i].QuickID, rows[i].ProposedBy = e.Kind, e.ID, e.ProposedBy
		for _, o := range e.Options {
			rows[i].Options = append(rows[i].Options, inboxOption{Key: o.Key, Text: o.Text})
		}
	}
}

// QuickID as text for a form field.
func (r inboxRow) QuickIDText() string { return strconv.FormatInt(r.QuickID, 10) }
