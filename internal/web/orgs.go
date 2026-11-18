package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// orgMemberRow ist eine Mitgliederzeile mit der Frage, ob es der Betrachter ist.
type orgMemberRow struct {
	store.OrgMemberInfo
	Self    bool
	Initial string
	// SoleOwner: the only owner of the organization, who can neither step
	// down nor leave. NoProject: a member without a role in any project of the
	// organization, shown to owners with a way to add one.
	SoleOwner, NoProject bool
	// LoginLink is the one-time sign-in link an owner made for this member, still
	// to copy (REQ-676); CanLoginLink says the owner may make one.
	LoginLink    string
	CanLoginLink bool
	// Service: an account that is no person (a background account set up
	// for the organization); the list says so.
	Service bool
	// Projects are the person's roles in the projects of the organization the
	// viewer sees, each with the form to change it where the viewer may
	// (REQ-704): the member list is the one place for every role of a person.
	Projects []memberProjectRole
}

// memberProjectRole is one project role in a member's row.
type memberProjectRole struct {
	projectRoleRow
	Remote, Short string
}

// inviteRow is an invitation with, for its creator while it is open, the link.
type inviteRow struct {
	store.Invitation
	URL string
	// Mail is what became of the mail of an invitation whose link is still held;
	// Subject is whom it is for (address, project or "single-use link").
	Mail    inviteMail
	Subject string
	// Named: the row names the person who accepted, not the target.
	Named string
}

// guestOrgRow is an organization the viewer is a guest of, with the projects
// shared with them.
type guestOrgRow struct {
	Name     string
	Projects []guestProjectRow
}

type guestProjectRow struct{ Remote, Href string }

// projectRoleRow ist ein Organisationsmitglied mit seiner Rolle in einem Projekt.
// Grantable sind die Rollen, die der Betrachter ihm geben darf; leer heißt,
// dass die Zeile nur angezeigt wird.
type projectRoleRow struct {
	Account, AccountID string
	Role               string
	CanReview          bool
	Implicit           bool
	Inherited          bool
	Grantable          []string
	Self               bool
	RowID              string
}

type projectRolesView struct {
	Remote string
	You    string
	// Shared: mit der Org geteilt. CanShare: der Betrachter darf das ändern.
	Shared, CanShare bool
	// Toggle ist der Wert, auf den der Schalter das Projekt stellt.
	Toggle string
	Rows   []projectRoleRow
}

// orgsView sammelt, was die Org-Seite zeigt.
type orgsView struct {
	// Retention is how many days the full text of workflow runs stays; set
	// only for an owner, who alone may change it (REQ-693).
	Retention int
	// Policy is the owner's policy for the workflows (REQ-695).
	Policy    *wfPolicyView
	Orgs      []store.Org
	Selected  store.Org
	Owner     bool
	CanRename bool // Owner oder Instanz-Admin
	Members   []orgMemberRow
	Invites   []inviteRow
	Projects  []store.Project
	Claimable []string // eigene, unbeanspruchte Remotes; nur für Owner
	Roles     []projectRolesView
	NewExpiry string
	// CanBind: an invitation can be bound to an address, because this server can
	// confirm one (mail or an identity provider). Without it the form says so.
	CanBind bool
	NewURL  string // Link der eben erstellten Einladung (?new=), nur für ihren Ersteller
	// NewMail is what became of its mail; NewNotice the sentence about it, empty
	// when the mail went out.
	NewMail   inviteMail
	NewNotice string
	// Open names the section that opens when the address names none ("members").
	Open string
	// GuestOf are the organizations the viewer is a guest of.
	GuestOf []guestOrgRow
	// InviteProjects are the projects an invitation can be for (those of the
	// organization plus the owner's unclaimed ones); DefaultProject is the
	// one preselected: the last one invited to, else the first.
	InviteProjects []string
	DefaultProject string
	GuestLinks     bool // Gast-Links gibt es nur bei durchgesetzter Sichtbarkeit
	// Quellen: verbundene GitHub-Organisationen, ihre Präfixe und, nach der
	// Rückkehr von GitHub, die zur Wahl stehenden Organisationen.
	Sources     []sourceRow
	SourceReady bool
	Pick        *sourcePick
	Notice      string
	// NoticeBad: the notice reports a failure and is shown as one.
	NoticeBad bool
	// HitRow is the id of the row the last action changed (marked on the page);
	// OpenRemote the project whose roles stay open after it.
	HitRow, OpenRemote string
	// NewFor is whom the invitation just made is for.
	NewFor string
	// NewOrg: the page shows only the form for a new organization.
	NewOrg bool
	// Explain is the administrator's block on "Kurz erklärt"; nil for anyone else.
	Explain *explainAdminView
	// Garden is the same for the gardener; nil for anyone else.
	Garden *gardenAdminView
	// Transcripts is the same for the stored transcripts; nil for anyone else.
	Transcripts *transcriptsAdminView
	// AI is the section "AI connections" of the namespace (REQ-694); AIServer
	// the server's, for the instance admin.
	AI       *aiView
	AIServer *aiServerView
	// AIForm is a connection form a refusal sends back, with what it found.
	AIForm *aiFormView
	// GardenSet is the gardener's settings section, for the owner (REQ-624).
	GardenSet *gardenSettingsView
	// Moved: the viewer's namespaces that had to yield their address to its
	// verified GitHub owner (decision E10), shown once.
	Moved []string
	// Assign is the instance admin's form to set a project's owner: a known
	// project is reassigned, an unknown remote decided (contested). Only here,
	// from an interactive session; the API refuses both.
	Assign []store.Org
}

// OpenInvites are the invitations still waiting; PastInvites the accepted,
// revoked and expired ones, folded away: who accepted stands in the member
// list already (REQ-704, P27).
func (v orgsView) OpenInvites() []inviteRow {
	return keep(v.Invites, 0, func(i inviteRow) bool { return i.Status == "pending" })
}
func (v orgsView) PastInvites() []inviteRow {
	return keep(v.Invites, 0, func(i inviteRow) bool { return i.Status != "pending" })
}

// PastOpen: the last action changed a past invitation (a revoke), so the
// folded list opens on it.
func (v orgsView) PastOpen() bool {
	for _, i := range v.PastInvites() {
		if "invite-"+strconv.FormatInt(i.ID, 10) == v.HitRow {
			return true
		}
	}
	return false
}

// People are the members that are no guests, Guests the others.
func (v orgsView) People() []orgMemberRow {
	return keep(v.Members, 0, func(m orgMemberRow) bool { return !m.Guest })
}
func (v orgsView) Guests() []orgMemberRow {
	return keep(v.Members, 0, func(m orgMemberRow) bool { return m.Guest })
}

// orgsPage zeigt Mitglieder, Einladungen und Projekte einer Organisation.
// Wer in mehreren ist, wählt über ?org=<slug>; sonst die Standard-Organisation.
func (a *app) orgsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	key := r.URL.Query().Get("notice")
	v := orgsView{Notice: orgNotice(l, key), NoticeBad: key == "source_failed"}
	// The note of the action that led here names the person and the object; the
	// generic text above stays for a link without one.
	if n, ok := adminNotes.take(r.URL.Query().Get("done"), browserPrincipal(r).ID); ok {
		v.Notice, v.HitRow, v.OpenRemote = n.Text, n.Row, n.Open
	}
	a.renderOrgs(w, r, http.StatusOK, v, r.URL.Query().Get("org"), "")
}

// orgNewPage is the form for a new organization. The "+" menu and the
// organization menu of the header lead here; the settings of an organization
// do not offer it.
func (a *app) orgNewPage(w http.ResponseWriter, r *http.Request) {
	a.renderOrgs(w, r, http.StatusOK, orgsView{NewOrg: true}, "", "")
}

func (a *app) renderOrgs(w http.ResponseWriter, r *http.Request, status int, v orgsView, ref, errMsg string) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	me := browserPrincipal(r).ID
	orgs, err := a.store.ListOrgs(me)
	if err != nil {
		http.Error(w, l.msg("adm.load_failed"), http.StatusInternalServerError)
		return
	}
	if notices, err := a.store.NamespaceNotices(me); err == nil && len(notices) > 0 {
		for _, n := range notices {
			v.Moved = append(v.Moved, l.msg("adm.notice.slug_yielded", n.OldSlug, n.NewSlug))
		}
		{
			_ = a.store.MarkNamespaceNoticesSeen(me)
		}
	}
	// Guests manage nothing and see no organization page (14 A, #4376).
	for _, o := range orgs {
		if !o.Guest {
			v.Orgs = append(v.Orgs, o)
			continue
		}
		// The page says what a guest is in: the organization and the projects shared
		// with them, each a way in.
		g := guestOrgRow{Name: o.Name}
		ps, _ := a.store.ListProjects(me, o.ID)
		for _, p := range a.access(r).VisibleProjects(ps) {
			g.Projects = append(g.Projects, guestProjectRow{Remote: p.Remote, Href: repoHref(p.Remote)})
		}
		v.GuestOf = append(v.GuestOf, g)
	}
	orgs = v.Orgs
	v.Explain = a.explainAdminFor(r)
	v.Garden = a.gardenAdminFor(r)
	v.Transcripts = a.transcriptsAdminFor(r)
	v.AIServer = a.aiServerFor(r, v.AIForm)
	if a.access(r).CanInstance(store.RightInstanceAdmin) && browserPrincipal(r).May(store.RightOrgProjects) {
		v.Assign, _ = a.store.AllOwners()
	}
	for _, o := range orgs {
		if o.Slug == ref {
			v.Selected = o
		}
	}
	// Without a choice: the one the viewer works in (the last used), else the first
	// that holds a project; a personal namespace only when there is nothing else.
	if v.Selected.ID == 0 && len(orgs) > 0 {
		v.Selected = a.pickOrg(r, orgs)
	}
	if v.Selected.ID != 0 && !v.NewOrg {
		r = a.enterOrg(w, r, v.Selected.Slug)
	}
	if v.Selected.ID != 0 {
		v.GardenSet = a.gardenSettingsFor(r, v.Selected)
		v.AI = a.aiViewFor(r, v.Selected, v.AIForm)
		v.Owner = store.OrgRoleHas(v.Selected.Role, store.RightOrgManage)
		if v.Owner {
			v.Retention = a.store.WorkflowSettingsFor(v.Selected.ID).RetentionDays
			v.Policy = a.wfPolicyViewOf(r, v.Selected, a.store.WorkflowPolicyFor(v.Selected.ID))
		}
		v.CanRename = v.Owner || a.isAdmin(browserPrincipal(r))
		members, _ := a.store.ListOrgMembersFor(v.Selected.ID, me, a.store.AccessEnforced())
		v.GuestLinks = a.store.AccessEnforced()
		owners := 0
		for _, m := range members {
			if store.IsOrgOwnerRole(m.Role) {
				owners++
			}
		}
		gardener := ""
		if id := a.store.GardenerAccountID(); id > 0 {
			gardener = "person:" + strconv.FormatInt(id, 10)
		}
		for _, m := range members {
			// The gardener is a feature with its own tab, not a member
			// (decision E6).
			if gardener != "" && m.AccountID == gardener {
				continue
			}
			m.Account = store.NormalizeAccountName(m.Account)
			row := orgMemberRow{OrgMemberInfo: m, Self: m.AccountID == me, Initial: initialOf(m.Account),
				SoleOwner: store.IsOrgOwnerRole(m.Role) && owners == 1, Service: m.Kind == store.AccountKindService}
			// An owner can hand a member a one-time sign-in link: the way back in
			// when the instance cannot send mail. Not for owners, services or oneself.
			if orgMay(r, v.Selected, store.RightOrgMembers) && store.IsOrgPlainMemberRole(m.Role) && !row.Service && m.AccountID != me && a.store.LoginLinkAllowed(v.Selected.ID, m.AccountID) {
				row.CanLoginLink = true
				row.LoginLink = a.loginLinkFor(r, me, m.AccountID)
			}
			v.Members = append(v.Members, row)
		}
		var orgAll []store.OrgMemberInfo
		v.Projects, _ = a.store.ListProjects(me, v.Selected.ID)
		v.Projects = a.access(r).VisibleProjects(v.Projects)
		for _, p := range v.Projects {
			rv := projectRolesView{Remote: p.Remote, You: a.store.ProjectRole(p.Remote, me).Role, Shared: p.Shared}
			rv.Toggle = map[bool]string{true: "0", false: "1"}[p.Shared]
			rv.CanShare = a.access(r).Can(p.Remote, store.RightProjectShare)
			// Ein Gast sieht nur den eigenen Eintrag, wie in der API
			// (listProjectMembers).
			guest := a.access(r).Role(p.Remote).GuestOnly() && a.store.AccessEnforced()
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
			// ohnehin alles) und ein Lead oder Projekt-Owner in seinem Projekt (vom Owner
			// eingesetzt). Member und Gast sehen die Mitglieder des Projekts.
			if v.Owner || !a.store.AccessEnforced() || (store.RoleInfo{Role: rv.You}).Has(store.RightRolesGrant) {
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
					Implicit: m.Implicit, Inherited: m.Inherited, Grantable: a.grantable(r, me, p.Remote, m.AccountID), Self: m.AccountID == me,
					RowID: rowAnchor("pr", p.Remote+"-"+m.AccountID),
				})
			}
			v.Roles = append(v.Roles, rv)
		}
		v.Members = attachProjectRoles(v.Members, v.Roles, orgAll, gardener)
		a.fillSources(r, &v, me)
		if v.Owner {
			v.Claimable, _ = a.store.ListClaimableProjects(me, v.Selected.ID)
			a.fillInvites(r, &v, me)
			// Members who hold no role in any project see nothing; say so and
			// let the owner fix it in one click.
			inProject := map[string]bool{}
			for _, rv := range v.Roles {
				for _, row := range rv.Rows {
					if row.Role != "" {
						inProject[row.AccountID] = true
					}
				}
			}
			for i, m := range v.Members {
				v.Members[i].NoProject = !store.IsOrgOwnerRole(m.Role) && !inProject[m.AccountID] && len(v.Projects) > 0
			}
		}
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	data := pageData{Title: l.msg("adm.org.title"), Error: errMsg, Orgs: v}
	if v.Selected.ID != 0 && !v.NewOrg {
		data.OrgBand, data.OrgName = a.orgBandFor(r, v.Selected, "settings"), v.Selected.Name
	}
	a.renderBrowser(w, r, "orgs", data)
}

// attachProjectRoles puts every project role of a person into their row of the
// member list (REQ-704). A row of another person whose account the list hides
// (a viewer without the owner role) is found by name. A person the viewer may
// give a role to but does not find in the list (a lead and an organization
// member without any role yet) gets a row of their own, from the organization's
// list the project roles were built from.
func attachProjectRoles(members []orgMemberRow, roles []projectRolesView, orgAll []store.OrgMemberInfo, gardener string) []orgMemberRow {
	find := func(row projectRoleRow) int {
		for i, m := range members {
			if m.AccountID != "" && m.AccountID == row.AccountID {
				return i
			}
		}
		for i, m := range members {
			if m.AccountID == "" && !m.Self && m.Account == row.Account {
				return i
			}
		}
		return -1
	}
	for _, rv := range roles {
		for _, row := range rv.Rows {
			i := find(row)
			if i < 0 && len(row.Grantable) > 0 && row.AccountID != gardener {
				for _, m := range orgAll {
					if m.AccountID == row.AccountID && !m.Guest {
						m.Account = store.NormalizeAccountName(m.Account)
						members = append(members, orgMemberRow{OrgMemberInfo: m, Initial: initialOf(m.Account), Service: m.Kind == store.AccountKindService})
						i = len(members) - 1
						break
					}
				}
			}
			if i >= 0 {
				members[i].Projects = append(members[i].Projects, memberProjectRole{projectRoleRow: row, Remote: rv.Remote, Short: shortName(rv.Remote)})
			}
		}
	}
	return members
}

// orgError übersetzt Store-Fehler in eine Meldung und einen Statuscode.
func orgError(l lang, err error) (int, string) {
	if status, msg, ok := aiError(l, err); ok {
		return status, msg
	}
	switch {
	case errors.Is(err, store.ErrLoginLinkScope):
		return http.StatusForbidden, l.msg("adm.err.login_scope")
	case errors.Is(err, store.ErrNotOrgOwner):
		return http.StatusForbidden, l.msg("adm.err.owner_only")
	case errors.Is(err, store.ErrOrgNotFound), errors.Is(err, store.ErrNotOrgMember):
		return http.StatusNotFound, l.msg("adm.err.org")
	case errors.Is(err, store.ErrLastOrgOwner):
		return http.StatusConflict, l.msg("adm.err.last_owner")
	case errors.Is(err, store.ErrNotGrantor):
		return http.StatusForbidden, l.msg("adm.err.grantor")
	case errors.Is(err, store.ErrRoleForbidden):
		return http.StatusForbidden, l.msg("adm.err.role")
	case errors.Is(err, store.ErrSelfPromotion):
		return http.StatusForbidden, l.msg("adm.err.self")
	case errors.Is(err, store.ErrLastProjectOwner):
		return http.StatusConflict, l.msg("adm.err.last_pro_owner")
	case errors.Is(err, store.ErrImplicitOwner):
		return http.StatusConflict, l.msg("adm.err.implicit")
	case errors.Is(err, store.ErrNoProjectRole):
		return http.StatusNotFound, l.msg("adm.err.no_role")
	case errors.Is(err, store.ErrProjectNotFound):
		return http.StatusNotFound, l.msg("adm.err.project")
	case errors.Is(err, store.ErrInvalidInput):
		return http.StatusBadRequest, l.msg("adm.err.input")
	case errors.Is(err, store.ErrGuestLinkNeedsEnforcement):
		return http.StatusConflict, l.msg("adm.err.guest_link")
	case errors.Is(err, store.ErrTooManyInvites):
		return http.StatusConflict, l.msg("adm.err.too_many")
	case errors.Is(err, store.ErrCodeInvalid):
		return http.StatusBadRequest, l.msg("adm.err.code")
	case errors.Is(err, store.ErrInvitationEmail):
		return http.StatusForbidden, l.msg("adm.err.email")
	case errors.Is(err, store.ErrAlreadyMember):
		return http.StatusConflict, l.msg("adm.err.member")
	case errors.Is(err, store.ErrProjectNameTaken):
		return http.StatusConflict, l.msg("adm.err.project_taken")
	case errors.Is(err, store.ErrOrgSlugTaken):
		return http.StatusConflict, l.msg("adm.err.org_taken")
	case errors.Is(err, store.ErrTooManyAttempts):
		return http.StatusTooManyRequests, l.msg("adm.err.attempts")
	case errors.Is(err, store.ErrNotProjectOwner):
		return http.StatusForbidden, l.msg("adm.err.not_project_owner")
	case errors.Is(err, store.ErrPrefixUnproven):
		return http.StatusForbidden, l.msg("adm.err.prefix_unproven")
	case errors.Is(err, store.ErrPrefixTaken), errors.Is(err, store.ErrSourceTaken):
		return http.StatusConflict, l.msg("adm.err.source_taken")
	case errors.Is(err, store.ErrSourceNotFound):
		return http.StatusNotFound, l.msg("adm.err.source_missing")
	case sourceError(err):
		return http.StatusConflict, l.msg("adm.err.source_off")
	case errors.Is(err, store.ErrNotGitHubOwner):
		return http.StatusForbidden, l.msg("adm.err.github_owner")
	case errors.Is(err, store.ErrProjectClaimed):
		return http.StatusConflict, l.msg("adm.err.repo_claimed")
	case errors.Is(err, store.ErrInvalidRemote):
		return http.StatusBadRequest, l.msg("adm.err.remote")
	}
	var contested *store.ContestedError
	if errors.As(err, &contested) {
		return http.StatusConflict, l.msg("adm.err.contested")
	}
	return http.StatusInternalServerError, l.msg("adm.err.generic")
}

func (a *app) orgFailure(w http.ResponseWriter, r *http.Request, err error) {
	l := a.langFor(r)
	status, msg := orgError(l, err)
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

// orgNotice übersetzt den Schlüssel aus ?notice= in den Text; ein unbekannter
// Schlüssel zeigt nichts, so kann ein Link keinen eigenen Text einschleusen.
func orgNotice(l lang, key string) string {
	switch key {
	case "revoked":
		return l.msg("adm.notice.revoked")
	case "role":
		return l.msg("adm.notice.role")
	case "removed":
		return l.msg("adm.notice.removed")
	case "project":
		return l.msg("adm.notice.project")
	case "garden":
		return l.msg("gardener.saved")
	case "workflows":
		return l.msg("wf.retention.saved")
	case "workflow_policy":
		return l.msg("wf.policy.saved")
	case "ai", "ai_server":
		return l.msg("ai.saved")
	case "moved":
		return l.msg("adm.notice.moved")
	case "joined":
		return l.msg("adm.notice.joined")
	case "created":
		return l.msg("adm.notice.created")
	case "renamed":
		return l.msg("adm.notice.renamed")
	case "project_created":
		return l.msg("adm.notice.project_created")
	case "visibility":
		return l.msg("adm.notice.visibility")
	case "source_added":
		return l.msg("adm.notice.source_added")
	case "source_removed":
		return l.msg("adm.notice.source_removed")
	case "source_failed":
		return l.msg("adm.notice.source_failed")
	case "source_proved":
		return l.msg("adm.notice.source_proved")
	case "imported":
		return l.msg("adm.notice.imported")
	case "follow":
		return l.msg("adm.notice.follow")
	case "assigned":
		return l.msg("adm.notice.assigned")
	}
	return ""
}

func orgRedirect(w http.ResponseWriter, r *http.Request, slug, notice string) {
	q := url.Values{"org": {slug}}
	if notice != "" {
		q.Set("notice", notice)
	}
	http.Redirect(w, r, "/ui/orgs?"+q.Encode()+noticeAnchor(notice), http.StatusSeeOther)
}

// orgDone goes back to the settings with the note of what was done: the page
// shows it once, marks the row that changed and opens its section.
func (a *app) orgDone(w http.ResponseWriter, r *http.Request, slug, notice string, n doneNote) {
	q := url.Values{"org": {slug}, "done": {adminNotes.put(browserPrincipal(r).ID, n)}}
	frag := noticeAnchor(notice)
	if n.Row != "" {
		frag = "#" + n.Row
	}
	http.Redirect(w, r, "/ui/orgs?"+q.Encode()+frag, http.StatusSeeOther)
}

// orgMemberName is the shown name of a member of the organization, the account
// itself when it is none.
func (a *app) orgMemberName(orgID int64, account string) string {
	members, _ := a.store.ListOrgMembers(orgID)
	for _, m := range members {
		if m.AccountID == account {
			return store.NormalizeAccountName(m.Account)
		}
	}
	return account
}

// inviteSubject says whom an invitation is for: the project of a guest link, the
// address it is bound to, else "single-use link".
func inviteSubject(l lang, inv store.Invitation) string {
	switch {
	case inv.ProjectRemote != "":
		return inv.ProjectRemote
	case inv.Email != "":
		return inv.Email
	}
	return l.msg("adm.invite.open_link")
}

// noticeAnchor names the section of the settings a notice belongs to, so that
// the page opens there and not on "General".
func noticeAnchor(notice string) string { return noticeSection[notice] }

// noticeSection is the part of the settings a notice belongs to: after
// connecting GitHub the page opens at the connected sources, not at General.
var noticeSection = map[string]string{
	"source_added": "#sources", "source_failed": "#sources", "source_proved": "#sources", "source_removed": "#sources", "imported": "#sources", "follow": "#sources", "assigned": "#projects",
	"visibility": "#projects", "project": "#projects", "moved": "#projects", "project_created": "#projects",
	"role": "#members", "removed": "#members", "revoked": "#invitations", "garden": "#gardener", "workflows": "#workflows", "workflow_policy": "#workflows", "ai": "#ai", "ai_server": "#ai-server",
}

// fillInvites lists the organization's invitations with the links their
// creator may still copy, the projects an invitation can be for, and the
// freshly created invitation named by ?new=.
func (a *app) fillInvites(r *http.Request, v *orgsView, me string) {
	v.CanBind = a.oidc != nil || a.mailAvailable()
	invs, _ := a.store.ListInvitations(me, v.Selected.ID)
	newID, _ := strconv.ParseInt(r.URL.Query().Get("new"), 10, 64)
	l := a.langFor(r)
	for _, inv := range invs {
		inv.AcceptedBy = store.NormalizeAccountName(inv.AcceptedBy)
		row := inviteRow{Invitation: inv, Subject: inviteSubject(l, inv)}
		// An accepted invitation is the person who joined, with the role they got.
		if inv.Status == "accepted" && inv.AcceptedBy != "" {
			row.Named = inv.AcceptedBy
		}
		if inv.Status != "pending" {
			a.inviteLinks.drop(inv.ID)
		} else if link, ok := a.inviteLinks.get(inv.ID, me); ok {
			row.URL, row.Mail = a.inviteURL(r, link.code, link.project), link.mail
			if link.mail.To != "" {
				row.Subject = link.mail.To
			}
			if inv.ID == newID {
				v.NewURL, v.NewExpiry, v.NewFor = row.URL, inv.ExpiresAt, row.Subject
				v.NewMail, v.NewNotice = link.mail, inviteNotice(l, link.mail)
			}
		}
		v.Invites = append(v.Invites, row)
	}
	seen := map[string]bool{}
	for _, p := range v.Projects {
		if !seen[p.Remote] {
			seen[p.Remote] = true
			v.InviteProjects = append(v.InviteProjects, p.Remote)
		}
	}
	for _, remote := range v.Claimable {
		if !seen[remote] {
			seen[remote] = true
			v.InviteProjects = append(v.InviteProjects, remote)
		}
	}
	for _, row := range v.Invites {
		if row.ProjectRemote != "" && seen[row.ProjectRemote] {
			v.DefaultProject = row.ProjectRemote
			break
		}
	}
	if v.DefaultProject == "" && len(v.InviteProjects) > 0 {
		v.DefaultProject = v.InviteProjects[0]
	}
}

// orgInvite creates an invitation: for a project (the normal way, the person
// lands in that project with the chosen role) or, as a side option, for the
// organization alone. A project not yet in the organization is taken over in
// the same step when it is one of the owner's own. The answer is a redirect to
// the page showing the link, so a reload repeats nothing.
func (a *app) orgInvite(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	// A refusal shows the members again: that is where the person was.
	refuse := func(err error) {
		status, msg := orgError(a.langFor(r), err)
		a.renderOrgs(w, r, status, orgsView{Open: "members"}, o.Slug, msg)
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	if days < 0 || days > int(store.MaxInvitationTTL/(24*time.Hour)) {
		refuse(store.ErrInvalidInput)
		return
	}
	// The address is for the mail (and, for a member, binds the invitation): one
	// that is no address is refused before anything is made, guests included.
	email := strings.TrimSpace(r.FormValue("email"))
	if email != "" {
		if _, err := store.NormalizeEmail(email); err != nil {
			refuse(store.ErrInvalidInput)
			return
		}
	}
	ttl := time.Duration(days) * 24 * time.Hour
	me := browserPrincipal(r).ID
	var code string
	var inv store.Invitation
	link := false
	project := strings.TrimSpace(r.FormValue("project"))
	if role := r.FormValue("role"); (store.RoleInfo{Role: role}).GuestOnly() || (role == "" && project != "") {
		// A project link is for guests only: people who work with the
		// organization are invited into it as members.
		if project == "" {
			refuse(store.ErrInvalidInput)
			return
		}
		link = true
		if _, known := a.store.ProjectByRemote(project); known && r.FormValue("claim") != "1" {
			code, inv, err = a.store.CreateProjectInvitation(me, o.ID, project, store.RoleGuest, ttl)
		} else {
			// One call takes the remote over and issues the link (one
			// transaction): a refused link leaves no half-done claim.
			code, inv, err = a.store.ClaimAndInviteProject(me, o.ID, project, store.RoleGuest, ttl)
		}
	} else {
		code, inv, err = a.store.CreateInvitation(me, o.ID, email, r.FormValue("role"), ttl)
	}
	if err != nil {
		refuse(err)
		return
	}
	until, perr := time.Parse(time.RFC3339, inv.ExpiresAt)
	if perr != nil {
		until = time.Now().Add(store.MaxInvitationTTL)
	}
	a.inviteLinks.put(inv.ID, me, code, link, until)
	l := a.langFor(r)
	target, role := o.Name, roleLabel(l, store.RoleMember)
	if link {
		target, role = shortName(inv.ProjectRemote), roleLabel(l, inv.ProjectRole)
	}
	a.inviteLinks.setMail(inv.ID, a.sendInviteMail(r, l, email, target, role, code, link, until))
	// The slug is [a-z0-9-], so it needs no escaping, and the order stays fixed.
	// The fragment opens the members: without it the page opens at "General".
	http.Redirect(w, r, "/ui/orgs?org="+o.Slug+"&new="+strconv.FormatInt(inv.ID, 10)+"#members", http.StatusSeeOther)
}

func (a *app) orgInviteRevoke(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	me := browserPrincipal(r).ID
	var id int64
	subject := ""
	if err == nil {
		if id, err = strconv.ParseInt(r.FormValue("id"), 10, 64); err != nil {
			err = store.ErrInvalidInput
		} else {
			invs, _ := a.store.ListInvitations(me, o.ID)
			for _, inv := range invs {
				if inv.ID == id {
					subject = inviteSubject(a.langFor(r), inv)
				}
			}
			err = a.store.RevokeInvitation(me, o.ID, id)
		}
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	a.inviteLinks.drop(id)
	a.orgDone(w, r, o.Slug, "revoked", doneNote{Text: a.langFor(r).msg("adm.done.revoked", subject), Row: "invite-" + strconv.FormatInt(id, 10)})
}

func (a *app) orgMemberRole(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	me := browserPrincipal(r).ID
	account, role := r.FormValue("account"), r.FormValue("role")
	name := ""
	if err == nil {
		name = a.orgMemberName(o.ID, account)
		err = a.store.SetOrgRole(me, o.ID, account, role)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	l := a.langFor(r)
	text := l.msg("adm.done.member", name, o.Name)
	switch {
	case store.IsOrgOwnerRole(role):
		text = l.msg("adm.done.owner", name, o.Name)
	case account == me:
		text = l.msg("adm.done.member_self", o.Name)
	}
	a.orgDone(w, r, o.Slug, "role", doneNote{Text: text, Row: rowAnchor("m", account)})
}

func (a *app) orgMemberRemove(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	me := browserPrincipal(r).ID
	account := r.FormValue("account")
	name := ""
	if err == nil {
		name = a.orgMemberName(o.ID, account)
		err = a.store.RemoveOrgMember(me, o.ID, account)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	l := a.langFor(r)
	if a.store.OrgRole(o.ID, me) == "" {
		token := adminNotes.put(me, doneNote{Text: l.msg("adm.done.left", o.Name)})
		http.Redirect(w, r, "/ui/orgs?done="+token, http.StatusSeeOther)
		return
	}
	a.orgDone(w, r, o.Slug, "removed", doneNote{Text: l.msg("adm.done.removed", name, o.Name)})
}

func (a *app) orgProjectMove(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		var p store.Project
		if p, err = a.store.MoveProject(browserPrincipal(r).ID, r.FormValue("remote"), r.FormValue("to")); err == nil {
			a.orgDone(w, r, o.Slug, "moved", doneNote{Text: a.langFor(r).msg("adm.done.moved", p.Remote, p.Org)})
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
	if errors.Is(err, store.ErrInvitationEmail) {
		// Say which address the invitation is for (masked) and who can issue a new one.
		l := a.langFor(r)
		msg := l.msg("adm.err.email")
		if p, perr := a.store.PreviewJoin(code, a.store.AccessEnforced(), true); perr == nil && p.BoundEmail != "" {
			msg = l.msg("auth.invitation_email.bound", store.MaskEmail(p.BoundEmail), firstNonEmpty(p.Inviter, l.msg("join.the_inviter")))
		}
		a.renderOrgs(w, r, http.StatusForbidden, orgsView{}, r.FormValue("org"), msg)
		return
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "joined")
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
		case r.FormValue("role") == "" && a.store.ProjectRole(p.Remote, r.FormValue("account")).Inherited:
			// The choice "Member (organization default)" is the role the person has without any row:
			// setting it changes nothing.
			orgRedirect(w, r, o.Slug, "")
			return
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
	l := a.langFor(r)
	remote, account, role := r.FormValue("remote"), r.FormValue("account"), r.FormValue("role")
	name := a.orgMemberName(o.ID, account)
	text := l.msg("adm.done.prole", name, adminLabel(l, "prole", role), remote)
	switch {
	case role == "none":
		text = l.msg("adm.done.prole_none", name, remote)
	case r.FormValue("review") == "1":
		text = l.msg("adm.done.prole_review", name, adminLabel(l, "prole", role), remote)
	}
	a.orgDone(w, r, o.Slug, "role", doneNote{Text: text, Row: rowAnchor("pr", remote+"-"+account)})
}

// grantable nennt die wählbaren Rollen. Rollen zu vergeben ist ein
// Menschen-Recht: aus eingefügtem Token zeigt die Seite keine Rollenformulare.
func (a *app) grantable(r *http.Request, actor, remote, target string) []string {
	if !browserPrincipal(r).May(store.RightRolesGrant) {
		return nil
	}
	return a.store.GrantableRoles(actor, remote, target)
}

// inviteURL ist der Link zu einem Einladungscode: die Beitrittsseite für ein
// Projekt, sonst die Anmeldung mit Code.
func (a *app) inviteURL(r *http.Request, code string, project bool) string {
	if project {
		return a.joinURL(r, code)
	}
	return a.joinBase(r) + "/ui/login/code?code=" + url.QueryEscape(code)
}

// joinURL ist der absolute Einladungslink, auch ohne GHOSTTREE_PUBLIC_URL.
func (a *app) joinURL(r *http.Request, code string) string { return a.joinBase(r) + "/join/" + code }

// initialOf ist der Anfangsbuchstabe für den runden Avatar.
func initialOf(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// orgCreate legt eine Organisation an; jedes angemeldete Konto darf das und wird
// deren Owner. Name und Adresse prüft der Store wie bei `ctx org create`. Danach
// geht es auf die Organisationsseite zum Einladen bzw. zum Projekt.
func (a *app) orgCreate(w http.ResponseWriter, r *http.Request) {
	o, err := a.store.CreateOrg(browserPrincipal(r).ID, r.FormValue("name"), "")
	if err != nil {
		status, msg := orgError(a.langFor(r), err)
		a.renderOrgs(w, r, status, orgsView{NewOrg: true}, "", msg)
		return
	}
	http.Redirect(w, r, "/ui/orgs?org="+o.Slug+"&notice=created", http.StatusSeeOther)
}

// orgAssign sets a project's owner, for the instance admin: a known project
// is reassigned (ReassignProject), an unknown remote decided (ClaimContested).
// Both are logged on the owners' side.
func (a *app) orgAssign(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r)
	if !a.isAdmin(me) {
		http.NotFound(w, r)
		return
	}
	remote, to := strings.TrimSpace(r.FormValue("remote")), r.FormValue("to")
	var p store.Project
	var err error
	if _, known := a.store.ProjectByRemote(remote); known {
		p, err = a.store.ReassignProject(me.ID, remote, to)
	} else {
		p, err = a.store.ClaimContested(me.ID, remote, to)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, p.Org, "assigned")
}

// orgProjectCreate legt ein Projekt der Organisation an. Das darf jedes Mitglied;
// wer es anlegt, wird sein Owner. Das Feld nimmt einen Namen (ein Projekt ohne
// Git) oder die Adresse eines Repos (https, ssh, github.com/owner/repo): dann
// ist die Remote die ID des Projekts, und jeder Agent in einem Klon mit dieser
// Remote arbeitet in genau diesem Projekt, ohne dass ein zweites entsteht.
func (a *app) orgProjectCreate(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	input := strings.TrimSpace(r.FormValue("name"))
	me := browserPrincipal(r).ID
	switch remote, isRemote := projectInputRemote(input, o.Slug); {
	case isRemote:
		// Claiming a repository the organization already has is a no-op for the
		// store; here it is a project that exists, and one of another organization
		// is not this one's to make.
		if known, ok := a.store.ProjectByRemote(remote); ok {
			if known.OrgID != o.ID {
				err = store.ErrProjectClaimed
			} else {
				err = store.ErrProjectNameTaken
			}
			break
		}
		_, err = a.store.ClaimProject(me, remote, o.Slug)
	default:
		_, err = a.store.CreateNamedProject(me, strings.ToLower(remote), o.Slug)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "project_created")
}

// projectInputRemote reads what was typed into the project field: a repository
// address (isRemote) or a name. A name that carries the slug of the organization
// ("alpha/infra" in "alpha") is that name; anything else with a slash, a colon or
// an at sign is an address.
func projectInputRemote(input, orgSlug string) (value string, isRemote bool) {
	if !strings.ContainsAny(input, "/:@") {
		return input, false
	}
	remote := scope.NormalizeRemote(input)
	if rest, ok := strings.CutPrefix(remote, strings.ToLower(orgSlug)+"/"); ok && scope.IsProjectName(rest) {
		return rest, false
	}
	return remote, true
}

// orgRename benennt eine Organisation um. Das dürfen ihre Owner und ein
// Instanz-Admin; die Adresse (Slug) bleibt, damit Links gültig bleiben.
func (a *app) orgRename(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r)
	admin := a.isAdmin(me)
	o, err := a.formOrg(r)
	if err != nil && admin {
		// Ein Instanz-Admin darf jede Organisation umbenennen, auch ohne Mitglied zu sein.
		o, err = a.store.OrgByRef(r.FormValue("org"))
	}
	if err == nil {
		if !a.access(r).CanOrg(o.ID, store.RightOrgManage) && admin {
			_, err = a.store.RenameOrgAsAdmin(me.ID, o.ID, r.FormValue("name"), "")
		} else {
			_, err = a.store.RenameOrg(me.ID, o.ID, r.FormValue("name"), "")
		}
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "renamed")
}
