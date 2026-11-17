package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Mentions in comments, issues and pains (REQ-677) stand in the same list as
// those of the rooms: "what was said to you". The store records them when the
// text is written (store.recordTextMentions) and lists only what the viewer may
// still read (ProjectAccess.TextMentions).

// textMentionTarget is the page a mention leads to and the key the object goes
// by ("REQ-12"), by the kind the text stands in.
func textMentionTarget(m store.TextMention) (key, href string) {
	id := strconv.FormatInt(m.ObjectID, 10)
	switch m.ObjectKind {
	case store.CommentOnPain:
		key, href = "PAIN-"+id, repoHref(m.Project)+"/pains/"+id
	case store.CommentOnIdea:
		key, href = "IDEA-"+id, repoHref(m.Project)+"/ideas/"+id
	case store.CommentOnKnowledge:
		key, href = "KN-"+id, knowledgeHref(m.Project, m.ObjectID)
	default:
		key, href = "REQ-"+id, repoHref(m.Project)+"/issues/"+id
	}
	return key, href
}

// inboxTextMentions lists the unread mentions of the viewer in texts, newest first.
func (a *app) inboxTextMentions(r *http.Request, project string) []inboxRow {
	if a.notifyOff(r)[store.NotifyMention] {
		return nil
	}
	l := a.langFor(r)
	pa := a.access(r)
	list, err := pa.TextMentions(project)
	if err != nil {
		return nil
	}
	now := overviewNow().UTC()
	rows := make([]inboxRow, 0, len(list))
	for _, m := range list {
		key, href := textMentionTarget(m)
		at := parseTime(m.CreatedAt)
		go2 := href
		if m.SourceKind == store.MentionInComment {
			go2 += "#comment-" + strconv.FormatInt(m.SourceID, 10)
		}
		who := a.textMentionAuthor(l, pa, m)
		row := inboxRow{Mark: "rooms", Mention: true, Actor: who.Label, ActorKind: who.Kind, ActorOwner: who.Owner(), Lead: who.Label, In: key, InHref: href, Href: go2, Body: m.Snippet,
			Age: shortAgo(l, now, at), at: at, Cats: []string{"direkt"}}
		if m.SourceKind != store.MentionInComment {
			row.Cats = append(row.Cats, "issues")
		}
		row.Actions = []inboxAction{{Label: l.msg("inbox.act.read"), MentionID: m.ID}}
		rows = append(rows, row)
	}
	return rows
}

// textMentionAuthor is who wrote the text: a person by name, an agent as "Robin’s
// Claude" (the stamp of the text says which), the same as on the page it stands on.
func (a *app) textMentionAuthor(l lang, pa *store.ProjectAccess, m store.TextMention) actorView {
	kind := actor.Comment
	switch m.SourceKind {
	case store.MentionInRequest:
		kind = actor.Request
	case store.MentionInPain:
		kind = actor.Pain
	}
	return a.actorViewOf(l, pa, kind, m.SourceID, m.Author, false)
}

// inboxMentionRead is POST /ui/inbox/mention-read: one mention in a text is read.
func (a *app) inboxMentionRead(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "mention is required", http.StatusBadRequest)
		return
	}
	if err := a.access(r).MarkTextMentionRead(id); err != nil && err != store.ErrTextMentionNotFound {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
}

// unreachedParam is the query a redirect after writing carries so the page can
// tell the sender which @names reached nobody.
func unreachedParam(names []string) string {
	out := ""
	for i, n := range names {
		if i == 5 {
			break
		}
		if out != "" {
			out += ","
		}
		out += n
	}
	if out == "" {
		return ""
	}
	return "&unreached=" + url.QueryEscape(out)
}
