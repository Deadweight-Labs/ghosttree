package web

import (
	"bytes"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Einladungsseite /join/<code> (REQ-434, Paket P1).
//
// Beobachtbar für einen Fremden ohne gültigen Code ist nur: 404 mit einem
// festen Rumpf und festen Headern, oder 429, wenn die eigene Adresse zu viele
// Aufrufe gemacht hat. Nichts davon hängt von einer Einladung ab (Pitfall
// #2447): nicht vorhanden, abgelaufen, verbraucht, widerrufen, Einlader nicht
// mehr Owner und Codes ohne Link-Rolle sehen gleich aus. Der Server schreibt
// für diese Routen kein Zugriffslog; der Code steht damit höchstens im Log des
// Proxys.
//
// Das Öffnen der Seite verbraucht nichts. Eingelöst wird erst nach der
// Anmeldung: mit Identitätsanbieter über /ui/login/oidc, ohne ihn über
// /ui/login/code (beide legen das Konto und die Rolle in einer Transaktion an),
// und für ein angemeldetes Konto über POST /join/<code>/accept.
//
// Nach dem Beitritt geht es auf /join/pair (joinpair.go), die Paarung des Geräts.

const (
	joinLimit      = 30
	joinWindow     = 10 * time.Minute
	joinLimiterMax = 10000
)

// joinCodeLen: Einladungscodes sind 32 Zufallsbytes in Hex.
const joinCodeLen = 64

type joinCount struct {
	start time.Time
	n     int
}

type joinLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	counts map[string]joinCount
}

func newJoinLimiter() *joinLimiter {
	return &joinLimiter{now: time.Now, counts: map[string]joinCount{}}
}

// allow zählt einen Aufruf für die Adresse in einem festen Fenster je Adresse.
// Der Zähler gehört der Adresse, nie dem Code, damit ein Fremder keine echte
// Einladung sperren kann. Ist die Tabelle voll, fliegen erst abgelaufene
// Fenster raus, dann die älteste Adresse; die Zähler der anderen bleiben.
func (l *joinLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.now()
	c, ok := l.counts[key]
	if !ok || at.Sub(c.start) >= joinWindow {
		if !ok && len(l.counts) >= joinLimiterMax {
			l.evict(at)
		}
		c = joinCount{start: at}
	}
	c.n++
	l.counts[key] = c
	return c.n <= joinLimit
}

func (l *joinLimiter) evict(at time.Time) {
	var oldest string
	var oldestAt time.Time
	for k, c := range l.counts {
		if at.Sub(c.start) >= joinWindow {
			delete(l.counts, k)
			continue
		}
		if oldest == "" || c.start.Before(oldestAt) {
			oldest, oldestAt = k, c.start
		}
	}
	if len(l.counts) >= joinLimiterMax && oldest != "" {
		delete(l.counts, oldest)
	}
}

// joinClientKey bündelt IPv6-Adressen auf ihr /64, sonst reichte ein Wechsel
// innerhalb des Anschlusses, um die Grenze zu umgehen.
func (a *app) joinClientKey(r *http.Request) string {
	addr := a.proxies.Client(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr
}

func (a *app) joinHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	// strict-origin, nicht no-referrer: mit no-referrer sendet der Browser bei
	// jedem POST "Origin: null", und die Same-Origin-Prüfung lehnt Beitritt und
	// Anmeldung ab. strict-origin schickt nur Schema und Host, nie den Pfad mit
	// dem Code, und beim Wechsel zu http gar nichts.
	h.Set("Referrer-Policy", "strict-origin")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("X-Content-Type-Options", "nosniff")
	// Keine Skripte, keine fremden Ressourcen, nicht einbettbar. form-action
	// bleibt offen: der Weg zum Identitätsanbieter ist eine Weiterleitung.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// joinGate zählt den Aufruf. Es antwortet selbst, wenn die Grenze erreicht ist.
func (a *app) joinGate(w http.ResponseWriter, r *http.Request) bool {
	if a.joinLimits == nil {
		return true
	}
	if a.joinLimits.allow(a.joinClientKey(r)) {
		return true
	}
	a.joinHeaders(w)
	w.Header().Set("Retry-After", strconv.Itoa(int(joinWindow/time.Second)))
	w.WriteHeader(http.StatusTooManyRequests)
	a.joinWrite(w, "joinbusy", nil)
	return false
}

func (a *app) joinWrite(w http.ResponseWriter, name string, data any) {
	var out bytes.Buffer
	if err := pages.ExecuteTemplate(&out, name, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_, _ = out.WriteTo(w)
}

// joinNotFound ist die eine Antwort für jeden Code, der nicht taugt.
func (a *app) joinNotFound(w http.ResponseWriter) {
	a.joinHeaders(w)
	w.WriteHeader(http.StatusNotFound)
	a.joinWrite(w, "joinnotfound", nil)
}

func wellFormedJoinCode(code string) bool {
	if len(code) != joinCodeLen {
		return false
	}
	for _, c := range code {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// joinPreview prüft den Code nur lesend. Ein Fehlerfall ist immer dasselbe.
func (a *app) joinPreview(code string) (store.InvitePreview, bool) {
	if !wellFormedJoinCode(code) {
		return store.InvitePreview{}, false
	}
	p, err := a.store.PreviewInvitation(code, a.store.AccessEnforced())
	return p, err == nil
}

type joinView struct {
	Org, Project, Role, RoleText, ExpiresAt string
	Code, CSRFToken, Person                 string
	SignedIn, OIDC, NeedsName               bool
	AccountID, Email                        string
}

func roleText(role string) string {
	if role == store.RoleGuest {
		return "guest, with limited access to this project"
	}
	return "member, who can work in this project"
}

// joinSession liefert das angemeldete Konto, wenn eine interaktive Sitzung da ist.
func (a *app) joinSession(r *http.Request) (browserSession, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return browserSession{}, false
	}
	session, ok := a.sessions.get(cookie.Value)
	if !ok || session.principal.TokenKind != store.WebSessionKind || !a.store.PrincipalValid(session.principal) {
		return browserSession{}, false
	}
	return session, true
}

func (a *app) joinPage(w http.ResponseWriter, r *http.Request) {
	if !a.joinGate(w, r) {
		return
	}
	code := r.PathValue("code")
	preview, ok := a.joinPreview(code)
	if !ok {
		a.joinNotFound(w)
		return
	}
	view := joinView{Org: preview.Org, Project: preview.Project, Role: preview.Role, RoleText: roleText(preview.Role),
		ExpiresAt: preview.ExpiresAt, Code: code, OIDC: a.oidc != nil, NeedsName: a.oidc == nil}
	if session, ok := a.joinSession(r); ok {
		view.SignedIn, view.Person, view.CSRFToken = true, session.principal.Label, session.csrf
		// Neben dem Namen die Kennung und die Adresse des Kontos: ein Name mit
		// ähnlich aussehenden Zeichen fällt so auf.
		view.AccountID = session.principal.ID
		if acct, err := a.store.AccountByPrincipalID(session.principal.ID); err == nil {
			view.Email = acct.Email
		}
	}
	a.joinHeaders(w)
	a.joinWrite(w, "join", view)
}

// joinAccept löst die Einladung für das angemeldete Konto ein. Aufgerufen wird
// es hinter requirePerson, requireInteractive und requireCSRF.
func (a *app) joinAccept(w http.ResponseWriter, r *http.Request) {
	if !a.joinGate(w, r) {
		return
	}
	code := r.PathValue("code")
	preview, ok := a.joinPreview(code)
	if !ok {
		a.joinNotFound(w)
		return
	}
	// Wer hier als fremdes Konto angemeldet ist (etwa durch einen untergeschobenen
	// Login), muss den Kontonamen ausdrücklich bestätigen. Ein Sitzungsalter
	// ließe sich nicht prüfen, ohne die Sitzungen zu ändern, und die Bestätigung
	// fängt auch den Fall ab, dass das Konto alt, aber nicht das eigene ist.
	if r.FormValue("confirm_account") != browserPrincipal(r).Label {
		a.joinMessage(w, http.StatusBadRequest, "Please confirm your account", "Go back to the invitation and tick the box that names the account you are joining with.")
		return
	}
	_, err := a.store.AcceptInvitation(browserPrincipal(r).ID, code)
	switch {
	case err == nil:
		a.joinAccepted(w, r, preview)
	case errors.Is(err, store.ErrCodeInvalid):
		a.joinNotFound(w)
	case errors.Is(err, store.ErrAlreadyMember):
		a.joinMessage(w, http.StatusConflict, "You already have this access", "Your account already has this role in the project or a higher one. The invitation was not used.")
	case errors.Is(err, store.ErrTooManyAttempts):
		a.joinMessage(w, http.StatusTooManyRequests, "Too many wrong codes", "Wait a few minutes before trying again.")
	case errors.Is(err, store.ErrAccountDisabled):
		a.joinMessage(w, http.StatusForbidden, "Account disabled", "This account is disabled.")
	default:
		a.joinMessage(w, http.StatusInternalServerError, "Joining failed", "Something went wrong. The invitation may still be usable; try again in a moment.")
	}
}

// joinSignOut beendet die Sitzung und führt zurück zur Einladung ("Not you?").
func (a *app) joinSignOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, a.sessionCookieFor(r, "", -1))
	back := "/ui/login"
	if code := r.PathValue("code"); wellFormedJoinCode(code) {
		back = "/join/" + code
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// joinAccepted ist der Ort, an dem der Beitritt endet: das Konto bekommt eine
// Join-Sitzung mit Paarungscode (joinpair.go).
func (a *app) joinAccepted(w http.ResponseWriter, r *http.Request, _ store.InvitePreview) {
	a.joinPairStart(w, r, browserPrincipal(r).ID)
}

// joinPairStart legt die Sitzung des Kontos an und leitet auf die Paarungsseite.
func (a *app) joinPairStart(w http.ResponseWriter, r *http.Request, account string) {
	if _, err := a.store.Join().Create(account); err != nil {
		http.Redirect(w, r, "/ui/requests", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
}

func (a *app) joinMessage(w http.ResponseWriter, status int, title, message string) {
	a.joinHeaders(w)
	w.WriteHeader(status)
	a.joinWrite(w, "joinmsg", struct{ Title, Message string }{title, message})
}
