package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The GitHub link of the profile page (REQ-613). A person proves who they are on
// GitHub (the same login flow and callback address as "connect an organization",
// told apart by the purpose of the flow), and the GitHub login and number GitHub
// itself answers are stored for that person's own account. No form field and no
// query parameter names the login: whatever the browser sends can only say "yes,
// this is the flow I started". The access token of the person is gone when the
// callback has read who they are.

// profileGitHub is the GitHub block of the profile page.
type profileGitHub struct {
	Linked bool
	Login  string
	Href   string
	// CanLink: nobody is linked yet and the server can ask GitHub. CanUnlink:
	// somebody is linked and this session may remove it.
	CanLink, CanUnlink bool
}

// githubBlock is what the profile page shows for the signed-in account; ok is
// false when there is nothing to show (no link, and no way to make one).
func (a *app) githubBlock(r *http.Request) (profileGitHub, bool) {
	me := browserPrincipal(r).ID
	may := interactive(r) && !a.store.NameLocked(me)
	if link, ok := a.store.AccountGitHub(me); ok {
		return profileGitHub{Linked: true, Login: link.Login, Href: "https://github.com/" + url.PathEscape(link.Login), CanUnlink: may}, true
	}
	if may && a.githubLinkReady() {
		return profileGitHub{CanLink: true}, true
	}
	return profileGitHub{}, false
}

// githubLinkReady: the server has GitHub credentials, and they can tell who logged in.
func (a *app) githubLinkReady() bool {
	_, ok := a.prover.(forge.Identifier)
	return ok && a.sourceReady()
}

func profileNotice(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/ui/profile?"+url.Values{"notice": {notice}}.Encode(), http.StatusSeeOther)
}

// guestAccount: guests have no name of their own and no GitHub link either.
func (a *app) guestAccount(me string) bool { return a.store.NameLocked(me) }

// profileGitHubLink sends the browser to GitHub to prove the login. The flow is
// bound to this account; the callback finishes it.
func (a *app) profileGitHubLink(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r).ID
	if a.guestAccount(me) {
		http.Error(w, a.langFor(r).msg("profile.err.locked"), http.StatusForbidden)
		return
	}
	if !a.githubLinkReady() {
		profileNotice(w, r, "github_failed")
		return
	}
	state, err := a.sourceFlows.startLink(me)
	if err != nil {
		profileNotice(w, r, "github_failed")
		return
	}
	http.Redirect(w, r, a.prover.AuthorizeURL(state, a.callbackURL(r)), http.StatusSeeOther)
}

// finishGitHubLink is the return from GitHub of a flow with the purpose link; the
// flow was taken (single use, bound to me) by the caller.
func (a *app) finishGitHubLink(w http.ResponseWriter, r *http.Request, me string) {
	code := r.URL.Query().Get("code")
	ident, canIdentify := a.prover.(forge.Identifier)
	if !canIdentify || !a.sourceReady() || code == "" || a.guestAccount(me) {
		profileNotice(w, r, "github_failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	who, err := ident.Identify(ctx, code, a.callbackURL(r))
	if err != nil || who.ID <= 0 || !store.ValidGitHubLogin(who.Login) {
		profileNotice(w, r, "github_failed")
		return
	}
	if err := a.store.SetAccountGitHub(me, who.Login, who.ID); err != nil {
		profileNotice(w, r, "github_failed")
		return
	}
	a.forgetAvatar(me)
	profileNotice(w, r, "github_linked")
}

// profileGitHubUnlink removes the GitHub link of the signed-in account, and with
// it the picture the server kept.
func (a *app) profileGitHubUnlink(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r).ID
	if a.guestAccount(me) {
		http.Error(w, a.langFor(r).msg("profile.err.locked"), http.StatusForbidden)
		return
	}
	if err := a.store.ClearAccountGitHub(me); err != nil {
		http.Error(w, a.langFor(r).msg("adm.err.generic"), http.StatusInternalServerError)
		return
	}
	a.forgetAvatar(me)
	profileNotice(w, r, "github_unlinked")
}

// forgetAvatar drops what the server kept of the picture of an account.
func (a *app) forgetAvatar(account string) {
	if id, ok := store.AccountNumericID(account); ok && a.avatars != nil {
		a.avatars.forget(id)
	}
}
