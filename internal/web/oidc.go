package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	flowCookie    = "gt_oidc_flow"
	flowTTL       = 10 * time.Minute
	idpHTTPTimout = 10 * time.Second

	// Grenzen für Login-Formulare: kein Feld soll Speicher oder Cookie sprengen.
	maxLoginBody  = 8 << 10
	maxCodeLength = 128
	maxNameLength = 128
	maxUsedStates = 10000
)

// OIDCConfig konfiguriert den Login über einen OpenID-Connect-Anbieter.
// ClientSecret darf leer sein (öffentlicher Client, PKCE schützt den Code).
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Name ist der Anzeigename des Anbieters auf der Anmeldeseite ("Continue
	// with <Name>"). Leer: die Seite nennt keinen Anbieter.
	Name string
	// HTTPClient ist für Tests austauschbar; Standard ist ein Client mit Timeout.
	HTTPClient *http.Client
}

// Enabled sagt, ob überhaupt etwas konfiguriert ist.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != "" || c.RedirectURL != ""
}

// Validate verlangt Issuer, Client-ID und Redirect-URL gemeinsam und Klartext-
// HTTP nur für Loopback (lokale Entwicklung und Tests).
func (c OIDCConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.Issuer == "" || c.ClientID == "" || c.RedirectURL == "" {
		return errors.New("oidc needs issuer, client id and redirect url together")
	}
	for name, raw := range map[string]string{"issuer": c.Issuer, "redirect url": c.RedirectURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Fragment != "" {
			return fmt.Errorf("oidc %s %q is not a valid absolute url", name, raw)
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
			return fmt.Errorf("oidc %s must use https (plain http only for loopback)", name)
		}
	}
	if u, _ := url.Parse(c.RedirectURL); u.Path != "/ui/login/oidc/callback" {
		return fmt.Errorf("oidc redirect url must end in /ui/login/oidc/callback")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// oidcFlow ist der Zustand eines laufenden Logins. Er liegt nicht auf dem
// Server, sondern verschlüsselt im Flow-Cookie des Browsers: ein unangemeldeter
// Client kann dem Server so keinen Speicher belegen, und viele Starts können
// den Login anderer nicht sperren.
type oidcFlow struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Code     string `json:"c,omitempty"`
	Join     bool   `json:"j,omitempty"` // Anmeldung über die Join-Seite
	Expires  int64  `json:"e"`
}

// flowSealer verschlüsselt und authentifiziert (AES-256-GCM) den Ablauf mit
// einem Schlüssel, der nur im Prozess lebt. Ein Neustart macht laufende
// Anmeldungen ungültig; der Nutzer beginnt neu, sonst ändert sich nichts.
type flowSealer struct{ aead cipher.AEAD }

func newFlowSealer() (*flowSealer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &flowSealer{aead: aead}, nil
}

var flowAAD = []byte("ghosttree oidc flow v1")

func (s *flowSealer) seal(f oidcFlow) (string, error) {
	plain, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, flowAAD)), nil
}

func (s *flowSealer) open(value string) (oidcFlow, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return oidcFlow{}, false
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], flowAAD)
	if err != nil {
		return oidcFlow{}, false
	}
	var f oidcFlow
	if json.Unmarshal(plain, &f) != nil {
		return oidcFlow{}, false
	}
	return f, true
}

// usedStates macht den State einmalig. Die Liste ist begrenzt: bei Überlauf
// verdrängt der älteste Eintrag. Ein Replay braucht trotzdem das (HttpOnly,
// beim Callback gelöschte) Cookie des Opfers, und der IdP löst den Code selbst
// nur einmal ein; die Liste ist die zweite Sperre, nicht die einzige.
type usedStates struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []usedState
}
type usedState struct {
	state   string
	expires time.Time
}

func newUsedStates() *usedStates { return &usedStates{seen: map[string]struct{}{}} }

// use gibt false zurück, wenn der State schon verbraucht wurde.
func (u *usedStates) use(state string, now, expires time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for len(u.order) > 0 && (now.After(u.order[0].expires) || len(u.order) >= maxUsedStates) {
		delete(u.seen, u.order[0].state)
		u.order = u.order[1:]
	}
	if _, dup := u.seen[state]; dup {
		return false
	}
	u.seen[state] = struct{}{}
	u.order = append(u.order, usedState{state, expires})
	return true
}

type oidcClient struct {
	cfg  OIDCConfig
	http *http.Client
	seal *flowSealer
	used *usedStates
	now  func() time.Time

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	provider *oidc.Provider
}

func newOIDCClient(cfg OIDCConfig) (*oidcClient, error) {
	sealer, err := newFlowSealer()
	if err != nil {
		return nil, err
	}
	c := &oidcClient{cfg: cfg, http: cfg.HTTPClient, seal: sealer, used: newUsedStates(), now: time.Now}
	if c.http == nil {
		c.http = &http.Client{Timeout: idpHTTPTimout}
	}
	return c, nil
}

func (c *oidcClient) context(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, c.http)
}

// ready entdeckt den Anbieter beim ersten Gebrauch statt beim Start, damit ein
// nicht erreichbarer IdP den Server nicht am Hochfahren hindert (CLI-Tokens und
// Login-Links bleiben dann nutzbar).
func (c *oidcClient) ready(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.oauth != nil {
		return c.oauth, c.verifier, nil
	}
	provider, err := oidc.NewProvider(c.context(ctx), c.cfg.Issuer)
	if err != nil {
		return nil, nil, err
	}
	endpoint := provider.Endpoint()
	// Feste Authentifizierungsart: die automatische Erkennung wiederholte den
	// Austausch bei jedem Fehler mit der anderen Art, und ein
	// Autorisierungscode ist einmalig.
	if c.cfg.ClientSecret == "" {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	} else {
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	c.oauth = &oauth2.Config{
		ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret,
		Endpoint: endpoint, RedirectURL: c.cfg.RedirectURL,
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
	c.verifier = provider.Verifier(&oidc.Config{ClientID: c.cfg.ClientID})
	c.provider = provider
	return c.oauth, c.verifier, nil
}

func randomString() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Option konfiguriert die Weboberfläche.
type Option func(*app)

// WithOIDC schaltet den OIDC-Login ein. Eine leere Konfiguration lässt alles
// wie bisher.
func WithOIDC(cfg OIDCConfig) Option {
	return func(a *app) {
		if cfg.Enabled() {
			c, err := newOIDCClient(cfg)
			if err != nil {
				panic("oidc: " + err.Error())
			}
			a.oidc = c
			a.oidcName = strings.TrimSpace(cfg.Name)
		}
	}
}

// WithTrustedProxies names the proxy networks whose X-Forwarded-Proto and
// X-Forwarded-Host headers are believed (loopback is always included).
func WithTrustedProxies(s proxytrust.Set) Option { return func(a *app) { a.proxies = s } }

// WithPublicURL declares the external address. With an https URL every cookie
// is Secure regardless of headers, and that origin is accepted for
// same-origin checks.
func WithPublicURL(raw string) Option {
	return func(a *app) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			panic("web: invalid public URL")
		}
		if u.User != nil || !distHostRE.MatchString(u.Host) {
			panic("web: invalid public URL host")
		}
		host := u.Host
		if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
			host = strings.TrimSuffix(host, ":"+u.Port())
		}
		a.publicOrigin = u.Scheme + "://" + host
		a.publicHTTPS = u.Scheme == "https"
	}
}

// WithBootstrapFile nennt die Datei mit dem Klartext-Bootstrap-Code; sie wird
// gelöscht, sobald das erste Konto angelegt ist.
func WithBootstrapFile(path string) Option { return func(a *app) { a.bootstrapFile = path } }

// secureCookies decides the Secure attribute for every cookie we set. It is
// true for a direct TLS connection, for an https public URL or OIDC redirect
// URL, and for X-Forwarded-Proto: https from a trusted proxy peer only.
func (a *app) secureCookies(r *http.Request) bool {
	if r.TLS != nil || a.publicHTTPS {
		return true
	}
	if a.oidc != nil && strings.HasPrefix(a.oidc.cfg.RedirectURL, "https://") {
		return true
	}
	proto, set := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	return set && proto == "https" && a.proxies.Trusts(r.RemoteAddr)
}

// sessionCookieFor builds the browser session cookie. Login and logout both go
// through it so a clearing cookie always carries the attributes of the one it
// replaces (browsers match on name, path and attributes).
func (a *app) sessionCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (a *app) flowCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: flowCookie, Value: value, Path: "/ui/login/oidc", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// oidcStart beginnt den Authorization-Code-Ablauf mit PKCE (S256) und Nonce.
// POST mit Same-Origin-Prüfung: ein fremder Link kann keinen Ablauf im
// Browser des Opfers anstoßen, und ein Code landet nie in einer URL.
func (a *app) oidcStart(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if !parseLoginForm(w, r) {
		return
	}
	code, joinCode := codeFromInput(r.FormValue("code"))
	if joinCode != "" {
		http.Redirect(w, r, "/join/"+joinCode, http.StatusSeeOther)
		return
	}
	a.beginOIDC(w, r, code)
}

// beginOIDC startet den Ablauf mit dem eingegebenen Code (leer, Claim-,
// Bootstrap- oder Einladungscode). Der Anbieterknopf und das Codefeld mit
// Enter laufen beide hierüber.
func (a *app) beginOIDC(w http.ResponseWriter, r *http.Request, code string) {
	if len(code) > maxCodeLength {
		http.Error(w, "code too long", http.StatusBadRequest)
		return
	}
	oauthCfg, _, err := a.oidc.ready(r.Context())
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "auth.idp_unreachable.title",
			"auth.idp_unreachable.text")
		return
	}
	state, err1 := randomString()
	nonce, err2 := randomString()
	if err1 != nil || err2 != nil {
		http.Error(w, "could not create secure flow", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	sealed, err := a.oidc.seal.seal(oidcFlow{State: state, Verifier: verifier, Nonce: nonce, Code: code, Join: r.FormValue("join") == "1" || a.isJoinInvitation(code),
		Expires: a.oidc.now().Add(flowTTL).Unix()})
	if err != nil {
		http.Error(w, "could not create secure flow", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.flowCookie(r, sealed, int(flowTTL.Seconds())))
	target := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// parseLoginForm begrenzt den Body (413 bei Überschreitung) und liest das
// Formular. Ohne Grenze erlaubt ParseForm 10 MB pro unangemeldeter Anfrage.
func parseLoginForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
		}
		return false
	}
	return true
}

// oidcCallback schließt den Ablauf ab. Der State muss in Parameter, Cookie und
// serverseitigem Ablauf übereinstimmen (Login-CSRF), der Ablauf ist einmalig.
func (a *app) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	cookie, cookieErr := r.Cookie(flowCookie)
	http.SetCookie(w, a.flowCookie(r, "", -1))
	state := r.URL.Query().Get("state")
	var flow oidcFlow
	valid := cookieErr == nil && state != ""
	if valid {
		flow, valid = a.oidc.seal.open(cookie.Value)
	}
	if !valid || subtle.ConstantTimeCompare([]byte(flow.State), []byte(state)) != 1 {
		a.loginMessage(w, http.StatusBadRequest, "auth.not_verified.title",
			"auth.not_verified.text")
		return
	}
	now := a.oidc.now()
	expires := time.Unix(flow.Expires, 0)
	if !now.Before(expires) || !a.oidc.used.use(state, now, expires) {
		a.loginMessage(w, http.StatusBadRequest, "auth.sign_in_expired.title", "auth.sign_in_expired.text")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		a.loginMessage(w, http.StatusForbidden, "auth.sso_reported.title", "auth.sso_reported.text", e)
		return
	}
	oauthCfg, verifier, err := a.oidc.ready(r.Context())
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "auth.idp_unreachable_retry.title", "auth.idp_unreachable_retry.text")
		return
	}
	ctx := a.oidc.context(r.Context())
	token, err := oauthCfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "auth.idp_rejected.title", "auth.idp_rejected.text")
		return
	}
	rawID, _ := token.Extra("id_token").(string)
	if rawID == "" {
		a.loginMessage(w, http.StatusBadGateway, "auth.no_id_token.title", "auth.no_id_token.text")
		return
	}
	idToken, err := verifier.Verify(ctx, rawID)
	if err != nil {
		a.loginMessage(w, http.StatusForbidden, "auth.id_token_invalid.title", "auth.id_token_invalid.text")
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		a.loginMessage(w, http.StatusForbidden, "auth.id_token_nonce.title", "auth.id_token_nonce.text")
		return
	}
	var claims profileClaims
	if err := idToken.Claims(&claims); err != nil || idToken.Subject == "" {
		a.loginMessage(w, http.StatusForbidden, "auth.id_token_subject.title", "auth.id_token_subject.text")
		return
	}
	// OIDC Core 3.1.3.7: bei mehreren Audiences oder gesetztem azp muss azp
	// dieser Client sein.
	if (len(idToken.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != a.oidc.cfg.ClientID {
		a.loginMessage(w, http.StatusForbidden, "auth.id_token_client.title", "auth.id_token_client.text")
		return
	}
	// Viele Anbieter (ZITADEL ohne "User Info Inside ID Token") legen Namen
	// nur am userinfo-Endpunkt ab. Wer im ID-Token nicht benannt ist, wird dort
	// nachgefragt; ein Fehler dort ändert am Login nichts.
	if !claims.named() {
		a.oidc.mergeUserInfo(ctx, oauthCfg, token, idToken.Subject, &claims)
	}
	// Eine nicht bestätigte Email ist eine Behauptung des Nutzers. Sie wird
	// nicht gespeichert und nicht zum Benennen verwendet.
	if !claims.EmailVerified {
		claims.Email = ""
	}
	account, outcome, err := a.store.LoginIdentity(store.IdentityLogin{
		Issuer: idToken.Issuer, Subject: idToken.Subject, Email: claims.Email,
		Name: claims.displayName(), Code: flow.Code,
	})
	if err != nil {
		a.identityRejected(w, err)
		return
	}
	if outcome == store.LoginBootstrapped {
		a.dropBootstrapFile()
	}
	// A name the IdP changed reaches the sessions that are already open.
	a.sessions.relabel(account.ID, account.Name)
	if flow.Join && (outcome == store.LoginInvited || outcome == store.LoginJoined) {
		a.finishJoinLogin(w, r, account)
		return
	}
	a.finishLogin(w, r, account)
}

// profileClaims sind die Profil-Claims aus ID-Token und userinfo.
type profileClaims struct {
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Nickname          string `json:"nickname"`
	AuthorizedParty   string `json:"azp"`
}

// fullName ist der Name, den der Anbieter für die Person selbst meldet.
func (c profileClaims) fullName() string {
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	return strings.TrimSpace(strings.TrimSpace(c.GivenName) + " " + strings.TrimSpace(c.FamilyName))
}

// named: das ID-Token nennt die Person schon mit ihrem Namen.
func (c profileClaims) named() bool { return c.fullName() != "" }

// displayName ist der Anzeigename: Name, Vor- und Nachname, Spitzname,
// Benutzername. Eine E-Mail-Adresse ist nie der Anzeigename; steht nichts
// anderes zur Verfügung, bleibt höchstens der Teil vor dem @, aus einem
// Benutzernamen in Adressform oder aus einer bestätigten Adresse.
func (c profileClaims) displayName() string {
	if n := c.fullName(); n != "" {
		return n
	}
	if n := strings.TrimSpace(c.Nickname); n != "" {
		return n
	}
	user := strings.TrimSpace(c.PreferredUsername)
	if user != "" && !strings.Contains(user, "@") {
		return user
	}
	if local, _, ok := strings.Cut(user, "@"); ok && local != "" {
		return local
	}
	if c.EmailVerified {
		if local, _, ok := strings.Cut(strings.TrimSpace(c.Email), "@"); ok && local != "" {
			return local
		}
	}
	return ""
}

// mergeUserInfo ergänzt fehlende Claims aus dem userinfo-Endpunkt. Die Antwort
// gilt nur, wenn ihr Subject das des ID-Tokens ist (OIDC Core 5.3.2).
func (c *oidcClient) mergeUserInfo(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token, subject string, claims *profileClaims) {
	c.mu.Lock()
	provider := c.provider
	c.mu.Unlock()
	if provider == nil {
		return
	}
	info, err := provider.UserInfo(ctx, cfg.TokenSource(ctx, token))
	if err != nil || info.Subject != subject {
		return
	}
	var got profileClaims
	if info.Claims(&got) != nil {
		return
	}
	if claims.Name == "" {
		claims.Name = got.Name
	}
	if claims.GivenName == "" && claims.FamilyName == "" {
		claims.GivenName, claims.FamilyName = got.GivenName, got.FamilyName
	}
	if claims.Nickname == "" {
		claims.Nickname = got.Nickname
	}
	if claims.PreferredUsername == "" {
		claims.PreferredUsername = got.PreferredUsername
	}
	if claims.Email == "" || !claims.EmailVerified {
		if got.Email != "" && got.EmailVerified {
			claims.Email, claims.EmailVerified = got.Email, true
		}
	}
}

func (a *app) identityRejected(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoAccountForIdentity):
		a.loginMessage(w, http.StatusForbidden, "auth.no_account.title",
			"auth.no_account.text")
	case errors.Is(err, store.ErrCodeInvalid):
		a.loginMessage(w, http.StatusForbidden, "auth.code_not_accepted.title", "auth.code_not_accepted.text")
	case errors.Is(err, store.ErrAccountHasIdentity):
		a.loginMessage(w, http.StatusForbidden, "auth.already_connected.title", "auth.already_connected.text")
	case errors.Is(err, store.ErrInvitationEmail):
		a.loginMessage(w, http.StatusForbidden, "auth.invitation_email.title",
			"auth.invitation_email.text")
	case errors.Is(err, store.ErrTooManyAttempts):
		a.loginMessage(w, http.StatusTooManyRequests, "auth.too_many_codes.title", "auth.too_many_codes.text")
	case errors.Is(err, store.ErrAccountDisabled):
		a.loginMessage(w, http.StatusForbidden, "auth.account_disabled.title", "auth.account_disabled.text")
	default:
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
	}
}

func (a *app) dropBootstrapFile() {
	if a.bootstrapFile != "" {
		_ = os.Remove(a.bootstrapFile)
	}
}

// loginMessage zeigt eine eigene Seite statt eines nackten Fehlertexts.
func (a *app) loginMessage(w http.ResponseWriter, status int, titleKey, textKey string, args ...any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	a.render(w, "loginmsg", pageData{Title: msg(titleKey), Error: msg(textKey, args...)})
}

// isJoinInvitation: ein im Codefeld eingegebener gültiger Einladungscode eines
// Projekts gilt wie die Anmeldung von der Join-Seite, damit das Ziel nach der
// Annahme /join/pair ist.
func (a *app) isJoinInvitation(code string) bool {
	_, ok := a.joinPreview(code)
	return ok
}
