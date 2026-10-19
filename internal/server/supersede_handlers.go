package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// supersedeActor ist, wer ein Ablösen auslöst: der Name kommt aus dem Token,
// der Weg (mcp, cli) aus dem, was der Client beim Schreiben angibt.
func supersedeActor(r *http.Request, via string) store.RelationActor {
	if via == "" {
		via = "api"
	}
	return store.RelationActor{Name: personOf(r), AccountID: authorIDOf(r), Via: via}
}

// checkSupersedable prüft vor dem Schreiben, dass der neue Eintrag die alten
// ablösen darf. Verlangt wird dasselbe Recht wie für Ändern und Zurückziehen im
// Web (ActEdit am alten Eintrag). Ein Eintrag, den der Aufrufer nicht sehen
// darf, antwortet wie ein unbekannter und verrät nichts über sich. Geprüft wird
// alles, bevor der neue Eintrag angelegt wird, damit ein abgelehntes Ablösen
// keinen Nachtrag hinterlässt.
func (a *api) checkSupersedable(w http.ResponseWriter, r *http.Request, k store.Knowledge, ids []int64, reason string) bool {
	if strings.TrimSpace(reason) == "" {
		writeCoded(w, http.StatusBadRequest, "reason_required", "say why the older entries are superseded (supersede_reason)")
		return false
	}
	for _, id := range ids {
		if !a.checkEditable(w, r, id) {
			return false
		}
		old, err := a.st.KnowledgeByID(id)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return false
		}
		if old.Status != "active" && old.Status != "stale" {
			writeCoded(w, http.StatusConflict, "not_current", fmt.Sprintf("#%d is %s: nothing to supersede", id, old.Status))
			return false
		}
		if !old.Scope.Same(k.Scope) {
			writeCoded(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("#%d is %s, the new entry is %s: the replacement needs exactly the scope of the old entry (move one of them first)", id, old.Scope.Label(), k.Scope.Label()))
			return false
		}
	}
	return true
}

// checkEditable verlangt ActEdit auf einen Eintrag. Wer ihn nicht lesen darf,
// bekommt dieselbe Antwort wie auf eine unbekannte Id; erst wer ihn sieht,
// erfährt mit 403, dass ihm das Recht zum Ändern fehlt.
func (a *api) checkEditable(w http.ResponseWriter, r *http.Request, id int64) bool {
	k, err := a.st.KnowledgeRef(id)
	if err == nil && a.access(r).CheckKnowledge(k, store.ActRead) != nil {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		a.access(r).Filtered()
		writeErr(w, http.StatusNotFound, "no such knowledge entry")
		return false
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return !denyAccess(w, a.access(r).CheckKnowledge(k, store.ActEdit))
}

// writeSupersedeError übersetzt die Regelfehler des Ablösens in Antworten und
// meldet, ob es geantwortet hat. Der Store rollt dabei alles zurück: es bleibt
// kein neuer Eintrag stehen.
func writeSupersedeError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotCurrent):
		writeCoded(w, http.StatusConflict, "not_current", err.Error())
	case errors.Is(err, store.ErrNotDeliverable):
		writeCoded(w, http.StatusBadRequest, "not_deliverable", err.Error())
	case errors.Is(err, store.ErrScopeMismatch):
		writeCoded(w, http.StatusBadRequest, "scope_mismatch", err.Error())
	case errors.Is(err, store.ErrReasonRequired):
		writeCoded(w, http.StatusBadRequest, "reason_required", err.Error())
	case errors.Is(err, store.ErrRelationInvalid), errors.Is(err, store.ErrRelationProject), errors.Is(err, store.ErrRelationExists), errors.Is(err, store.ErrRelationCycle), errors.Is(err, store.ErrRelationConflict):
		writeCoded(w, http.StatusBadRequest, "supersede_invalid", err.Error())
	default:
		return false
	}
	return true
}

// retireKnowledge zieht einen Eintrag zurück, dessen beschriebener Zustand
// nicht mehr existiert. Recht und Wirkung wie Zurückziehen im Web (ActEdit,
// Status deprecated), dazu die Begründung als Pflicht.
func (a *api) retireKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkEditable(w, r, id) {
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeCoded(w, http.StatusBadRequest, "reason_required", "say why the entry is retired (reason)")
		return
	}
	if err := a.st.RetireKnowledge(id, in.Reason, personOf(r)); err != nil {
		if errors.Is(err, store.ErrHasPredecessors) {
			writeCoded(w, http.StatusConflict, "has_predecessors", err.Error())
			return
		}
		if writeSupersedeError(w, err) {
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

// supersedeKnowledge macht einen bestehenden Eintrag zum Ablöser älterer
// Einträge. Verlangt wird das Recht zum Ändern am neuen wie an jedem alten
// Eintrag; geprüft wird alles, bevor etwas geschrieben wird.
func (a *api) supersedeKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		Supersedes []int64 `json:"supersedes"`
		Reason     string  `json:"reason"`
		Via        string  `json:"via"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkEditable(w, r, id) {
		return
	}
	newer, err := a.st.KnowledgeByID(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if !a.checkSupersedable(w, r, newer, in.Supersedes, in.Reason) {
		return
	}
	if err := a.st.SupersedeKnowledge(id, in.Supersedes, in.Reason, supersedeActor(r, in.Via)); err != nil {
		if writeSupersedeError(w, err) {
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
