package forge

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func appKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

// fakeApp ist ein GitHub, das die App-Endpunkte so beantwortet, wie die Doku sie beschreibt.
type fakeApp struct {
	t        *testing.T
	srv      *httptest.Server
	key      *rsa.PrivateKey
	mu       sync.Mutex
	secret   string
	tokenReq []map[string]any
	// userInstalls ist, was GitHub dem Nutzer als Installationen zeigt.
	userInstalls []map[string]any
	roles        map[string]string // Org -> Rolle des Nutzers
	repoCount    map[int64]int
	pulls        string
	pullsStatus  int
}

func newFakeApp(t *testing.T) *fakeApp {
	f := &fakeApp{t: t, key: appKey(t), roles: map[string]string{}, repoCount: map[int64]int{}, pullsStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.secret = r.PostForm.Get("client_secret")
		f.mu.Unlock()
		if r.PostForm.Get("code") != "good" || r.PostForm.Get("client_id") != "Iv1.app" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "utok"})
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer utok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.userInstalls), "installations": f.userInstalls})
	})
	mux.HandleFunc("GET /user/memberships/orgs/{org}", func(w http.ResponseWriter, r *http.Request) {
		role, ok := f.roles[r.PathValue("org")]
		if !ok || r.Header.Get("Authorization") != "Bearer utok" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "active", "role": role})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !f.verifyJWT(r.Header.Get("Authorization")) {
			http.Error(w, "bad jwt", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tokenReq = append(f.tokenReq, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "itok-" + r.PathValue("id")})
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		if _, err := fmt.Sscanf(r.Header.Get("Authorization"), "Bearer itok-%d", &id); err != nil {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		total := f.repoCount[id]
		var page int
		_, _ = fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		if page == 0 {
			page = 1
		}
		var repos []map[string]string
		for i := (page - 1) * 100; i < total && i < page*100; i++ {
			repos = append(repos, map[string]string{"full_name": fmt.Sprintf("Deadweight-Labs/repo-%03d", i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": total, "repositories": repos})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", func(w http.ResponseWriter, r *http.Request) {
		if f.pullsStatus != 200 {
			http.Error(w, "forbidden", f.pullsStatus)
			return
		}
		if r.URL.Query().Get("head") != "deadweight-labs:feat/x" || r.Header.Get("Authorization") != "Bearer itok-5" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(f.pulls))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// verifyJWT prüft RS256-Signatur, Aussteller und Laufzeit mit dem öffentlichen Schlüssel.
func (f *fakeApp) verifyJWT(auth string) bool {
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	var hdr map[string]string
	var claims struct {
		Iss, Iat, Exp int64
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(hb, &hdr) != nil || json.Unmarshal(cb, &claims) != nil {
		return false
	}
	return hdr["alg"] == "RS256" && claims.Iss == 77 && claims.Exp-claims.Iat <= 10*60 && claims.Exp > time.Now().Unix() && claims.Iat <= time.Now().Unix()
}

func (f *fakeApp) app() *GitHubApp {
	return NewGitHubApp(AppConfig{AppID: 77, Slug: "ghosttree-dw", PrivateKey: f.key, WebhookSecret: "whsec",
		ClientID: "Iv1.app", ClientSecret: "appsecret", WebBase: f.srv.URL, APIBase: f.srv.URL})
}

func install(id int64, login, typ string) map[string]any {
	return map[string]any{"id": id, "app_id": 77, "account": map[string]string{"login": login, "type": typ}}
}

func TestAppConfigFromEnvNeedsEverythingAndKeepsSecretsOutOfText(t *testing.T) {
	k := appKey(t)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	env := map[string]string{EnvAppID: "77", EnvAppSlug: "ghosttree-dw", EnvAppClientID: "Iv1.app",
		EnvAppPrivateKeyFile: "/run/key.pem", EnvAppClientSecretFile: "/run/cs", EnvAppWebhookSecretFile: "/run/wh"}
	files := map[string]string{"/run/key.pem": pemKey, "/run/cs": "cs3cret\n", "/run/wh": "wh\n"}
	get := func(k string) string { return env[k] }
	read := func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("no file")
	}
	app := NewGitHubApp(AppConfigFromEnv(get, read))
	if !app.Configured() || !app.WebhookConfigured() {
		t.Fatal("complete configuration")
	}
	if NewGitHubApp(AppConfigFromEnv(func(string) string { return "" }, read)).Configured() {
		t.Fatal("empty environment must read as not configured")
	}
	files["/run/key.pem"] = "not a key"
	if NewGitHubApp(AppConfigFromEnv(get, read)).Configured() {
		t.Fatal("an unreadable key is not configured")
	}
	files["/run/key.pem"] = pemKey
	delete(files, "/run/cs")
	if NewGitHubApp(AppConfigFromEnv(get, read)).Configured() {
		t.Fatal("without a client secret the user proof is impossible")
	}
	files["/run/cs"] = "x"
	delete(files, "/run/wh")
	a := NewGitHubApp(AppConfigFromEnv(get, read))
	if !a.Configured() || a.WebhookConfigured() {
		t.Fatal("the webhook secret is separate from the proof")
	}
	// PKCS#8 geht auch.
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	if _, err := ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatalf("pkcs8: %v", err)
	}
	if _, err := ParsePrivateKey([]byte("x")); err == nil || strings.Contains(err.Error(), "x\n") {
		t.Fatalf("garbage: %v", err)
	}
}

func TestPreferTakesTheAppWhenConfigured(t *testing.T) {
	f := newFakeApp(t)
	oauth := NewGitHub(Config{ClientID: "id", ClientSecret: "s"})
	if _, ok := Prefer(f.app(), oauth).(*GitHubApp); !ok {
		t.Fatal("a configured app takes precedence")
	}
	if _, ok := Prefer(NewGitHubApp(AppConfig{}), oauth).(*GitHub); !ok {
		t.Fatal("the OAuth app stays as the fallback")
	}
	if _, ok := Prefer(nil, oauth).(*GitHub); !ok {
		t.Fatal("no app at all")
	}
}

func TestAuthorizeURLIsTheUserAuthorizationAndInstallURLTheInstallationPage(t *testing.T) {
	f := newFakeApp(t)
	u := f.app().AuthorizeURL("st8", "http://x/cb")
	if !strings.HasPrefix(u, f.srv.URL+"/login/oauth/authorize?") || !strings.Contains(u, "state=st8") ||
		!strings.Contains(u, "client_id=") || !strings.Contains(u, "redirect_uri=http%3A%2F%2Fx%2Fcb") ||
		strings.Contains(u, "installations/new") || strings.Contains(u, "appsecret") || strings.Contains(u, "whsec") {
		t.Fatalf("authorize url: %s", u)
	}
	i := f.app().InstallURL("st8")
	if !strings.HasPrefix(i, f.srv.URL+"/apps/ghosttree-dw/installations/new?") || !strings.Contains(i, "state=st8") ||
		strings.Contains(i, "appsecret") || strings.Contains(i, "whsec") {
		t.Fatalf("install url: %s", i)
	}
}

func TestProveListsAllReposOfAnAdminOrgThroughTheInstallation(t *testing.T) {
	f := newFakeApp(t)
	f.userInstalls = []map[string]any{install(5, "Deadweight-Labs", "Organization"), install(6, "robin", "User"), install(8, "other-org", "Organization")}
	f.roles = map[string]string{"Deadweight-Labs": "admin", "other-org": "member"}
	f.repoCount[5] = 231 // drei Seiten: auch die privaten Repos, die read:org nie sah
	accts, err := f.app().Prove(context.Background(), "good", "http://x/cb")
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 1 || accts[0].Login != "deadweight-labs" || accts[0].InstallationID != 5 {
		t.Fatalf("only the admin org counts, not a user account or a member org: %+v", accts)
	}
	if len(accts[0].Repos) != 231 || accts[0].Repos[0] != "github.com/deadweight-labs/repo-000" || accts[0].Repos[230] != "github.com/deadweight-labs/repo-230" {
		t.Fatalf("repos (paginated): %d", len(accts[0].Repos))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secret != "appsecret" {
		t.Fatal("the client secret goes to the token endpoint")
	}
	// Das Installations-Token wird auf Metadata: read begrenzt angefordert.
	if len(f.tokenReq) == 0 {
		t.Fatal("no installation token requested")
	}
	perms, _ := f.tokenReq[0]["permissions"].(map[string]any)
	if len(perms) != 1 || perms["metadata"] != "read" {
		t.Fatalf("token permissions: %v", f.tokenReq[0])
	}
}

func TestProveRejectsNonAdminsAndForeignInstallations(t *testing.T) {
	f := newFakeApp(t)
	f.userInstalls = []map[string]any{install(5, "Deadweight-Labs", "Organization"), install(8, "other-org", "Organization")}
	f.roles = map[string]string{"Deadweight-Labs": "admin", "other-org": "member"}
	f.repoCount[5], f.repoCount[8] = 2, 2
	app := f.app()

	// Mitglied statt Admin: abgelehnt.
	if _, err := app.ProveInstallation(context.Background(), "good", "http://x/cb", 8); !errors.Is(err, ErrNotOrgAdmin) {
		t.Fatalf("member of the org: %v", err)
	}
	// Installation, die GitHub dem Nutzer nicht zeigt (aus der Adresse erfunden): abgelehnt.
	if _, err := app.ProveInstallation(context.Background(), "good", "http://x/cb", 999); !errors.Is(err, ErrInstallationNotYours) {
		t.Fatalf("foreign installation: %v", err)
	}
	// Eine persönliche Installation belegt keine Organisation.
	f.userInstalls = append(f.userInstalls, install(6, "robin", "User"))
	if _, err := app.ProveInstallation(context.Background(), "good", "http://x/cb", 6); !errors.Is(err, ErrNotOrgAdmin) {
		t.Fatalf("user account installation: %v", err)
	}
	// Admin einer Org, Installation passt: angenommen, nur diese.
	accts, err := app.ProveInstallation(context.Background(), "good", "http://x/cb", 5)
	if err != nil || len(accts) != 1 || accts[0].InstallationID != 5 {
		t.Fatalf("own installation: %+v %v", accts, err)
	}
	// Ohne Admin-Rolle irgendwo bleibt die Liste leer statt zu raten.
	f.roles = map[string]string{"Deadweight-Labs": "member"}
	if accts, err := app.Prove(context.Background(), "good", "http://x/cb"); err != nil || len(accts) != 0 {
		t.Fatalf("no admin role: %+v %v", accts, err)
	}
	// Falscher Code und fehlende Einrichtung.
	_, err = app.Prove(context.Background(), "forged", "http://x/cb")
	if err == nil || strings.Contains(err.Error(), "appsecret") || strings.Contains(err.Error(), "forged") {
		t.Fatalf("a refused code is an error without secrets: %v", err)
	}
	if _, err := NewGitHubApp(AppConfig{}).Prove(context.Background(), "good", "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
}

func TestAppJWTIsVerifiableAndShortLived(t *testing.T) {
	f := newFakeApp(t)
	jwt, err := f.app().appJWT()
	if err != nil {
		t.Fatal(err)
	}
	if !f.verifyJWT("Bearer " + jwt) {
		t.Fatal("the fake verifies RS256 signature, issuer and lifetime with the public key")
	}
	if f.verifyJWT("Bearer " + jwt + "x") {
		t.Fatal("a damaged signature must not verify")
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	wrong := NewGitHubApp(AppConfig{AppID: 77, Slug: "s", PrivateKey: other, ClientID: "i", ClientSecret: "s"})
	bad, _ := wrong.appJWT()
	if f.verifyJWT("Bearer " + bad) {
		t.Fatal("a token signed with another key must not verify")
	}
}

func TestPullRequestsForABranch(t *testing.T) {
	f := newFakeApp(t)
	f.pulls = `[
	 {"number":12,"title":"feat: x","state":"closed","draft":false,"html_url":"https://github.com/deadweight-labs/gt/pull/12","updated_at":"2026-11-07T10:00:00Z","merged_at":"2026-11-07T09:00:00Z","head":{"ref":"feat/x","sha":"abc"},"base":{"ref":"dev"}},
	 {"number":9,"title":"wip","state":"open","draft":true,"html_url":"u","updated_at":"2026-11-01T10:00:00Z","merged_at":null,"head":{"ref":"feat/x","sha":"def"},"base":{"ref":"main"}}]`
	prs, err := f.app().PullRequests(context.Background(), 5, "github.com/Deadweight-Labs/gt", "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0].Number != 12 || prs[0].State != "merged" || prs[0].HeadRef != "feat/x" || prs[0].BaseRef != "dev" || prs[0].HeadSHA != "abc" ||
		prs[1].State != "open" || !prs[1].Draft || prs[1].BaseRef != "main" {
		t.Fatalf("prs: %+v", prs)
	}
	// Ohne das Recht "Pull requests: read" ein klarer Fehler statt leerer Liste.
	f.pullsStatus = http.StatusForbidden
	if _, err := f.app().PullRequests(context.Background(), 5, "github.com/deadweight-labs/gt", "feat/x"); !errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("missing permission: %v", err)
	}
	for _, bad := range [][2]string{{"deadweight-labs/gt", "b"}, {"github.com/a/b/c", "b"}, {"github.com/a/b", " "}} {
		if _, err := f.app().PullRequests(context.Background(), 5, bad[0], bad[1]); err == nil {
			t.Fatalf("bad input %v must fail", bad)
		}
	}
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

type syncCall struct {
	inst        int64
	add, remove []string
}

func deliver(t *testing.T, h http.Handler, event, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/github/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sign(secret, []byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookChecksTheSignature(t *testing.T) {
	f := newFakeApp(t)
	var calls []syncCall
	h := f.app().WebhookHandler(func(i int64, a, r []string) error { calls = append(calls, syncCall{i, a, r}); return nil }, nil)
	body := `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/new","owner":{"login":"Deadweight-Labs"}}}`

	if rec := deliver(t, h, "repository", "whsec", body); rec.Code != http.StatusNoContent || len(calls) != 1 {
		t.Fatalf("good signature: %d %v", rec.Code, calls)
	}
	calls = nil
	if rec := deliver(t, h, "repository", "wrong", body); rec.Code != http.StatusUnauthorized || len(calls) != 0 || rec.Body.Len() > 20 {
		t.Fatalf("bad signature: %d %q %v", rec.Code, rec.Body.String(), calls)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "repository")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(calls) != 0 {
		t.Fatalf("no signature: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sign("whsec", []byte("other body")))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("signature of another body: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	// Ohne Webhook-Secret nimmt der Handler nichts an, auch nicht mit "passender" Signatur über das leere Secret.
	noSecret := NewGitHubApp(AppConfig{AppID: 77, Slug: "s", PrivateKey: f.key, ClientID: "i", ClientSecret: "s"})
	h2 := noSecret.WebhookHandler(func(int64, []string, []string) error { t.Fatal("must not run"); return nil }, nil)
	if rec := deliver(t, h2, "repository", "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no secret configured: %d", rec.Code)
	}
}

func TestWebhookTranslatesRepositoryEvents(t *testing.T) {
	f := newFakeApp(t)
	var calls []syncCall
	h := f.app().WebhookHandler(func(i int64, a, r []string) error { calls = append(calls, syncCall{i, a, r}); return nil }, nil)
	cases := []struct {
		event, body string
		add, remove []string
		inst        int64
	}{
		{"repository", `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/New","owner":{"login":"Deadweight-Labs"}}}`,
			[]string{"github.com/deadweight-labs/new"}, nil, 5},
		{"repository", `{"action":"deleted","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/old","owner":{"login":"Deadweight-Labs"}}}`,
			nil, []string{"github.com/deadweight-labs/old"}, 5},
		{"repository", `{"action":"renamed","installation":{"id":5},"changes":{"repository":{"name":{"from":"before"}}},"repository":{"full_name":"Deadweight-Labs/after","owner":{"login":"Deadweight-Labs"}}}`,
			[]string{"github.com/deadweight-labs/after"}, []string{"github.com/deadweight-labs/before"}, 5},
		{"installation_repositories", `{"action":"added","installation":{"id":6},"repositories_added":[{"full_name":"A/one"},{"full_name":"A/two"}],"repositories_removed":[]}`,
			[]string{"github.com/a/one", "github.com/a/two"}, nil, 6},
		{"installation_repositories", `{"action":"removed","installation":{"id":6},"repositories_removed":[{"full_name":"A/one"}]}`,
			nil, []string{"github.com/a/one"}, 6},
	}
	for _, c := range cases {
		calls = nil
		if rec := deliver(t, h, c.event, "whsec", c.body); rec.Code != http.StatusNoContent || len(calls) != 1 {
			t.Fatalf("%s: %d %v", c.event, rec.Code, calls)
		}
		got := calls[0]
		if got.inst != c.inst || strings.Join(got.add, ",") != strings.Join(c.add, ",") || strings.Join(got.remove, ",") != strings.Join(c.remove, ",") {
			t.Fatalf("%s: got %+v want add=%v remove=%v", c.event, got, c.add, c.remove)
		}
	}
	// Unbekanntes bleibt unberührt und wird mit 204 quittiert.
	calls = nil
	for _, c := range [][2]string{
		{"ping", `{"zen":"x"}`},
		{"push", `{"installation":{"id":5}}`},
		{"repository", `{"action":"publicized","installation":{"id":5},"repository":{"full_name":"A/b"}}`},
		{"repository", `{"action":"created","repository":{"full_name":"A/b"}}`}, // ohne Installation
		{"repository", `not json`},
	} {
		if rec := deliver(t, h, c[0], "whsec", c[1]); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", c[0], rec.Code)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("unknown events must change nothing: %v", calls)
	}
	// Ein Fehler beim Anwenden wird gemeldet (GitHub kann neu zustellen), ohne den Körper preiszugeben.
	var logged error
	bad := f.app().WebhookHandler(func(int64, []string, []string) error { return errors.New("db down") }, func(err error) { logged = err })
	rec := deliver(t, bad, "repository", "whsec", cases[0].body)
	if rec.Code != http.StatusInternalServerError || logged == nil || strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("apply failure: %d %v %q", rec.Code, logged, rec.Body.String())
	}
}
