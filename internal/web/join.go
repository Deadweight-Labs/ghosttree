package web

import (
	"bytes"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
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
// Schon die Einladungsseite legt eine Join-Sitzung mit Paarungscode an (ohne die
// Einladung zu verbrauchen), damit Installation und Anmeldung parallel laufen.
// Nach dem Beitritt wird sie an das Konto gebunden und es geht auf /join/pair
// (joinpair.go), die Freigabe des Geräts.

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
	// bleibt offen: der Weg zum Identitätsanbieter ist eine Weiterleitung, und
	// 'self' würde auch die 303-Weiterleitung der Freigabe auf den Loopback des
	// Installers (http://127.0.0.1:<port>/callback) blockieren, weil Browser
	// form-action auf die Ziele von Weiterleitungen anwenden.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// joinScriptHeaders erlaubt dem Paarungsablauf das eine eigene Skript
// (/static/join.js) und Abfragen an dieselbe Herkunft; sonst bleibt alles zu.
func (a *app) joinScriptHeaders(w http.ResponseWriter) {
	a.joinHeaders(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
}

// joinGate zählt den Aufruf. Es antwortet selbst, wenn die Grenze erreicht ist.
func (a *app) joinGate(w http.ResponseWriter, r *http.Request) bool {
	l := a.langFor(r)
	if a.joinLimits == nil {
		return true
	}
	if a.joinLimits.allow(a.joinClientKey(r)) {
		return true
	}
	a.joinHeaders(w)
	w.Header().Set("Retry-After", strconv.Itoa(int(joinWindow/time.Second)))
	w.WriteHeader(http.StatusTooManyRequests)
	a.joinWrite(l, w, "joinbusy", nil)
	return false
}

func (a *app) joinWrite(l lang, w http.ResponseWriter, name string, data any) {
	var out bytes.Buffer
	if err := l.page().ExecuteTemplate(&out, name, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_, _ = out.WriteTo(w)
}

// joinNotFound ist die eine Antwort für jeden Code, der nicht taugt.
func (a *app) joinNotFound(l lang, w http.ResponseWriter) {
	a.joinHeaders(w)
	w.WriteHeader(http.StatusNotFound)
	a.joinWrite(l, w, "joinnotfound", nil)
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
	Inviter, Target, Org, RoleText, ExpiresAt string
	Code, CSRFToken, Person                   string
	Initial, Email                            string
	SignedIn, OIDC, Signup                    bool
	// Guest wählt den Satz, der sagt, was die Rolle sieht; Project ist der Name
	// des Projekts darin.
	Guest   bool
	Project string
}

func roleLabel(l lang, role string) string {
	if role == store.RoleGuest {
		return l.msg("join.role_guest")
	}
	return l.msg("join.role_member")
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
	l := a.langFor(r)
	if !a.joinGate(w, r) {
		return
	}
	code := r.PathValue("code")
	preview, ok := a.joinPreview(code)
	if !ok {
		a.joinNotFound(l, w)
		return
	}
	// Die Seite zeigt nur Name der einladenden Person, Projekt, Organisation,
	// Rolle und Ablauf. Eine Paarungssitzung legt erst die Annahme an (joinAccepted),
	// ein bloßes Öffnen oder Neuladen erzeugt keinen Code.
	view := joinView{Inviter: preview.Inviter, Target: preview.Project, Org: preview.Org, RoleText: roleLabel(l, preview.Role),
		ExpiresAt: preview.ExpiresAt, Guest: preview.Role == store.RoleGuest, Project: firstNonEmpty(preview.Project, l.msg("join.shares.project")),
		Code: code, OIDC: a.oidc != nil, Signup: a.oidc != nil && a.oidc.signupOffered(r.Context())}
	if session, ok := a.joinSession(r); ok {
		view.SignedIn, view.Person, view.CSRFToken = true, session.principal.Label, session.csrf
		view.Initial = initialOf(view.Person)
		if acct, err := a.store.AccountByPrincipalID(session.principal.ID); err == nil {
			view.Email = acct.Email
		}
		if a.store.InvitationCovered(session.principal.ID, code) {
			a.joinAlreadyMember(l, w, preview)
			return
		}
	}
	a.joinHeaders(w)
	a.joinWrite(l, w, "join", view)
}

// joinAlreadyMember tells a signed-in member that the link gives them nothing
// new, with a way into the project. The invitation stays unused.
func (a *app) joinAlreadyMember(l lang, w http.ResponseWriter, p store.InvitePreview) {
	a.joinHeaders(w)
	target := "/ui/overview"
	if p.Remote != "" {
		target = "/ui/knowledge?project=" + url.QueryEscape(p.Remote)
	}
	a.joinWrite(l, w, "joinmember", struct{ Title, URL string }{
		l.msg("join.member_title", firstNonEmpty(p.Project, p.Org)), target})
}

// joinAccept löst die Einladung für das angemeldete Konto ein. Aufgerufen wird
// es hinter requirePerson, requireInteractive und requireCSRF.
func (a *app) joinAccept(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !a.joinGate(w, r) {
		return
	}
	code := r.PathValue("code")
	if _, ok := a.joinPreview(code); !ok {
		a.joinNotFound(l, w)
		return
	}
	// Wer hier als fremdes Konto angemeldet ist (etwa durch einen untergeschobenen
	// Login), muss den Kontonamen ausdrücklich bestätigen. Ein Sitzungsalter
	// ließe sich nicht prüfen, ohne die Sitzungen zu ändern, und die Bestätigung
	// fängt auch den Fall ab, dass das Konto alt, aber nicht das eigene ist.
	if r.FormValue("confirm_account") != browserPrincipal(r).Label {
		a.joinAcceptMessage(l, w, code, http.StatusBadRequest, l.msg("join.msg_confirm_t"), l.msg("join.msg_confirm_j"))
		return
	}
	_, err := a.store.AcceptInvitation(browserPrincipal(r).ID, code)
	switch {
	case err == nil:
		a.joinAccepted(w, r)
	case errors.Is(err, store.ErrCodeInvalid):
		a.joinNotFound(l, w)
	case errors.Is(err, store.ErrAlreadyMember):
		preview, _ := a.joinPreview(code)
		a.joinAlreadyMember(l, w, preview)
	case errors.Is(err, store.ErrTooManyAttempts):
		a.joinAcceptMessage(l, w, code, http.StatusTooManyRequests, l.msg("join.msg_many_t"), l.msg("join.msg_many"))
	case errors.Is(err, store.ErrAccountDisabled):
		a.joinAcceptMessage(l, w, code, http.StatusForbidden, l.msg("join.msg_disabled_t"), l.msg("join.msg_disabled"))
	default:
		a.joinAcceptMessage(l, w, code, http.StatusInternalServerError, l.msg("join.msg_failed_t"), l.msg("join.msg_failed"))
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

// joinAccepted ist der Ort, an dem der Beitritt endet: für das Konto entsteht
// eine Join-Sitzung, und die Paarungsseite zeigt den Code.
func (a *app) joinAccepted(w http.ResponseWriter, r *http.Request) {
	account := browserPrincipal(r).ID
	if _, err := a.store.Join().Create(account); err != nil {
		http.Redirect(w, r, "/ui/requests", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
}

// joinMessageBack zeigt eine Meldung mit einem Link zurück; ohne Ziel bleibt es
// bei der Meldung.
func (a *app) joinMessageBack(l lang, w http.ResponseWriter, status int, title, message, backURL, backLabel string) {
	a.joinHeaders(w)
	w.WriteHeader(status)
	a.joinWrite(l, w, "joinmsg", struct {
		Title, Message, BackURL, BackLabel string
	}{title, message, backURL, backLabel})
}

// joinAcceptMessage ist die Meldung auf eine gültige, aber nicht angenommene
// Einladung; sie führt zurück zur Einladung. Nur für Codes, die die Vorschau
// bestanden haben, damit ungültige Codes nichts anderes als die 404 sehen.
func (a *app) joinAcceptMessage(l lang, w http.ResponseWriter, code string, status int, title, message string) {
	a.joinMessageBack(l, w, status, title, message, "/join/"+code, l.msg("join.msg_back_invite"))
}
