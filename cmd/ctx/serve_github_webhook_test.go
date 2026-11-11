package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGitHubWebhookCreatesProjectsOfAConnectedOrg(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Deadweight", "deadweight")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, store.SourceGitHub, "deadweight-labs", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", org.ID, "github.com/deadweight-labs/*", store.VisibilityShared); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	files := map[string]string{
		"key.pem":    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		"webhook":    "whsec\n",
		"client-sec": "cs\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(forge.EnvAppID, "77")
	t.Setenv(forge.EnvAppSlug, "ghosttree-dw")
	t.Setenv(forge.EnvAppClientID, "Iv1.app")
	t.Setenv(forge.EnvAppClientSecretFile, filepath.Join(dir, "client-sec"))
	t.Setenv(forge.EnvAppPrivateKeyFile, filepath.Join(dir, "key.pem"))
	t.Setenv(forge.EnvAppWebhookSecretFile, filepath.Join(dir, "webhook"))

	srv := httptest.NewServer(buildServerHandler(st, serveConfig{SnapshotLimits: snapshot.DefaultLimits()}, io.Discard))
	t.Cleanup(srv.Close)
	post := func(secret, body string) int {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/github/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "repository")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/brand-new","owner":{"login":"Deadweight-Labs"}}}`
	if code := post("wrong", body); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/brand-new"); known {
		t.Fatal("a forged webhook created a project")
	}
	if code := post("whsec", body); code != http.StatusNoContent {
		t.Fatalf("good signature: %d", code)
	}
	p, known := st.ProjectByRemote("github.com/deadweight-labs/brand-new")
	if !known || p.OrgID != org.ID || !p.Shared {
		t.Fatalf("project after repository.created: %+v %v", p, known)
	}
}

func TestGitHubWebhookRouteIsAbsentWithoutTheApp(t *testing.T) {
	for _, k := range []string{forge.EnvAppID, forge.EnvAppSlug, forge.EnvAppClientID, forge.EnvAppClientSecret, forge.EnvAppClientSecretFile, forge.EnvAppPrivateKeyFile, forge.EnvAppWebhookSecretFile} {
		t.Setenv(k, "")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rr := httptest.NewRecorder()
	buildServerHandler(st, serveConfig{SnapshotLimits: snapshot.DefaultLimits()}, io.Discard).
		ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/github/webhook", strings.NewReader("{}")))
	if rr.Code == http.StatusNoContent {
		t.Fatal("without an app nothing may accept the webhook")
	}
}
