package web

import (
	"errors"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// profileView is what the profile page shows: the name as the store has it.
type profileView struct {
	Name   string
	Notice string
	Locked bool // guests cannot rename themselves
}

// profilePage shows the account's display name and, in an interactive session,
// the form to change it.
func (a *app) profilePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.renderProfile(w, r, http.StatusOK, "", "")
}

func (a *app) renderProfile(w http.ResponseWriter, r *http.Request, status int, typed, errMsg string) {
	me := browserPrincipal(r)
	acct, err := a.store.AccountByPrincipalID(me.ID)
	if err != nil {
		http.Error(w, msg("adm.load_failed"), http.StatusInternalServerError)
		return
	}
	v := profileView{Name: acct.Name, Locked: a.store.NameLocked(me.ID)}
	if typed != "" {
		v.Name = typed
	}
	if r.URL.Query().Get("notice") == "saved" {
		v.Notice = msg("profile.saved")
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	a.renderBrowser(w, r, "profile", pageData{Title: msg("profile.title"), Error: errMsg, Profile: v})
}

// profileSave changes the display name. The store cleans and checks it; a
// rejected name is shown again with the reason, never echoed unescaped.
func (a *app) profileSave(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r)
	acct, err := a.store.SetOwnName(me.ID, r.FormValue("name"))
	switch {
	case err == nil:
		a.sessions.relabel(acct.ID, acct.Name)
		http.Redirect(w, r, "/ui/profile?notice=saved", http.StatusSeeOther)
	case errors.Is(err, store.ErrAccountNameTaken):
		a.renderProfile(w, r, http.StatusConflict, r.FormValue("name"), msg("profile.err.taken"))
	case errors.Is(err, store.ErrNameRateLimited):
		a.renderProfile(w, r, http.StatusTooManyRequests, "", msg("profile.err.rate"))
	case errors.Is(err, store.ErrNameNotAllowed):
		a.renderProfile(w, r, http.StatusForbidden, "", msg("profile.err.locked"))
	case errors.Is(err, store.ErrInvalidInput):
		a.renderProfile(w, r, http.StatusBadRequest, r.FormValue("name"), msg("profile.err.invalid"))
	default:
		http.Error(w, msg("adm.err.generic"), http.StatusInternalServerError)
	}
}
