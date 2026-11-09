package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Kanonische Adressen von Wissen und Sitzungen (Entscheidung #4331): unter dem
// Repository, in dem sie liegen. Die alten Adressen /ui/knowledge/{id} und
// /ui/sessions/{id} prüfen zuerst das Recht (wer die Seite nicht lesen darf, bekommt
// 404 wie bisher) und leiten dann auf die neue weiter; Einträge ohne Repository
// bleiben an der alten Adresse.

// knowledgeHref ist die Adresse eines Wissenseintrags: unter seinem Repository, ohne
// Projekt an der alten Adresse.
func knowledgeHref(project string, id int64) string {
	if !repoPathOK(project) {
		return "/ui/knowledge/" + strconv.FormatInt(id, 10)
	}
	return repoHref(project) + "/knowledge/" + strconv.FormatInt(id, 10)
}

// sessionPageHref ist die Adresse einer Sitzung nach ihrer öffentlichen Kennung.
func sessionPageHref(project, publicID string) string {
	if !repoPathOK(project) {
		return "/ui/sessions/" + publicID
	}
	return repoHref(project) + "/sessions/" + publicID
}

// repoPathOK sagt, ob ein Projekt die Form host/owner/repo hat, die eine
// Adresse unter /ui/{org}/{repo} tragen kann. Ein Projekt ohne diese Form (ein
// freier Name) bleibt an den alten Adressen.
func repoPathOK(project string) bool {
	return strings.Count(scope.NormalizeRemote(project), "/") == 2
}

// withRawQuery hängt die Abfrage der Anfrage an eine Adresse, damit eine
// Weiterleitung Suche, Bearbeitung und Sprungmarken behält.
func withRawQuery(target string, r *http.Request) string {
	if r.URL.RawQuery == "" {
		return target
	}
	return target + "?" + r.URL.RawQuery
}

// knowledgeInRepo ist /ui/{org}/{repo}/knowledge/{id}. Ein Eintrag eines anderen
// Repositories führt auf dessen Adresse, wenn der Betrachter ihn lesen und das
// Repository öffnen darf, sonst ist er hier unbekannt.
func (a *app) knowledgeInRepo(w http.ResponseWriter, r *http.Request, remote, id string) {
	r.SetPathValue("id", id)
	k, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	if k.Scope.Project != remote {
		if k.Scope.Project != "" && a.canOpenRepo(r, a.access(r), k.Scope.Project) {
			http.Redirect(w, r, withRawQuery(knowledgeHref(k.Scope.Project, k.ID), r), http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	a.knowledgeItemPage(w, r)
}

// sessionInRepo ist /ui/{org}/{repo}/sessions/{id}, mit derselben Regel.
func (a *app) sessionInRepo(w http.ResponseWriter, r *http.Request, remote, id string) {
	r.SetPathValue("id", id)
	sess, ok := a.lookupSession(r, a.access(r))
	if !ok {
		a.sessionMissing(w, r)
		return
	}
	if sess.Scope.Project != remote {
		if sess.Scope.Project != "" && a.canOpenRepo(r, a.access(r), sess.Scope.Project) {
			http.Redirect(w, r, withRawQuery(sessionPageHref(sess.Scope.Project, sess.PublicID), r), http.StatusFound)
			return
		}
		a.sessionMissing(w, r)
		return
	}
	a.sessionPage(w, r)
}
