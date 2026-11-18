package web

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
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
	// Keine Skripte, keine fremden Ressourcen, nicht einbettbar. Formulare gehen an
	// diese Herkunft, an den Identitätsanbieter und auf den Loopback des Installers
	// (die 303-Weiterleitung der Freigabe auf http://127.0.0.1:<port>/callback),
	// weil Browser form-action auch auf die Ziele von Weiterleitungen anwenden.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; "+a.formAction(true)+"; frame-ancestors 'none'")
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// joinScriptHeaders erlaubt dem Paarungsablauf das eine eigene Skript
// (/static/join.js) und Abfragen an dieselbe Herkunft; sonst bleibt alles zu.
func (a *app) joinScriptHeaders(w http.ResponseWriter) {
	a.joinHeaders(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; "+a.formAction(true)+"; frame-ancestors 'none'")
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
	// Short ist der Name des Projekts ohne Pfad (letztes Segment), ExpiresIn der
	// Ablauf in Worten ("Läuft ab in 3 Tagen").
	Short, ExpiresIn string
	// OrgMark ist der Anfangsbuchstabe der Organisation für ihr Zeichen im Kopf, Tab
	// der Titel des Browser-Tabs.
	OrgMark, Tab            string
	Code, CSRFToken, Person string
	Initial, Email          string
	SignedIn, OIDC, Signup  bool
	// NameError ist die Meldung am Namensfeld, NameValue, was die Person getippt hat.
	NameError, NameValue string
	// Provider is the display name of the identity provider ("GitHub"), empty when unnamed.
	Provider string
	// Guest wählt den Satz, der sagt, was die Rolle sieht; Project ist der Name
	// des Projekts darin.
	Guest   bool
	Project string
	// Mail: the instance sends mail, so the page asks for an address (REQ-676).
	// Bound is the masked address an invitation is bound to; the address is then
	// not asked for but mailed. Blocked: the invitation is bound and this
	// instance cannot confirm an address, so it cannot be accepted here.
	Mail, Blocked          bool
	Bound                  string
	EmailValue, EmailError string
}

// shortName ist das letzte Segment eines Pfads: aus "github.com/acme/app" wird "app".
func shortName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// expiresIn sagt in Worten, wie lange eine Einladung noch gilt. Es wird
// aufgerundet: eine Einladung für drei Tage zeigt am ersten Tag "3 Tagen".
func expiresIn(l lang, stamp string) string {
	t := parseTime(stamp)
	if t.IsZero() {
		return ""
	}
	d := t.Sub(overviewNow().UTC())
	switch hours := int(math.Ceil(d.Hours())); {
	case d < time.Hour:
		return l.msg("join.expires_soon")
	case hours < 24 && hours == 1:
		return l.msg("join.expires_hour")
	case hours < 24:
		return l.msg("join.expires_hours", hours)
	default:
		if days := int(math.Ceil(d.Hours() / 24)); days > 1 {
			return l.msg("join.expires_days", days)
		}
		return l.msg("join.expires_day")
	}
}

func roleLabel(l lang, role string) string {
	if (store.RoleInfo{Role: role}).GuestOnly() {
		return l.msg("join.role_guest")
	}
	return l.msg("join.role_member")
}

// joinSession liefert das angemeldete Konto, wenn eine Sitzung aus einer Anmeldung
// mit Nachweis da ist. Eine Sitzung aus einem Einmal-Link zählt nicht: wer damit
// auf der Anmelde- oder Einladungsseite ist, kann sich dort mit Nachweis anmelden.
func (a *app) joinSession(r *http.Request) (browserSession, bool) {
	cookie, err := a.sessionCookieOf(r)
	if err != nil {
		return browserSession{}, false
	}
	session, ok := a.sessions.get(cookie.Value)
	if !ok || session.link || session.principal.TokenKind != store.WebSessionKind || !a.store.PrincipalValid(session.principal) {
		return browserSession{}, false
	}
	return session, true
}

// joinViewFor baut die Sicht der Einladungsseite aus der Vorschau.
func (a *app) joinViewFor(r *http.Request, l lang, code string, preview store.InvitePreview) joinView {
	view := joinView{Inviter: preview.Inviter, Target: preview.Project, Org: preview.Org, RoleText: roleLabel(l, preview.Role),
		ExpiresAt: preview.ExpiresAt, ExpiresIn: expiresIn(l, preview.ExpiresAt), Short: shortName(preview.Project),
		Guest: (store.RoleInfo{Role: preview.Role}).GuestOnly(), Project: firstNonEmpty(shortName(preview.Project), l.msg("join.shares.project")),
		Code: code, OIDC: a.oidc != nil, Provider: a.oidcName, Signup: a.oidc != nil && a.oidc.signupOffered(r.Context()),
		Mail: a.oidc == nil && a.mailAvailable()}
	view.OrgMark = initialOf(firstNonEmpty(preview.Org, view.Short))
	view.Tab = l.msg("join.invite_tab")
	if session, ok := a.joinSession(r); ok {
		view.SignedIn, view.Person, view.CSRFToken = true, session.principal.Label, session.csrf
		view.Initial = initialOf(view.Person)
		if acct, err := a.store.AccountByPrincipalID(session.principal.ID); err == nil {
			view.Email = acct.Email
		}
	}
	return view
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
	view := a.joinViewFor(r, l, code, preview)
	if session, ok := a.joinSession(r); ok && a.store.InvitationCovered(session.principal.ID, code) {
		a.joinAlreadyMember(l, w, preview)
		return
	}
	a.joinHeaders(w)
	a.joinWrite(l, w, "join", view)
}

// joinLogin ist der Weg "Ich habe schon ein Konto": die Anmeldeseite merkt sich die
// Einladung als Ziel und führt danach hierher zurück. Der Code steht so in keiner
// Adresse der Anmeldeseite.
func (a *app) joinLogin(w http.ResponseWriter, r *http.Request) {
	if code := r.PathValue("code"); wellFormedJoinCode(code) {
		a.setNext(w, r, "/join/"+code)
	}
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// joinNameRejected zeigt die Einladungsseite wieder, mit der Meldung am Namensfeld
// und dem getippten Namen; es wurde nichts angelegt.
func (a *app) joinNameRejected(l lang, w http.ResponseWriter, r *http.Request, code, name, msgKey string, status int) {
	preview, ok := a.joinPreview(code)
	if !ok {
		a.joinNotFound(l, w)
		return
	}
	view := a.joinViewFor(r, l, code, preview)
	view.NameError, view.NameValue = l.msg(msgKey), name
	a.joinHeaders(w)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(int(inviteNameLockFor/time.Second)))
	}
	w.WriteHeader(status)
	a.joinWrite(l, w, "join", view)
}

// joinAlreadyMember tells a signed-in member that the link gives them nothing
// new, with a way into the project. The invitation stays unused.
func (a *app) joinAlreadyMember(l lang, w http.ResponseWriter, p store.InvitePreview) {
	a.joinHeaders(w)
	target := "/ui/inbox"
	if p.Remote != "" {
		target = repoHref(p.Remote)
	}
	a.joinWrite(l, w, "joinmember", struct{ Title, URL, Tab string }{
		l.msg("join.member_title", shortName(firstNonEmpty(p.Project, p.Org))), target, l.msg("join.member_tab")})
}

// joinAccept löst die Einladung für das angemeldete Konto ein. Aufgerufen wird
// es hinter requirePerson, requireSignIn(account.security) und requireCSRF.
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
	// Where the person lands is read before the invitation is used up.
	land := a.joinLanding(code)
	// Wer hier als fremdes Konto angemeldet ist (etwa durch einen untergeschobenen
	// Login), muss den Kontonamen ausdrücklich bestätigen. Ein Sitzungsalter
	// ließe sich nicht prüfen, ohne die Sitzungen zu ändern, und die Bestätigung
	// fängt auch den Fall ab, dass das Konto alt, aber nicht das eigene ist.
	if r.FormValue("confirm_account") != browserPrincipal(r).Label {
		a.joinAcceptMessage(l, w, code, http.StatusBadRequest, l.msg("join.msg_confirm_t"), l.msg("join.msg_confirm_j"))
		return
	}
	org, err := a.store.AcceptInvitation(browserPrincipal(r).ID, code)
	switch {
	case err == nil:
		// The organization joined is the one the person works in from now on.
		if org.Slug != "" {
			http.SetCookie(w, a.orgCookieFor(r, org.Slug, 90*24*60*60))
		}
		a.joinAccepted(w, r, land)
	case errors.Is(err, store.ErrCodeInvalid):
		a.joinNotFound(l, w)
	case errors.Is(err, store.ErrAlreadyMember):
		preview, _ := a.joinPreview(code)
		a.joinAlreadyMember(l, w, preview)
	case errors.Is(err, store.ErrInvitationEmail):
		a.invitationEmailRejected(l, w, code)
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
	if cookie, err := a.sessionCookieOf(r); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, a.sessionCookieFor(r, "", -1))
	back := "/ui/login"
	if code := r.PathValue("code"); wellFormedJoinCode(code) {
		back = "/join/" + code
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// joinLanding is where a person lands after accepting the invitation of code when
// that is not the pairing page: a guest goes into the project that was shared with
// them. Empty for everyone else. It has to be read before the invitation is used
// up, as a used one has no preview any more.
func (a *app) joinLanding(code string) string {
	if p, ok := a.joinPreview(code); ok && (store.RoleInfo{Role: p.Role}).GuestOnly() && p.Remote != "" {
		return repoHref(p.Remote)
	}
	return ""
}

// guestLanding is the project a guest lands in: the account is a guest in every
// organization it is in (a personal namespace aside) and sees exactly one project.
// Empty for anyone else, who pairs a computer.
func (a *app) guestLanding(account string) string {
	orgs, err := a.store.ListOrgs(account)
	if err != nil {
		return ""
	}
	var remotes []string
	for _, o := range orgs {
		if o.IsUser() {
			continue
		}
		if !o.Guest {
			return ""
		}
		ps, _ := a.store.ListProjects(account, o.ID)
		for _, p := range ps {
			switch role := a.store.ProjectRole(p.Remote, account).Role; {
			case role == "":
			case !(store.RoleInfo{Role: role}).GuestOnly():
				// Invited to one project as a member: a person who works there pairs a computer.
				return ""
			default:
				remotes = append(remotes, p.Remote)
			}
		}
	}
	if len(remotes) != 1 {
		return ""
	}
	return repoHref(remotes[0])
}

// joinAccepted ist der Ort, an dem der Beitritt endet: für das Konto entsteht
// eine Join-Sitzung, und die Paarungsseite zeigt den Code.
func (a *app) joinAccepted(w http.ResponseWriter, r *http.Request, land string) {
	// A guest has no computer to pair: they go to the project shared with them.
	if land != "" {
		http.Redirect(w, r, land, http.StatusSeeOther)
		return
	}
	account := browserPrincipal(r).ID
	if _, err := a.store.Join().Create(account); err != nil {
		http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
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
