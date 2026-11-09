package web

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Reactions on comments, ideas and issues (REQ-599): "👍 3" under the text, blue
// when the viewer reacted, and a round button that opens the eight of the fixed
// list. A click switches the reaction: POST /ui/reactions looks at what the viewer
// has set and sets or takes back. Without script the buttons are forms that lead
// back to the page (next); with script they ask for the row again as a fragment
// and replace it, so the page does not reload. A guest sees the numbers only.

// reactionLabelKeys are the catalog keys of the names of the eight.
var reactionLabelKeys = map[string]string{"+1": "ui.react.l.up", "-1": "ui.react.l.down", "laugh": "ui.react.l.laugh", "hooray": "ui.react.l.hooray",
	"confused": "ui.react.l.confused", "heart": "ui.react.l.heart", "rocket": "ui.react.l.rocket", "eyes": "ui.react.l.eyes"}

// reactionsFor turns the reactions of an object into the row of the component. It
// is nil when there is nothing to show: no reaction yet and none to add (a guest,
// a session from a pasted token). kind and id name the object the buttons act on.
func (a *app) reactionsFor(r *http.Request, l lang, kind string, id int64, s store.ReactionSummary) *reactionsView {
	can := s.CanReact && interactive(r)
	if len(s.Items) == 0 && !can {
		return nil
	}
	v := &reactionsView{}
	me := browserPrincipal(r).Label
	mine := map[string]bool{}
	for _, it := range s.Items {
		mine[it.Content] = it.Mine
		v.Items = append(v.Items, reaction{Emoji: it.Emoji, Value: it.Content, Count: it.Count, Mine: it.Mine, Tip: reactionTip(l, it, me, can)})
	}
	if can {
		v.Action, v.Token, v.Kind, v.ID = "/ui/reactions", csrfOf(r), kind, strconv.FormatInt(id, 10)
		for _, k := range store.ReactionSet {
			v.Choices = append(v.Choices, reactionChoice{Value: k.Key, Emoji: k.Emoji, Label: label(l, reactionLabelKeys, k.Key), Mine: mine[k.Key]})
		}
	}
	return v
}

// reactionTip is the tooltip of one reaction: who reacted (the viewer as "You",
// first), and, for those who may react, what a click does.
func reactionTip(l lang, it store.ReactionCount, me string, can bool) string {
	var names []string
	if it.Mine {
		names = append(names, l.msg("ui.react.you"))
	}
	for _, p := range it.People {
		if it.Mine && p == me {
			continue
		}
		names = append(names, p)
	}
	var lines []string
	if len(names) > 0 {
		list := strings.Join(names, ", ")
		if more := it.Count - len(names); more > 0 {
			list += " " + l.msg("ui.react.more", more)
		}
		lines = append(lines, l.msg("ui.react.tip", it.Emoji, list))
	}
	if can {
		if it.Mine {
			lines = append(lines, l.msg("ui.react.remove"))
		} else {
			lines = append(lines, l.msg("ui.react.add_with", it.Emoji))
		}
	}
	return strings.Join(lines, "\n")
}

// reactionToggle is POST /ui/reactions (kind, id, reaction): sets the reaction
// when the viewer has not, takes it back when they have. The rights are the
// store's: members react, a guest gets 403, what the viewer may not read is 404.
// With next the form goes back to the page of the object (the target is computed
// here, never taken from the request); without it the answer is the row of
// reactions as HTML, for the script to put in place.
func (a *app) reactionToggle(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	key, ok := store.ParseReaction(r.FormValue("reaction"))
	if err != nil || id <= 0 || !ok || (kind != store.ReactOnComment && kind != store.ReactOnIdea && kind != store.ReactOnRequest) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	now, err := pa.Reactions(kind, id)
	if err == nil {
		set := true
		for _, it := range now.Items {
			if it.Content == key && it.Mine {
				set = false
			}
		}
		if set {
			now, err = pa.React(kind, id, key)
		} else {
			now, err = pa.Unreact(kind, id, key)
		}
	}
	if err != nil {
		var rule *requestdomain.RuleError
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound), errors.Is(err, sql.ErrNoRows),
			errors.As(err, &rule) && rule.ErrorCode == "reaction_target_not_found":
			http.NotFound(w, r)
		case errors.As(err, &rule):
			http.Error(w, "invalid input", http.StatusBadRequest)
		default:
			http.Error(w, "could not save", http.StatusInternalServerError)
		}
		return
	}
	if r.FormValue("next") != "" {
		http.Redirect(w, r, a.reactionPage(pa, kind, id), http.StatusSeeOther)
		return
	}
	var body bytes.Buffer
	if v := a.reactionsFor(r, a.langFor(r), kind, id, now); v != nil {
		if err := a.langFor(r).page().ExecuteTemplate(&body, "uireactions", v); err != nil {
			http.Error(w, "could not render", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body.Bytes())
}

// reactionPage is where a form without script returns to: the page of the object
// the reaction hangs on, at the comment for a comment.
func (a *app) reactionPage(pa *store.ProjectAccess, kind string, id int64) string {
	if kind == store.ReactOnComment {
		if c, err := pa.CommentView(id); err == nil {
			return commentPage(c) + "#comment-" + strconv.FormatInt(id, 10)
		}
		return "/ui/inbox"
	}
	return a.objectPage(pa, kind, id)
}
