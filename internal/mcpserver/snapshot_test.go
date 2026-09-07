package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestContextSnapshotToolsWireSchemaAndBounds(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "p"}})
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"context_snapshot_create", "context_snapshot_get", "context_snapshot_list"}
	var got []string
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "context_snapshot_") {
			got = append(got, tool.Name)
			if tool.Name == "context_snapshot_create" || tool.Name == "context_snapshot_get" {
				raw, _ := json.Marshal(tool.InputSchema)
				var schema struct {
					Required []string `json:"required"`
				}
				if err := json.Unmarshal(raw, &schema); err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(schema.Required, "name") {
					t.Fatalf("%s does not require name: %s", tool.Name, raw)
				}
			}
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("snapshot tools = %v, want %v", got, want)
	}
	if _, failed := callTool(t, session, "context_snapshot_list", map[string]any{"limit": 101}); !failed {
		t.Fatal("list accepted limit above 100")
	}
	if _, failed := callTool(t, session, "context_snapshot_get", map[string]any{"name": "n", "key": "1"}); !failed {
		t.Fatal("get accepted key without domain")
	}
}

func TestContextSnapshotCreateAndBoundedGetOverWire(t *testing.T) {
	c, st := newTestClient(t)
	if err := st.SetContextSnapshotAccess("alice", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "kept", Body: "payload-marker", Scope: scope.Axes{Project: "p"}}); err != nil {
		t.Fatal(err)
	}
	repo := snapshotWireRepo(t)
	mirrorCalls := 0
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "p"}, repoRoot: repo, sessionRef: "wire:1", afterSnapshot: func(context.Context, string) error {
		mirrorCalls++
		return errors.New("mirror unavailable")
	}})

	created, failed := callTool(t, session, "context_snapshot_create", map[string]any{"name": "baseline", "message": "wire create"})
	if failed || !strings.Contains(created, `"created": true`) || !strings.Contains(created, "snapshot_mirror_degraded") || mirrorCalls != 1 {
		t.Fatalf("create failed=%v output=%s", failed, created)
	}
	head, failed := callTool(t, session, "context_snapshot_get", map[string]any{"name": "baseline"})
	if failed || strings.Contains(head, `"payload"`) || !strings.Contains(head, `"counts"`) {
		t.Fatalf("head failed=%v output=%s", failed, head)
	}
	summaries, failed := callTool(t, session, "context_snapshot_get", map[string]any{"name": "baseline", "domain": "knowledge", "limit": 100})
	if failed || strings.Contains(summaries, "payload-marker") || !strings.Contains(summaries, `"payload_size"`) {
		t.Fatalf("summaries failed=%v output=%s", failed, summaries)
	}
	exact, failed := callTool(t, session, "context_snapshot_get", map[string]any{"name": "baseline", "domain": "knowledge", "key": "1"})
	if failed || !strings.Contains(exact, "payload-marker") {
		t.Fatalf("exact failed=%v output=%s", failed, exact)
	}
}

func snapshotWireRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.invalid"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}
