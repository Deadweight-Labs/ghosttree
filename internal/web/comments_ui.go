package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Comments on an issue, an idea, a pain and a knowledge entry (REQ-599, store part
// 6). The pages show them as boxes in their timeline, between the entries of the
// history, each with an anchor (#comment-n). Writing, editing and deleting go
// through ProjectAccess (AddComment, EditComment, DeleteComment), so the rights
// are the store's: members write, the author edits, the author and leads delete.
// Everything written here carries the web mark (gitanchor.Web()).

// commentVersionView is an earlier version of an edited comment.
type commentVersionView struct {
	N         int
	Age, Long string
	Raw       string
	Blocks    []bodyBlock
}

// commentView is the box of one comment in the timeline.
type commentView struct {
	ID              int64
	Anchor          string
	Who, Head, Long string
	// Actor is who wrote it: an agent with its mark, else the person (Who).
	Actor               actorView
	Author, Edited      bool
	EditedAge, EditedAt string
	Deleted             bool
	DeletedBy           string
	CanEdit, CanDelete  bool
	Blocks              []bodyBlock
	// Raw is the text as written; the script renders it as Markdown over Blocks.
	Raw      string
	Versions []commentVersionView
	// Edit is the composer of a comment that is being edited; EditHref opens it,
	// DeleteAction is the form that deletes.
	Edit         *composerView
	EditHref     string
	DeleteAction string
	Token        string
	// Reactions is the row under the text; nil when there is none to show.
	Reactions *reactionsView
	// Git is the anchor the comment was written at.
	Git *anchorView
}

// commentTimeline turns the comments of an object into entries of the timeline.
// author is the person who wrote the object ("Author" mark), editing the number
// of the comment that is open for editing (0: none), base the address of the
// page the edit link leads to.
func (a *app) commentTimeline(r *http.Request, l lang, pa *store.ProjectAccess, kind string, objectID int64, author, base string, editing int64, age func(string) string) []tlItem {
	list, err := pa.Comments(kind, objectID)
	if err != nil {
		return nil
	}
	forms := interactive(r)
	token := csrfOf(r)
	reacted, _ := pa.CommentReactions(kind, objectID)
	project := ""
	if rv := repoFrom(r); rv != nil {
		project = rv.Project
	}
	items := make([]tlItem, 0, len(list))
	persons := map[int64]string{}
	for _, c := range list {
		persons[c.ID] = c.Person
	}
	actors := a.stampedActors(l, actor.Comment, persons)
	for _, c := range list {
		at := parseTime(c.CreatedAt)
		v := &commentView{ID: c.ID, Anchor: "comment-" + strconv.FormatInt(c.ID, 10), Who: c.Person, Deleted: c.Deleted, DeletedBy: c.DeletedBy,
			Author: c.Person != "" && c.Person == author, Token: token, Actor: actors[c.ID]}
		v.Head = l.msg("comments.commented", age(c.CreatedAt))
		v.Long = c.CreatedAt
		if !c.Deleted {
			v.Git = anchorViewOf(pa, project, c.GitAnchor, c.Via)
		}
		if c.Deleted {
			items = append(items, tlItem{ID: v.Anchor, Act: "comment.deleted", Comment: v, at: at})
			continue
		}
		v.Blocks, v.Raw = bodyBlocks(c.Body, 0), c.Body
		if s, ok := reacted[c.ID]; ok {
			v.Reactions = a.reactionsFor(r, l, store.ReactOnComment, c.ID, s)
		}
		if c.Revision > 1 && c.EditedAt != "" {
			v.Edited, v.EditedAge, v.EditedAt = true, age(c.EditedAt), c.EditedAt
			if vs, err := pa.CommentVersions(c.ID); err == nil {
				for _, x := range vs {
					if x.Revision >= c.Revision {
						continue
					}
					v.Versions = append(v.Versions, commentVersionView{N: x.Revision, Age: age(x.CreatedAt), Long: x.CreatedAt, Blocks: bodyBlocks(x.Body, 0), Raw: x.Body})
				}
			}
		}
		if forms {
			v.CanEdit, v.CanDelete = c.CanEdit, c.CanDelete
		}
		if v.CanEdit {
			v.EditHref = base + "?edit-comment=" + strconv.FormatInt(c.ID, 10) + "#" + v.Anchor
			if editing == c.ID {
				v.Edit = &composerView{Action: "/ui/comments/" + strconv.FormatInt(c.ID, 10) + "/edit", Token: token, Name: "body", Value: c.Body,
					Placeholder: l.msg("comments.cx.placeholder"), Submit: l.msg("comments.update"), Cancel: l.msg("comments.cancel"), CancelHref: base + "#" + v.Anchor}
			}
		}
		if v.CanDelete {
			v.DeleteAction = "/ui/comments/" + strconv.FormatInt(c.ID, 10) + "/delete"
		}
		items = append(items, tlItem{ID: v.Anchor, Act: "comment", Comment: v, at: at})
	}
	return items
}

// mergeTimeline puts the comments between the entries by time; entries of the
// same moment keep their order and the comment comes last.
func mergeTimeline(events, comments []tlItem) []tlItem {
	if len(comments) == 0 {
		return events
	}
	all := append(append([]tlItem{}, events...), comments...)
	sort.SliceStable(all, func(i, j int) bool { return before(all[i].at, all[j].at) })
	return all
}

// before orders two moments; an entry without a time stays where it is.
func before(x, y time.Time) bool {
	if x.IsZero() || y.IsZero() {
		return false
	}
	return x.Before(y)
}

// editingComment is the number in ?edit-comment=, 0 when there is none.
func editingComment(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("edit-comment"), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// commentPage is where a comment lives: the page of its object.
func commentPage(c store.Comment) string {
	id := strconv.FormatInt(c.ObjectID, 10)
	switch c.ObjectKind {
	case store.CommentOnIdea:
		return repoHref(c.Project) + "/ideas/" + id
	case store.CommentOnPain:
		return repoHref(c.Project) + "/pains/" + id
	case store.CommentOnKnowledge:
		return knowledgeHref(c.Project, c.ObjectID)
	}
	return repoHref(c.Project) + "/issues/" + id
}

// objectPage is the address of an object page by what the form named.
func (a *app) objectPage(pa *store.ProjectAccess, kind string, id int64) string {
	switch kind {
	case store.CommentOnRequest:
		if ref, err := a.store.RequestRef(store.RefRequest, id); err == nil && ref.Project != "" {
			return repoHref(ref.Project) + "/issues/" + strconv.FormatInt(id, 10)
		}
	case store.CommentOnIdea:
		if i, err := pa.IdeaView(id); err == nil {
			return repoHref(i.Project) + "/ideas/" + strconv.FormatInt(id, 10)
		}
	case store.CommentOnPain:
		if p, err := pa.PainDetailView(id); err == nil {
			return repoHref(p.Pain.Project) + "/pains/" + strconv.FormatInt(id, 10)
		}
	case store.CommentOnKnowledge:
		if k, err := a.store.KnowledgeByID(id); err == nil && pa.CanSeeKnowledge(k) && k.Scope.Project != "" {
			return knowledgeHref(k.Scope.Project, id)
		}
	}
	return "/ui/inbox"
}

func (a *app) commentActor(r *http.Request) store.CommentActor {
	return store.CommentActor{Person: personOf(r), GitAnchor: gitanchor.Web()}
}

// commentFail answers a failed write: a rule error goes back to the page with a
// message, no right is 403, an unknown comment or object is 404.
func (a *app) commentFail(w http.ResponseWriter, r *http.Request, back string, err error) {
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch requestdomain.Code(err) {
	case "comment_empty":
		http.Redirect(w, r, back+"?comment=empty#cx", http.StatusSeeOther)
	case "comment_too_long":
		http.Redirect(w, r, back+"?comment=too_long#cx", http.StatusSeeOther)
	case "comment_not_found", "comment_target_not_found", "invalid_comment_target":
		http.NotFound(w, r)
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
	}
}

// commentResult is the message line a redirect asks for (?comment=…).
func commentResult(r *http.Request, l lang) *requestResult {
	switch r.URL.Query().Get("comment") {
	case "added":
		return &requestResult{Text: l.msg("comments.result.added"), Kind: "done"}
	case "edited":
		return &requestResult{Text: l.msg("comments.result.edited"), Kind: "done"}
	case "deleted":
		return &requestResult{Text: l.msg("comments.result.deleted"), Kind: "done"}
	case "empty":
		return &requestResult{Text: l.msg("comments.result.empty"), Kind: "failed", Fail: true}
	case "too_long":
		return &requestResult{Text: l.msg("comments.result.too_long"), Kind: "failed", Fail: true}
	case "failed":
		return &requestResult{Text: l.msg("comments.result.failed"), Kind: "failed", Fail: true}
	}
	return nil
}

// commentEdit is POST /ui/comments/{id}/edit: a new version of the text.
func (a *app) commentEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	c, err := pa.EditComment(id, r.FormValue("body"), a.commentActor(r))
	if err != nil {
		back := "/ui/inbox"
		if old, verr := pa.CommentView(id); verr == nil {
			back = commentPage(old) + "?edit-comment=" + strconv.FormatInt(id, 10)
			if requestdomain.Code(err) == "comment_empty" || requestdomain.Code(err) == "comment_too_long" {
				http.Redirect(w, r, back+"&comment="+map[string]string{"comment_empty": "empty", "comment_too_long": "too_long"}[requestdomain.Code(err)]+"#comment-"+strconv.FormatInt(id, 10), http.StatusSeeOther)
				return
			}
		}
		a.commentFail(w, r, back, err)
		return
	}
	http.Redirect(w, r, commentPage(c)+"?comment=edited#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
}

// commentDelete is POST /ui/comments/{id}/delete: soft delete, a placeholder stays.
func (a *app) commentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	c, err := pa.DeleteComment(id, a.commentActor(r))
	if err != nil {
		a.commentFail(w, r, "/ui/inbox", err)
		return
	}
	http.Redirect(w, r, commentPage(c)+"?comment=deleted#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
}

// knowledgeComment is POST /ui/knowledge/{id}/comment: a comment on an entry. The
// rights are the store's, as for a comment on an issue: whoever may read the
// entry and write in its project; a guest reads only.
func (a *app) knowledgeComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	c, err := pa.AddComment(store.CommentOnKnowledge, id, r.FormValue("body"), a.commentActor(r))
	if err != nil {
		a.commentFail(w, r, a.objectPage(pa, store.CommentOnKnowledge, id), err)
		return
	}
	a.stampActor(r, actor.Comment, c.ID)
	http.Redirect(w, r, commentPage(c)+"?comment=added#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
}

// fillKnowledgeComments gives the page of an entry what the page of an issue has:
// the comments, the reactions under the text and the box to write a comment. An
// entry without a project has no place for either (store.CommentOnKnowledge).
func (a *app) fillKnowledgeComments(r *http.Request, l lang, pa *store.ProjectAccess, k store.Knowledge, v *knowledgeItemView) {
	project := strings.TrimSpace(k.Scope.Project)
	if project == "" {
		return
	}
	now := overviewNow().UTC()
	ago := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	base := knowledgeHref(project, k.ID)
	v.Comments = a.commentTimeline(r, l, pa, store.CommentOnKnowledge, k.ID, k.Person, base, editingComment(r), ago)
	for _, it := range v.Comments {
		if it.Comment != nil && !it.Comment.Deleted {
			v.CommentsN++
		}
	}
	if s, err := pa.Reactions(store.ReactOnKnowledge, k.ID); err == nil {
		v.Reactions = a.reactionsFor(r, l, store.ReactOnKnowledge, k.ID, s)
	}
	v.CommentResult = commentResult(r, l)
	if interactive(r) && pa.Allow(project, store.ResComment, store.ActCreate, store.Object{Own: true}) {
		v.Composer = &composerView{Action: "/ui/knowledge/" + strconv.FormatInt(k.ID, 10) + "/comment", Token: csrfOf(r), Name: "body",
			Avatar: browserPrincipal(r).Label, Placeholder: l.msg("comments.cx.placeholder"), Submit: l.msg("comments.cx.submit")}
	}
}
