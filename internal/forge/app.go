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
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// ErrCommitUnavailable: GitHub kennt diesen einen Commit nicht (mehr), etwa weil
	// ein Force-Push ihn verworfen hat. Anders als ErrPermissionMissing sagt das
	// nichts über die übrigen Commits der Installation.
	ErrCommitUnavailable = errors.New("github does not know that commit")
	// ErrRateLimited: GitHub hat das Ratenlimit der Installation erreicht. Der
	// konkrete Fehler (RateLimitError) nennt, wann es wieder geht.
	ErrRateLimited = errors.New("github rate limit reached")
)

// RateLimitError ist die Antwort von GitHub, wenn das Ratenlimit erreicht ist
// (429, oder 403 mit leerem X-RateLimit-Remaining oder Retry-After). Reset ist
// der Zeitpunkt, ab dem es wieder geht, sofern GitHub ihn nannte.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	if e.Reset.IsZero() {
		return "github rate limit reached"
	}
	return "github rate limit reached, resets at " + e.Reset.UTC().Format(time.RFC3339)
}

// Is lässt errors.Is(err, ErrRateLimited) greifen.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// rateLimited erkennt die Antworten, die ein Ratenlimit melden.
func rateLimited(resp *http.Response, now time.Time) (*RateLimitError, bool) {
	limited := resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""))
	if !limited {
		return nil, false
	}
	e := &RateLimitError{}
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs >= 0 {
		e.Reset = now.Add(time.Duration(secs) * time.Second)
	} else if unix, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && unix > 0 {
		e.Reset = time.Unix(unix, 0)
	}
	return e, true
}

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

	// tokens hält die Installations-Token bis kurz vor ihrem Ablauf, damit ein Lauf
	// über viele Commits nicht für jeden Abruf ein neues bei GitHub holt.
	tokMu  sync.Mutex
	tokens map[string]cachedToken
}

// cachedToken ist ein Installations-Token mit seinem Ablauf. Es steht nur im
// Speicher und kommt in keinen Fehler und kein Protokoll.
type cachedToken struct {
	token   string
	expires time.Time
}

// tokenSlack ist die Zeit vor dem Ablauf, ab der ein Token nicht mehr benutzt wird.
const tokenSlack = 5 * time.Minute

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

// AuthorizeURL ist die Nutzer-Autorisierung der App (login/oauth/authorize).
// Sie führt auch bei schon installierter App zurück auf die Rückkehradresse mit
// code und state. installations/new tut das nicht: GitHub leitet dort bei
// installierter App auf die Einstellungsseite und nie zurück (beobachtet am
// 2026-11-08). Der Admin-Nachweis kommt danach aus Prove.
func (g *GitHubApp) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{"client_id": {g.cfg.ClientID}, "redirect_uri": {redirectURI}, "state": {state}}
	return g.cfg.WebBase + "/login/oauth/authorize?" + q.Encode()
}

// InstallURL ist die Installationsadresse der App, für Organisationen ohne
// Installation. Der state kommt mit dem Code an die Setup-URL zurück, die bei
// der App selbst hinterlegt ist.
func (g *GitHubApp) InstallURL(state string) string {
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
		if infos, err := g.InstallationRepoInfos(ctx, in.ID); err == nil {
			acct.Visibility = map[string]bool{}
			for _, r := range infos {
				acct.Repos = append(acct.Repos, r.Remote)
				acct.Visibility[r.Remote] = r.Private
			}
		}
		out = append(out, acct)
	}
	if installationID > 0 && !seen {
		return nil, ErrInstallationNotYours
	}
	return out, nil
}

// Identify tauscht den Code gegen das Token des Nutzers und liest damit, wer er
// auf GitHub ist (GET /user, mit dem Benutzer-Token der App erlaubt). Das Token
// bleibt in diesem Aufruf.
func (g *GitHubApp) Identify(ctx context.Context, code, redirectURI string) (Identity, error) {
	if !g.Configured() {
		return Identity{}, ErrNotConfigured
	}
	token, err := g.exchange(ctx, code, redirectURI)
	if err != nil {
		return Identity{}, err
	}
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := g.getAs(ctx, token, "/user", &u); err != nil {
		return Identity{}, err
	}
	return parseIdentity(u.Login, u.ID)
}

// RepoInfo ist ein Repo der Installation mit dem, was GitHub über seine Sichtbarkeit
// sagt.
type RepoInfo struct {
	// Remote ist die normalisierte Remote (github.com/owner/repo, klein).
	Remote  string
	Private bool
}

// InstallationRepos listet alle Repos der Installation (auch private) als
// normalisierte Remotes, über ein auf Metadata: read begrenztes Token.
func (g *GitHubApp) InstallationRepos(ctx context.Context, installationID int64) ([]string, error) {
	infos, err := g.InstallationRepoInfos(ctx, installationID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos))
	for _, r := range infos {
		out = append(out, r.Remote)
	}
	return out, nil
}

// InstallationRepoInfos ist InstallationRepos mit der Sichtbarkeit je Repo (das Feld
// private der Antwort). Davon hängt ab, ob ghosttree etwas auf GitHub schreibt, das
// jeder lesen kann.
func (g *GitHubApp) InstallationRepoInfos(ctx context.Context, installationID int64) ([]RepoInfo, error) {
	tok, err := g.installationToken(ctx, installationID, map[string]string{"metadata": "read"})
	if err != nil {
		return nil, err
	}
	var out []RepoInfo
	for page := 1; page <= maxRepoPages; page++ {
		var resp struct {
			Repositories []struct {
				FullName string `json:"full_name"`
				Private  bool   `json:"private"`
			} `json:"repositories"`
		}
		if err := g.getAs(ctx, tok, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Repositories {
			out = append(out, RepoInfo{Remote: "github.com/" + strings.ToLower(r.FullName), Private: r.Private})
		}
		if len(resp.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// OrgProfile is what an organization shows of itself on GitHub: the login (the
// address), the display name and the number of its picture.
type OrgProfile struct {
	Login string
	Name  string
	ID    int64
}

// OrgProfiler reads the profile of a connected organization (GitHub App).
type OrgProfiler interface {
	OrgProfile(ctx context.Context, installationID int64, login string) (OrgProfile, error)
}

// OrgProfile reads GET /orgs/{login} with the installation's token and only
// the metadata right. The login comes back as GitHub spells it; a login that
// is not a GitHub login is refused before it goes into an address.
func (g *GitHubApp) OrgProfile(ctx context.Context, installationID int64, login string) (OrgProfile, error) {
	if !loginSyntax.MatchString(login) {
		return OrgProfile{}, ErrBadIdentity
	}
	tok, err := g.installationToken(ctx, installationID, map[string]string{"metadata": "read"})
	if err != nil {
		return OrgProfile{}, err
	}
	var resp struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		ID    int64  `json:"id"`
	}
	if err := g.getAs(ctx, tok, "/orgs/"+url.PathEscape(login), &resp); err != nil {
		return OrgProfile{}, err
	}
	if !loginSyntax.MatchString(resp.Login) || !strings.EqualFold(resp.Login, login) {
		return OrgProfile{}, ErrBadIdentity
	}
	name := strings.TrimSpace(resp.Name)
	if len(name) > 100 {
		name = ""
	}
	return OrgProfile{Login: resp.Login, Name: name, ID: resp.ID}, nil
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
	Author    string    `json:"author,omitempty"`
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
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
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
			HeadRef: r.Head.Ref, HeadSHA: r.Head.SHA, BaseRef: r.Base.Ref, Author: r.User.Login, URL: r.HTMLURL, UpdatedAt: r.Updated})
	}
	return out, nil
}

// PullCommit ist ein Commit eines PRs, wie GitHub ihn meldet.
type PullCommit struct {
	SHA string `json:"sha"`
	// Author ist der GitHub-Login (leer, wenn GitHub den Autor keinem Konto
	// zuordnet), AuthorName der Name aus dem Commit.
	Author     string `json:"author,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	// Title ist die erste Zeile der Commit-Nachricht, Message die ganze.
	Title       string `json:"title"`
	Message     string `json:"message,omitempty"`
	CommittedAt string `json:"committed_at,omitempty"`
	// Parents sind die SHAs der Eltern-Commits; sie kommen mit derselben Antwort.
	Parents []string `json:"parents,omitempty"`
}

const (
	maxCommitPages = 3 // GitHub liefert für einen PR höchstens 250 Commits
	commitsPerPage = 100
)

// PullRequestCommits liefert die Commits eines PRs in der Reihenfolge des PRs,
// über ein auf "Pull requests: read" begrenztes Installations-Token. repo ist ein
// normalisiertes Remote (github.com/owner/repo). Fehlt das Recht, kommt
// ErrPermissionMissing; ist das Ratenlimit erreicht, ein RateLimitError. Das Token
// kommt in keinen Fehler und kein Protokoll.
func (g *GitHubApp) PullRequestCommits(ctx context.Context, installationID int64, repo string, number int64) ([]PullCommit, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(repo)), "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" || number <= 0 {
		return nil, fmt.Errorf("pull request commits: repo must look like github.com/owner/repo and the number must be positive")
	}
	tok, err := g.installationToken(ctx, installationID, map[string]string{"pull_requests": "read"})
	if err != nil {
		var se *statusError
		if errors.As(err, &se) && (se.code == http.StatusForbidden || se.code == http.StatusUnprocessableEntity) {
			return nil, ErrPermissionMissing
		}
		return nil, err
	}
	var out []PullCommit
	for page := 1; page <= maxCommitPages; page++ {
		var raw []struct {
			SHA     string `json:"sha"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
				} `json:"author"`
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
			} `json:"commit"`
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=%d&page=%d", url.PathEscape(parts[1]), url.PathEscape(parts[2]), number, commitsPerPage, page)
		if err := g.getAs(ctx, tok, path, &raw); err != nil {
			var se *statusError
			if errors.As(err, &se) && (se.code == http.StatusForbidden || se.code == http.StatusNotFound) {
				return nil, ErrPermissionMissing
			}
			return nil, err
		}
		for _, r := range raw {
			c := PullCommit{SHA: r.SHA, AuthorName: r.Commit.Author.Name, Title: firstLine(r.Commit.Message), Message: r.Commit.Message}
			for _, p := range r.Parents {
				c.Parents = append(c.Parents, p.SHA)
			}
			if r.Author != nil {
				c.Author = r.Author.Login
			}
			if !r.Commit.Committer.Date.IsZero() {
				c.CommittedAt = r.Commit.Committer.Date.UTC().Format(time.RFC3339)
			}
			out = append(out, c)
		}
		if len(raw) < commitsPerPage {
			break
		}
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// CommitFile ist eine Datei, die ein Commit ändert, mit den Zeilen, die er
// hinzufügt und entfernt.
type CommitFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// CommitFiles ist die Dateiliste eines Commits. Total ist die Zahl der Dateien
// laut GitHub (die Liste hat höchstens maxCommitFiles), Additions und Deletions
// die Summen über den ganzen Commit.
type CommitFiles struct {
	Files     []CommitFile `json:"files"`
	Total     int          `json:"total"`
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
}

const maxCommitFiles = 300

// CommitFiles liefert die geänderten Dateien eines Commits mit ihren Zeilenzahlen
// (GET /repos/{owner}/{repo}/commits/{sha}). GitHub verlangt dafür das Recht
// "Contents: read"; das Token wird darauf begrenzt. Fehlt es, kommt
// ErrPermissionMissing: die Commit-Seite zeigt dann nur, was sie sonst weiß. Kennt
// GitHub den Commit nicht (404, 422), kommt ErrCommitUnavailable.
func (g *GitHubApp) CommitFiles(ctx context.Context, installationID int64, repo, sha string) (CommitFiles, error) {
	parts, ok := repoParts(repo)
	if !ok || !hexSHA(sha) {
		return CommitFiles{}, fmt.Errorf("commit files: repo must look like github.com/owner/repo and the sha must be hexadecimal")
	}
	tok, err := g.installationToken(ctx, installationID, map[string]string{"contents": "read"})
	if err != nil {
		return CommitFiles{}, tokenRefusal(err)
	}
	type commitBody struct {
		Stats struct {
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
		} `json:"stats"`
		Files []struct {
			Filename  string `json:"filename"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
		} `json:"files"`
	}
	var raw commitBody
	path := "/repos/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(parts[2]) + "/commits/" + sha
	hdr, err := g.getHeader(ctx, tok, path, &raw)
	if err != nil {
		return CommitFiles{}, commitRefusal(err)
	}
	out := CommitFiles{Total: len(raw.Files), Additions: raw.Stats.Additions, Deletions: raw.Stats.Deletions}
	// Hat der Commit mehr Dateien, als eine Seite fasst, nennt GitHub die letzte Seite
	// im Link-Kopf. Alle Seiten bis auf die letzte sind voll; mit der letzten (ein
	// Abruf mehr, nur für solche Commits) ist die Zahl genau. Gelingt er nicht, bleibt
	// es bei der Zahl der ersten Seite: sie dient nur der Anzeige.
	if last := lastPage(hdr.Get("Link")); last > 1 && len(raw.Files) > 0 {
		var tail commitBody
		if _, err := g.getHeader(ctx, tok, path+"?page="+strconv.Itoa(last), &tail); err == nil {
			out.Total = (last-1)*len(raw.Files) + len(tail.Files)
		}
	}
	for i, f := range raw.Files {
		if i >= maxCommitFiles {
			break
		}
		out.Files = append(out.Files, CommitFile{Path: f.Filename, Additions: f.Additions, Deletions: f.Deletions})
	}
	return out, nil
}

// Stände der Prüfungen eines Commits.
const (
	CIPending = "pending"
	CISuccess = "success"
	CIFailure = "failure"
)

// CommitCI fasst die Prüfungen eines Commits zu einem Stand zusammen: ein Check-Run
// oder Status, der scheitert, ergibt "failure"; läuft noch einer, "pending"; sind
// alle durch, "success". Hat der Commit keine Prüfung, ist der Stand leer. Das
// braucht "Checks: read" (und, wenn die App es hat, "Commit statuses: read"); fehlt
// "Checks", kommt ErrPermissionMissing; kennt GitHub den Commit nicht,
// ErrCommitUnavailable.
func (g *GitHubApp) CommitCI(ctx context.Context, installationID int64, repo, sha string) (string, error) {
	parts, ok := repoParts(repo)
	if !ok || !hexSHA(sha) {
		return "", fmt.Errorf("commit checks: repo must look like github.com/owner/repo and the sha must be hexadecimal")
	}
	withStatuses := true
	tok, err := g.installationToken(ctx, installationID, map[string]string{"checks": "read", "statuses": "read"})
	if err != nil {
		// Eine App ohne "Commit statuses" bekommt das Token nur für die Checks.
		withStatuses = false
		if tok, err = g.installationToken(ctx, installationID, map[string]string{"checks": "read"}); err != nil {
			return "", tokenRefusal(err)
		}
	}
	base := "/repos/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(parts[2]) + "/commits/" + sha
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := g.getAs(ctx, tok, base+"/check-runs?per_page=100", &runs); err != nil {
		return "", commitRefusal(err)
	}
	state := ""
	rank := map[string]int{"": 0, CISuccess: 1, CIPending: 2, CIFailure: 3}
	worse := func(s string) {
		if rank[s] > rank[state] {
			state = s
		}
	}
	for _, r := range runs.CheckRuns {
		switch {
		case r.Status != "completed":
			worse(CIPending)
		case r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped":
			worse(CISuccess)
		default:
			worse(CIFailure)
		}
	}
	if withStatuses {
		var st struct {
			State      string `json:"state"`
			TotalCount int    `json:"total_count"`
		}
		if err := g.getAs(ctx, tok, base+"/status", &st); err == nil && st.TotalCount > 0 {
			switch st.State {
			case "success":
				worse(CISuccess)
			case "pending":
				worse(CIPending)
			default:
				worse(CIFailure)
			}
		}
	}
	return state, nil
}

var linkLastRE = regexp.MustCompile(`<[^>]*[?&]page=(\d+)[^>]*>\s*;\s*rel="last"`)

// lastPage liest aus dem Link-Kopf von GitHub die Nummer der letzten Seite; 0, wenn
// es keine gibt.
func lastPage(link string) int {
	m := linkLastRE.FindStringSubmatch(link)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1000 {
		return 0
	}
	return n
}

func repoParts(repo string) ([]string, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(repo)), "/")
	return parts, len(parts) == 3 && parts[0] == "github.com" && parts[1] != "" && parts[2] != ""
}

func hexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// tokenRefusal macht aus der Ablehnung eines Installations-Tokens "Recht fehlt":
// verlangt die App ein Recht, das die Installation nicht hat, antwortet GitHub mit
// 422 (oder 403); ein 404 heißt, dass es die Installation nicht mehr gibt. Das gilt
// für alle Commits der Installation. Ein Ratenlimit und alles andere bleiben, wie
// sie sind.
func tokenRefusal(err error) error {
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusForbidden || se.code == http.StatusNotFound || se.code == http.StatusUnprocessableEntity) {
		return ErrPermissionMissing
	}
	return err
}

// commitRefusal deutet die Antwort auf die Abfrage eines Commits: ein 403 heißt,
// dass dem Token das Recht fehlt (gilt für die ganze Installation); ein 404 oder 422
// betrifft nur diesen Commit (etwa nach einem Force-Push verschwunden). Ein
// Ratenlimit und alles andere bleiben, wie sie sind.
func commitRefusal(err error) error {
	var se *statusError
	if errors.As(err, &se) {
		switch se.code {
		case http.StatusForbidden:
			return ErrPermissionMissing
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			return ErrCommitUnavailable
		}
	}
	return err
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
	key := tokenKey(installationID, permissions)
	if tok, ok := g.cachedToken(key); ok {
		return tok, nil
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
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := g.do(req, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("github gave no installation token")
	}
	g.storeToken(key, out.Token, out.ExpiresAt)
	return out.Token, nil
}

// tokenKey ist der Schlüssel eines Tokens im Speicher: Installation und die
// (sortierten) Rechte, auf die es begrenzt ist.
func tokenKey(installationID int64, permissions map[string]string) string {
	if permissions == nil {
		return strconv.FormatInt(installationID, 10) + "|*"
	}
	parts := make([]string, 0, len(permissions))
	for k, v := range permissions {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strconv.FormatInt(installationID, 10) + "|" + strings.Join(parts, ",")
}

func (g *GitHubApp) cachedToken(key string) (string, bool) {
	g.tokMu.Lock()
	defer g.tokMu.Unlock()
	t, ok := g.tokens[key]
	if !ok || !g.cfg.Now().Before(t.expires.Add(-tokenSlack)) {
		delete(g.tokens, key)
		return "", false
	}
	return t.token, true
}

// storeToken merkt sich ein Token bis zum Ablauf, den GitHub nennt. Ohne Ablauf
// (oder schon fast abgelaufen) bleibt es ungespeichert.
func (g *GitHubApp) storeToken(key, token string, expires time.Time) {
	if expires.IsZero() || !g.cfg.Now().Before(expires.Add(-tokenSlack)) {
		return
	}
	g.tokMu.Lock()
	defer g.tokMu.Unlock()
	if g.tokens == nil {
		g.tokens = map[string]cachedToken{}
	}
	g.tokens[key] = cachedToken{token: token, expires: expires}
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
	_, err := g.getHeader(ctx, token, path, v)
	return err
}

// getHeader ist getAs, das auch die Kopfzeilen der Antwort zurückgibt (für Link).
func (g *GitHubApp) getHeader(ctx context.Context, token, path string, v any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.APIBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return g.doHeader(req, v)
}

func (g *GitHubApp) do(req *http.Request, v any) error {
	_, err := g.doHeader(req, v)
	return err
}

func (g *GitHubApp) doHeader(req *http.Request, v any) (http.Header, error) {
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", redactErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseLen))
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		if rl, ok := rateLimited(resp, g.cfg.Now()); ok {
			return nil, rl
		}
		return nil, &statusError{code: resp.StatusCode}
	}
	return resp.Header, json.Unmarshal(body, v)
}
