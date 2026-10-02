package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Join-Sitzung und Paarung (REQ-434, Paket P2).
//
// Nach der Annahme einer Einladung (oder auf Wunsch eines angemeldeten Kontos)
// ist die Join-Sitzung an das Konto gebunden, und die Seite zeigt den
// Paarungscode samt Befehl. Der Installer meldet sich damit bei
// POST /api/join/claim; die Seite zeigt dann "<Gerät> wants to connect" mit
// Maschinenname, Absenderadresse und (wenn aussagekräftig) "Same network".
// Freigabe und Ablehnung kommen nur vom Konto aus einer interaktiven Sitzung
// (CSRF, gleiche Origin, Kontobestätigung, Kennung der gezeigten Anfrage).
//
// Zwei Wege zum Token. Loopback (Regelfall): die Entscheidung antwortet mit 303
// auf http://127.0.0.1:<port>/callback des Installers, bei Freigabe mit code und
// state, bei Ablehnung mit error=access_denied und state; das Token holt der
// Installer mit dem code_verifier (POST /api/join/token). Rückfall (Code): die
// Freigabe verlangt den im Terminal gezeigten Bestätigungscode, das Token kommt
// über den Geräte-Ablauf.
//
// Die Seite gehört dem angemeldeten Konto: Code und Gerät stehen nur dort, nie in
// einer URL, nie in einem Log. Sie lädt sich selbst neu (meta refresh), solange
// sie wartet; das CSP der Join-Seiten erlaubt keine Skripte.

// joinPairView ist die Sicht der Paarungsseite.
type joinPairView struct {
	State, Pair, Command, Machine, Remote string
	Nonce                                 string
	SameNet, ShowNet, NeedsCode           bool
	Interrupted                           bool
	Callback                              string
	Person, AccountID, CSRFToken, Base    string
	Refresh                               int
}

// joinBase ist die Adresse, unter der der Server von außen erreichbar ist: die
// öffentliche URL, sonst Schema und Host der Anfrage, mit X-Forwarded-* nur von
// einem vertrauten Proxy (wie sameOrigin und der Server beim Geräte-Login).
func (a *app) joinBase(r *http.Request) string {
	if a.publicOrigin != "" {
		return a.publicOrigin
	}
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	proto, protoSet := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	fwdHost, hostSet := singleForwardedValue(r.Header, "X-Forwarded-Host")
	if (protoSet || hostSet) && a.proxies.Trusts(r.RemoteAddr) && protoSet && hostSet &&
		(proto == "http" || proto == "https") && validForwardedHost(fwdHost) {
		scheme, host = proto, fwdHost
	}
	return scheme + "://" + host
}

// joinCommand ist der Befehl für den Installer. Er steht nur für https oder
// Loopback: ein Skript über http wäre auf dem Weg veränderbar.
func (a *app) joinCommand(r *http.Request, pair string) string {
	base := a.joinBase(r)
	u, err := url.Parse(base)
	if err != nil || pair == "" {
		return ""
	}
	loop := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !loop {
		return ""
	}
	return "curl -fsSL " + base + "/install.sh | sh -s -- --pair " + pair
}

// Der Browser-Cookie der Join-Sitzung: bindet den Browser an seine Sitzung, ohne
// dass ein Code in einer URL steht. Er gilt für alle Pfade, weil die Anmeldung
// (/ui/login/..., OIDC-Callback) ihn braucht, und hat SameSite=Lax, damit er auf
// dem Rückweg vom Identitätsanbieter mitkommt.
//
// Wo Secure gilt, heißt er __Host-gt_join: der Browser nimmt das Präfix nur mit
// Secure, Path=/ und ohne Domain an, ein Nachbar-Subdomain kann ihn also nicht
// setzen (Cookie-Fixierung). Über http (Entwicklung, Loopback) bleibt es gt_join.
const joinCookie = "gt_join"

func (a *app) joinCookieName(r *http.Request) string {
	if a.secureCookies(r) {
		return "__Host-" + joinCookie
	}
	return joinCookie
}

func (a *app) joinCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: a.joinCookieName(r), Value: value, Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (a *app) joinCookieValue(r *http.Request) string {
	if c, err := r.Cookie(a.joinCookieName(r)); err == nil && len(c.Value) <= 64 {
		return c.Value
	}
	return ""
}

// sameNetworkMeaningful: "Same network" sagt nur etwas, wenn der Server die
// echte Absenderadresse kennt. Hinter einer öffentlichen URL ohne benannte
// vertraute Proxys sähe jeder Absender wie der Proxy aus, die Zeile wäre immer
// "yes" und würde täuschen.
func (a *app) sameNetworkMeaningful() bool {
	return a.publicOrigin == "" || a.proxies.Configured()
}

// joinPairPage zeigt, je nach Zustand der Sitzung des Kontos, den Code mit
// Befehl, die Freigabefrage oder das Ergebnis.
func (a *app) joinPairPage(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	v := a.store.Join().View(p.ID)
	view := joinPairView{State: v.State, Pair: v.Pair, Machine: v.Machine, Remote: v.Remote, Nonce: v.Nonce,
		SameNet: v.Net != "" && v.Net == a.joinClientKey(r), ShowNet: a.sameNetworkMeaningful(), Callback: v.Callback, NeedsCode: v.Mode == store.JoinModeCode,
		Person: p.Label, AccountID: p.ID, CSRFToken: csrfOf(r), Base: a.joinBase(r)}
	switch v.State {
	case store.JoinWaiting:
		view.Command = a.joinCommand(r, v.Pair)
		view.Refresh = 10
	case store.JoinClaimed:
		view.Refresh = 20
	case store.JoinApproved:
		view.Refresh = 5
	case store.JoinNone:
		// Eine Seite, die sich selbst neu lud (w=1) und ihre Sitzung nicht mehr
		// findet, sagt es; ein erster Besuch zeigt nur den Knopf.
		view.Interrupted = r.URL.Query().Get("w") == "1"
	}
	a.joinHeaders(w)
	a.joinWrite(w, "joinpair", view)
}

// joinPairCreate legt die Sitzung an oder ersetzt sie durch einen neuen Code.
// Gibt es noch keine Sitzung, bestätigt das Konto seinen Namen (wie beim
// Beitritt); ein neuer Code für eine bestehende braucht das nicht.
func (a *app) joinPairCreate(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	if a.store.Join().View(p.ID).State == store.JoinNone && r.FormValue("confirm_account") != p.Label {
		a.joinMessage(w, http.StatusBadRequest, "Please confirm your account", "Tick the box that names the account this machine will connect to.")
		return
	}
	if _, err := a.store.Join().Create(p.ID); err != nil {
		a.joinMessage(w, http.StatusInternalServerError, "Setup failed", "Something went wrong. Try again in a moment.")
		return
	}
	http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
}

// joinPairDecide gibt das gemeldete Gerät frei oder lehnt es ab. Die Anfrage
// trägt die Kennung des angezeigten Geräts; gehört sie zu einem anderen, ändert
// sich nichts. Beim Loopback-Weg geht der Browser nach der Freigabe auf den
// Loopback des Installers.
func (a *app) joinPairDecide(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	approve := r.FormValue("decision") == "approve"
	if approve && r.FormValue("confirm_account") != p.Label {
		a.joinMessage(w, http.StatusBadRequest, "Please confirm your account", "Go back and tick the box that names the account this machine will connect to.")
		return
	}
	check := func(machine string) error { return a.store.MachineClaimable(machine, p.ID) }
	dec, err := a.store.Join().Decide(p.ID, approve, r.FormValue("nonce"), r.FormValue("confirm_code"), check)
	switch {
	case err == nil && dec.Redirect != "":
		http.Redirect(w, r, dec.Redirect, http.StatusSeeOther)
	case err == nil:
		http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
	case errors.Is(err, store.ErrJoinConfirm):
		a.joinMessage(w, http.StatusBadRequest, "That is not the code your terminal shows", "Go back and type the code from the terminal.")
	case errors.Is(err, store.ErrMachineTaken):
		a.joinMessage(w, http.StatusConflict, "Machine name taken", "That machine name belongs to another account. Run the command again with a different machine name.")
	case errors.Is(err, store.ErrJoinNotReady):
		a.joinMessage(w, http.StatusConflict, "Nothing to approve", "No machine is waiting for this account, or the request has changed.")
	default:
		a.joinMessage(w, http.StatusInternalServerError, "Setup failed", "Something went wrong. Try again in a moment.")
	}
}
