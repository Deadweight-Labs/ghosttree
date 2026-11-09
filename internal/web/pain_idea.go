package web

import (
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// painIdea ist POST /ui/pains/{id}/idea: aus einem Pain wird eine Lösungsidee.
// Die Idee entsteht im Projekt des Pains, und der Pain „motiviert“ sie (die
// Relation, die auch die Idee als „Entstanden aus“ zeigt). Die Rechte sind die
// des Stores: Arbeiten am Pain und Anlegen einer Idee. Ein Fremder bekommt
// dieselbe Antwort wie bei einem Pain, den es nicht gibt.
func (a *app) painIdea(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title, ok := cleanText(r.FormValue("title"), 200)
	if !ok || title == "" || len(r.FormValue("body")) > maxFieldBytes {
		http.Redirect(w, r, a.painHref(r, id)+"?fail=idea_invalid", http.StatusSeeOther)
		return
	}
	pa := a.access(r)
	detail, err := pa.PainDetailView(id)
	if err != nil {
		a.painFail(w, r, err)
		return
	}
	p := detail.Pain
	if err := pa.CheckPain(p, store.ActWork); err != nil {
		a.painFail(w, r, err)
		return
	}
	// Ein Mensch hat den Knopf gedrückt und den Titel selbst geschrieben: die
	// Dublettenprüfung für Agenten würde ihn nur aufhalten.
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: p.Project, Title: title, Body: strings.TrimSpace(r.FormValue("body")),
		Actor: store.IdeaActorFor(browserPrincipal(r), ""), AllowDuplicate: true})
	if err != nil {
		a.painFail(w, r, err)
		return
	}
	a.stampActor(r, actor.Idea, i.ID)
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	actor := pain.Actor{AccountID: accountID, Person: personOf(r), GitAnchor: gitanchor.Web()}
	if _, err := pa.AddPainLink(id, pain.Target{IdeaID: i.ID}, actor); err != nil {
		// Die Idee gibt es, nur die Verknüpfung fehlt: zur Idee führen und es sagen.
		http.Redirect(w, r, repoHref(p.Project)+"/ideas/"+strconv.FormatInt(i.ID, 10)+"?fail=pain_link", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, repoHref(p.Project)+"/ideas/"+strconv.FormatInt(i.ID, 10)+"?done=from_pain&pain="+url.QueryEscape(strconv.FormatInt(id, 10)), http.StatusSeeOther)
}
