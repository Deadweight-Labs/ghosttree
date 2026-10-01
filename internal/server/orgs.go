package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	switch {
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
	case errors.Is(err, store.ErrOrgSlugTaken):
		writeCoded(w, http.StatusConflict, "slug_taken", err.Error())
	case errors.Is(err, store.ErrProjectClaimed):
		writeCoded(w, http.StatusConflict, "project_claimed", err.Error())
	case errors.Is(err, store.ErrAlreadyMember):
		writeCoded(w, http.StatusConflict, "already_member", err.Error())
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
	members, err := a.st.ListOrgMembers(o.ID)
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
	o, err := a.st.AcceptInvitation(principalOf(r).ID, strings.TrimSpace(body.Code))
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (a *api) setDefaultOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Org string `json:"org"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	if body.Org == "0" || body.Org == "none" {
		if err := a.st.SetDefaultOrg(principalOf(r).ID, 0); err != nil {
			writeOrgError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, store.Org{})
		return
	}
	o, err := a.st.OrgByRef(body.Org)
	if err == nil {
		err = a.st.SetDefaultOrg(principalOf(r).ID, o.ID)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotOrgMember) || errors.Is(err, store.ErrOrgNotFound) {
			err = store.ErrOrgNotFound
		}
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
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
	writeJSON(w, http.StatusOK, projects)
}

func (a *api) claimProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remote string `json:"remote"`
		Org    string `json:"org"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	p, err := a.st.ClaimProject(principalOf(r).ID, body.Remote, body.Org)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// moveProject: ein Projekt in eine andere Organisation zu legen ändert, wer
// implizit Owner ist, und löscht seine Rollen. Das macht nur ein Mensch in der
// Weboberfläche.
func (a *api) moveProject(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "project moves")
}

// gateProject ordnet beim Schreiben eine noch unbekannte Remote einer
// Organisation zu (Regel: Standard-Organisation des Schreibenden, sonst seine
// einzige). In ein bekanntes Projekt schreibt nur ein Mitglied seiner Org (409
// project_claimed sonst). Ein Konto ohne Organisation schreibt in unbekannte
// Projekte wie bisher; mit mehreren Organisationen und ohne
// Standard wird nicht geraten (409 project_unclaimed). Gibt false zurück, wenn
// schon geantwortet wurde.
func (a *api) gateProject(w http.ResponseWriter, r *http.Request, project string) bool {
	if strings.TrimSpace(project) == "" {
		return true
	}
	if p, known := a.st.ProjectByRemote(project); known {
		// Ein bekanntes Projekt nimmt nur Schreibzugriffe seiner Org an, auch
		// von Konten ohne Org: sonst landeten Daten still in einer fremden Org.
		if a.st.OrgRole(p.OrgID, principalOf(r).ID) == "" {
			writeOrgError(w, store.ErrProjectClaimed)
			return false
		}
		return true
	}
	_, err := a.st.EnsureProject(principalOf(r).ID, project)
	if err == nil || errors.Is(err, store.ErrNoOrg) {
		return true
	}
	var unclaimed *store.ProjectUnclaimedError
	if errors.As(err, &unclaimed) {
		writeOrgError(w, err)
		return false
	}
	writeStoreError(w, http.StatusInternalServerError, err)
	return false
}
