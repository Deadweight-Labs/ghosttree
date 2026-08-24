package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A reviewer decides whether a claim is supported by its source. The listing
// showed the title and the source quote but never the claim itself, so the one
// judgement the review exists for could not be made from its output.
func TestPendingEntryShowsTheClaimItAsksAbout(t *testing.T) {
	var out bytes.Buffer
	writePendingEntry(&out, client.PendingEntry{
		Knowledge: store.Knowledge{
			ID: 7, Type: "pitfall", Confidence: "quarantined",
			Title: "Contains-tests miss broken grammar",
			Body:  "A contains assertion passes on the broken sentence too; forbid the concrete pattern instead.",
			Scope: scope.Axes{Project: "github.com/x/y"},
		},
		Evidence: []store.Evidence{{SessionID: 314, ChunkSeq: 110, Quote: "Kasusbruch im Repair-Template"}},
	})
	got := out.String()
	if !strings.Contains(got, "forbid the concrete pattern instead") {
		t.Fatalf("listing omits the claim under review:\n%s", got)
	}
	if !strings.Contains(got, "Kasusbruch im Repair-Template") {
		t.Fatalf("listing omits the evidence:\n%s", got)
	}
}

// Releasing a queue and judging one finding must not leave the same mark. On
// 2026-08-24 a batch of 136 entries was approved in one command and every one
// of them became `verified`, which ranks above `trusted` — so automatically
// distilled material outranked every hand-written entry in the archive.
func TestBulkApprovalRecordsTrustedNotVerified(t *testing.T) {
	var patches []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/knowledge/pending"):
			json.NewEncoder(w).Encode([]map[string]any{
				{"knowledge": map[string]any{"id": 7, "type": "pitfall", "title": "a", "confidence": "quarantined"}},
				{"knowledge": map[string]any{"id": 9, "type": "pitfall", "title": "b", "confidence": "quarantined"}},
			})
		case r.Method == "PATCH":
			var p map[string]string
			json.NewDecoder(r.Body).Decode(&p)
			patches = append(patches, p)
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	if code := cmdReview([]string{"approve", "--all"}, &out); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	if len(patches) != 2 {
		t.Fatalf("patched %d entries, want 2: %s", len(patches), out.String())
	}
	for _, p := range patches {
		if p["confidence"] != "trusted" {
			t.Errorf("bulk approval wrote %q, want trusted", p["confidence"])
		}
	}
}

// Naming an id is a read-and-judged statement and keeps the stronger tier.
func TestNamedApprovalStillRecordsVerified(t *testing.T) {
	var patches []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			var p map[string]string
			json.NewDecoder(r.Body).Decode(&p)
			patches = append(patches, p)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 1})
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	if code := cmdReview([]string{"approve", "42"}, &out); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	if len(patches) != 1 || patches[0]["confidence"] != "verified" {
		t.Errorf("named approval wrote %+v, want verified", patches)
	}
}

// Mixing the two is a contradiction, not a shorthand.
func TestAllRejectsExplicitIds(t *testing.T) {
	withConfig(t, "http://127.0.0.1:1")
	var out bytes.Buffer
	if code := cmdReview([]string{"approve", "--all", "42"}, &out); code == 0 {
		t.Errorf("--all with ids must fail, got success: %s", out.String())
	}
}

// withConfig points the client at a test server. XDG_CONFIG_HOME is isolated
// because a test that reads the real configuration once wrote the operator's
// live API key into a transcript.
func withConfig(t *testing.T, serverURL string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, ".config", "ghosttree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"server_url":"` + serverURL + `","token":"test-token","machine":"testbox"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
