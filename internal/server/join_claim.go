package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Join-Paarung für den Installer (REQ-434, Paket P2). Beide Aufrufe sind ohne
// Token erreichbar.
//
// POST /api/join/claim meldet ein Gerät mit dem Paarungscode der Join-Seite an.
// Mit code_challenge (S256), loopback_port und state wählt der Client den
// Loopback-Weg (RFC 8252 mit PKCE): nach der Freigabe im Browser landet ein
// einmaliger Code auf http://127.0.0.1:<port>/callback, und
// POST /api/join/token tauscht ihn gegen das Token, aber nur mit dem
// code_verifier. Ohne diese Felder gilt der Rückfall: die Antwort enthält einen
// Bestätigungscode, den die Freigabeseite abfragt, und das Token kommt über den
// Geräte-Ablauf (/api/auth/device/token).
//
// Die Antwort trägt ein zufälliges "resume". Nur wer es beim erneuten Claim
// zurückschickt (derselbe Installer nach Strg-C), ersetzt seine eigene noch
// nicht freigegebene Anfrage; jeder andere zweite Claim verwirft die Sitzung.
//
// Beobachtbar für einen Fremden ist nur: 400 invalid_pair bzw. invalid_grant für
// jeden Code, der nicht taugt, 429 (Netz gesperrt oder zu viele offene Abläufe;
// hängt nie vom Code ab) und 400/413 invalid_request für einen kaputten Körper
// (ebenfalls vom Code unabhängig). Codes stehen nur in Körpern, nie in URLs.
// 409 machine_name_taken kommt erst nach der Prüfung des Codes und nennt nur,
// was die Freigabeseite dem Konto ohnehin sagte: der Name ist vergeben.
// validJoinMachine ist strenger als validMachine: der Name steht auf der
// Freigabeseite, wo ein Fremder mit einem Claim Text anzeigen lassen kann, und
// darf dort kein Satz sein, der zum Freigeben auffordert. Der Geräte-Ablauf
// behält die weitere Regel.
func validJoinMachine(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

type joinClaimRequest struct {
	Pair                string `json:"pair"`
	Machine             string `json:"machine"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	LoopbackPort        int    `json:"loopback_port"`
	LoopbackHost        string `json:"loopback_host"`
	State               string `json:"state"`
	Resume              string `json:"resume"`
	// MachineAuto: machine ist der Hostname, kein Wunsch. Ist er vergeben,
	// wählt der Server einen freien Namen und nennt ihn in der Antwort.
	MachineAuto bool `json:"machine_auto"`
}

func (a *api) claimJoin(w http.ResponseWriter, r *http.Request) {
	var req joinClaimRequest
	if !readDeviceBody(w, r, &req) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	machine := strings.TrimSpace(req.Machine)
	if !validJoinMachine(machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "machine is required (letters, digits, '.', '_' and '-', at most 64)"})
		return
	}
	claim := store.JoinClaimRequest{Addr: a.clientAddr(r), Pair: req.Pair, Machine: machine,
		Challenge: req.CodeChallenge, State: req.State, Host: req.LoopbackHost, Port: req.LoopbackPort, Resume: req.Resume,
		Auto: req.MachineAuto, Resolve: a.resolveJoinMachine}
	if claim.Loopback() && (!claim.ValidLoopback() || req.CodeChallengeMethod != "S256") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "loopback needs code_challenge (S256), loopback_port (1024-65535) and state"})
		return
	}
	if len(req.Resume) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "resume is too long"})
		return
	}
	var locked *store.JoinNamesLockedError
	out, err := a.st.Join().Claim(claim)
	switch {
	case err == nil:
		body := map[string]any{"mode": out.Mode, "expires_in": int(out.ExpiresIn.Seconds()), "machine": out.Machine,
			"verification_uri": a.requestBaseURL(r) + "/join/pair", "resume": out.Resume}
		if out.Mode == store.JoinModeLoopback {
			body["token_endpoint"] = "/api/join/token"
		} else {
			body["token_endpoint"] = "/api/auth/device/token"
			body["device_code"] = out.DeviceCode
			body["confirm_code"] = out.Confirm
			body["interval"] = int(out.Interval.Seconds())
		}
		writeJSON(w, http.StatusOK, body)
	case errors.Is(err, store.ErrJoinInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pair"})
	case errors.As(err, &locked):
		secs := int(locked.RetryAfter.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "names_locked", "retry_after": secs,
			"error_description": "too many different machine names were refused for this account; try again later"})
	case errors.Is(err, store.ErrMachineTaken):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "machine_name_taken", "error_description": "that machine name belongs to another account; start again with a different machine name"})
	case errors.Is(err, store.ErrJoinLocked), errors.Is(err, store.ErrDeviceBusy):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_requests"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
	}
}

type joinTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

// exchangeJoin tauscht den Code vom Loopback-Callback gegen das Token (PKCE).
func (a *api) exchangeJoin(w http.ResponseWriter, r *http.Request) {
	var req joinTokenRequest
	if !readDeviceBody(w, r, &req) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	grant, err := a.st.Join().Exchange(a.clientAddr(r), strings.TrimSpace(req.Code), req.CodeVerifier)
	switch {
	case errors.Is(err, store.ErrJoinInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	case errors.Is(err, store.ErrJoinLocked):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_requests"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	token, info, err := a.st.CreateDeviceToken(grant.Account, grant.Machine)
	if errors.Is(err, store.ErrMachineTaken) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "machine_name_taken", "error_description": "that machine name belongs to another account; start again with a different machine name"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
		return
	}
	a.st.Join().Delivered(grant)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "bearer", "machine": info.Machine, "token_id": info.ID})
}

// resolveJoinMachine wählt den Namen, unter dem das Konto der Join-Sitzung die
// Maschine anmelden kann. Der Aufruf kommt erst nach der Prüfung des Codes.
func (a *api) resolveJoinMachine(account, machine string, auto bool) (string, error) {
	return a.st.ResolveMachineName(account, machine, auto)
}
