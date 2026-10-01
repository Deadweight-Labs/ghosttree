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
		}
	}
}

// WithBootstrapFile nennt die Datei mit dem Klartext-Bootstrap-Code; sie wird
// gelöscht, sobald das erste Konto angelegt ist.
func WithBootstrapFile(path string) Option { return func(a *app) { a.bootstrapFile = path } }

func (a *app) secureCookies(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if a.oidc != nil && strings.HasPrefix(a.oidc.cfg.RedirectURL, "https://") {
		return true
	}
	proto, set := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	return set && proto == "https" && remoteIsLoopback(r.RemoteAddr)
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
	code := strings.TrimSpace(r.FormValue("code"))
	if len(code) > maxCodeLength {
		http.Error(w, "code too long", http.StatusBadRequest)
		return
	}
	oauthCfg, _, err := a.oidc.ready(r.Context())
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "Identity provider unreachable",
			"The identity provider could not be reached. Try again in a moment, or ask the operator for a one-time login link.")
		return
	}
	state, err1 := randomString()
	nonce, err2 := randomString()
	if err1 != nil || err2 != nil {
		http.Error(w, "could not create secure flow", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	sealed, err := a.oidc.seal.seal(oidcFlow{State: state, Verifier: verifier, Nonce: nonce, Code: code,
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
		a.loginMessage(w, http.StatusBadRequest, "Sign-in could not be verified",
			"This sign-in was not started in this browser or has already been used. Start again from the sign-in page.")
		return
	}
	now := a.oidc.now()
	expires := time.Unix(flow.Expires, 0)
	if !now.Before(expires) || !a.oidc.used.use(state, now, expires) {
		a.loginMessage(w, http.StatusBadRequest, "Sign-in expired", "The sign-in took too long or was already used. Start again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		a.loginMessage(w, http.StatusForbidden, "Sign-in was not completed", "The identity provider reported: "+e+".")
		return
	}
	oauthCfg, verifier, err := a.oidc.ready(r.Context())
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "Identity provider unreachable", "Try again in a moment.")
		return
	}
	ctx := a.oidc.context(r.Context())
	token, err := oauthCfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "Sign-in failed", "The identity provider rejected the sign-in.")
		return
	}
	rawID, _ := token.Extra("id_token").(string)
	if rawID == "" {
		a.loginMessage(w, http.StatusBadGateway, "Sign-in failed", "The identity provider returned no ID token.")
		return
	}
	idToken, err := verifier.Verify(ctx, rawID)
	if err != nil {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token is invalid or expired.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token does not belong to this sign-in.")
		return
	}
	var claims struct {
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		AuthorizedParty   string `json:"azp"`
	}
	if err := idToken.Claims(&claims); err != nil || idToken.Subject == "" {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token carries no usable subject.")
		return
	}
	// OIDC Core 3.1.3.7: bei mehreren Audiences oder gesetztem azp muss azp
	// dieser Client sein.
	if (len(idToken.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != a.oidc.cfg.ClientID {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token was issued for another client.")
		return
	}
	// Eine nicht bestätigte Email ist eine Behauptung des Nutzers. Sie wird
	// nicht gespeichert und nicht zum Benennen verwendet.
	if !claims.EmailVerified {
		claims.Email = ""
	}
	account, outcome, err := a.store.LoginIdentity(store.IdentityLogin{
		Issuer: idToken.Issuer, Subject: idToken.Subject, Email: claims.Email,
		Name: displayName(claims.PreferredUsername, claims.Name, claims.Email), Code: flow.Code,
	})
	if err != nil {
		a.identityRejected(w, err)
		return
	}
	if outcome == store.LoginBootstrapped {
		a.dropBootstrapFile()
	}
	a.finishLogin(w, r, account)
}

func displayName(preferred, name, email string) string {
	for _, v := range []string{preferred, name} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	if local, _, ok := strings.Cut(strings.TrimSpace(email), "@"); ok && local != "" {
		return local
	}
	return ""
}

func (a *app) identityRejected(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoAccountForIdentity):
		a.loginMessage(w, http.StatusForbidden, "No ghosttree account for this identity",
			"This identity is not linked to a ghosttree account, and accounts are created by invitation only. Ask an owner for an invitation, or for a claim code if your account already exists.")
	case errors.Is(err, store.ErrCodeInvalid):
		a.loginMessage(w, http.StatusForbidden, "Code not accepted", "The code is invalid, expired or was already used. Ask for a new one.")
	case errors.Is(err, store.ErrAccountHasIdentity):
		a.loginMessage(w, http.StatusForbidden, "Account already connected", "This account is already connected to an identity and cannot be claimed again.")
	case errors.Is(err, store.ErrAccountDisabled):
		a.loginMessage(w, http.StatusForbidden, "Account disabled", "This account is disabled.")
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
func (a *app) loginMessage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	a.render(w, "loginmsg", pageData{Title: title, Error: message})
}
