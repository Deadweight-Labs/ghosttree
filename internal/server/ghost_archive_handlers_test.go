package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGhostArchiveHTTPRequiresExplicitSelectionAndAuthenticatedActor(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, err := st.AddPerson("operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(store.GhostFile{Project: "p", Path: "gone.go", Description: "original"}); err != nil {
		t.Fatal(err)
	}
	candidate, _ := st.PrepareGhostArchive("p", "gone.go")
	srv := httptest.NewServer(New(st))
	defer srv.Close()
	body := map[string]any{"project": "p", "targets": []store.GhostArchiveTarget{{Path: "gone.go", ExpectedToken: candidate.Target.ExpectedToken}}, "reason": "deleted", "person": "forged"}
	resp := req(t, "POST", srv.URL+"/api/ghosts/archive", token, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing confirmation: %d", resp.StatusCode)
	}
	body["confirm_deleted"] = true
	resp = req(t, "POST", srv.URL+"/api/ghosts/archive", "wrong", body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", resp.StatusCode)
	}
	resp = req(t, "POST", srv.URL+"/api/ghosts/archive", token, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d", resp.StatusCode)
	}
	hist, _ := st.GhostFileHistory("p", "gone.go", 0)
	if len(hist) != 1 || !strings.Contains(hist[0].Reason, "operator") || strings.Contains(hist[0].Reason, "forged") {
		t.Fatalf("archive actor: %+v", hist)
	}
	body["targets"] = []store.GhostArchiveTarget{{Path: "absent.go", ExpectedToken: candidate.Target.ExpectedToken}}
	resp = req(t, "POST", srv.URL+"/api/ghosts/archive", token, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale selection: %d", resp.StatusCode)
	}
}
