package mcpserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

var featureTools = []string{"feature_get", "feature_search", "feature_create", "feature_update", "feature_state", "feature_link", "feature_pr_paths", "feature_check"}

func TestFeatureToolsAreRegisteredWithSchemas(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		if slices.Contains(featureTools, tool.Name) {
			seen++
			if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil || tool.Description == "" {
				t.Errorf("%s lacks schema, annotations or description", tool.Name)
			}
		}
	}
	if seen != len(featureTools) {
		t.Fatalf("found %d of %d tools", seen, len(featureTools))
	}
}

// Anlegen, lesen, fortschreiben, suchen, verknüpfen, PR-Checkliste und der Kontext
// eines Bereichs über das Protokoll.
func TestFeatureToolsOverTheWire(t *testing.T) {
	c, _ := newTestClient(t)
	project := "github.com/x/y"
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})

	out, failed := callTool(t, session, "feature_create", map[string]any{"name": "Arbeitspakete", "summary": "Ein Branch trägt seine Requests.",
		"behavior": "Pakete binden Requests an Branches.", "limits": "Kein Abgleich mit GitHub.", "cli": "ctx package", "areas": []string{"internal/request", "cmd/ctx/package.go"}, "message": "erste Fassung"})
	if failed || !strings.Contains(out, "FEAT-1 Arbeitspakete [active, version 1 of 1]") || !strings.Contains(out, "Paths") || !strings.Contains(out, "/features/1") {
		t.Fatalf("feature_create: %v %s", failed, out)
	}
	if got, failed := callTool(t, session, "feature_create", map[string]any{"name": "arbeitspakete"}); !failed || !strings.Contains(got, "exists") {
		t.Errorf("the same name twice: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "feature_create", map[string]any{"name": "Böse", "areas": []string{"../x"}}); !failed {
		t.Errorf("a path outside the repository: %s", got)
	}
	got, failed := callTool(t, session, "feature_get", map[string]any{"id": 1})
	if failed || !strings.Contains(got, "How it behaves now\nPakete binden Requests an Branches.") || !strings.Contains(got, "CLI: ctx package") || !strings.Contains(got, "link: ") {
		t.Fatalf("feature_get: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "feature_get", map[string]any{"id": 99}); !failed || !strings.Contains(got, "no feature FEAT-99") {
		t.Errorf("a missing feature: %v %s", failed, got)
	}

	// Fortschreiben: nur was genannt wird ändert sich, Idee/Issue/PR stehen an der Fassung.
	idea, _ := callTool(t, session, "idea_record", map[string]any{"title": "Pakete pro PR"})
	if !strings.Contains(idea, "IDEA-1") {
		t.Fatalf("idea: %s", idea)
	}
	if got, failed := callTool(t, session, "feature_link", map[string]any{"id": 1, "kind": "idea", "ref": 1}); failed || !strings.Contains(got, "IDEA-1") || !strings.Contains(got, "description not carried forward yet") {
		t.Fatalf("feature_link: %v %s", failed, got)
	}
	// Die Idee zeigt das Ist des Features neben dem Soll.
	got, _ = callTool(t, session, "idea_get", map[string]any{"id": 1})
	if !strings.Contains(got, `"affects":[{"feature":"FEAT-1","name":"Arbeitspakete","version":1,"advanced":false`) || !strings.Contains(got, `"behavior_now":"Pakete binden Requests an Branches."`) {
		t.Fatalf("idea_get shows the feature it changes:\n%s", got)
	}
	got, failed = callTool(t, session, "feature_update", map[string]any{"id": 1, "behavior": "Pakete binden Requests an Branches und PRs.", "idea": 1, "pr": 9, "message": "PR dazu"})
	if failed || !strings.Contains(got, "FEAT-1 is at version 2") {
		t.Fatalf("feature_update: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "idea_get", map[string]any{"id": 1}); !strings.Contains(got, `"version":2,"advanced":true`) {
		t.Errorf("idea_get after the version: %s", got)
	}
	if got, _ := callTool(t, session, "feature_update", map[string]any{"id": 1, "behavior": "Pakete binden Requests an Branches und PRs."}); !strings.Contains(got, "Nothing changed") {
		t.Errorf("the same text again: %s", got)
	}
	if got, failed := callTool(t, session, "feature_update", map[string]any{"id": 1, "summary": "x", "base": 1}); !failed || !strings.Contains(got, "revision 2") {
		t.Errorf("a stale base: %v %s", failed, got)
	}
	got, _ = callTool(t, session, "feature_get", map[string]any{"id": 1, "versions": true})
	if !strings.Contains(got, "version 2") || !strings.Contains(got, "PR dazu") || !strings.Contains(got, "IDEA-1, PR #9") || !strings.Contains(got, "(carried forward)") {
		t.Fatalf("versions with what triggered each:\n%s", got)
	}
	if got, _ := callTool(t, session, "feature_get", map[string]any{"id": 1, "revision": 1}); !strings.Contains(got, "version 1 of 2") || strings.Contains(got, "und PRs") {
		t.Errorf("an older version: %s", got)
	}

	// Suche.
	if got, _ := callTool(t, session, "feature_search", map[string]any{"query": "branch"}); !strings.Contains(got, "FEAT-1 Arbeitspakete") {
		t.Errorf("feature_search: %s", got)
	}
	if got, _ := callTool(t, session, "feature_search", map[string]any{"path": "cmd/ctx/package.go"}); !strings.Contains(got, "FEAT-1") {
		t.Errorf("by path: %s", got)
	}
	if got, _ := callTool(t, session, "feature_search", map[string]any{"path": "internal/web/x.go"}); !strings.Contains(got, "No features match") {
		t.Errorf("another path: %s", got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "is:feature area:internal/request"}); !strings.Contains(got, "## features") || !strings.Contains(got, "FEAT-1") {
		t.Errorf("context_search is:feature: %s", got)
	}

	// PR-Pfade und Checkliste.
	got, failed = callTool(t, session, "feature_pr_paths", map[string]any{"pr": 10, "paths": []string{"internal/request/package.go", "../x"}})
	if failed || !strings.Contains(got, "Stored 1 paths for PR #10 (1 dropped") || !strings.Contains(got, "[ ] Feature FEAT-1 Arbeitspakete: Beschreibung angepasst?") {
		t.Fatalf("feature_pr_paths: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "feature_pr_paths", map[string]any{"pr": 10}); !strings.Contains(got, "[ ] Feature FEAT-1") {
		t.Errorf("reading the checklist: %s", got)
	}
	if got, failed := callTool(t, session, "feature_check", map[string]any{"id": 1, "pr": 10, "note": ""}); !failed || !strings.Contains(got, "note") {
		t.Errorf("a check needs a note: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "feature_check", map[string]any{"id": 1, "pr": 10, "note": "nur umbenannt"}); failed || !strings.Contains(got, "still holds for PR #10") {
		t.Fatalf("feature_check: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "feature_pr_paths", map[string]any{"pr": 10}); !strings.Contains(got, "[x] Feature FEAT-1") || !strings.Contains(got, "still right: nur umbenannt") {
		t.Errorf("checked item: %s", got)
	}
	if got, _ := callTool(t, session, "feature_pr_paths", map[string]any{"pr": 11}); !strings.Contains(got, "No feature description is touched by PR #11") {
		t.Errorf("an untouched PR: %s", got)
	}

	// context_get mit Pfaden: die Features des Bereichs kommen kurz dazu.
	got, _ = callTool(t, session, "context_get", map[string]any{"paths": []string{"internal/request/package.go"}})
	if !strings.Contains(got, "## Features in this area") || !strings.Contains(got, "FEAT-1 Arbeitspakete (version 2)") || !strings.Contains(got, "Limits: Kein Abgleich mit GitHub.") {
		t.Fatalf("context_get with paths:\n%s", got)
	}
	if got, _ := callTool(t, session, "context_get", map[string]any{"paths": []string{"internal/web/a.go"}}); strings.Contains(got, "Features in this area") {
		t.Errorf("another area: %s", got)
	}

	// Einstellen: der Token ist ein Mitglied mit allen Rechten dieses Projekts
	// (kein Rollenmodell ohne Durchsetzung), der Grund ist Pflicht.
	if got, failed := callTool(t, session, "feature_state", map[string]any{"id": 1, "state": "retired"}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("retire without a reason: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "feature_state", map[string]any{"id": 1, "state": "retired", "reason": "weg"}); failed || !strings.Contains(got, "now retired") {
		t.Fatalf("retire: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "context_get", map[string]any{"paths": []string{"internal/request/package.go"}}); strings.Contains(got, "Features in this area") {
		t.Errorf("a retired feature in the context: %s", got)
	}
	if got, _ := callTool(t, session, "feature_search", nil); !strings.Contains(got, "No features match") {
		t.Errorf("a retired feature in the list: %s", got)
	}
}
