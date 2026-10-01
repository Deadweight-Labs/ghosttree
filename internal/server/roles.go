package server

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// roleProject löst {id} auf und verlangt Mitgliedschaft in der Organisation
// des Projekts; für Fremde gibt es das Projekt nicht.
func (a *api) roleProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_input", "invalid project id")
		return store.Project{}, false
	}
	p, ok := a.st.ProjectByID(id)
	if !ok || a.st.OrgRole(p.OrgID, principalOf(r).ID) == "" {
		writeOrgError(w, store.ErrProjectNotFound)
		return store.Project{}, false
	}
	return p, true
}

// rejectAgentGrant lehnt Rollenänderungen ab, die als Agentenaufruf
// gekennzeichnet sind. Nur ein Konto in eigenem Namen vergibt Rollen.
func rejectAgentGrant(w http.ResponseWriter, r *http.Request, bodyAgent string) bool {
	if bodyAgent != "" || r.URL.Query().Get("agent_external_id") != "" {
		writeCoded(w, http.StatusForbidden, "agent_cannot_grant", "agents cannot grant, change or revoke roles")
		return true
	}
	return false
}

func (a *api) listProjectMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := a.roleProject(w, r)
	if !ok {
		return
	}
	members, err := a.st.ListProjectMembers(p.Remote)
	if err != nil {
		writeOrgError(w, err)
		return
	}
	if members == nil {
		members = []store.ProjectMember{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "members": members, "you": a.st.ProjectRole(p.Remote, principalOf(r).ID)})
}

func (a *api) setProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	p, ok := a.roleProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Role            string `json:"role"`
		CanReview       bool   `json:"can_review"`
		AgentExternalID string `json:"agent_external_id"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	if rejectAgentGrant(w, r, body.AgentExternalID) {
		return
	}
	target, err := a.accountRef(r.PathValue("account"))
	if err != nil {
		writeCoded(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err := a.st.SetProjectRole(principalOf(r).ID, p.Remote, target.ID, body.Role, body.CanReview, store.RoleViaAPI); err != nil {
		writeOrgError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": target.Name, "role": body.Role, "can_review": body.CanReview})
}

func (a *api) removeProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	p, ok := a.roleProject(w, r)
	if !ok {
		return
	}
	if rejectAgentGrant(w, r, "") {
		return
	}
	target, err := a.accountRef(r.PathValue("account"))
	if err != nil {
		writeCoded(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err := a.st.RemoveProjectRole(principalOf(r).ID, p.Remote, target.ID, store.RoleViaAPI); err != nil {
		writeOrgError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
