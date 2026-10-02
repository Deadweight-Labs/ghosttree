package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// POST /api/join/claim (REQ-434, Paket P2): der Installer (ctx join) meldet sein
// Gerät mit dem Paarungscode an, den die Join-Seite zeigt. Der Aufruf braucht
// kein Token. Er startet den gewöhnlichen Geräte-Ablauf; das Token holt der
// Client danach bei POST /api/auth/device/token mit dem device_code ab, sobald
// das Konto im Browser freigegeben hat.
//
// Beobachtbar für einen Fremden ist nur: 400 invalid_pair (für jeden Code, der
// nicht taugt), 429 (Adresse gesperrt oder zu viele offene Abläufe; hängt nie
// vom Code ab), 400/413 invalid_request für einen kaputten Körper (ebenfalls
// vom Code unabhängig). Der Paarungscode steht nur im Körper, nie in einer URL.
type joinClaimRequest struct {
	Pair    string `json:"pair"`
	Machine string `json:"machine"`
}

func (a *api) claimJoin(w http.ResponseWriter, r *http.Request) {
	var req joinClaimRequest
	if !readDeviceBody(w, r, &req) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	machine := strings.TrimSpace(req.Machine)
	if !validMachine(machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "machine is required (printable, at most 128 bytes)"})
		return
	}
	addr := a.clientAddr(r)
	claim, err := a.st.Join().Claim(addr, req.Pair, machine, addr)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code":      claim.DeviceCode,
			"expires_in":       int(claim.ExpiresIn.Seconds()),
			"interval":         int(claim.Interval.Seconds()),
			"token_endpoint":   "/api/auth/device/token",
			"verification_uri": a.requestBaseURL(r) + "/join/pair",
		})
	case errors.Is(err, store.ErrJoinInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pair"})
	case errors.Is(err, store.ErrJoinLocked), errors.Is(err, store.ErrDeviceBusy):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_requests"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
	}
}
