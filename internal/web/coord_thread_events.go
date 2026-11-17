package web

import (
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// threadStateEvents lists the state changes of a thread the viewer already
// reads, oldest first, each as "who did what". A failed lookup only costs the
// lines: the thread itself is shown either way.
func (a *app) threadStateEvents(l lang, threadID int64) []coordThreadEventView {
	events, err := a.store.ThreadEvents(threadID)
	if err != nil {
		return nil
	}
	now := overviewNow().UTC()
	var out []coordThreadEventView
	for _, e := range events {
		if e.Kind != store.ThreadEventState {
			continue
		}
		var sentence func(who string) string
		switch e.Data {
		case store.ThreadResolved:
			sentence = func(who string) string { return l.msg("coord.event.resolved", who) }
		case store.ThreadOpen:
			sentence = func(who string) string { return l.msg("coord.event.open", who) }
		case store.ThreadDeferred:
			sentence = func(who string) string { return l.msg("coord.event.deferred", who) }
		default:
			continue
		}
		who := cleanLineWeb(e.Person, 80)
		if who == "" {
			who = l.msg("coord.event.someone")
		}
		at := parseTime(e.CreatedAt)
		out = append(out, coordThreadEventView{Text: sentence(who), When: shortAgo(l, now, at), At: e.CreatedAt, State: e.Data})
	}
	return out
}
