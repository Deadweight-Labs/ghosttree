package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// entryShareNeedsWebSession stellt das Rechte-Modell um (DEC-3, Variante C):
// ist es wahr, gibt auch das Freigeben einzelner Einträge nur noch eine
// Web-Sitzung frei. Heute darf der Agent des Besitzers einen Eintrag in eine
// Org freigeben, für die ein Mensch die Maschine schon freigegeben hat.
const entryShareNeedsWebSession = false

func (a *api) registerMachineRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/machines/{name}", a.getMachine)
	a.routeFunc(mux, "PUT /api/machines/{name}/shares/{org}", a.machineAdminOnly)
	a.routeFunc(mux, "DELETE /api/machines/{name}/shares/{org}", a.machineAdminOnly)
	a.routeFunc(mux, "PUT /api/machines/{name}/owner", a.machineAdminOnly)
	a.routeFunc(mux, "PUT /api/knowledge/{id}/shares/{org}", a.shareKnowledgeEntry)
	a.routeFunc(mux, "DELETE /api/knowledge/{id}/shares/{org}", a.unshareKnowledgeEntry)
	a.routeFunc(mux, "PUT /api/runbooks/{id}/shares/{org}", a.shareRunbookEntry)
	a.routeFunc(mux, "DELETE /api/runbooks/{id}/shares/{org}", a.unshareRunbookEntry)
}

// machineAdminOnly: Freigaben einer Maschine, ihre Rücknahme und die Übertragung
// an eine Org verwaltet nur ein Mensch in der Web-Sitzung (#2411), oder der
// Betreiber mit ctx machine ... --db. Ein Token liegt in der Konfiguration
// jeder Maschine, auf der ein Agent läuft; wer damit Wissen an eine Org öffnen
// könnte, bräuchte dafür keinen Menschen.
func (a *api) machineAdminOnly(w http.ResponseWriter, r *http.Request) {
	writeCoded(w, http.StatusForbidden, "web_session_required",
		"sharing a machine requires an interactive web session: use the machine page of the web UI, or run ctx machine share with --db on the server")
}

func writeMachineNotFound(w http.ResponseWriter) {
	writeCoded(w, http.StatusNotFound, "machine_not_found", "machine not found")
}

// getMachine liefert eine Maschine, soweit sie den Aufrufer etwas angeht
// (Besitzer, Org-Maschine seiner Org, Freigabe an seine Org). Für alle anderen
// gibt es sie nicht.
func (a *api) getMachine(w http.ResponseWriter, r *http.Request) {
	m, err := a.st.MachineFor(principalOf(r).ID, r.PathValue("name"))
	switch {
	case errors.Is(err, store.ErrMachineNotFound):
		writeMachineNotFound(w)
		return
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Die Einträge, die diese Maschine nennen und die der Fragende lesen darf:
	// Projekt-Einträge über seine Projektrolle, reines Maschinen-Wissen als
	// Besitzer oder über die Freigabe.
	m.Entries, err = a.access(r).MachineEntries(m.Name)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// writeEntryShareError übersetzt die Fehler des Freigebens von Einträgen.
func writeEntryShareError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrAccessNotFound):
		writeCoded(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrAccessForbidden):
		writeCoded(w, http.StatusForbidden, "forbidden", "only the owner of the machine may share its entries")
	case errors.Is(err, store.ErrNoMachineShare):
		writeCoded(w, http.StatusConflict, "machine_not_shared", err.Error())
	case errors.Is(err, store.ErrEntryNotShareable):
		writeCoded(w, http.StatusConflict, "not_shareable", err.Error())
	case errors.Is(err, store.ErrShareNotFound):
		writeCoded(w, http.StatusNotFound, "share_not_found", "entry is not shared with that organization")
	default:
		writeStoreError(w, http.StatusInternalServerError, err)
	}
	return true
}

// entryShareGate hält die Web-Pflicht ein, wenn die Richtlinie sie verlangt.
func entryShareGate(w http.ResponseWriter, r *http.Request) bool {
	if entryShareNeedsWebSession && principalOf(r).TokenKind != store.WebSessionKind {
		webSessionOnly(w, "sharing entries")
		return false
	}
	return true
}

func (a *api) shareKnowledgeEntry(w http.ResponseWriter, r *http.Request) {
	a.setKnowledgeShare(w, r, true)
}

func (a *api) unshareKnowledgeEntry(w http.ResponseWriter, r *http.Request) {
	a.setKnowledgeShare(w, r, false)
}

func (a *api) setKnowledgeShare(w http.ResponseWriter, r *http.Request, share bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	if !entryShareGate(w, r) {
		return
	}
	k, err := a.st.KnowledgeByID(id)
	if err != nil {
		a.access(r).Filtered()
		writeCoded(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if denyAccess(w, a.access(r).CheckKnowledge(k, store.ActRead)) {
		return
	}
	org := strings.TrimSpace(r.PathValue("org"))
	if share {
		err = a.st.ShareEntry(principalOf(r), store.EntryKnowledge, id, org)
	} else {
		err = a.st.UnshareEntry(principalOf(r), store.EntryKnowledge, id, org)
	}
	if writeEntryShareError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "shared_with": a.st.EntryShareOrgs(principalOf(r), store.EntryKnowledge, id)})
}

func (a *api) shareRunbookEntry(w http.ResponseWriter, r *http.Request) {
	a.setRunbookShare(w, r, true)
}

func (a *api) unshareRunbookEntry(w http.ResponseWriter, r *http.Request) {
	a.setRunbookShare(w, r, false)
}

func (a *api) setRunbookShare(w http.ResponseWriter, r *http.Request, share bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	if !entryShareGate(w, r) {
		return
	}
	ref, err := a.st.RunbookRefByID(id)
	if err != nil {
		a.access(r).Filtered()
		writeRunbookNotFound(w)
		return
	}
	if a.access(r).CheckRunbook(ref, store.ActRead) != nil {
		writeRunbookNotFound(w)
		return
	}
	org := strings.TrimSpace(r.PathValue("org"))
	if share {
		err = a.st.ShareEntry(principalOf(r), store.EntryRunbook, id, org)
	} else {
		err = a.st.UnshareEntry(principalOf(r), store.EntryRunbook, id, org)
	}
	if writeEntryShareError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "shared_with": a.st.EntryShareOrgs(principalOf(r), store.EntryRunbook, id)})
}

// resolveShareOrgs löst die Org-Namen einer Schreibanfrage zu Nummern auf. Eine
// Org, die es nicht gibt oder in der der Schreiber nicht Mitglied ist, bleibt
// "gibt es nicht": der Aufrufer erfährt nichts über fremde Orgs.
func (a *api) resolveShareOrgs(r *http.Request, refs []string) ([]int64, bool) {
	var out []int64
	for _, ref := range refs {
		o, err := a.st.OrgByRef(strings.TrimSpace(ref))
		if err != nil || a.st.OrgRole(o.ID, principalOf(r).ID) == "" {
			return nil, false
		}
		out = append(out, o.ID)
	}
	return out, true
}

// shareOrgsForNewEntry bestimmt, an welche Orgs ein neuer Eintrag mit
// Maschinen-Scope gleich freigegeben wird. Ohne ausdrückliche Angabe bleibt er
// privat; auf einer Maschine, die einer Org gehört, geht er an diese Org, wenn
// ein Maintainer ihn schreibt. Ein Eintrag mit Projekt wird über das Projekt
// gesehen: die Maschine ist dort Zuordnung, eine Freigabe an Orgs gibt es für
// ihn nicht (ausdrücklich verlangt ist das ein Fehler, nie ein stilles Weglassen).
func (a *api) shareOrgsForNewEntry(w http.ResponseWriter, r *http.Request, project, machine string, asked *[]string) ([]int64, bool) {
	machine = strings.ToLower(strings.TrimSpace(machine))
	if strings.TrimSpace(project) != "" {
		if asked != nil && len(*asked) > 0 {
			writeCoded(w, http.StatusBadRequest, "invalid_input", "share_with_orgs only goes with an entry that has a machine and no project: an entry with a project is seen through the project")
			return nil, false
		}
		return nil, true
	}
	if asked != nil && len(*asked) > 0 {
		if machine == "" {
			writeCoded(w, http.StatusBadRequest, "invalid_input", "share_with_orgs needs a machine scope")
			return nil, false
		}
		ids, ok := a.resolveShareOrgs(r, *asked)
		if !ok {
			writeCoded(w, http.StatusNotFound, "org_not_found", "organization not found")
			return nil, false
		}
		return ids, true
	}
	if asked == nil && machine != "" {
		if org := a.st.MachineOwnerOrgID(machine); org > 0 && a.access(r).OwnsMachine(machine) {
			return []int64{org}, true
		}
	}
	return nil, true
}
