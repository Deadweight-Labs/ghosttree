package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// writableProjects antwortet mit den Projekten, in die das Konto schreiben darf.
// Die Zielangabe `project` beim Schreiben und Verschieben löst sich allein
// gegen diese Liste auf, also gegen dieselbe Matrix wie das Schreiben im
// eigenen Projekt.
func (a *api) writableProjects(w http.ResponseWriter, r *http.Request) {
	list, err := a.access(r).WritableProjects()
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// resolveTargetProject löst eine Zielangabe auf. Ein Projekt, das es nicht gibt,
// und eines ohne Schreibrecht antworten mit demselben Status und Text.
func (a *api) resolveTargetProject(w http.ResponseWriter, r *http.Request, input string) (string, bool) {
	list, err := a.access(r).WritableProjects()
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return "", false
	}
	project, err := scope.ResolveProject(input, list)
	var ambiguous *scope.ErrProjectAmbiguous
	switch {
	case err == nil:
		return project, true
	case errors.As(err, &ambiguous):
		writeCoded(w, http.StatusConflict, "project_ambiguous", err.Error())
	default:
		writeCoded(w, http.StatusNotFound, "project_not_found", "project not found")
	}
	return "", false
}

// gateMachineNote prüft den Maschinennamen eines Hinweises auf eine Maschine.
// Anders als gateMachine bindet ihn kein Token an seine eigene Maschine: ein
// Hinweis über eine andere Maschine ist kein Upload in deren Namen, und genau
// der Fall (von hier aus über ssh dort) ist der Zweck. Ein Name, der einem
// anderen Konto gehört, bleibt vergeben. Beansprucht wird nichts, damit sich
// niemand durch Hinweise Namen sichert.
func (a *api) gateMachineNote(w http.ResponseWriter, r *http.Request, machine string) bool {
	machine = strings.ToLower(strings.TrimSpace(machine))
	if machine == "" {
		return true
	}
	switch err := a.st.MachineClaimable(machine, principalOf(r).ID); {
	case errors.Is(err, store.ErrMachineTaken):
		writeCoded(w, http.StatusConflict, "machine_name_taken", "machine name "+machine+" belongs to another account")
		return false
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return true
}

// moveKnowledge verschiebt einen Eintrag in ein Projekt, auf eine Maschine oder
// nach global. Schreibrecht in Quelle und Ziel; das Ziel antwortet bei
// "gibt es nicht" und "darf ich nicht" gleich.
func (a *api) moveKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		Project string `json:"project"`
		Machine string `json:"machine"`
		Global  bool   `json:"global"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	chosen := 0
	for _, set := range []bool{strings.TrimSpace(in.Project) != "", strings.TrimSpace(in.Machine) != "", in.Global} {
		if set {
			chosen++
		}
	}
	if chosen != 1 {
		writeCoded(w, http.StatusBadRequest, "invalid_target", "name exactly one target: project, machine or global")
		return
	}
	if !a.checkKnowledgeRef(w, r, id, store.ActEdit) {
		return
	}
	k, err := a.st.KnowledgeByID(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	var to scope.Axes
	switch {
	case strings.TrimSpace(in.Project) != "":
		project, ok := a.resolveTargetProject(w, r, in.Project)
		if !ok {
			return
		}
		to.Project = project
	case strings.TrimSpace(in.Machine) != "":
		if !a.gateMachineNote(w, r, in.Machine) {
			return
		}
		to.Machine = scope.CanonicalAxes(scope.Axes{Machine: in.Machine}).Machine
	}
	dest := k
	dest.Scope = to
	if denyAccess(w, a.access(r).CheckKnowledgeCreate(dest)) {
		return
	}
	if err := a.st.MoveKnowledge(id, to, personOf(r)); err != nil {
		if errors.Is(err, store.ErrNothingToMove) {
			writeCoded(w, http.StatusConflict, "nothing_to_move", err.Error())
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	saved, err := a.st.KnowledgeByID(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).KnowledgeView(saved))
}

// moveRequest verschiebt einen Auftrag in ein anderes Projekt.
func (a *api) moveRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var in struct {
		Project string `json:"project"`
		Reason  string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Project) == "" {
		writeCoded(w, http.StatusBadRequest, "invalid_target", "a request belongs to a project: name the project")
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActEdit) {
		return
	}
	project, ok := a.resolveTargetProject(w, r, in.Project)
	if !ok {
		return
	}
	if err := a.st.MoveRequest(id, project, personOf(r), in.Reason); err != nil {
		if errors.Is(err, store.ErrNothingToMove) {
			writeCoded(w, http.StatusConflict, "nothing_to_move", err.Error())
			return
		}
		writeRequestError(w, err)
		return
	}
	detail, err := a.st.RequestByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).RequestDetailView(detail))
}
