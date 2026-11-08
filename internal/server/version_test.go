package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/version"
)

func versionServer(t *testing.T, opts ...Option) (*httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, _ := st.AddPerson("test")
	srv := httptest.NewServer(New(st, opts...))
	t.Cleanup(srv.Close)
	return srv, tok
}

func TestAPIVersionIsPublicAndDescribesTheServer(t *testing.T) {
	srv, _ := versionServer(t, WithBuildVersion("0.1.0-dev+b0cd1b9"), WithCapabilities("github_app"))
	resp := req(t, "GET", srv.URL+"/api/version", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200 without a token", resp.StatusCode)
	}
	var got version.Info
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "0.1.0-dev+b0cd1b9" || got.Commit != "b0cd1b9" || got.Channel != "dev" ||
		got.Protocol != version.Protocol || got.MinClient != version.MinClient {
		t.Fatalf("info = %+v", got)
	}
	for _, c := range []string{"anchor", "origin", "packages", "query", "labels", "presets", "ideas", "runbooks", "github_app"} {
		if !slices.Contains(got.Capabilities, c) {
			t.Errorf("capability %s missing in %v", c, got.Capabilities)
		}
	}
}

func TestAPIVersionNamesAReleaseAsStable(t *testing.T) {
	srv, _ := versionServer(t, WithBuildVersion("0.2.0"))
	var got version.Info
	json.NewDecoder(req(t, "GET", srv.URL+"/api/version", "", nil).Body).Decode(&got)
	if got.Channel != "stable" || got.Version != "0.2.0" {
		t.Fatalf("info = %+v", got)
	}
}

func TestHealthStaysCompatible(t *testing.T) {
	srv, _ := versionServer(t)
	b, _ := io.ReadAll(req(t, "GET", srv.URL+"/api/health", "", nil).Body)
	if strings.TrimSpace(string(b)) != `{"ok":true}` {
		t.Fatalf("health = %s", b)
	}
}

func bootstrapWith(t *testing.T, srv *httptest.Server, tok string, hdr map[string]string) string {
	t.Helper()
	rq, _ := http.NewRequest("GET", srv.URL+"/api/context/bootstrap", nil)
	rq.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestBriefingRemindsAnOlderClientToUpdate(t *testing.T) {
	srv, tok := versionServer(t, WithBuildVersion("0.1.0-dev+b0cd1b9"))
	out := bootstrapWith(t, srv, tok, map[string]string{"X-Ctx-Version": "0.1.0-dev+1c48c74", "X-Ctx-Protocol": "1"})
	if !strings.Contains(out, "ctx update") || !strings.Contains(out, "0.1.0-dev+b0cd1b9") {
		t.Fatalf("no update hint:\n%s", out)
	}
	for _, h := range []map[string]string{
		nil, // an older client sends no version: no hint, no failure
		{"X-Ctx-Version": "0.1.0-dev+b0cd1b9", "X-Ctx-Protocol": "1"},
	} {
		if out := bootstrapWith(t, srv, tok, h); strings.Contains(out, "ctx update") {
			t.Fatalf("hint for %v:\n%s", h, out)
		}
	}
}

func TestBriefingHintForAReleaseClientBehindTheServer(t *testing.T) {
	srv, tok := versionServer(t, WithBuildVersion("0.2.0"))
	if out := bootstrapWith(t, srv, tok, map[string]string{"X-Ctx-Version": "0.1.0", "X-Ctx-Protocol": "1"}); !strings.Contains(out, "ctx update") {
		t.Fatalf("no hint:\n%s", out)
	}
}
