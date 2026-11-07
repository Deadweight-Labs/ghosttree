package forge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Umgebung der GitHub-App. Der Private Key und das Webhook-Secret liegen nur als
// Datei auf dem Server; das Client-Geheimnis der App (für den Nachweis des
// Nutzers) als Datei oder direkt in der Umgebung.
const (
	EnvAppID                = "GHOSTTREE_GITHUB_APP_ID"
	EnvAppSlug              = "GHOSTTREE_GITHUB_APP_SLUG"
	EnvAppPrivateKeyFile    = "GHOSTTREE_GITHUB_APP_PRIVATE_KEY_FILE"
	EnvAppWebhookSecretFile = "GHOSTTREE_GITHUB_APP_WEBHOOK_SECRET_FILE"
	EnvAppClientID          = "GHOSTTREE_GITHUB_APP_CLIENT_ID"
	EnvAppClientSecret      = "GHOSTTREE_GITHUB_APP_CLIENT_SECRET"
	EnvAppClientSecretFile  = "GHOSTTREE_GITHUB_APP_CLIENT_SECRET_FILE"
)

var (
	// ErrInstallationNotYours: die gemeldete Installation gehört nicht zu den
	// Installationen, die GitHub dem verbindenden Nutzer zeigt.
	ErrInstallationNotYours = errors.New("that installation does not belong to you")
	// ErrNotOrgAdmin: der Nutzer ist nicht Admin der Organisation der Installation.
	ErrNotOrgAdmin = errors.New("you are not an admin of the organization of that installation")
	// ErrPermissionMissing: die App hat für diese Abfrage keine Berechtigung
	// (z. B. Pull requests: read fehlt).
	ErrPermissionMissing = errors.New("the GitHub App lacks the permission for that request")
)

// AppConfig sind die Zugangsdaten der GitHub-App.
type AppConfig struct {
	AppID         int64
	Slug          string
	PrivateKey    *rsa.PrivateKey
	WebhookSecret string
	ClientID      string
	ClientSecret  string
	// WebBase und APIBase sind für Tests austauschbar.
	WebBase string
	APIBase string
	// Now ist für Tests austauschbar (JWT-Zeiten).
	Now func() time.Time
}

// AppConfigFromEnv liest die Zugangsdaten. Fehlt etwas oder ist der Schlüssel
// nicht lesbar, ist die App "nicht eingerichtet" (Configured() false), kein
// Fehler, und nichts davon gelangt in einen Text.
func AppConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) AppConfig {
	c := AppConfig{
		Slug:         strings.TrimSpace(getenv(EnvAppSlug)),
		ClientID:     strings.TrimSpace(getenv(EnvAppClientID)),
		ClientSecret: strings.TrimSpace(getenv(EnvAppClientSecret)),
	}
	c.AppID, _ = strconv.ParseInt(strings.TrimSpace(getenv(EnvAppID)), 10, 64)
	fromFile := func(env string) string {
		if path := strings.TrimSpace(getenv(env)); path != "" {
			if b, err := readFile(path); err == nil {
				return strings.TrimSpace(string(b))
			}
		}
		return ""
	}
	if c.ClientSecret == "" {
		c.ClientSecret = fromFile(EnvAppClientSecretFile)
	}
	c.WebhookSecret = fromFile(EnvAppWebhookSecretFile)
	if path := strings.TrimSpace(getenv(EnvAppPrivateKeyFile)); path != "" {
		if b, err := readFile(path); err == nil {
			c.PrivateKey, _ = ParsePrivateKey(b)
		}
	}
	return c
}

// AppConfigFromOS ist AppConfigFromEnv mit der echten Umgebung.
func AppConfigFromOS() AppConfig { return AppConfigFromEnv(os.Getenv, os.ReadFile) }

// ParsePrivateKey liest einen RSA-Schlüssel im PEM-Format, wie GitHub ihn
// ausstellt (PKCS#1), oder als PKCS#8.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("private key is neither PKCS#1 nor PKCS#8")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not an RSA key")
	}
	return rk, nil
}

// GitHubApp beweist die Admin-Rolle über die Installation der GitHub-App: der
// Nutzer meldet sich mit der App an (user-to-server), GitHub zeigt seine
// Installationen, und nur eine Installation einer Organisation, in der er Admin
// ist, zählt. Die Repo-Liste kommt über das Installations-Token der App, also
// auch mit privaten Repos, aber nur mit dem Recht Metadata: read.
type GitHubApp struct {
	cfg    AppConfig
	client *http.Client
}

func NewGitHubApp(cfg AppConfig) *GitHubApp {
	if cfg.WebBase == "" {
		cfg.WebBase = "https://github.com"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &GitHubApp{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}}
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)

// Configured: App-ID, Slug, Schlüssel und Client-Zugang liegen vor.
func (g *GitHubApp) Configured() bool {
	return g.cfg.AppID > 0 && slugRE.MatchString(g.cfg.Slug) && g.cfg.PrivateKey != nil &&
		g.cfg.ClientID != "" && g.cfg.ClientSecret != ""
}

// WebhookConfigured: das Webhook-Secret liegt vor.
func (g *GitHubApp) WebhookConfigured() bool { return g.Configured() && g.cfg.WebhookSecret != "" }

// AuthorizeURL ist die Installationsadresse der App. Der state kommt mit dem
// Code an die Setup-URL zurück; die Rückkehradresse ist bei der App selbst
// hinterlegt (Setup URL).
func (g *GitHubApp) AuthorizeURL(state, redirectURI string) string {
	return g.cfg.WebBase + "/apps/" + g.cfg.Slug + "/installations/new?" + url.Values{"state": {state}}.Encode()
}

// Prove ist ProveInstallation ohne Vorgabe: alle Installationen des Nutzers,
// deren Organisation er administriert.
func (g *GitHubApp) Prove(ctx context.Context, code, redirectURI string) ([]Account, error) {
	return g.ProveInstallation(ctx, code, redirectURI, 0)
}

type appInstallation struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// ProveInstallation tauscht den Code gegen das Token des Nutzers und liefert die
// Organisationen mit Installation der App, in denen er Admin ist. Nennt
// installationID eine Installation (aus der Rückkehr-Adresse, also vom Browser
// und nicht vertrauenswürdig), muss sie unter den Installationen des Nutzers
// sein und ihre Organisation von ihm administriert werden, sonst wird abgelehnt.
func (g *GitHubApp) ProveInstallation(ctx context.Context, code, redirectURI string, installationID int64) ([]Account, error) {
	if !g.Configured() {
		return nil, ErrNotConfigured
	}
	token, err := g.exchange(ctx, code, redirectURI)
	if err != nil {
		return nil, err
	}
	var installs []appInstallation
	for page := 1; page <= maxRepoPages; page++ {
		var resp struct {
			Installations []appInstallation `json:"installations"`
		}
		if err := g.getAs(ctx, token, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), &resp); err != nil {
			return nil, err
		}
		installs = append(installs, resp.Installations...)
		if len(resp.Installations) < 100 {
			break
		}
	}
	var out []Account
	seen := false
	for _, in := range installs {
		if installationID > 0 && in.ID != installationID {
			continue
		}
		if in.AppID != 0 && in.AppID != g.cfg.AppID {
			continue
		}
		seen = true
		if in.Account.Type != "Organization" || in.Account.Login == "" {
			if installationID > 0 {
				return nil, ErrNotOrgAdmin
			}
			continue
		}
		var m struct {
			State string `json:"state"`
			Role  string `json:"role"`
		}
		path := "/user/memberships/orgs/" + url.PathEscape(in.Account.Login)
		if err := g.getAs(ctx, token, path, &m); err != nil || m.State != "active" || m.Role != "admin" {
			if installationID > 0 {
				return nil, ErrNotOrgAdmin
			}
			continue
		}
		if len(out) >= maxAdminOrgs {
			break
		}
		acct := Account{Login: strings.ToLower(in.Account.Login), InstallationID: in.ID}
		// Der Admin-Nachweis liegt vor; kann GitHub die Repos nicht liefern, wird
		// die Org ohne Repos angeboten statt alle zu verwerfen.
		if repos, err := g.InstallationRepos(ctx, in.ID); err == nil {
			acct.Repos = repos
		}
		out = append(out, acct)
	}
	if installationID > 0 && !seen {
		return nil, ErrInstallationNotYours
	}
	return out, nil
}

// InstallationRepos listet alle Repos der Installation (auch private) als
// normalisierte Remotes, über ein auf Metadata: read begrenztes Token.
func (g *GitHubApp) InstallationRepos(ctx context.Context, installationID int64) ([]string, error) {
	tok, err := g.installationToken(ctx, installationID, map[string]string{"metadata": "read"})
	if err != nil {
		return nil, err
	}
	var out []string
	for page := 1; page <= maxRepoPages; page++ {
		var resp struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := g.getAs(ctx, tok, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Repositories {
			out = append(out, "github.com/"+strings.ToLower(r.FullName))
		}
		if len(resp.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// PullRequest ist der Stand eines PR für die Zuordnung von Branch und PR.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"` // open, closed oder merged
	Draft     bool      `json:"draft"`
	HeadRef   string    `json:"head_ref"`
	HeadSHA   string    `json:"head_sha"`
	BaseRef   string    `json:"base_ref"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updated_at"`
}

const maxPullRequests = 30

// PullRequests liefert die offenen und zuletzt geschlossenen PRs eines Branches
// (neueste zuerst), über das Installations-Token. repo ist ein normalisiertes
// Remote (github.com/owner/repo). Das braucht das App-Recht "Pull requests:
// read", das über Metadata: read hinaus geht; fehlt es, kommt ErrPermissionMissing.
func (g *GitHubApp) PullRequests(ctx context.Context, installationID int64, repo, branch string) ([]PullRequest, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(repo)), "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" || strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("pull requests: repo must look like github.com/owner/repo and a branch is needed")
	}
	tok, err := g.installationToken(ctx, installationID, nil)
	if err != nil {
		return nil, err
	}
	q := url.Values{"state": {"all"}, "head": {parts[1] + ":" + branch}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {strconv.Itoa(maxPullRequests)}}
	var raw []struct {
		Number   int        `json:"number"`
		Title    string     `json:"title"`
		State    string     `json:"state"`
		Draft    bool       `json:"draft"`
		HTMLURL  string     `json:"html_url"`
		Updated  time.Time  `json:"updated_at"`
		MergedAt *time.Time `json:"merged_at"`
		Head     struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	path := "/repos/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(parts[2]) + "/pulls?" + q.Encode()
	if err := g.getAs(ctx, tok, path, &raw); err != nil {
		var se *statusError
		if errors.As(err, &se) && (se.code == http.StatusForbidden || se.code == http.StatusNotFound) {
			return nil, ErrPermissionMissing
		}
		return nil, err
	}
	out := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		st := r.State
		if r.MergedAt != nil {
			st = "merged"
		}
		out = append(out, PullRequest{Number: r.Number, Title: r.Title, State: st, Draft: r.Draft,
			HeadRef: r.Head.Ref, HeadSHA: r.Head.SHA, BaseRef: r.Base.Ref, URL: r.HTMLURL, UpdatedAt: r.Updated})
	}
	return out, nil
}

// ---------------------------------------------------------------- JWT und Token

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// appJWT ist das kurzlebige RS256-Token, mit dem sich die App bei GitHub
// ausweist. Es lebt neun Minuten (GitHub erlaubt höchstens zehn) und beginnt
// eine Minute in der Vergangenheit gegen Uhrenabweichung.
func (g *GitHubApp) appJWT() (string, error) {
	now := g.cfg.Now()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": g.cfg.AppID})
	signing := b64(hdr) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.cfg.PrivateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("signing the app token failed")
	}
	return signing + "." + b64(sig), nil
}

// installationToken holt ein Installations-Token; permissions begrenzt es
// (nil: alle Rechte der App).
func (g *GitHubApp) installationToken(ctx context.Context, installationID int64, permissions map[string]string) (string, error) {
	if !g.Configured() {
		return "", ErrNotConfigured
	}
	if installationID <= 0 {
		return "", errors.New("github: no installation")
	}
	jwt, err := g.appJWT()
	if err != nil {
		return "", err
	}
	var body io.Reader
	if permissions != nil {
		b, _ := json.Marshal(map[string]any{"permissions": permissions})
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.APIBase+"/app/installations/"+strconv.FormatInt(installationID, 10)+"/access_tokens", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := g.do(req, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("github gave no installation token")
	}
	return out.Token, nil
}

// ---------------------------------------------------------------- HTTP

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("github answered %d", e.code) }

func (g *GitHubApp) exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{"client_id": {g.cfg.ClientID}, "client_secret": {g.cfg.ClientSecret}, "code": {code}}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.WebBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := g.do(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("github refused the code: %s", out.Error)
	}
	return out.AccessToken, nil
}

func (g *GitHubApp) getAs(ctx context.Context, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.APIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return g.do(req, v)
}

func (g *GitHubApp) do(req *http.Request, v any) error {
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", redactErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseLen))
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{code: resp.StatusCode}
	}
	return json.Unmarshal(body, v)
}
