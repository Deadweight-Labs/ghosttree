package web

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// orgCookie remembers the organization the viewer worked in last, so that the
// "+" menu and "Team & devices" open it and not the empty standard organization.
// The value is only a wish: it counts when the viewer is a member.
const orgCookie = "gt_org"

type orgCtxKey struct{}

func (a *app) orgCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: orgCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// enterOrg says that this request is about the organization, remembers it for the
// next pages, and returns the request the shell is built from.
func (a *app) enterOrg(w http.ResponseWriter, r *http.Request, slug string) *http.Request {
	a.rememberOrg(w, r, slug)
	return r.WithContext(context.WithValue(r.Context(), orgCtxKey{}, slug))
}

func (a *app) rememberOrg(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet || slug == "" {
		return
	}
	if c, err := r.Cookie(orgCookie); err == nil && c.Value == slug {
		return
	}
	http.SetCookie(w, a.orgCookieFor(r, slug, 90*24*60*60))
}

// rememberRepoOrg remembers the organization of the repository the page belongs to.
func (a *app) rememberRepoOrg(w http.ResponseWriter, r *http.Request) {
	if rv := repoFrom(r); rv != nil && rv.Project != "" {
		if p, ok := a.store.ProjectByRemote(rv.Project); ok {
			a.rememberOrg(w, r, p.Org)
		}
	}
}

// workingOrg is the organization of this page: the one named by the request
// (an organization page), the one of the repository, else the last one used.
func (a *app) workingOrg(r *http.Request, orgs []store.Org) (store.Org, bool) {
	slug, _ := r.Context().Value(orgCtxKey{}).(string)
	if slug == "" {
		if rv := repoFrom(r); rv != nil && rv.Project != "" {
			if p, ok := a.store.ProjectByRemote(rv.Project); ok {
				slug = p.Org
			}
		}
	}
	if slug == "" {
		if c, err := r.Cookie(orgCookie); err == nil {
			slug = c.Value
		}
	}
	for _, o := range orgs {
		if slug != "" && o.Slug == slug {
			return o, true
		}
	}
	return store.Org{}, false
}

// orgsOf are the organizations of the viewer that belong in the menus: a guest
// sees their project only (14 A, #4376).
func (a *app) orgsOf(r *http.Request) []store.Org {
	all, err := a.store.ListOrgs(browserPrincipal(r).ID)
	if err != nil {
		return nil
	}
	var orgs []store.Org
	for _, o := range all {
		if !o.Guest {
			orgs = append(orgs, o)
		}
	}
	return orgs
}

// defaultOrgSlug is the slug of the organization the first account of an instance
// is given. Not every account has it as its standard organization (Org.Default),
// so the slug counts, too.
const defaultOrgSlug = "default"

// placeholderOrg is the standard organization an account was given that still
// holds nothing: no project and nobody else. It is no place to work in once the
// person has an organization of their own.
func (a *app) placeholderOrg(me string, o store.Org) bool {
	if !(o.Default || o.Slug == defaultOrgSlug) || o.IsUser() {
		return false
	}
	if ps, _ := a.store.ListProjects(me, o.ID); len(ps) > 0 {
		return false
	}
	members, _ := a.store.ListOrgMembers(o.ID)
	return len(members) <= 1
}

// pickOrg is the organization the viewer works in on a page that names none: the
// one of this request or the last one used, else the first that holds a project
// (the standard organization first), else one that is really theirs: an empty
// organization they made or joined wins over the empty standard organization
// their account was given (REQ-683, "You are in Default" after every sign-in),
// else the standard one.
func (a *app) pickOrg(r *http.Request, orgs []store.Org) store.Org {
	if len(orgs) == 0 {
		return store.Org{}
	}
	if o, ok := a.workingOrg(r, orgs); ok {
		return o
	}
	me := browserPrincipal(r).ID
	var def *store.Org
	for i, o := range orgs {
		if o.Default && def == nil {
			def = &orgs[i]
		}
	}
	// The standard organization first, when it holds anything; then the others.
	ordered := orgs
	if def != nil {
		ordered = append([]store.Org{*def}, orgs...)
	}
	for _, o := range ordered {
		if o.IsUser() {
			continue
		}
		if ps, _ := a.store.ListProjects(me, o.ID); len(ps) > 0 {
			return o
		}
	}
	for _, o := range orgs {
		if !o.IsUser() && !a.placeholderOrg(me, o) {
			return o
		}
	}
	if def != nil && !def.IsUser() {
		return *def
	}
	for _, o := range orgs {
		if !o.IsUser() {
			return o
		}
	}
	return orgs[0]
}

// orgSettingsHref is the settings of an organization, at a section.
func orgSettingsHref(o store.Org, fragment string) string {
	return "/ui/orgs?org=" + url.QueryEscape(o.Slug) + fragment
}
