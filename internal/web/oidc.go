package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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
	maxOpenFlows  = 2000
	idpHTTPTimout = 10 * time.Second
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

type oidcFlow struct {
	verifier, nonce, code string
	expires               time.Time
}

type oidcClient struct {
	cfg  OIDCConfig
	http *http.Client

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier

	flowsMu sync.Mutex
	flows   map[string]oidcFlow
}

func newOIDCClient(cfg OIDCConfig) *oidcClient {
	c := &oidcClient{cfg: cfg, http: cfg.HTTPClient, flows: map[string]oidcFlow{}}
	if c.http == nil {
		c.http = &http.Client{Timeout: idpHTTPTimout}
	}
	return c
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

// begin legt einen Ablauf an. Abgelaufene Einträge werden hier entsorgt; die
// Obergrenze hält unangemeldete POSTs davon ab, den Speicher zu füllen.
func (c *oidcClient) begin(f oidcFlow) (string, bool, error) {
	state, err := randomString()
	if err != nil {
		return "", false, err
	}
	c.flowsMu.Lock()
	defer c.flowsMu.Unlock()
	now := time.Now()
	for k, v := range c.flows {
		if now.After(v.expires) {
			delete(c.flows, k)
		}
	}
	if len(c.flows) >= maxOpenFlows {
		return "", false, nil
	}
	f.expires = now.Add(flowTTL)
	c.flows[state] = f
	return state, true, nil
}

// take gibt den Ablauf genau einmal heraus.
func (c *oidcClient) take(state string) (oidcFlow, bool) {
	c.flowsMu.Lock()
	defer c.flowsMu.Unlock()
	f, ok := c.flows[state]
	delete(c.flows, state)
	return f, ok && time.Now().Before(f.expires)
}

// Option konfiguriert die Weboberfläche.
type Option func(*app)

// WithOIDC schaltet den OIDC-Login ein. Eine leere Konfiguration lässt alles
// wie bisher.
func WithOIDC(cfg OIDCConfig) Option {
	return func(a *app) {
		if cfg.Enabled() {
			a.oidc = newOIDCClient(cfg)
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	oauthCfg, _, err := a.oidc.ready(r.Context())
	if err != nil {
		a.loginMessage(w, http.StatusBadGateway, "Identity provider unreachable",
			"The identity provider could not be reached. Try again in a moment, or ask the operator for a one-time login link.")
		return
	}
	nonce, err := randomString()
	if err != nil {
		http.Error(w, "could not create secure flow", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	state, ok, err := a.oidc.begin(oidcFlow{verifier: verifier, nonce: nonce, code: strings.TrimSpace(r.FormValue("code"))})
	if err != nil {
		http.Error(w, "could not create secure flow", http.StatusInternalServerError)
		return
	}
	if !ok {
		a.loginMessage(w, http.StatusServiceUnavailable, "Too many sign-ins in progress", "Try again in a few minutes.")
		return
	}
	http.SetCookie(w, a.flowCookie(r, state, int(flowTTL.Seconds())))
	target := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	http.Redirect(w, r, target, http.StatusSeeOther)
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
	if cookieErr != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		a.loginMessage(w, http.StatusBadRequest, "Sign-in could not be verified",
			"This sign-in was not started in this browser or has already been used. Start again from the sign-in page.")
		return
	}
	flow, ok := a.oidc.take(state)
	if !ok {
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
	token, err := oauthCfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(flow.verifier))
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
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.nonce)) != 1 {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token does not belong to this sign-in.")
		return
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil || idToken.Subject == "" {
		a.loginMessage(w, http.StatusForbidden, "Sign-in failed", "The ID token carries no usable subject.")
		return
	}
	account, outcome, err := a.store.LoginIdentity(store.IdentityLogin{
		Issuer: idToken.Issuer, Subject: idToken.Subject, Email: claims.Email,
		Name: displayName(claims.PreferredUsername, claims.Name, claims.Email), Code: flow.code,
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
