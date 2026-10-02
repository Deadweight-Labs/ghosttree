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
		a.access(r).Filtered()
		// Dieselbe Antwort wie die Rollenprüfung unten, damit Projekt-Ids sich
		// nicht aufzählen lassen.
		denyAccess(w, store.ErrAccessNotFound)
		return store.Project{}, false
	}
	// Die Mitgliederliste ist Projektsache: ohne Rolle im Projekt gibt es sie
	// nicht, auch nicht für Mitglieder der Organisation.
	if denyAccess(w, a.access(r).Check(p.Remote, store.ResMembers, store.ActRead, store.Object{})) {
		return store.Project{}, false
	}
	return p, true
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
	// Der Gast sieht nur den eigenen Eintrag.
	if store.RoleRank(a.access(r).Role(p.Remote).Role) == 1 && a.st.AccessEnforced() {
		me := principalOf(r).ID
		members = filterTo(members, 0, func(m store.ProjectMember) bool { return m.AccountID == me })
	}
	if members == nil {
		members = []store.ProjectMember{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "members": members, "you": a.st.ProjectRole(p.Remote, principalOf(r).ID)})
}

// webSessionOnly lehnt eine Verwaltungsänderung über ein Bearer-Token ab. Ein
// Maschinen-Token liegt in der Konfiguration jedes Rechners, auf dem ein Agent
// läuft; wer Rollen, Mitgliedschaften oder Einladungen mit ihm ändern könnte,
// bräuchte dafür keinen Menschen. Diese Änderungen macht ein Mensch in der
// Weboberfläche (Browser-Sitzung, CSRF, Same-Origin) oder der Betreiber mit
// Datenbankzugriff (ctx ... --db). Mit oder ohne agent_external_id ist die
// Antwort dieselbe.
func webSessionOnly(w http.ResponseWriter, what string) {
	writeCoded(w, http.StatusForbidden, "web_session_required",
		what+" require an interactive web session: use the organizations page of the web UI (/ui/orgs), or run the ctx command with --db on the server")
}

func (a *api) setProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "role changes")
}

func (a *api) removeProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "role changes")
}
