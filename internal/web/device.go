package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const maxDeviceForm = 4 << 10

// limitBody begrenzt Formularkörper angemeldeter Seiten. requireCSRF liest den
// Körper mit ParseForm, das ohne Grenze bis zu 10 MB annimmt.
func limitBody(next http.Handler) http.Handler { return limitBodyN(maxDeviceForm, next) }

// limitBodyN ist limitBody mit eigener Grenze, für Formulare mit langem Text.
func limitBodyN(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// devicePage zeigt das Eingabefeld für den User-Code. Ein Aufruf mit
// ?user_code= füllt es vor, bestätigt aber nichts: erst ein POST mit
// CSRF-Token entscheidet.
func (a *app) devicePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	code := r.URL.Query().Get("user_code")
	if len(code) > 32 {
		code = ""
	}
	a.renderBrowser(w, r, "device", pageData{Title: "Approve a device", Code: code})
}

// deviceLookup prüft den eingegebenen Code und zeigt, wer ihn angefordert hat.
func (a *app) deviceLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	code := strings.TrimSpace(r.FormValue("user_code"))
	req, err := a.store.Device().Lookup(code, browserPrincipal(r).ID)
	if err != nil {
		a.deviceError(w, r, err, code)
		return
	}
	if err := a.store.MachineClaimable(req.Machine, browserPrincipal(r).ID); err != nil {
		a.deviceError(w, r, err, "")
		return
	}
	a.renderBrowser(w, r, "devicecheck", pageData{Title: "Approve a device",
		Code: store.FormatUserCode(store.NormalizeUserCode(code)), DeviceMachine: req.Machine,
		DeviceRemote: req.Remote, DeviceStarted: req.StartedAt.UTC().Format(time.RFC3339)})
}

// deviceDecide bestätigt oder verweigert. Die Maschine, die die CLI nannte,
// wird mit dem Token des Kontos verbunden, sobald die CLI es abholt.
func (a *app) deviceDecide(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	code := strings.TrimSpace(r.FormValue("user_code"))
	approve := r.FormValue("decision") == "approve"
	if approve {
		// Der Name wird erst beim Abholen beansprucht; wer ihn schon vor der
		// Bestätigung vergeben sieht, spart der CLI den Fehlschlag.
		if req, err := a.store.Device().Lookup(code, browserPrincipal(r).ID); err == nil {
			if err := a.store.MachineClaimable(req.Machine, browserPrincipal(r).ID); err != nil {
				a.deviceError(w, r, err, "")
				return
			}
		}
	}
	if err := a.store.Device().Decide(code, browserPrincipal(r).ID, approve); err != nil {
		a.deviceError(w, r, err, code)
		return
	}
	title := "Device denied"
	if approve {
		title = "Device approved"
	}
	a.renderBrowser(w, r, "devicedone", pageData{Title: title, Approved: approve})
}

func (a *app) deviceError(w http.ResponseWriter, r *http.Request, err error, code string) {
	status, msg := http.StatusBadRequest, "That code is not valid or has expired. Run ctx login again if it keeps failing."
	if errors.Is(err, store.ErrMachineTaken) {
		status, msg = http.StatusConflict, "That machine name belongs to another account. Run ctx login again with --machine and a different name."
	}
	if errors.Is(err, store.ErrDeviceLocked) {
		status, msg = http.StatusTooManyRequests, "Too many wrong codes. Wait a few minutes and try again."
	}
	w.WriteHeader(status)
	a.renderBrowser(w, r, "device", pageData{Title: "Approve a device", Error: msg, Code: ""})
}

type tokenRow struct {
	ID                                    int64
	Account, Label, Kind, Machine, Status string
	Created, LastUsed, Expires, Revoked   string
	Revocable                             bool
}

// tokenKindLabel übersetzt die gespeicherte Art in die angezeigte: 'cli' sind
// von Hand ausgestellte Tokens.
func tokenKindLabel(kind string) string {
	if kind == "cli" {
		return "manual"
	}
	return kind
}

// tokensPage listet die Tokens des Kontos; Administratoren sehen alle.
func (a *app) tokensPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal := browserPrincipal(r)
	admin := a.isAdmin(principal)
	var tokens []store.TokenInfo
	var err error
	if admin {
		tokens, err = a.store.ListAllTokens()
	} else {
		tokens, err = a.store.ListTokens(principal.Label)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nowText := time.Now().UTC().Format(time.RFC3339)
	rows := make([]tokenRow, 0, len(tokens))
	for _, t := range tokens {
		status := "active"
		switch {
		case t.RevokedAt != "":
			status = "revoked"
		case t.ExpiresAt != "" && t.ExpiresAt <= nowText:
			status = "expired"
		}
		account := t.Account
		if account == "" {
			account = principal.Label
		}
		rows = append(rows, tokenRow{ID: t.ID, Account: account, Label: t.Label, Kind: tokenKindLabel(t.Kind), Machine: t.Machine,
			Status: status, Created: t.CreatedAt, LastUsed: t.LastUsedAt, Expires: t.ExpiresAt, Revoked: t.RevokedAt,
			Revocable: status == "active"})
	}
	a.renderBrowser(w, r, "tokens", pageData{Title: "Tokens", Tokens: rows, Admin: admin})
}

func (a *app) isAdmin(p store.Principal) bool {
	acct, err := a.store.AccountByPrincipalID(p.ID)
	return err == nil && acct.Admin
}

// tokenRevoke widerruft ein Token des Kontos, als Administrator auch ein
// fremdes. Der Widerruf gilt sofort: API und Websitzungen prüfen das Token bei
// jeder Anfrage.
func (a *app) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("token_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	principal := browserPrincipal(r)
	info, err := a.store.TokenByID(id)
	if err != nil || (info.AccountID != principal.ID && !a.isAdmin(principal)) {
		http.NotFound(w, r)
		return
	}
	// Fremde Tokens widerruft ein Administrator nur aus einer interaktiven
	// Sitzung; eigene darf jede Sitzung widerrufen, das nimmt nur Macht.
	if info.AccountID != principal.ID && !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	if err := a.store.RevokeToken(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/account/tokens", http.StatusSeeOther)
}
