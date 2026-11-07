package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// anchorFromRequest liest den Git-Anker, den der Client an einen schreibenden
// Aufruf hängt (Header gitanchor.Header). nil heißt: kein oder unlesbarer
// Anker; der Schreibzugriff läuft trotzdem, ohne.
//
// Nur die Handler, die ihn ausdrücklich lesen, speichern einen Anker. Der Body
// ist dafür nie die Quelle: ein Feld git_anchor in einem JSON-Rumpf wird von
// jedem Handler überschrieben, der es zuließe (siehe die Aufrufer), damit ein
// Client sich keinen Stand ausdenkt, den er nicht gemessen hat. Die PR-Nummer
// kommt ohnehin nie vom Client (gitanchor.Decode verwirft sie).
func anchorFromRequest(r *http.Request) *gitanchor.Anchor {
	a := gitanchor.Decode(r.Header.Get(gitanchor.Header))
	if a.IsZero() {
		return nil
	}
	return &a
}

// anchorFilterFromQuery liest die Lesefilter commit und pr. Ein ungültiger Wert
// wird mit 400 abgelehnt: ein Filter, der still ignoriert wird, liefert auf die
// Frage "was wurde zu diesem Commit gesagt" alles.
func anchorFilterFromQuery(w http.ResponseWriter, r *http.Request) (store.AnchorFilter, bool) {
	q := r.URL.Query()
	f := store.AnchorFilter{Commit: gitanchor.Normalize(q.Get("commit"))}
	if raw := strings.TrimPrefix(strings.TrimSpace(q.Get("pr")), "#"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeCoded(w, http.StatusBadRequest, "invalid_pr", "pr must be a positive pull request number")
			return f, false
		}
		f.PR = n
	}
	if err := f.Valid(); err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_commit", err.Error())
		return f, false
	}
	return f, true
}
