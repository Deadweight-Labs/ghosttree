package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// maxOrgBody begrenzt die JSON-Körper der Org-Routen. Sie tragen Namen, Codes
// und Adressen; mehr braucht keine.
const maxOrgBody = 16 << 10

// readOrgJSON liest einen begrenzten JSON-Körper (413 bei Überschreitung).
func readOrgJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxOrgBody)
	if err := readJSON(r, v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeCoded(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
		} else {
			writeCoded(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		}
		return false
	}
	return true
}

// writeOrgError übersetzt Fehler der Org-Operationen in Statuscodes mit
// maschinenlesbarem Code.
func writeOrgError(w http.ResponseWriter, err error) {
	var unclaimed *store.ProjectUnclaimedError
	var contested *store.ContestedError
	switch {
	case errors.As(err, &contested):
		// Kinds and counts of other accounts' entries, never their content.
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "message": err.Error(), "code": "contested", "details": map[string]any{"kinds": contested.Kinds}})
	case errors.Is(err, store.ErrGuestOnly):
		writeCoded(w, http.StatusConflict, "guest_only", err.Error())
	case errors.Is(err, store.ErrNotAdmin):
		writeCoded(w, http.StatusForbidden, "admin_only", err.Error())
	case errors.Is(err, store.ErrNotGitHubOwner):
		writeCoded(w, http.StatusForbidden, "not_github_owner", err.Error())
	case errors.Is(err, store.ErrNoHandover):
		writeCoded(w, http.StatusConflict, "no_handover", err.Error())
	case errors.As(err, &unclaimed):
		recordResponseError(w, classifyRequestError(http.StatusConflict, "", err.Error()), err.Error())
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "message": err.Error(), "code": "project_unclaimed", "choices": unclaimed.Choices,
			"details": map[string]any{"choices": unclaimed.Choices}})
	case errors.Is(err, store.ErrOrgNotFound):
		writeCoded(w, http.StatusNotFound, "org_not_found", "organization not found")
	case errors.Is(err, store.ErrProjectNotFound):
		writeCoded(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, store.ErrNotOrgOwner):
		writeCoded(w, http.StatusForbidden, "not_org_owner", err.Error())
	case errors.Is(err, store.ErrNotOrgMember):
		writeCoded(w, http.StatusForbidden, "not_org_member", err.Error())
	case errors.Is(err, store.ErrNotGrantor):
		writeCoded(w, http.StatusForbidden, "not_grantor", err.Error())
	case errors.Is(err, store.ErrRoleForbidden):
		writeCoded(w, http.StatusForbidden, "role_forbidden", err.Error())
	case errors.Is(err, store.ErrSelfPromotion):
		writeCoded(w, http.StatusForbidden, "self_promotion", err.Error())
	case errors.Is(err, store.ErrLastProjectOwner):
		writeCoded(w, http.StatusConflict, "last_owner", err.Error())
	case errors.Is(err, store.ErrImplicitOwner):
		writeCoded(w, http.StatusConflict, "implicit_owner", err.Error())
	case errors.Is(err, store.ErrNoProjectRole):
		writeCoded(w, http.StatusNotFound, "no_role", err.Error())
	case errors.Is(err, store.ErrLastOrgOwner):
		writeCoded(w, http.StatusConflict, "last_owner", err.Error())
	case errors.Is(err, store.ErrProjectNameTaken):
		writeCoded(w, http.StatusConflict, "project_name_taken", err.Error())
	case errors.Is(err, store.ErrOrgSlugTaken):
		writeCoded(w, http.StatusConflict, "slug_taken", err.Error())
	case errors.Is(err, store.ErrProjectClaimed):
		writeCoded(w, http.StatusConflict, "project_claimed", err.Error())
	case errors.Is(err, store.ErrProjectArchived):
		writeCoded(w, http.StatusConflict, "project_archived", err.Error())
	case errors.Is(err, store.ErrInvalidRemote):
		writeCoded(w, http.StatusBadRequest, "invalid_remote", err.Error())
	case errors.Is(err, store.ErrServiceAccount):
		writeCoded(w, http.StatusForbidden, "service_account", err.Error())
	case errors.Is(err, store.ErrUserNamespace):
		writeCoded(w, http.StatusConflict, "user_namespace", err.Error())
	case errors.Is(err, store.ErrAlreadyMember):
		writeCoded(w, http.StatusConflict, "already_member", err.Error())
	case errors.Is(err, store.ErrGuestLinkNeedsEnforcement):
		writeCoded(w, http.StatusConflict, "guest_link_needs_enforcement", err.Error())
	case errors.Is(err, store.ErrTooManyInvites):
		writeCoded(w, http.StatusConflict, "too_many_invitations", err.Error())
	case errors.Is(err, store.ErrNoOrg):
		writeCoded(w, http.StatusConflict, "no_org", err.Error())
	case errors.Is(err, store.ErrInvalidInput):
		writeCoded(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, store.ErrCodeInvalid):
		writeCoded(w, http.StatusBadRequest, "invalid_code", err.Error())
	case errors.Is(err, store.ErrInvitationEmail):
		writeCoded(w, http.StatusForbidden, "invitation_email", err.Error())
	case errors.Is(err, store.ErrAccountDisabled):
		writeCoded(w, http.StatusForbidden, "account_disabled", err.Error())
	case errors.Is(err, store.ErrTooManyAttempts):
		w.Header().Set("Retry-After", "600")
		writeCoded(w, http.StatusTooManyRequests, "too_many_attempts", err.Error())
	default:
		writeStoreError(w, http.StatusInternalServerError, err)
	}
}

// memberOrg löst {org} (Id oder Slug) auf und verlangt, dass der Aufrufer
// Mitglied ist. Für Nichtmitglieder sieht eine fremde Organisation aus wie
// eine, die es nicht gibt.
func (a *api) memberOrg(w http.ResponseWriter, r *http.Request) (store.Org, string, bool) {
	o, err := a.st.OrgByRef(r.PathValue("org"))
	if err != nil {
		writeOrgError(w, err)
		return store.Org{}, "", false
	}
	role := a.st.OrgRole(o.ID, principalOf(r).ID)
	if role == "" {
		writeOrgError(w, store.ErrOrgNotFound)
		return store.Org{}, "", false
	}
	return o, role, true
}

// accountRef löst ein Konto per Name oder "person:<id>" auf.
func (a *api) accountRef(ref string) (store.Account, error) {
	if strings.HasPrefix(ref, "person:") {
		return a.st.AccountByPrincipalID(ref)
	}
	return a.st.AccountByName(ref)
}

func (a *api) listOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := a.st.ListOrgs(principalOf(r).ID)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orgs)
}

// createOrg legt eine Organisation an. Das darf nur ein Instanz-Admin: wer
// Organisationen anlegen kann, kann sie auch füllen und Konten per Einladung
// erzeugen, das ist Betreiberpolitik.
// createOrg founds an organization over the API, for an instance admin only.
// Every person may found one too, but in the web, from an interactive session
// (POST /ui/orgs/create): API tokens are mostly agents' and CLI tokens, and
// founding an organization is a person's decision, not an agent's. Services
// found none on either path (the store refuses them).
func (a *api) createOrg(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if acct, err := a.st.AccountByPrincipalID(p.ID); err != nil || !acct.Admin {
		writeCoded(w, http.StatusForbidden, "admin_only", "only an instance admin may create organizations")
		return
	}
	var body struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	o, err := a.st.CreateOrg(p.ID, body.Name, body.Slug)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	o.Role, o.Default = store.OrgOwner, false
	writeJSON(w, http.StatusCreated, o)
}

// renameOrg ändert Name und optional Slug einer Organisation (nur Owner).
func (a *api) renameOrg(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.memberOrg(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	renamed, err := a.st.RenameOrg(principalOf(r).ID, o.ID, body.Name, body.Slug)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renamed)
}

func (a *api) listOrgMembers(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.memberOrg(w, r)
	if !ok {
		return
	}
	members, err := a.st.ListOrgMembersFor(o.ID, principalOf(r).ID, a.st.AccessEnforced())
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, members)
}

// setOrgMemberRole: Org-Rollen ändert nur ein Mensch in der Weboberfläche.
func (a *api) setOrgMemberRole(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "organization role changes")
}

// removeOrgMember: Mitglieder entfernt nur ein Mensch in der Weboberfläche.
// Das eigene Verlassen bleibt über die API möglich, es nimmt nur eigene Macht.
func (a *api) removeOrgMember(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.memberOrg(w, r)
	if !ok {
		return
	}
	target, err := a.accountRef(r.PathValue("account"))
	if err != nil {
		writeCoded(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if target.ID != principalOf(r).ID {
		webSessionOnly(w, "member removals")
		return
	}
	if err := a.st.RemoveOrgMember(principalOf(r).ID, o.ID, target.ID); err != nil {
		writeOrgError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listOrgInvitations(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.memberOrg(w, r)
	if !ok {
		return
	}
	list, err := a.st.ListInvitations(principalOf(r).ID, o.ID)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// createOrgInvitation: Einladungen (und damit neue Mitglieder, auch Owner)
// stellt nur ein Mensch in der Weboberfläche aus.
func (a *api) createOrgInvitation(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "invitations")
}

func (a *api) revokeOrgInvitation(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.memberOrg(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_input", "invalid invitation id")
		return
	}
	if err := a.st.RevokeInvitation(principalOf(r).ID, o.ID, id); err != nil {
		writeOrgError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// acceptInvitation nimmt das Konto des Tokens in die Organisation auf. Wer noch
// kein Konto hat, löst die Einladung beim Login in der Weboberfläche ein.
func (a *api) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	code := strings.TrimSpace(body.Code)
	// Nur eine offene Projekt-Einladung wird hier abgewiesen; jeder andere Code,
	// auch ein ungültiger, geht durch AcceptInvitation und seine Sperre und
	// bekommt die einheitliche Antwort. Eine Projekt-Einladung nimmt nur ein Mensch im Browser an: ein Token liegt
	// in der Konfiguration jedes Agenten, und ein Link in einem fremden Text
	// darf ihm keine Rolle verschaffen.
	if a.st.OpenProjectInvitation(code, a.st.AccessEnforced()) {
		webSessionOnly(w, "project invitations")
		return
	}
	o, err := a.st.AcceptInvitation(principalOf(r).ID, code)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// setDefaultOrg answers clients from before REQ-623. "Organization for new
// projects" no longer exists: a new project belongs to the organization whose
// connected GitHub account holds the repository, else to the writer's own
// namespace.
func (a *api) setDefaultOrg(w http.ResponseWriter, r *http.Request) {
	writeCoded(w, http.StatusGone, "default_org_gone", "there is no default organization any more: a new project belongs to the organization that connected its GitHub account, else to your own namespace")
}

func (a *api) listProjects(w http.ResponseWriter, r *http.Request) {
	var orgID int64
	if ref := r.URL.Query().Get("org"); ref != "" {
		o, err := a.st.OrgByRef(ref)
		if err != nil || a.st.OrgRole(o.ID, principalOf(r).ID) == "" {
			writeOrgError(w, store.ErrOrgNotFound)
			return
		}
		orgID = o.ID
	}
	projects, err := a.st.ListProjects(principalOf(r).ID, orgID)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).VisibleProjects(projects))
}

// createProject legt ein benanntes Projekt ohne Git an (REQ-459). Jedes Mitglied
// der Organisation darf das; der Ersteller wird Owner.
func (a *api) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Org  string `json:"org"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	p, err := a.st.CreateNamedProject(principalOf(r).ID, body.Name, body.Org)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (a *api) claimProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remote string `json:"remote"`
		Org    string `json:"org"`
		// Force decides a contested remote; only the instance admin may.
		Force bool `json:"force"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	var p store.Project
	var err error
	if body.Force {
		// Deciding a contested remote moves ownership: the web (/ui/orgs) or
		// ctx project claim --force --db, never an API token.
		webSessionOnly(w, "deciding a contested remote")
		return
	}
	p, err = a.st.ClaimProject(principalOf(r).ID, body.Remote, body.Org)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	// Der Claim eines schon beanspruchten Projekts ist idempotent und gibt es
	// zurück; ein Mitglied der Org ohne Rolle im Projekt erfährt damit weder
	// Id noch Namen.
	if denyAccess(w, a.access(r).Check(p.Remote, store.ResProject, store.ActRead, store.Object{})) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// reassignProject: giving a project another owner is the instance admin's
// decision in the web (/ui/orgs) or with the database in hand (ctx project
// reassign --db). An API token is often an agent's; a prompt must never move
// ownership.
func (a *api) reassignProject(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "reassigning a project")
}

// listHeldEntries tells an agent or a script how many entries a project
// holds back, by kind, for its reviewers; titles and content only show in the
// web (/ui/review), where reviewers decide them.
func (a *api) listHeldEntries(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project (?project=)")
		return
	}
	list, err := a.access(r).HeldEntries(project, false)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	kinds := map[string]int{}
	for _, h := range list {
		kinds[h.Kind]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "held": len(list), "kinds": kinds})
}

// decideHeldEntry: releasing or discarding is a reviewer's decision in the web.
func (a *api) decideHeldEntry(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "releasing or discarding held entries")
}

// moveProject: ein Projekt in eine andere Organisation zu legen ändert, wer
// implizit Owner ist, und löscht seine Rollen. Das macht nur ein Mensch in der
// Weboberfläche.
func (a *api) moveProject(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "project moves")
}

// projectGate ordnet beim Schreiben eine noch unbekannte Remote ihrem Besitzer
// zu (REQ-623: Präfix einer verbundenen Org, sonst der eigene Namensraum des
// Schreibenden) und prüft ein bekanntes Projekt: hineinschreiben darf nur ein
// Mitglied seines Besitzers, und ein archiviertes nimmt nichts mehr an. Der
// Fehler sagt, warum nicht; nil heißt: weiter.
func (a *api) projectGate(r *http.Request, project string) error {
	if strings.TrimSpace(project) == "" {
		return nil
	}
	if p, known := a.st.ProjectByRemote(project); known {
		// Ein bekanntes Projekt nimmt nur Schreibzugriffe seines Besitzers an,
		// auch von Konten ohne Org: sonst landeten Daten still bei Fremden.
		if a.st.OrgRole(p.OrgID, principalOf(r).ID) == "" {
			if p.Named {
				// Ein benanntes Projekt eines Fremden und eines, das es nicht
				// gibt, antworten gleich.
				return store.ErrProjectNotFound
			}
			return store.ErrProjectClaimed
		}
		if p.ArchivedAt != "" {
			return store.ErrProjectArchived
		}
		return nil
	}
	_, err := a.st.EnsureProject(principalOf(r).ID, project)
	if errors.Is(err, store.ErrNoOrg) {
		return nil
	}
	return err
}

// gateKnownProject prüft wie gateProject ein bekanntes Projekt, legt aber für
// eine unbekannte Remote keines an: für Zugriffe, die keine Arbeit am Projekt
// sind (einem Raum beitreten). Antwort wie gateProject.
func (a *api) gateKnownProject(w http.ResponseWriter, r *http.Request, project string) bool {
	if strings.TrimSpace(project) == "" {
		return true
	}
	if _, known := a.st.ProjectByRemote(project); !known {
		return true
	}
	return a.gateProject(w, r, project)
}

// gateProject ist projectGate mit Antwort: false heißt, es wurde schon
// geantwortet.
func (a *api) gateProject(w http.ResponseWriter, r *http.Request, project string) bool {
	err := a.projectGate(r, project)
	if err == nil {
		return true
	}
	var unclaimed *store.ProjectUnclaimedError
	if errors.As(err, &unclaimed) || errors.Is(err, store.ErrProjectNotFound) || errors.Is(err, store.ErrProjectClaimed) ||
		errors.Is(err, store.ErrProjectArchived) || errors.Is(err, store.ErrInvalidRemote) || errors.Is(err, store.ErrServiceAccount) ||
		errors.Is(err, store.ErrGuestOnly) {
		writeOrgError(w, err)
		return false
	}
	writeStoreError(w, http.StatusInternalServerError, err)
	return false
}

// sessionProject decides the project axis of an incoming session. A session is
// the writer's own record, so it is never refused for its project: one in a
// repository the writer may not write to (someone else's, archived, a
// throwaway checkout of a test run), one in a remote nobody has claimed, and
// one of a guest outside its projects is kept without the project. ok is
// false only after an internal error has been answered.
func (a *api) sessionProject(w http.ResponseWriter, r *http.Request, project string) (string, bool) {
	err := a.projectGate(r, project)
	switch {
	case err == nil:
		if _, known := a.st.ProjectByRemote(project); !known && strings.TrimSpace(project) != "" {
			return "", true
		}
		return project, true
	case errors.Is(err, store.ErrProjectNotFound), errors.Is(err, store.ErrProjectClaimed), errors.Is(err, store.ErrProjectArchived),
		errors.Is(err, store.ErrInvalidRemote), errors.Is(err, store.ErrServiceAccount), errors.Is(err, store.ErrGuestOnly):
		return "", true
	}
	var unclaimed *store.ProjectUnclaimedError
	if errors.As(err, &unclaimed) {
		return "", true
	}
	writeStoreError(w, http.StatusInternalServerError, err)
	return "", false
}
