package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// orgMemberRow ist eine Mitgliederzeile mit der Frage, ob es der Betrachter ist.
type orgMemberRow struct {
	store.OrgMemberInfo
	Self bool
}

// projectRoleRow ist ein Organisationsmitglied mit seiner Rolle in einem Projekt.
// Grantable sind die Rollen, die der Betrachter ihm geben darf; leer heißt,
// dass die Zeile nur angezeigt wird.
type projectRoleRow struct {
	Account, AccountID string
	Role               string
	CanReview          bool
	Implicit           bool
	Grantable          []string
	Self               bool
}

type projectRolesView struct {
	Remote string
	You    string
	Rows   []projectRoleRow
}

// orgsView sammelt, was die Org-Seite zeigt.
type orgsView struct {
	Orgs       []store.Org
	Selected   store.Org
	Owner      bool
	Members    []orgMemberRow
	Invites    []store.Invitation
	Projects   []store.Project
	Roles      []projectRolesView
	NewCode    string // einmalig angezeigter Einladungscode
	NewExpiry  string
	NewLink    bool // der Code gehört zu einer Projekt-Einladung (/join/<code>)
	NewURL     string
	GuestLinks bool // Gast-Links gibt es nur bei durchgesetzter Sichtbarkeit
	Notice     string
}

// orgsPage zeigt Mitglieder, Einladungen und Projekte einer Organisation.
// Wer in mehreren ist, wählt über ?org=<slug>; sonst die Standard-Organisation.
func (a *app) orgsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.renderOrgs(w, r, http.StatusOK, orgsView{Notice: r.URL.Query().Get("notice")}, r.URL.Query().Get("org"), "")
}

func (a *app) renderOrgs(w http.ResponseWriter, r *http.Request, status int, v orgsView, ref, errMsg string) {
	w.Header().Set("Cache-Control", "no-store")
	me := browserPrincipal(r).ID
	orgs, err := a.store.ListOrgs(me)
	if err != nil {
		http.Error(w, "could not load organizations", http.StatusInternalServerError)
		return
	}
	v.Orgs = orgs
	for _, o := range orgs {
		if o.Slug == ref || (v.Selected.ID == 0 && o.Default && ref == "") {
			v.Selected = o
		}
	}
	if v.Selected.ID == 0 && len(orgs) > 0 {
		v.Selected = orgs[0]
	}
	if v.Selected.ID != 0 {
		v.Owner = v.Selected.Role == store.OrgOwner
		members, _ := a.store.ListOrgMembersFor(v.Selected.ID, me, a.store.AccessEnforced())
		v.GuestLinks = a.store.AccessEnforced()
		for _, m := range members {
			m.Account = store.NormalizeAccountName(m.Account)
			v.Members = append(v.Members, orgMemberRow{OrgMemberInfo: m, Self: m.AccountID == me})
		}
		var orgAll []store.OrgMemberInfo
		v.Projects, _ = a.store.ListProjects(me, v.Selected.ID)
		v.Projects = a.access(r).VisibleProjects(v.Projects)
		for _, p := range v.Projects {
			rv := projectRolesView{Remote: p.Remote, You: a.store.ProjectRole(p.Remote, me).Role}
			// Ein Gast sieht nur den eigenen Eintrag, wie in der API
			// (listProjectMembers).
			guest := store.RoleRank(a.access(r).Role(p.Remote).Role) == 1 && a.store.AccessEnforced()
			// Die Zeilen kommen aus den Mitgliedern des Projekts, nicht aus der
			// Org-Liste: sonst stünde jedes Org-Mitglied mit "none" darin.
			projectMembers, _ := a.store.ListProjectMembers(p.Remote)
			byID := map[string]store.ProjectMember{}
			for _, m := range projectMembers {
				byID[m.AccountID] = m
			}
			rows := projectMembers
			// Die ganze Org sehen, um auch Konten ohne Rolle eine zu geben: ein
			// Owner, jeder ohne durchgesetzte Sichtbarkeit (dort sehen alle
			// ohnehin alles) und ein Lead in seinem Projekt (vom Owner
			// eingesetzt). Member und Gast sehen die Mitglieder des Projekts.
			if v.Owner || !a.store.AccessEnforced() || rv.You == store.RoleLead {
				if orgAll == nil {
					orgAll, _ = a.store.ListOrgMembers(v.Selected.ID)
				}
				rows = rows[:0:0]
				for _, m := range orgAll {
					if pm, ok := byID[m.AccountID]; ok {
						rows = append(rows, pm)
					} else {
						rows = append(rows, store.ProjectMember{AccountID: m.AccountID, Account: m.Account})
					}
				}
			}
			for _, m := range rows {
				if guest && m.AccountID != me {
					continue
				}
				rv.Rows = append(rv.Rows, projectRoleRow{
					Account: store.NormalizeAccountName(m.Account), AccountID: m.AccountID, Role: m.Role, CanReview: m.CanReview,
					Implicit: m.Implicit, Grantable: a.grantable(r, me, p.Remote, m.AccountID), Self: m.AccountID == me,
				})
			}
			v.Roles = append(v.Roles, rv)
		}
		if v.Owner {
			v.Invites, _ = a.store.ListInvitations(me, v.Selected.ID)
		}
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	a.renderBrowser(w, r, "orgs", pageData{Title: "Organizations", Error: errMsg, Orgs: v})
}

// orgError übersetzt Store-Fehler in eine Meldung und einen Statuscode.
func orgError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotOrgOwner):
		return http.StatusForbidden, "Only an organization owner can do that."
	case errors.Is(err, store.ErrOrgNotFound), errors.Is(err, store.ErrNotOrgMember):
		return http.StatusNotFound, "Organization not found."
	case errors.Is(err, store.ErrLastOrgOwner):
		return http.StatusConflict, "An organization needs at least one owner."
	case errors.Is(err, store.ErrNotGrantor):
		return http.StatusForbidden, "Only a project owner or lead can change roles."
	case errors.Is(err, store.ErrRoleForbidden):
		return http.StatusForbidden, "You may not give that role to that account."
	case errors.Is(err, store.ErrSelfPromotion):
		return http.StatusForbidden, "You cannot raise your own role."
	case errors.Is(err, store.ErrLastProjectOwner):
		return http.StatusConflict, "A project needs at least one owner."
	case errors.Is(err, store.ErrImplicitOwner):
		return http.StatusConflict, "Organization owners are implicit project owners; change their organization role instead."
	case errors.Is(err, store.ErrNoProjectRole):
		return http.StatusNotFound, "That account holds no role in this project."
	case errors.Is(err, store.ErrProjectNotFound):
		return http.StatusNotFound, "Project not found."
	case errors.Is(err, store.ErrInvalidInput):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, store.ErrGuestLinkNeedsEnforcement):
		return http.StatusConflict, "Guest links are not available: this server does not enforce project visibility (GHOSTTREE_ENFORCE_ACCESS=1 is not set), so a guest would see more than the invitation promises. Create a member link, or ask the operator to turn enforcement on."
	case errors.Is(err, store.ErrTooManyInvites):
		return http.StatusConflict, "Too many pending invitations; revoke some first."
	case errors.Is(err, store.ErrCodeInvalid):
		return http.StatusBadRequest, "That code is invalid, expired or already used."
	case errors.Is(err, store.ErrInvitationEmail):
		return http.StatusForbidden, "That invitation is bound to another email address."
	case errors.Is(err, store.ErrAlreadyMember):
		return http.StatusConflict, "You are already a member of that organization."
	case errors.Is(err, store.ErrTooManyAttempts):
		return http.StatusTooManyRequests, "Too many wrong codes. Try again in a few minutes."
	}
	return http.StatusInternalServerError, "Something went wrong."
}

func (a *app) orgFailure(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := orgError(err)
	a.renderOrgs(w, r, status, orgsView{}, r.FormValue("org"), msg)
}

// formOrg löst das versteckte Feld "org" auf, nur unter den Organisationen des
// Betrachters.
func (a *app) formOrg(r *http.Request) (store.Org, error) {
	o, err := a.store.OrgByRef(r.FormValue("org"))
	if err != nil || a.store.OrgRole(o.ID, browserPrincipal(r).ID) == "" {
		return store.Org{}, store.ErrOrgNotFound
	}
	return o, nil
}

func orgRedirect(w http.ResponseWriter, r *http.Request, slug, notice string) {
	q := url.Values{"org": {slug}}
	if notice != "" {
		q.Set("notice", notice)
	}
	http.Redirect(w, r, "/ui/orgs?"+q.Encode(), http.StatusSeeOther)
}

func (a *app) orgInvite(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	if days < 0 || days > int(store.MaxInvitationTTL/(24*time.Hour)) {
		a.orgFailure(w, r, store.ErrInvalidInput)
		return
	}
	ttl := time.Duration(days) * 24 * time.Hour
	var code string
	var inv store.Invitation
	link := false
	if project := strings.TrimSpace(r.FormValue("project")); project != "" {
		// Ein Link vergibt nur member oder guest für dieses eine Projekt.
		link = true
		code, inv, err = a.store.CreateProjectInvitation(browserPrincipal(r).ID, o.ID, project, r.FormValue("project_role"), ttl)
	} else {
		code, inv, err = a.store.CreateInvitation(browserPrincipal(r).ID, o.ID, r.FormValue("email"), r.FormValue("role"), ttl)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	// Der Code erscheint nur in dieser Antwort, nicht in einer URL oder einem
	// Redirect.
	a.renderOrgs(w, r, http.StatusOK, orgsView{NewCode: code, NewExpiry: inv.ExpiresAt, NewLink: link, NewURL: a.joinURL(code)}, o.Slug, "")
}

func (a *app) orgInviteRevoke(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		var id int64
		if id, err = strconv.ParseInt(r.FormValue("id"), 10, 64); err != nil {
			err = store.ErrInvalidInput
		} else {
			err = a.store.RevokeInvitation(browserPrincipal(r).ID, o.ID, id)
		}
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "Invitation revoked.")
}

func (a *app) orgMemberRole(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		err = a.store.SetOrgRole(browserPrincipal(r).ID, o.ID, r.FormValue("account"), r.FormValue("role"))
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "Role updated.")
}

func (a *app) orgMemberRemove(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		err = a.store.RemoveOrgMember(browserPrincipal(r).ID, o.ID, r.FormValue("account"))
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	if a.store.OrgRole(o.ID, browserPrincipal(r).ID) == "" {
		http.Redirect(w, r, "/ui/orgs", http.StatusSeeOther)
		return
	}
	orgRedirect(w, r, o.Slug, "Member removed.")
}

func (a *app) orgProjectMove(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		var p store.Project
		if p, err = a.store.MoveProject(browserPrincipal(r).ID, r.FormValue("remote"), r.FormValue("to")); err == nil {
			orgRedirect(w, r, o.Slug, p.Remote+" moved to "+p.Org+".")
			return
		}
	}
	a.orgFailure(w, r, err)
}

func (a *app) orgAccept(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.FormValue("code"))
	// Eine Projekt-Einladung geht nur über die Einladungsseite: dort verlangt der
	// Beitritt eine interaktive Sitzung und die Bestätigung des Kontos.
	if wellFormedJoinCode(code) && a.store.OpenProjectInvitation(code, a.store.AccessEnforced()) {
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}
	o, err := a.store.AcceptInvitation(browserPrincipal(r).ID, code)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "You joined "+o.Name+".")
}

func (a *app) orgDefault(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		err = a.store.SetDefaultOrg(browserPrincipal(r).ID, o.ID)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "Default organization set.")
}

// orgProjectRole setzt oder entfernt (role=none) die Projektrolle eines
// Organisationsmitglieds. Die Regeln prüft der Store; die Oberfläche zeigt nur,
// was er erlaubt.
func (a *app) orgProjectRole(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		p, known := a.store.ProjectByRemote(r.FormValue("remote"))
		switch {
		case !known || p.OrgID != o.ID:
			err = store.ErrProjectNotFound
		case r.FormValue("role") == "none":
			err = a.store.RemoveProjectRole(browserPrincipal(r).ID, p.Remote, r.FormValue("account"), store.RoleViaWeb)
		default:
			err = a.store.SetProjectRole(browserPrincipal(r).ID, p.Remote, r.FormValue("account"), r.FormValue("role"), r.FormValue("review") == "1", store.RoleViaWeb)
		}
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "Project role updated.")
}

// grantable nennt die wählbaren Rollen, aber nur in einer interaktiven Sitzung:
// aus eingefügtem Token zeigt die Seite keine Rollenformulare.
func (a *app) grantable(r *http.Request, actor, remote, target string) []string {
	if !interactive(r) {
		return nil
	}
	return a.store.GrantableRoles(actor, remote, target)
}

// joinURL ist der Einladungslink; mit gesetzter GHOSTTREE_PUBLIC_URL absolut.
func (a *app) joinURL(code string) string { return a.publicOrigin + "/join/" + code }
