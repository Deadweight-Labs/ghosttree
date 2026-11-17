package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// attachAnswerOptions puts the answers an asking agent proposed on the question cards
// of the room page, so a question can be answered from the room with one click as
// it can from the inbox. The options come from the store through the viewer's
// attention items, which are already limited to what they may read; a session
// that may not change anything (a pasted token) gets none, as in the inbox.
func (a *app) attachAnswerOptions(r *http.Request, views []coordAttentionView, items []store.AttentionItem) {
	if len(views) == 0 || !interactive(r) {
		return
	}
	options, err := a.access(r).AttentionOptions(items)
	if err != nil || len(options) == 0 {
		return
	}
	for i := range views {
		for _, o := range options[views[i].ID] {
			views[i].Options = append(views[i].Options, inboxOption{Key: o.Key, Text: o.Text})
		}
	}
}

// answerBackTo is where a quick answer returns to: the inbox, or the room the form
// came from. The room key only ever becomes an escaped query value behind the fixed
// /ui/coord path, so a request cannot send the browser anywhere else.
func answerBackTo(r *http.Request) string {
	if room := strings.TrimSpace(r.FormValue("room")); room != "" {
		return "/ui/coord?room=" + url.QueryEscape(room)
	}
	return "/ui/inbox"
}
