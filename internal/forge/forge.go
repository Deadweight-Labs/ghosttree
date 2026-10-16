package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Prover ist der Nachweis hinter "GitHub verbinden": er sagt, ob jemand Admin
// einer Organisation auf der Quelle ist, und liefert deren Repo-Liste. Die
// Server-Logik kennt nur diese Schnittstelle, damit sie ohne echtes GitHub
// testbar ist. Das Zugriffstoken des Nutzers lebt nur während des Aufrufs.
type Prover interface {
	// Configured: die Zugangsdaten der App liegen auf dem Server.
	Configured() bool
	// AuthorizeURL ist die Adresse, auf die der Browser des Admins geschickt wird.
	AuthorizeURL(state, redirectURI string) string
	// Prove tauscht den Code gegen den Nachweis: die Konten, in denen der
	// Mensch Admin ist, samt ihrer Repos als normalisierte Remotes.
	Prove(ctx context.Context, code, redirectURI string) ([]Account, error)
}

// Account ist ein Konto (eine GitHub-Org) auf der Quelle, in dem der Mensch Admin ist.
type Account struct {
	Login string
	Repos []string
}

// ErrNotConfigured: die App-Zugangsdaten fehlen auf dem Server.
var ErrNotConfigured = errors.New("source is not configured on this server")

// Config sind die Zugangsdaten der GitHub-App (oder OAuth-App). Sie liegen nur
// als Secret auf dem Server: Client-Id aus der Umgebung, das Geheimnis aus der
// Umgebung oder aus einer Datei.
type Config struct {
	ClientID     string
	ClientSecret string
	// WebBase und APIBase sind für Tests austauschbar.
	WebBase string
	APIBase string
}

const (
	EnvClientID         = "GHOSTTREE_GITHUB_CLIENT_ID"
	EnvClientSecret     = "GHOSTTREE_GITHUB_CLIENT_SECRET"
	EnvClientSecretFile = "GHOSTTREE_GITHUB_CLIENT_SECRET_FILE"
)

// ConfigFromEnv liest die Zugangsdaten. Fehlt etwas, ist die Quelle "nicht
// eingerichtet" (Configured() false), kein Fehler.
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) Config {
	c := Config{ClientID: strings.TrimSpace(getenv(EnvClientID)), ClientSecret: strings.TrimSpace(getenv(EnvClientSecret))}
	if c.ClientSecret == "" {
		if path := strings.TrimSpace(getenv(EnvClientSecretFile)); path != "" {
			if b, err := readFile(path); err == nil {
				c.ClientSecret = strings.TrimSpace(string(b))
			}
		}
	}
	return c
}

// ConfigFromOS ist ConfigFromEnv mit der echten Umgebung.
func ConfigFromOS() Config { return ConfigFromEnv(os.Getenv, os.ReadFile) }

// GitHub beweist die Admin-Rolle über den Web-Ablauf der App (OAuth): Code gegen
// Token tauschen, die Mitgliedschaften des Nutzers mit Rolle "admin" lesen und
// deren Repos auflisten.
type GitHub struct {
	cfg    Config
	client *http.Client
}

func NewGitHub(cfg Config) *GitHub {
	if cfg.WebBase == "" {
		cfg.WebBase = "https://github.com"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	return &GitHub{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *GitHub) Configured() bool { return g.cfg.ClientID != "" && g.cfg.ClientSecret != "" }

func (g *GitHub) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{"client_id": {g.cfg.ClientID}, "redirect_uri": {redirectURI}, "state": {state}, "scope": {"read:org"}}
	return g.cfg.WebBase + "/login/oauth/authorize?" + q.Encode()
}

const (
	maxAdminOrgs   = 10
	maxRepoPages   = 5
	maxResponseLen = 4 << 20
)

func (g *GitHub) Prove(ctx context.Context, code, redirectURI string) ([]Account, error) {
	if !g.Configured() {
		return nil, ErrNotConfigured
	}
	token, err := g.exchange(ctx, code, redirectURI)
	if err != nil {
		return nil, err
	}
	var memberships []struct {
		State string `json:"state"`
		Role  string `json:"role"`
		Org   struct {
			Login string `json:"login"`
		} `json:"organization"`
	}
	if err := g.get(ctx, token, "/user/memberships/orgs?state=active&per_page=100", &memberships); err != nil {
		return nil, err
	}
	var out []Account
	for _, m := range memberships {
		if m.Role != "admin" || m.State != "active" || m.Org.Login == "" {
			continue
		}
		if len(out) >= maxAdminOrgs {
			break
		}
		acct := Account{Login: strings.ToLower(m.Org.Login)}
		for page := 1; page <= maxRepoPages; page++ {
			var repos []struct {
				FullName string `json:"full_name"`
			}
			path := fmt.Sprintf("/orgs/%s/repos?type=all&per_page=100&page=%d", url.PathEscape(m.Org.Login), page)
			if err := g.get(ctx, token, path, &repos); err != nil {
				return nil, err
			}
			for _, r := range repos {
				acct.Repos = append(acct.Repos, "github.com/"+strings.ToLower(r.FullName))
			}
			if len(repos) < 100 {
				break
			}
		}
		out = append(out, acct)
	}
	return out, nil
}

func (g *GitHub) exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{"client_id": {g.cfg.ClientID}, "client_secret": {g.cfg.ClientSecret}, "code": {code}, "redirect_uri": {redirectURI}}
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
		// Die Fehlerart ist bekannt, der Code selbst und alles Übrige nicht für das Log.
		return "", fmt.Errorf("github refused the code: %s", out.Error)
	}
	return out.AccessToken, nil
}

func (g *GitHub) get(ctx context.Context, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.APIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return g.do(req, v)
}

func (g *GitHub) do(req *http.Request, v any) error {
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
		return fmt.Errorf("github answered %d", resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

// redactErr entfernt die URL aus einem Transportfehler: die Anfrage des Tausches
// trägt Client-Geheimnis und Code im Körper, aber das Log braucht sie nie.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
