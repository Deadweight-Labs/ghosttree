package server

import (
	"net/http"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Projektbezug im Startkontext und in der Suche (REQ-641, PAIN-74, DEC-15).
//
// Breites Wissen (Maschine, global) gehört zu keinem Projekt. Der Start stellt
// es nur noch zu, wenn es zum Projekt der Sitzung passt: dort entstanden oder
// belegt (store.TiedBroadKnowledge) oder von einem Menschen als "gilt überall"
// markiert. Was ausgelassen wird, bleibt erreichbar: ein Wort im Prompt stellt es
// über die Relevanz zu, ein Pfadauslöser über die Anweisung, und die Suche findet
// es. Verbindliche Anweisungen bleiben ausgenommen; sie binden, ob jemand sie
// liest oder nicht.

// heldBroadKey ist der Schlüssel in der Zahlenliste der zurückgehaltenen Einträge,
// unter dem breite Einträge ohne Bezug zum Projekt stehen. Er ist keine Art und
// kollidiert mit keiner.
const heldBroadKey = "broad"

// broadSearchShareDivisor: von den Treffern einer Suche im Projekt darf höchstens
// ein Viertel aus Maschine und global stammen (5 von 20), solange genug
// Projekttreffer da sind.
const broadSearchShareDivisor = 4

// withoutUntiedBroad lässt aus dem Startkontext die breiten Einträge ohne Bezug
// zum Projekt weg und zählt sie. Ohne Projekt (eine Sitzung außerhalb eines
// Repos) gibt es nichts, wozu ein Bezug bestehen könnte, und alles bleibt.
//
// Ein Fehler beim Lesen des Bezugs kostet nur die Auslese: besser zu viel
// zustellen als einen Start ohne Wissen.
func (a *api) withoutUntiedBroad(entries []store.Knowledge, project string) ([]store.Knowledge, int) {
	if project == "" {
		return entries, 0
	}
	hasBroad := false
	for _, k := range entries {
		if k.Scope.Project == "" && k.Type != "instruction" {
			hasBroad = true
			break
		}
	}
	if !hasBroad {
		return entries, 0
	}
	tied, err := a.st.TiedBroadKnowledge(project)
	if err != nil {
		return entries, 0
	}
	kept := make([]store.Knowledge, 0, len(entries))
	withheld := 0
	for _, k := range entries {
		if k.Scope.Project == "" && k.Type != "instruction" && !tied[k.ID] {
			withheld++
			continue
		}
		kept = append(kept, k)
	}
	return kept, withheld
}

// capBroadHits begrenzt die breiten Treffer einer Suche im Projekt auf einen
// Anteil von limit und schneidet auf limit zu, in der Rangfolge der Suche. Gibt
// es zu wenige Projekttreffer, füllen die übersprungenen breiten Treffer den Rest
// auf: die Quote schützt das Projekt vor Verdrängung und lässt keine Seite leer.
func capBroadHits(ks []store.Knowledge, limit int) []store.Knowledge {
	if limit <= 0 || len(ks) == 0 {
		return ks
	}
	maxBroad := max(1, limit/broadSearchShareDivisor)
	picked := make([]bool, len(ks))
	n, broad := 0, 0
	for i, k := range ks {
		if n >= limit {
			break
		}
		if k.Scope.Project == "" {
			if broad >= maxBroad {
				continue
			}
			broad++
		}
		picked[i] = true
		n++
	}
	for i := range ks {
		if n >= limit {
			break
		}
		if !picked[i] {
			picked[i] = true
			n++
		}
	}
	out := make([]store.Knowledge, 0, n)
	for i, k := range ks {
		if picked[i] {
			out = append(out, k)
		}
	}
	return out
}

// setKnowledgeEverywhere setzt oder nimmt das Kennzeichen "gilt überall".
func (a *api) setKnowledgeEverywhere(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		Everywhere bool `json:"everywhere"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	// Eine Aussage über die Reichweite eines Eintrags ist eine Beurteilung wie
	// "verified": wer das nicht darf, darf auch das nicht.
	if !a.checkKnowledgeRef(w, r, id, store.ActVerify) {
		return
	}
	if err := a.st.SetKnowledgeEverywhere(id, in.Everywhere); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// triageResult ist die Antwort der Durchsicht.
type triageResult struct {
	Items []triageEntry `json:"items"`
}

type triageEntry struct {
	Knowledge  store.Knowledge  `json:"knowledge"`
	Kind       store.TriageKind `json:"kind"`
	Project    string           `json:"project,omitempty"`
	Deliveries int              `json:"deliveries,omitempty"`
}

// knowledgeTriage legt breite Einträge zur Durchsicht vor. Es ändert nichts.
func (a *api) knowledgeTriage(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if !a.listGate(w, r, project, store.ResKnowledge) {
		return
	}
	pa := a.access(r)
	limit := min(intParam(r, "limit", 50), 200)
	items, err := a.st.BroadTriage(project, time.Now(), limit, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k)
	})
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	out := triageResult{Items: make([]triageEntry, 0, len(items))}
	for _, it := range items {
		view := pa.KnowledgeViews([]store.Knowledge{it.Knowledge})
		if len(view) == 0 {
			continue
		}
		out.Items = append(out.Items, triageEntry{Knowledge: view[0], Kind: it.Kind, Project: it.Project, Deliveries: it.Deliveries})
	}
	writeJSON(w, http.StatusOK, out)
}
