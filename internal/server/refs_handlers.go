package server

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func init() { accessRoutes["GET /api/refs"] = classProject }

func (a *api) registerRefRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/refs", a.lookupRefs)
}

// refsReply ist die Antwort von GET /api/refs: je Verweis ein Eintrag, in der
// Reihenfolge der Anfrage.
type refsReply struct {
	Refs []links.Info `json:"refs"`
}

// lookupRefs liefert die Hover-Karten zu Verweisen (REQ-n, PAIN-n, TASK-n,
// IDEA-n, FEAT-n, DEC-n, #n, pr:n mit project). ref darf mehrfach stehen,
// höchstens links.MaxPeekRefs. Die Rechte sind die des Lesens am Objekt; ein
// Verweis, den es nicht gibt, einer, den der Aufrufer nicht lesen darf, und einer
// ohne Hover-Karte antworten gleich: ein Eintrag mit gefunden=false. Ist keiner
// der Verweise lesbar, antwortet der Aufruf 404 mit immer demselben Rumpf, damit
// ein einzelner Verweis nichts über das Objekt verrät.
func (a *api) lookupRefs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	refs := q["ref"]
	if len(refs) == 0 {
		writeCoded(w, http.StatusBadRequest, "ref_required", "name at least one reference, for example ?ref=REQ-12")
		return
	}
	if len(refs) > links.MaxPeekRefs {
		writeCoded(w, http.StatusBadRequest, "too_many_refs", "at most 50 references per call")
		return
	}
	project := scope.NormalizeRemote(q.Get("project"))
	pa := a.access(r)
	pa.Filtered()
	b := links.New(a.requestBaseURL(r), project)
	list, err := links.Lookup(b, links.AccessReader{PA: pa}, refs, project)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_ref", err.Error())
		return
	}
	for _, info := range list {
		if info.Gefunden {
			writeJSON(w, http.StatusOK, refsReply{Refs: list})
			return
		}
	}
	writeCoded(w, http.StatusNotFound, "not_found", "not found")
}
