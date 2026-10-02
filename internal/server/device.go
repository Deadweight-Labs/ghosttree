package server

import (
	"errors"
	"net"
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
	return path == "/api/auth/device" || path == "/api/auth/device/token"
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

// clientAddr ist die Absenderadresse für die Begrenzung offener Abläufe. Hinter
// einem Proxy auf Loopback zählt der letzte X-Forwarded-For-Eintrag, den der
// Proxy selbst angehängt hat; von sonst woher ist der Header beliebig.
func (a *api) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a.proxies.Trusts(r.RemoteAddr) {
		if parts := strings.Split(r.Header.Get("X-Forwarded-For"), ","); len(parts) > 0 {
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
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
		if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
			scheme = p
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" && !strings.ContainsAny(h, " /\\@,") {
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
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": token, "token_type": "bearer", "machine": info.Machine, "token_id": info.ID,
		})
	}
}
