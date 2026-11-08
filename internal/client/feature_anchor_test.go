package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

// Features anlegen und fortschreiben tragen den Git-Anker und die Session mit
// (REQ-597, REQ-600). Was keinen Anker speichert (Zustand, Verweise,
// Bestätigungen, PR-Pfade, Lesen) schickt ihn nicht: es löst keine git-Aufrufe aus.
func TestFeatureWritesCarryTheAnchorAndTheSession(t *testing.T) {
	type seen struct{ anchor, session string }
	got := map[string]seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got[r.Method+" "+r.URL.Path] = seen{r.Header.Get(gitanchor.Header), r.Header.Get(originctx.SessionHeader)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	c.SetAnchorSource(func() gitanchor.Anchor {
		return gitanchor.Anchor{Commit: "abcdef1234567890abcdef1234567890abcdef12", Branch: "feat/x"}
	})
	c.SetOriginSession("claude-session-1")
	name := "N"
	_, _ = c.CreateFeature(FeatureInput{Project: "github.com/x/y"})
	_, _ = c.UpdateFeature(7, FeatureInput{})
	_, _ = c.SetFeatureState(7, "active", "")
	_, _ = c.LinkFeature(7, "request", 1, "origin")
	_ = c.ConfirmFeatureUnchanged(7, 5, "ok")
	_, _ = c.GetFeature(7)
	_, _ = c.ReportPullPaths("github.com/x/y", 5, []string{"a.go"})
	_ = name
	for _, route := range []string{"POST /api/features", "POST /api/features/7/versions"} {
		s, ok := got[route]
		if !ok || s.anchor == "" || s.session != "claude-session-1" {
			t.Errorf("%s carries anchor %q session %q", route, s.anchor, s.session)
		}
	}
	for _, route := range []string{"GET /api/features/7", "POST /api/features/pr-paths", "POST /api/features/7/state", "POST /api/features/7/links", "POST /api/features/7/checks"} {
		if s := got[route]; s.anchor != "" || s.session != "" {
			t.Errorf("%s carries an anchor: %+v", route, s)
		}
	}
}
