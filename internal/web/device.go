package web

import (
	"errors"
	"net/http"
	"slices"
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
	a.renderBrowser(w, r, "device", pageData{Title: msg("adm.device.title"), Code: code})
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
	a.renderBrowser(w, r, "devicecheck", pageData{Title: msg("adm.device.title"),
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
				if errors.Is(err, store.ErrMachineTaken) {
					// The terminal learns it now, not when the code expires.
					_ = a.store.Device().RefuseMachineTaken(code, browserPrincipal(r).ID)
				}
				a.deviceError(w, r, err, "")
				return
			}
		}
	}
	if err := a.store.Device().Decide(code, browserPrincipal(r).ID, approve); err != nil {
		a.deviceError(w, r, err, code)
		return
	}
	title := msg("adm.device.denied")
	if approve {
		title = msg("adm.device.approved")
	}
	a.renderBrowser(w, r, "devicedone", pageData{Title: title, Approved: approve})
}

func (a *app) deviceError(w http.ResponseWriter, r *http.Request, err error, code string) {
	status, text := http.StatusBadRequest, msg("adm.device.err_code")
	if errors.Is(err, store.ErrMachineTaken) {
		status, text = http.StatusConflict, msg("adm.device.err_machine")
	}
	if errors.Is(err, store.ErrDeviceLocked) {
		status, text = http.StatusTooManyRequests, msg("adm.device.err_locked")
	}
	w.WriteHeader(status)
	a.renderBrowser(w, r, "device", pageData{Title: msg("adm.device.title"), Error: text, Code: ""})
}

type tokenRow struct {
	ID                                    int64
	Account, Label, Kind, Machine, Status string
	Created, LastUsed, Expires, Revoked   string
	Revocable                             bool
	// Unused: never used, and old enough that it is not simply waiting for its
	// first call. These are what "Revoke unused" revokes.
	Unused bool
	// Tracked: use of this token has been recorded long enough for an empty
	// last use to mean "never".
	Tracked bool
}

// tokenUnusedGrace is how long a token has to have been observed without a
// single call before it counts as unused. Observed means: since it was issued
// or since use tracking began, whichever is later. A token used only now and
// then must have had the chance to show up.
const tokenUnusedGrace = 14 * 24 * time.Hour

// tokenTracked says whether a token has been observed for the grace period.
func tokenTracked(t store.TokenInfo, now, since time.Time) bool {
	observed := parseTime(t.CreatedAt)
	if since.IsZero() || observed.IsZero() {
		return false
	}
	if since.After(observed) {
		observed = since
	}
	return now.Sub(observed) >= tokenUnusedGrace
}

// tokenIsUnused says whether a token is active, was never used and is older
// than the grace period. The token the current session signed in with is never
// unused, whatever its last-used column says: revoking it would end the very
// session that asks.
func tokenIsUnused(t store.TokenInfo, now time.Time, current int64, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	if t.RevokedAt != "" || t.LastUsedAt != "" || (current != 0 && t.ID == current) {
		return false
	}
	if t.ExpiresAt != "" && t.ExpiresAt <= now.Format(time.RFC3339) {
		return false
	}
	observed := parseTime(t.CreatedAt)
	if observed.IsZero() {
		return false
	}
	if since.After(observed) {
		observed = since
	}
	return now.Sub(observed) >= tokenUnusedGrace
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
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339)
	since := parseTime(a.store.TokenUseTrackingSince())
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
			Revocable: status == "active", Tracked: tokenTracked(t, now, since), Unused: tokenIsUnused(t, now, principal.TokenID, since)})
	}
	sortTokenRows(rows)
	data := pageData{Title: msg("adm.tokens.title"), Tokens: rows, Admin: admin}
	for _, row := range rows {
		if row.Unused {
			data.UnusedTokens++
			if row.Account != principal.Label {
				data.UnusedForeign = true
			}
		}
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("revoked")); err == nil && n > 0 {
		data.TokenResult = msg("adm.tokens.revoke_unused_done", n)
	}
	a.renderBrowser(w, r, "tokens", data)
}

// sortTokenRows puts the tokens in use first, the most recently used on top;
// what was never used follows, newest first; revoked and expired ones close the
// list.
func sortTokenRows(rows []tokenRow) {
	rank := func(t tokenRow) int {
		switch {
		case t.Status != "active":
			return 2
		case t.LastUsed == "":
			return 1
		}
		return 0
	}
	slices.SortStableFunc(rows, func(x, y tokenRow) int {
		if rx, ry := rank(x), rank(y); rx != ry {
			return rx - ry
		}
		if rank(x) == 0 && x.LastUsed != y.LastUsed {
			return strings.Compare(y.LastUsed, x.LastUsed)
		}
		return strings.Compare(y.Created, x.Created)
	})
}

// tokensRevokeUnused revokes the tokens that were never used: the person's own,
// and for an administrator those of every account the page lists. Tokens of
// other accounts only from an interactive session, like a single revoke.
func (a *app) tokensRevokeUnused(w http.ResponseWriter, r *http.Request) {
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
	now := time.Now().UTC()
	since := parseTime(a.store.TokenUseTrackingSince())
	var ids []int64
	foreign := false
	for _, t := range tokens {
		if !tokenIsUnused(t, now, principal.TokenID, since) {
			continue
		}
		if t.AccountID != "" && t.AccountID != principal.ID {
			foreign = true
		}
		ids = append(ids, t.ID)
	}
	if foreign && !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	for _, id := range ids {
		if err := a.store.RevokeToken(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/ui/account/tokens?revoked="+strconv.Itoa(len(ids)), http.StatusSeeOther)
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
