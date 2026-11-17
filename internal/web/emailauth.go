package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/mail"
	"github.com/Deadweight-Labs/ghosttree/internal/passhash"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Anmelden per E-Mail, E-Mail am Konto, Passwort (REQ-676, KN-4724).
//
// Die Anmeldeseite nimmt eine Adresse und schickt eine Mail mit einem Code und
// einem Link. Die Antwort ist gleich, ob es die Adresse gibt oder nicht: die Mail
// geht im Hintergrund hinaus (die Antwortzeit verrät nichts), und die Grenzen
// zählen Adressen, nicht Konten. Der Link öffnet nur eine Seite mit einem Knopf;
// eingelöst wird erst per POST, damit eine Vorschau der Mail (oder ein Virenscanner)
// ihn nicht verbrennt.
//
// Mails werden hier nur angestoßen; Inhalt und Fristen stehen im Store
// (emailauth.go), die Wege nach draußen in internal/mail.

// WithMail schaltet den Mailversand ein. Ohne ihn gibt es keine Anmeldung per
// E-Mail, keine Bestätigung von Adressen und kein Zurücksetzen.
func WithMail(s mail.Sender) Option { return func(a *app) { a.mailer = s } }

// WithPasswordLogin schaltet je Instanz die Anmeldung mit E-Mail und Passwort
// ein. Sie braucht den Mailversand (Bestätigung der Adresse, Zurücksetzen).
func WithPasswordLogin(on bool) Option { return func(a *app) { a.passwordLogin = on } }

// mailAvailable: die Instanz kann Adressen bestätigen.
func (a *app) mailAvailable() bool { return a.mailer != nil }

// passwordsOn: Passwort-Anmeldung ist eingeschaltet und möglich.
func (a *app) passwordsOn() bool { return a.passwordLogin && a.mailer != nil }

// ---------------------------------------------------------------- Grenzen

// rateWindow zählt Aufrufe je Schlüssel in einem festen Fenster. Ist die Tabelle
// voll, fliegen erst abgelaufene Fenster raus, dann der älteste Schlüssel.
type rateWindow struct {
	mu     sync.Mutex
	now    func() time.Time
	limit  int
	window time.Duration
	counts map[string]joinCount
}

const rateWindowMax = 10000

func newRateWindow(limit int, window time.Duration) *rateWindow {
	return &rateWindow{now: time.Now, limit: limit, window: window, counts: map[string]joinCount{}}
}

func (l *rateWindow) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.now()
	c, ok := l.counts[key]
	if !ok || at.Sub(c.start) >= l.window {
		if !ok && len(l.counts) >= rateWindowMax {
			var oldest string
			var oldestAt time.Time
			for k, v := range l.counts {
				if at.Sub(v.start) >= l.window {
					delete(l.counts, k)
					continue
				}
				if oldest == "" || v.start.Before(oldestAt) {
					oldest, oldestAt = k, v.start
				}
			}
			if len(l.counts) >= rateWindowMax && oldest != "" {
				delete(l.counts, oldest)
			}
		}
		c = joinCount{start: at}
	}
	c.n++
	l.counts[key] = c
	return c.n <= l.limit
}

// emailGuards sind die Grenzen der Mail- und Passwortwege: je Adresse (oder
// Konto) und je Absender, getrennt für "Mail anfordern" und "etwas eintippen".
type emailGuards struct {
	askAddr, askIP *rateWindow
	tryAddr, tryIP *rateWindow
}

const guardWindow = 15 * time.Minute

func newEmailGuards() *emailGuards {
	return &emailGuards{
		askAddr: newRateWindow(5, guardWindow), askIP: newRateWindow(20, guardWindow),
		tryAddr: newRateWindow(10, guardWindow), tryIP: newRateWindow(40, guardWindow),
	}
}

// tooMany antwortet mit der Seite "zu viele Versuche"; es gibt keinen Hinweis
// darauf, welcher Schlüssel die Grenze erreicht hat.
func (a *app) tooMany(l lang, w http.ResponseWriter) {
	w.Header().Set("Retry-After", "900")
	a.loginMessage(l, w, http.StatusTooManyRequests, "mail.too_many.title", "mail.too_many.text")
}

func (a *app) askAllowedIP(w http.ResponseWriter, r *http.Request, l lang) bool {
	if a.guards.askIP.allow(a.joinClientKey(r)) {
		return true
	}
	a.tooMany(l, w)
	return false
}

func (a *app) tryAllowedIP(w http.ResponseWriter, r *http.Request, l lang) bool {
	if a.guards.tryIP.allow(a.joinClientKey(r)) {
		return true
	}
	a.tooMany(l, w)
	return false
}

// ---------------------------------------------------------------- Versand

// mailBase is the address the links in a mail are built from. It is the configured
// public address only. An address taken from the Host header of the request would
// let anyone who asks for a mail for someone else's address point the link, and
// with it the token, at a server of their own. Only a file mailbox, which nobody
// but the operator reads, may fall back to the address of the request. Empty: no
// link can be made.
func (a *app) mailBase(r *http.Request) string {
	base := a.publicOrigin
	if l, ok := a.mailer.(interface{ Local() bool }); base == "" && ok && l.Local() {
		base = a.joinBase(r)
	}
	return base
}

// sendChallengeMail schreibt die Mail zu einer Herausforderung und schickt sie im
// Hintergrund. Fehler stehen im Log, ohne die Adresse.
func (a *app) sendChallengeMail(r *http.Request, l lang, ch store.Challenge) {
	if a.mailer == nil {
		return
	}
	base := a.mailBase(r)
	link := "\x00"
	if base != "" {
		link = base + "/ui/mail/" + ch.Purpose + "/link?t=" + url.QueryEscape(ch.Link)
	}
	minutes := int(time.Until(ch.ExpiresAt).Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	k := mailKeys[ch.Purpose]
	text := l.msg(k.body, store.FormatCode(ch.Code), link, minutes)
	if base == "" {
		text = withoutLink(text)
	}
	msg := mail.Message{To: ch.Email, Subject: l.msg(k.subject), Text: text}
	a.mailWG.Add(1)
	go func() {
		defer a.mailWG.Done()
		if err := a.mailer.Send(msg); err != nil {
			logger := a.logger
			if logger == nil {
				logger = slog.Default()
			}
			// SMTP-Antworten nennen gern die Adresse; das Log bekommt sie nie.
			text := strings.ReplaceAll(err.Error(), msg.To, "<address>")
			logger.Error("mail_send_failed", "purpose", ch.Purpose, "error", text)
		}
	}()
}

// withoutLink drops the paragraph with the link and the one that introduces it,
// for an instance that has no public address to build a link from: the mail then
// carries the code only.
func withoutLink(text string) string {
	paras := strings.Split(text, "\n\n")
	var out []string
	for i, p := range paras {
		if strings.Contains(p, "\x00") || (i+1 < len(paras) && strings.Contains(paras[i+1], "\x00")) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "\n\n")
}

// ---------------------------------------------------------------- Seiten

// mailKeys are the catalog keys of the texts of each kind of mail and page.
var mailKeys = map[string]struct{ title, sent, button, linkTitle, linkText, subject, body string }{
	store.ChallengeLogin:   {"mail.login.title", "mail.login.sent", "mail.login.go", "mail.login.link_title", "mail.login.link_text", "mail.login.subject", "mail.login.body"},
	store.ChallengeConfirm: {"mail.confirm.title", "mail.confirm.sent", "mail.confirm.go", "mail.confirm.link_title", "mail.confirm.link_text", "mail.confirm.subject", "mail.confirm.body"},
	store.ChallengeJoin:    {"mail.join.title", "mail.join.sent", "mail.join.go", "mail.join.link_title", "mail.join.link_text", "mail.join.subject", "mail.join.body"},
	store.ChallengeReset:   {"mail.reset.title", "mail.reset.sent", "mail.reset.go", "mail.reset.link_title", "mail.reset.link_text", "mail.reset.subject", "mail.reset.body"},
}

// emailAuthView ist die Sicht der Seiten "Mail ist unterwegs", "Link bestätigen"
// und "erledigt".
type emailAuthView struct {
	Purpose string
	Masked  string // die Adresse, wie sie die Seite zeigt
	Email   string // die Adresse für das Formular (die Person hat sie selbst getippt)
	Token   string // der Link-Token, nur auf der Linkseite
	Name    string // beim Beitritt: der Name, nicht nötig für die Seite
	Error   string
	// NeedsPassword: das Formular fragt nach dem neuen Passwort.
	NeedsPassword bool
	// Resend ist der Pfad, an den "Noch einmal senden" geht; leer: kein Knopf.
	Resend string
	// Next ist, wohin der Knopf der Erledigt-Seite führt.
	Next string
	// The texts of the page, in the language of the server.
	Title, Sent, Button, LinkTitle, LinkText string
	// Heading and Body are what the "done" page says: the address was confirmed
	// just now, or it was confirmed before.
	Heading, Body string
}

// confirmDonePage is the answer to a confirmation: the address is confirmed.
// already is for a link whose address was confirmed before (with the code of the
// same mail), so the page says so instead of calling the link broken.
func (a *app) confirmDonePage(l lang, w http.ResponseWriter, email, next string, already bool) {
	title, text := "mail.confirm.done_title", "mail.confirm.done_text"
	if already {
		title, text = "mail.confirm.already_title", "mail.confirm.already_text"
	}
	masked := maskedOr(email)
	a.renderEmailAuth(l, w, http.StatusOK, "authdone", emailAuthView{Purpose: store.ChallengeConfirm, Masked: masked, Next: next,
		Heading: l.msg(title), Body: l.msg(text, masked)})
}

// deadLink answers a link that redeems nothing. For the link of a confirmation
// mail whose address is already the confirmed one of its account, the answer is
// that the address is confirmed; any other dead link says it has expired or was
// used, and where to ask for a new mail.
func (a *app) deadLink(l lang, w http.ResponseWriter, r *http.Request, purpose, token string) {
	if purpose == store.ChallengeConfirm && hasText(token, 128) {
		if email, ok := a.store.SpentConfirmLink(token); ok {
			next := "/ui/login"
			if _, in := a.joinSession(r); in {
				next = "/ui/profile"
			}
			a.confirmDonePage(l, w, email, next, true)
			return
		}
	}
	a.loginMessage(l, w, http.StatusGone, "mail.link_dead.title", "mail.link_dead.text")
}

func maskedOr(email string) string {
	if m := store.MaskEmail(email); m != "" {
		return m
	}
	return email
}

func (a *app) renderEmailAuth(l lang, w http.ResponseWriter, status int, tmpl string, v emailAuthView) {
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	k := mailKeys[v.Purpose]
	v.Title, v.Sent, v.Button = l.msg(k.title), l.msg(k.sent, v.Masked), l.msg(k.button)
	v.LinkTitle, v.LinkText = l.msg(k.linkTitle), l.msg(k.linkText, v.Masked)
	title := v.Title
	if tmpl == "authlink" {
		title = v.LinkTitle
	}
	data := a.loginData(title, "")
	data.EmailV = &v
	a.render(l, w, tmpl, data)
}

// mailPurpose liest die Art aus dem Pfad; nur die vier bekannten gelten.
func mailPurpose(r *http.Request) (string, bool) {
	switch p := r.PathValue("purpose"); p {
	case store.ChallengeLogin, store.ChallengeConfirm, store.ChallengeReset, store.ChallengeJoin:
		return p, true
	}
	return "", false
}

func hasText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }

// mailRequest nimmt eine Adresse und schickt eine Anmelde- oder Reset-Mail. Die
// Antwort ist immer die Seite "Mail ist unterwegs".
func (a *app) mailRequest(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	purpose, ok := mailPurpose(r)
	if !ok || (purpose != store.ChallengeLogin && purpose != store.ChallengeReset) || !a.mailAvailable() || (purpose == store.ChallengeReset && !a.passwordsOn()) {
		http.NotFound(w, r)
		return
	}
	if !parseLoginForm(w, r) {
		return
	}
	email, err := store.NormalizeEmail(r.FormValue("email"))
	if err != nil || len(r.FormValue("email")) > 320 {
		a.rejectEmailForm(l, w, purpose, strings.TrimSpace(r.FormValue("email")))
		return
	}
	if !a.askAllowedIP(w, r, l) {
		return
	}
	// Ist die Grenze für die Adresse erreicht, kommt dieselbe Seite, nur ohne Mail.
	if a.guards.askAddr.allow(purpose + "|" + email) {
		ch, found, err := a.store.IssueLoginChallenge(purpose, email)
		if err != nil {
			http.Error(w, "could not start sign-in", http.StatusInternalServerError)
			return
		}
		if found {
			a.sendChallengeMail(r, l, ch)
		}
	}
	a.renderEmailAuth(l, w, http.StatusOK, "authcode", emailAuthView{
		Purpose: purpose, Masked: maskedOr(email), Email: email, NeedsPassword: purpose == store.ChallengeReset,
		Resend: "/ui/mail/" + purpose + "/request",
	})
}

// rejectEmailForm zeigt das Formular mit einer Meldung, wenn die Eingabe keine
// Adresse ist (das sagt nichts über Konten).
func (a *app) rejectEmailForm(l lang, w http.ResponseWriter, purpose, typed string) {
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	if purpose == store.ChallengeReset {
		a.render(l, w, "forgot", func() pageData {
			d := a.loginData(l.msg("mail.reset.forgot_title"), l.msg("mail.error_address"))
			d.Code = typed
			return d
		}())
		return
	}
	d := a.loginData(l.msg("login.tab"), l.msg("mail.error_address"))
	d.Code = typed
	d.EmailValue = typed
	a.render(l, w, "login", d)
}

// mailCode löst Adresse und Code ein: Anmelden, Bestätigen, Beitritt, Reset.
func (a *app) mailCode(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	purpose, ok := mailPurpose(r)
	if !ok || !a.mailAvailable() || (purpose == store.ChallengeReset && !a.passwordsOn()) {
		http.NotFound(w, r)
		return
	}
	if !parseLoginForm(w, r) {
		return
	}
	email, err := store.NormalizeEmail(r.FormValue("email"))
	code := r.FormValue("code")
	if !a.tryAllowedIP(w, r, l) {
		return
	}
	if err == nil && !a.guards.tryAddr.allow("code|"+purpose+"|"+email) {
		a.tooMany(l, w)
		return
	}
	view := emailAuthView{Purpose: purpose, Masked: maskedOr(email), Email: email, NeedsPassword: purpose == store.ChallengeReset,
		Resend: "/ui/mail/" + purpose + "/request"}
	if purpose == store.ChallengeConfirm || purpose == store.ChallengeJoin {
		view.Resend = ""
	}
	if err != nil || !hasText(code, 32) {
		view.Error = l.msg("mail.error_code")
		a.renderEmailAuth(l, w, http.StatusUnprocessableEntity, "authcode", view)
		return
	}
	a.redeemMail(w, r, l, store.Redemption{Purpose: purpose, Email: email, Code: code}, view, "authcode")
}

// mailLinkPage ist die Seite, die ein Link aus der Mail öffnet: ein Knopf, sonst
// nichts. Das Ansehen verbraucht den Link nicht.
func (a *app) mailLinkPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	purpose, ok := mailPurpose(r)
	if !ok || !a.mailAvailable() || (purpose == store.ChallengeReset && !a.passwordsOn()) {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("t"))
	email, valid := "", false
	if len(token) <= 128 && token != "" {
		email, valid = a.store.PeekChallenge(purpose, token)
	}
	if !valid {
		a.deadLink(l, w, r, purpose, token)
		return
	}
	a.renderEmailAuth(l, w, http.StatusOK, "authlink", emailAuthView{
		Purpose: purpose, Masked: maskedOr(email), Token: token, NeedsPassword: purpose == store.ChallengeReset,
	})
}

func (a *app) mailLinkSubmit(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	purpose, ok := mailPurpose(r)
	if !ok || !a.mailAvailable() || (purpose == store.ChallengeReset && !a.passwordsOn()) {
		http.NotFound(w, r)
		return
	}
	if !parseLoginForm(w, r) {
		return
	}
	token := strings.TrimSpace(r.FormValue("t"))
	if !a.tryAllowedIP(w, r, l) {
		return
	}
	if !hasText(token, 128) {
		a.deadLink(l, w, r, purpose, token)
		return
	}
	email, _ := a.store.PeekChallenge(purpose, token)
	view := emailAuthView{Purpose: purpose, Masked: maskedOr(email), Token: token, NeedsPassword: purpose == store.ChallengeReset}
	a.redeemMail(w, r, l, store.Redemption{Purpose: purpose, Link: token}, view, "authlink")
}

// redeemMail löst ein und antwortet je nach Art: Anmelden und Beitritt melden
// an, Bestätigen zeigt die Erledigt-Seite, Reset setzt das Passwort und meldet
// an. Jeder Fehlschlag, der nichts über Konten verrät, bleibt auf der Seite.
func (a *app) redeemMail(w http.ResponseWriter, r *http.Request, l lang, in store.Redemption, view emailAuthView, page string) {
	if in.Purpose == store.ChallengeReset {
		pw, again := r.FormValue("password"), r.FormValue("password2")
		switch {
		case pw != again:
			view.Error = l.msg("mail.error_pw_mismatch")
		case passhash.Check(pw) != nil:
			view.Error = l.msg("mail.error_pw_short", passhash.MinLength)
		}
		if view.Error != "" {
			a.renderEmailAuth(l, w, http.StatusUnprocessableEntity, page, view)
			return
		}
		hash, err := passhash.Hash(pw)
		if err != nil {
			http.Error(w, "could not set password", http.StatusInternalServerError)
			return
		}
		in.PasswordHash = hash
	}
	account, err := a.store.RedeemChallenge(in)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrEmailTaken):
		a.loginMessage(l, w, http.StatusConflict, "mail.taken.title", "mail.taken.text")
		return
	case errors.Is(err, store.ErrNameTaken):
		a.loginMessage(l, w, http.StatusConflict, "mail.name_taken.title", "mail.name_taken.text")
		return
	case errors.Is(err, store.ErrCodeInvalid), errors.Is(err, store.ErrAccountDisabled):
		view.Error = l.msg("mail.error_code")
		if page == "authlink" {
			a.deadLink(l, w, r, in.Purpose, in.Link)
			return
		}
		a.renderEmailAuth(l, w, http.StatusUnprocessableEntity, page, view)
		return
	default:
		http.Error(w, "could not complete this step", http.StatusInternalServerError)
		return
	}
	switch in.Purpose {
	case store.ChallengeConfirm:
		next := "/ui/login"
		if s, ok := a.joinSession(r); ok && s.principal.ID == account.ID {
			next = "/ui/profile?notice=email_confirmed"
		}
		a.confirmDonePage(l, w, account.Email, next, false)
	case store.ChallengeReset:
		// Ein neues Passwort beendet alle Sitzungen des Kontos, auch gestohlene.
		// Die Person landet dort, wo sie es geändert sieht: am Passwortformular
		// ihres Profils, mit der Bestätigung.
		a.sessions.dropAccount(account.ID)
		a.takeNext(w, r, "")
		a.startSessionAt(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind}, "/ui/profile?notice=password_reset#"+profilePasswordSection)
	default:
		a.finishLogin(w, r, account)
	}
}

// forgotPage ist das Formular "Passwort vergessen".
func (a *app) forgotPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !a.passwordsOn() {
		http.NotFound(w, r)
		return
	}
	codeHeaders(w)
	a.render(l, w, "forgot", a.loginData(l.msg("mail.reset.forgot_title"), ""))
}

// ---------------------------------------------------------------- Passwort

// passwordLoginSubmit meldet mit Adresse und Passwort an. Jede Ablehnung ist
// dieselbe Meldung, auch wenn es die Adresse nicht gibt oder das Konto kein
// Passwort hat; ein Fehlschlag kostet dieselbe Rechenzeit.
func (a *app) passwordLoginSubmit(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !a.passwordsOn() {
		http.NotFound(w, r)
		return
	}
	if !parseLoginForm(w, r) {
		return
	}
	if !a.tryAllowedIP(w, r, l) {
		return
	}
	typed := strings.TrimSpace(r.FormValue("email"))
	email, err := store.NormalizeEmail(typed)
	pw := r.FormValue("password")
	if err == nil && !a.guards.tryAddr.allow("pw|"+email) {
		a.tooMany(l, w)
		return
	}
	reject := func() {
		codeHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		d := a.loginData(l.msg("login.tab"), l.msg("mail.error_signin"))
		d.EmailValue = typed
		a.render(l, w, "login", d)
	}
	if err != nil || len(pw) > passhash.MaxLength {
		passhash.Burn(pw)
		reject()
		return
	}
	account, hash, found := a.store.PasswordLogin(email)
	if !found || hash == "" {
		passhash.Burn(pw)
		reject()
		return
	}
	if !passhash.Verify(pw, hash) {
		reject()
		return
	}
	if passhash.NeedsRehash(hash) {
		if h, err := passhash.Hash(pw); err == nil {
			_ = a.store.SetPassword(account.ID, h)
		}
	}
	a.finishLogin(w, r, account)
}

// ---------------------------------------------------------------- Profil

// profileEmail ändert die Adresse des Kontos: die neue Adresse bekommt eine
// Mail, und erst deren Bestätigung ersetzt die alte.
func (a *app) profileEmail(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !a.mailAvailable() {
		http.NotFound(w, r)
		return
	}
	if a.linkSession(r) {
		a.renderProfile(w, r, http.StatusForbidden, "", profileMailSection, l.msg("mail.error_link_session"))
		return
	}
	me := browserPrincipal(r)
	email, err := store.NormalizeEmail(r.FormValue("email"))
	if err != nil || len(r.FormValue("email")) > 320 {
		a.renderProfile(w, r, http.StatusUnprocessableEntity, "", profileMailSection, l.msg("mail.error_address"))
		return
	}
	if !a.askAllowedIP(w, r, l) {
		return
	}
	if !a.guards.askAddr.allow(store.ChallengeConfirm + "|" + email) {
		a.renderProfile(w, r, http.StatusTooManyRequests, "", profileMailSection, l.msg("mail.too_many.text"))
		return
	}
	ch, err := a.store.IssueConfirmChallenge(me.ID, email)
	if err != nil {
		http.Error(w, l.msg("adm.load_failed"), http.StatusInternalServerError)
		return
	}
	a.sendChallengeMail(r, l, ch)
	a.renderEmailAuth(l, w, http.StatusOK, "authcode", emailAuthView{Purpose: store.ChallengeConfirm, Masked: maskedOr(email), Email: email})
}

// confirmPending zeigt die Eingabe des Codes für die Adresse, die am Konto auf
// Bestätigung wartet (nach dem Beitritt: "Code eingeben").
func (a *app) confirmPending(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	email := a.store.PendingEmail(browserPrincipal(r).ID)
	if email == "" {
		http.Redirect(w, r, "/ui/profile", http.StatusSeeOther)
		return
	}
	a.renderEmailAuth(l, w, http.StatusOK, "authcode", emailAuthView{Purpose: store.ChallengeConfirm, Masked: maskedOr(email), Email: email})
}

// confirmResend schickt die Bestätigung an die wartende Adresse noch einmal.
func (a *app) confirmResend(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	me := browserPrincipal(r)
	email := a.store.PendingEmail(me.ID)
	if email == "" || !a.mailAvailable() {
		http.Redirect(w, r, "/ui/profile", http.StatusSeeOther)
		return
	}
	if !a.askAllowedIP(w, r, l) {
		return
	}
	if a.guards.askAddr.allow(store.ChallengeConfirm + "|" + email) {
		if ch, err := a.store.IssueConfirmChallenge(me.ID, email); err == nil {
			a.sendChallengeMail(r, l, ch)
		}
	}
	a.renderEmailAuth(l, w, http.StatusOK, "authcode", emailAuthView{Purpose: store.ChallengeConfirm, Masked: maskedOr(email), Email: email})
}

// profilePassword setzt oder ändert das Passwort des angemeldeten Kontos. Ein
// vorhandenes Passwort muss man dafür kennen; wer es vergessen hat, setzt es
// über die Mail zurück.
func (a *app) profilePassword(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !a.passwordsOn() {
		http.NotFound(w, r)
		return
	}
	if a.linkSession(r) {
		a.renderProfile(w, r, http.StatusForbidden, "", profilePasswordSection, l.msg("mail.error_link_session"))
		return
	}
	me := browserPrincipal(r)
	if !a.guards.tryAddr.allow("pwset|" + me.ID) {
		a.renderProfile(w, r, http.StatusTooManyRequests, "", profilePasswordSection, l.msg("mail.too_many.text"))
		return
	}
	fail := func(key string, args ...any) {
		a.renderProfile(w, r, http.StatusUnprocessableEntity, "", profilePasswordSection, l.msg(key, args...))
	}
	pw, again := r.FormValue("password"), r.FormValue("password2")
	if old := a.store.PasswordHash(me.ID); old != "" && !passhash.Verify(r.FormValue("current"), old) {
		fail("mail.error_pw_current")
		return
	}
	switch {
	case pw != again:
		fail("mail.error_pw_mismatch")
		return
	case passhash.Check(pw) != nil:
		fail("mail.error_pw_short", passhash.MinLength)
		return
	}
	hash, err := passhash.Hash(pw)
	if err == nil {
		err = a.store.SetPassword(me.ID, hash)
	}
	if err != nil {
		http.Error(w, l.msg("adm.load_failed"), http.StatusInternalServerError)
		return
	}
	// Andere Sitzungen enden; die jetzige bleibt.
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.dropAccountExcept(me.ID, c.Value)
	}
	http.Redirect(w, r, "/ui/profile?notice=password_saved", http.StatusSeeOther)
}

// profileMailView ist, was die Profilseite zur Anmeldung zeigt.
type profileMailView struct {
	Mail, Passwords bool
	Email, Pending  string
	HasPassword     bool
}

func (a *app) profileMail(r *http.Request, acct store.Account) *profileMailView {
	if !a.mailAvailable() {
		return nil
	}
	return &profileMailView{Mail: true, Passwords: a.passwordsOn(), Email: acct.Email,
		Pending: a.store.PendingEmail(acct.ID), HasPassword: a.store.PasswordHash(acct.ID) != ""}
}

// ---------------------------------------------------------------- Beitritt per Mail

// joinByMail führt den Beitritt über eine Einladung aus, wenn die Instanz Mails
// schickt (ohne IdP). Die Einladung ist nicht an eine Adresse gebunden: das Konto
// entsteht sofort, die angegebene Adresse wartet auf ihre Bestätigung. Ist sie
// gebunden, geht die Bestätigung an die gebundene Adresse, und erst sie legt das
// Konto an.
func (a *app) joinByMail(w http.ResponseWriter, r *http.Request, l lang, code string, join bool) {
	name := strings.TrimSpace(r.FormValue("name"))
	typed := strings.TrimSpace(r.FormValue("email"))
	if !a.joinGate(w, r) {
		return
	}
	if a.inviteNames.locked(code) {
		a.inviteNameRejected(l, w, r, code, "join.error_name_locked", http.StatusTooManyRequests)
		return
	}
	if !a.store.InviteNameFree(name) {
		key, status := "join.error_name", http.StatusUnprocessableEntity
		if a.inviteNames.miss(code) {
			key, status = "join.error_name_locked", http.StatusTooManyRequests
		}
		a.inviteNameRejected(l, w, r, code, key, status)
		return
	}
	if !a.askAllowedIP(w, r, l) {
		return
	}
	form, ferr := a.store.PreviewJoin(code, a.store.AccessEnforced(), true)
	if ferr != nil {
		a.codeRejected(l, w, http.StatusUnprocessableEntity, "login.error_link", code, true)
		return
	}
	if form.BoundEmail == "" {
		if _, err := store.NormalizeEmail(typed); err != nil || len(typed) > 320 {
			a.joinEmailRejected(l, w, r, code, name, typed, "mail.error_address")
			return
		}
	}
	account, ch, err := a.store.BeginJoinByMail(code, name, typed)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNameTaken):
		a.inviteNameRejected(l, w, r, code, "join.error_name", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrCodeInvalid):
		a.codeRejected(l, w, http.StatusUnprocessableEntity, "login.error_link", code, true)
		return
	default:
		a.identityRejected(l, w, err)
		return
	}
	a.inviteNames.accepted(code)
	if a.guards.askAddr.allow(ch.Purpose + "|" + ch.Email) {
		a.sendChallengeMail(r, l, ch)
	}
	if account.ID == "" {
		// Gebunden: das Konto entsteht erst mit der Bestätigung.
		a.renderEmailAuth(l, w, http.StatusOK, "authcode", emailAuthView{Purpose: store.ChallengeJoin, Masked: maskedOr(ch.Email), Email: ch.Email})
		return
	}
	if join {
		a.finishJoinLogin(w, r, account)
		return
	}
	a.finishLogin(w, r, account)
}

// joinEmailRejected zeigt die Einladungsseite mit der Meldung am Adressfeld.
func (a *app) joinEmailRejected(l lang, w http.ResponseWriter, r *http.Request, code, name, email, msgKey string) {
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	if r.FormValue("join") == "1" {
		if preview, ok := a.joinPreview(code); ok {
			view := a.joinViewFor(r, l, code, preview)
			view.NameValue, view.EmailValue, view.EmailError = name, email, l.msg(msgKey)
			a.joinHeaders(w)
			a.joinWrite(l, w, "join", view)
			return
		}
	}
	data := a.loginData(l.msg("login.code_title"), "")
	data.Code, data.NeedsName, data.Invite = code, true, true
	data.InviteV = a.orgInviteView(r, l, code)
	if data.InviteV != nil {
		data.InviteV.NameValue, data.InviteV.EmailValue, data.InviteV.EmailError = name, email, l.msg(msgKey)
	} else {
		data.Error = l.msg(msgKey)
	}
	a.render(l, w, "logincode", data)
}

// ---------------------------------------------------------------- Login-Link für Mitglieder

// orgMemberLoginLink gibt einem Owner einen einmaligen Login-Link für ein
// Mitglied; er steht danach einmal in der Mitgliederliste, für diesen Owner.
func (a *app) orgMemberLoginLink(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	me := browserPrincipal(r).ID
	target := r.FormValue("member")
	code, ttl, err := a.store.IssueMemberLoginLink(me, o.ID, target)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	id, perr := principalNumber(target)
	if perr != nil {
		a.orgFailure(w, r, store.ErrInvalidInput)
		return
	}
	// Die Nummern von Einladungen sind positiv, die der Login-Links negativ:
	// beide teilen sich den Speicher, ohne sich zu treffen.
	a.inviteLinks.put(-id, me, code, false, time.Now().Add(ttl))
	http.Redirect(w, r, "/ui/orgs?org="+o.Slug+"&login="+target+"#members", http.StatusSeeOther)
}

// loginLinkFor ist der Link, den ein Owner für ein Mitglied noch kopieren kann.
func (a *app) loginLinkFor(r *http.Request, me, member string) string {
	id, err := principalNumber(member)
	if err != nil {
		return ""
	}
	link, ok := a.inviteLinks.get(-id, me)
	if !ok {
		return ""
	}
	return a.joinBase(r) + "/ui/login/code?code=" + url.QueryEscape(link.code)
}
