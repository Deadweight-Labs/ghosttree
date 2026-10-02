package server

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Grenzen für die beiden unangemeldeten Endpunkte des Geräte-Logins.
const (
	maxDeviceBody    = 4 << 10
	maxMachineLength = 128
)

// deviceStartRequest und deviceTokenRequest sind die JSON-Körper.
type deviceStartRequest struct {
	Machine string `json:"machine"`
}
type deviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

func isDevicePath(path string) bool {
	return path == "/api/auth/device" || path == "/api/auth/device/token" || path == "/api/join/claim" || path == "/api/join/token"
}

// readDeviceBody liest einen begrenzten JSON-Körper. Beide Endpunkte sind ohne
// Token erreichbar, also darf keine Anfrage mehr als maxDeviceBody belegen.
func readDeviceBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxDeviceBody)
	if err := readJSON(r, v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "invalid_request"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		}
		return false
	}
	return true
}

// validMachine erlaubt druckbare Zeichen ohne Steuerzeichen, bis 128 Byte.
func validMachine(name string) bool {
	if name == "" || len(name) > maxMachineLength || !utf8.ValidString(name) {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) || !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// clientAddr ist die Absenderadresse für die Begrenzung offener Abläufe. Nur
// von einem vertrauenswürdigen Proxy zählt X-Forwarded-For: alle Zeilen werden
// verbunden und von rechts gelesen, der erste nicht vertrauenswürdige Eintrag
// ist der Client. Vom Client mitgeschickte Einträge stehen links davon.
func (a *api) clientAddr(r *http.Request) string {
	return a.proxies.Client(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
}

func (a *api) requestBaseURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	host := r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if a.proxies.Trusts(r.RemoteAddr) {
		if p := singleHeader(r.Header, "X-Forwarded-Proto"); p == "http" || p == "https" {
			scheme = p
		}
		if h := singleHeader(r.Header, "X-Forwarded-Host"); h != "" && !strings.ContainsAny(h, " /\\@,") {
			host = h
		}
	}
	return scheme + "://" + host
}

// startDeviceLogin ist der Beginn des Geräte-Logins (RFC 8628, Abschnitt 3.1).
func (a *api) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req deviceStartRequest
	if !readDeviceBody(w, r, &req) {
		return
	}
	machine := strings.TrimSpace(req.Machine)
	if !validMachine(machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "machine is required (printable, at most 128 bytes)"})
		return
	}
	start, err := a.st.Device().Start(a.clientAddr(r), machine, a.clientAddr(r))
	if err != nil {
		if errors.Is(err, store.ErrDeviceBusy) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_requests"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	uri := a.requestBaseURL(r) + "/ui/device"
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               start.DeviceCode,
		"user_code":                 store.FormatUserCode(start.UserCode),
		"verification_uri":          uri,
		"verification_uri_complete": uri + "?user_code=" + store.FormatUserCode(start.UserCode),
		"expires_in":                int(start.ExpiresIn.Seconds()),
		"interval":                  int(start.Interval.Seconds()),
	})
}

// pollDeviceLogin beantwortet die Abfrage des Clients (RFC 8628, Abschnitt 3.4
// und 3.5). Das Token wird erst hier ausgestellt und nie gespeichert; der
// Ablauf ist danach verbraucht.
func (a *api) pollDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req deviceTokenRequest
	if !readDeviceBody(w, r, &req) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	approval, interval, err := a.st.Device().Poll(strings.TrimSpace(req.DeviceCode))
	switch {
	case errors.Is(err, store.ErrDevicePending):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending", "interval": int(interval.Seconds())})
	case errors.Is(err, store.ErrDeviceSlowDown):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slow_down", "interval": int(interval.Seconds())})
	case errors.Is(err, store.ErrDeviceDenied):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
	case errors.Is(err, store.ErrDeviceUnknown):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
	default:
		token, info, err := a.st.CreateDeviceToken(approval.AccountID, approval.Machine)
		if errors.Is(err, store.ErrMachineTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "machine_name_taken", "error_description": "that machine name belongs to another account; run ctx login with a different --machine"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
			return
		}
		// Erst jetzt, mit ausgestelltem Token, gilt eine Join-Paarung als verbunden.
		a.st.Join().DeliveredDevice(strings.TrimSpace(req.DeviceCode))
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": token, "token_type": "bearer", "machine": info.Machine, "token_id": info.ID,
		})
	}
}

// singleHeader returns the value of a header sent exactly once and without a
// comma list; repeated or combined values are not believed.
func singleHeader(h http.Header, name string) string {
	v := h.Values(name)
	if len(v) != 1 || strings.Contains(v[0], ",") {
		return ""
	}
	return strings.TrimSpace(v[0])
}

// revokeOwnToken widerruft das Token, mit dem die Anfrage authentifiziert ist
// (`ctx join` räumt damit ein ungenutztes Token wieder ab). Nur für Bearer-
// Token; eine Web-Sitzung hat keines. Ein zweiter Aufruf scheitert schon an der
// Authentifizierung mit 401. Das Log nennt nur Token-ID und Konto.
func (a *api) revokeOwnToken(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.TokenKind == store.WebSessionKind || p.TokenID == 0 {
		writeCoded(w, http.StatusForbidden, "bearer_token_required", "this call revokes the bearer token it was made with")
		return
	}
	switch err := a.st.RevokeOwnToken(p); {
	case errors.Is(err, store.ErrTokenNotActive):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	if a.logger != nil {
		a.logger.Info("token_self_revoked", "token_id", p.TokenID, "account", p.ID)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
