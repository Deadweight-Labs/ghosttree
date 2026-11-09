package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Hilfen der Seiten aus Phase 2, Teil B (REQ-599): Wissen, Räume, Sessions,
// Agenten, Abläufe, Organisation und Anmeldung.

// repoScoped legt das Band eines Repositorys über eine Seite, die nicht unter
// /ui/{org}/{repo} liegt, aber zu einem Projekt gehört (?project=…, ein Eintrag,
// eine Session). tab ist der Reiter, der im Band offen steht. Das Band erscheint
// nur, wenn das Projekt ausdrücklich genannt ist (?project=, ein Eintrag), nicht
// schon durch die gemerkte Auswahl: dort bleibt die Projektauswahl im Kopf. Ohne
// Projekt, bei einem Projekt ohne Rolle des Betrachters oder wenn die Anfrage
// schon ein Band trägt, bleibt sie unverändert.
func (a *app) repoScoped(r *http.Request, project, tab string) *http.Request {
	if project == "" || repoFrom(r) != nil || (r.URL.Query().Get("project") == "" && r.URL.Query().Get("room") == "" && r.PathValue("id") == "") {
		return r
	}
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == project }) {
		return r
	}
	return a.withRepoBand(r, project, tab)
}

// withRepoBand puts the band of the repository over the page without asking
// whether the address named the project: the caller knows it does.
func (a *app) withRepoBand(r *http.Request, project, tab string) *http.Request {
	if repoFrom(r) != nil {
		return r
	}
	view := a.repoViewFor(r, a.access(r), a.shellBaseFor(r).who.kind, project, tab)
	return r.WithContext(context.WithValue(r.Context(), repoCtxKey{}, view))
}

// withQuery ist die Adresse der Anfrage mit geänderten Abfragewerten: ein leerer
// Wert entfernt den Schlüssel. Der Pfad bleibt der der Anfrage.
func withQuery(r *http.Request, set map[string]string) string {
	q := r.URL.Query()
	for k, v := range set {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	// Das Projekt eines Repository-Pfads steht schon im Pfad.
	if repoFrom(r) != nil && strings.HasPrefix(r.URL.Path, "/ui/") {
		q.Del("project")
	}
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

// pathWith hängt Abfragewerte an einen Pfad an; leere Werte fehlen.
func pathWith(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
