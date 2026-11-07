package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// originFor friert beim Schreiben den Entstehungskontext ein (REQ-600). Die
// Session kommt aus dem Header des Clients (originctx.SessionHeader), ersatz-
// weise aus fallbackRef, einer Kennung, die der Handler ohnehin im Body hat
// (session_ref an Wissen und Ghosts). Die Pfade ermittelt der Server selbst aus
// path_activity; ein Agent gibt nichts davon an.
//
// nil heißt: nicht ermittelbar, und der Schreibzugriff läuft trotzdem. Das gilt
// auch für eine Session, die dem schreibenden Konto nicht gehört: sie ergäbe
// einen Kontext aus fremder Aktivität. named sind ausdrücklich genannte Pfade
// des Eintrags (z. B. der Pfad einer Ghost-Beschreibung), sie zählen am
// stärksten und brauchen keine Session.
func (a *api) originFor(r *http.Request, fallbackRef string, named ...string) *originctx.Context {
	session := strings.TrimSpace(r.Header.Get(originctx.SessionHeader))
	if !originctx.ValidSession(session) {
		session = ""
	}
	if session == "" && originctx.ValidSession(fallbackRef) {
		session = fallbackRef
	}
	if session != "" && !a.st.SessionOwnedBy(session, principalOf(r).ID) {
		session = ""
	}
	ctx, err := a.st.OriginFor(session, store.AccountIDOf(principalOf(r).ID), named)
	if err != nil {
		return nil
	}
	return ctx
}

// dropForeignSessionRef verwirft eine session_ref, die der Schreiber nicht
// besitzt: eine Session eines anderen Kontos (nach Kennung oder als
// "session:<id>"). Ein Eintrag mit fremder Verweis erschiene sonst als Fund aus
// dieser Session, und der Entstehungskontext (auch der Nachtrag) läse ihre
// Pfade. Eine unbekannte Kennung bleibt, die Session kann noch nicht
// hochgeladen sein.
func (a *api) dropForeignSessionRef(r *http.Request, ref *string) {
	if *ref == "" {
		return
	}
	if a.access(r).CheckWrittenSessionRef(*ref) != nil || a.st.SessionForeignTo(*ref, principalOf(r).ID) {
		*ref = ""
	}
}

// dropForeignPlainSessionRef ist dasselbe nur für die Kennung ohne Präfix. Bei
// Wissen lehnt CheckWrittenSessionRef die "session:<id>"-Form schon mit 403 ab.
func (a *api) dropForeignPlainSessionRef(r *http.Request, ref *string) {
	if *ref != "" && a.st.SessionForeignTo(*ref, principalOf(r).ID) {
		*ref = ""
	}
}

// originNamedFromGlobs macht aus den Pfadmustern einer Aktivierungsregel
// ("core/**", "web/src/a.tsx") ausdrücklich genannte Pfade: ein Muster zählt
// bis zum ersten Platzhalter als Verzeichnis.
func originNamedFromGlobs(globs []string) []string {
	var out []string
	for _, g := range globs {
		if i := strings.IndexAny(g, "*?[{"); i >= 0 {
			g = g[:i]
			if j := strings.LastIndex(g, "/"); j >= 0 {
				g = g[:j+1]
			} else {
				continue
			}
		}
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

// originFilterFromQuery liest die Lesefilter since, until und area (mehrfach).
// Ein ungültiger Wert wird mit 400 abgelehnt: ein Filter, der still ignoriert
// wird, liefert auf "was entstand in diesem Zeitraum" alles.
func originFilterFromQuery(w http.ResponseWriter, r *http.Request) (originctx.Filter, bool) {
	q := r.URL.Query()
	spec := originctx.Spec{Since: q.Get("since"), Until: q.Get("until"), Areas: q["area"]}
	f, err := spec.Resolve(time.Now())
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_origin_filter", err.Error())
		return originctx.Filter{}, false
	}
	return f, true
}
