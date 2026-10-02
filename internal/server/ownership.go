package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// writeCoded antwortet mit einem maschinenlesbaren Fehlercode neben dem Text.
func writeCoded(w http.ResponseWriter, status int, code, msg string) {
	recordResponseError(w, classifyRequestError(status, "", msg), msg)
	writeJSON(w, status, map[string]string{"error": msg, "message": msg, "code": code})
}

// gateMachine setzt die Maschinenregeln eines Schreibzugriffs durch.
//
//   - Ein Token mit gebundener Maschine schreibt nur unter dieser (403).
//   - Gehört der Maschinenname einem anderen Konto, ist er vergeben (409
//     machine_name_taken).
//   - claim=true beansprucht einen noch freien Namen für das Konto des Tokens.
//     Das tun Sessions und Agent-Anmeldungen, die ohnehin eine Maschine
//     bezeugen; Wissen und Aufträge nur prüfen, damit sich niemand durch einen
//     Maschinenwert im Scope Namen sichert.
//
// Ein Token ohne Bindung (Legacy) darf weiter jede Maschine nennen, solange sie
// ihm gehört oder frei ist. Gibt false zurück, wenn schon geantwortet wurde.
func (a *api) gateMachine(w http.ResponseWriter, r *http.Request, machine string, claim bool) bool {
	machine = strings.ToLower(strings.TrimSpace(machine))
	if machine == "" {
		return true
	}
	p := principalOf(r)
	if p.Machine != "" && strings.ToLower(p.Machine) != machine {
		writeCoded(w, http.StatusForbidden, "machine_bound", "this token is bound to machine "+p.Machine)
		return false
	}
	var err error
	// Beansprucht wird nur implizit durch Legacy-Tokens (der Collector lädt für
	// neue Hostnamen hoch) und gebundene Tokens für ihre eigene Maschine. Alle
	// anderen tragen ihre Maschine schon seit
	// dem Geräte-Login oder prüfen nur.
	if claim && (p.TokenKind == "legacy" || p.Machine != "") {
		err = a.st.ClaimMachine(machine, p.ID)
	} else {
		err = a.st.MachineClaimable(machine, p.ID)
	}
	switch {
	case errors.Is(err, store.ErrMachineTaken):
		writeCoded(w, http.StatusConflict, "machine_name_taken", "machine name "+machine+" belongs to another account")
		return false
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return true
}

// ownerFilter übersetzt ?owner=me in die Konto-ID des Aufrufers. Andere Werte
// filtern nicht.
func ownerFilter(r *http.Request) string {
	if r.URL.Query().Get("owner") == "me" {
		return principalOf(r).ID
	}
	return ""
}

func (a *api) listMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := a.st.ListMachines(ownerFilter(r))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Maschinennamen sind Metadaten ihres Besitzers: mit Durchsetzung sieht jedes
	// Konto die eigenen, der Instanz-Admin alle.
	if pa := a.access(r); a.st.AccessEnforced() && !pa.IsAdmin() {
		me := principalOf(r).ID
		machines = filterTo(machines, 0, func(m store.Machine) bool { return m.AccountID == me })
	}
	writeJSON(w, http.StatusOK, machines)
}

// accountOf liefert die numerische Konto-ID eines Principals.
func accountOf(p store.Principal) (int64, bool) {
	rest, ok := strings.CutPrefix(p.ID, "person:")
	if !ok {
		return 0, false
	}
	var n int64
	for _, c := range rest {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, n > 0
}

// mayWriteSession verlangt, dass die Session dem Konto des Tokens gehört und,
// bei gebundenem Token, unter dessen Maschine läuft. Eine unbekannte Session
// lässt der Aufruf durch; der Store meldet sie wie bisher.
func (a *api) mayWriteSession(w http.ResponseWriter, r *http.Request, id int64) bool {
	sess, err := a.st.SessionByID(id)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	p := principalOf(r)
	if acct, ok := accountOf(p); !ok || acct != sess.AccountID {
		writeCoded(w, http.StatusForbidden, "session_owned", "that session belongs to another account")
		return false
	}
	if p.Machine != "" && sess.Scope.Machine != "" && !strings.EqualFold(p.Machine, sess.Scope.Machine) {
		writeCoded(w, http.StatusForbidden, "machine_bound", "this token is bound to machine "+p.Machine)
		return false
	}
	return true
}
