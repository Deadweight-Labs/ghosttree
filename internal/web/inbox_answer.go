package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (a *app) registerInboxAnswer(mux *http.ServeMux) {
	a.handle(mux, "POST /ui/inbox/answer", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.inboxAnswer))))))
}

// inboxAnswer beantwortet einen Eintrag des Posteingangs mit einem der
// vorgeschlagenen Texte (kind attention, task oder decision; id; option). Es ist der
// Weg der Weboberfläche zu ProjectAccess.QuickAnswer: nur eine interaktive Sitzung
// mit gültigem CSRF-Token kommt hierher, ein Token der API bekommt dort 403. Was es
// nicht gibt oder was der Betrachter nicht lesen darf, antwortet gleich (404).
func (a *app) inboxAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	res, err := a.access(r).QuickAnswer(a.browserCoord(r), store.QuickAnswerInput{Kind: r.FormValue("kind"), ID: id, Option: r.FormValue("option")})
	if err != nil {
		var rule *requestdomain.RuleError
		switch {
		case errors.As(err, &rule):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": rule.ErrorCode, "error": rule.Error()})
		case errors.Is(err, store.ErrAccessForbidden), errors.Is(err, store.ErrAccessNotFound),
			errors.Is(err, store.ErrCoordNotFound), errors.Is(err, store.ErrCoordForbidden), errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	// Ein Formular ohne Skript kehrt in den Posteingang zurück. Das Ziel ist
	// fest, nichts aus der Anfrage bestimmt, wohin weitergeleitet wird.
	if r.FormValue("next") != "" {
		http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}
