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
	if k.Confidence == "staged" || k.Confidence == "quarantined" {
		writeCoded(w, http.StatusBadRequest, "not_deliverable", "an entry that is held back cannot supersede another: the old one would vanish without a replacement")
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
		if old.Scope.Project != k.Scope.Project {
			writeCoded(w, http.StatusBadRequest, "other_project", fmt.Sprintf("#%d belongs to another project than the new entry", id))
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
		if errors.Is(err, store.ErrNotCurrent) {
			writeCoded(w, http.StatusConflict, "not_current", err.Error())
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
