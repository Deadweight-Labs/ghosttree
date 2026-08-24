package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestCanonicalizeScopesRewritesAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"github.com/Example/SampleProject", "github.com/example/sampleproject"} {
		if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "change", Title: "soak", Description: "seven days",
			Scope: scope.Axes{Project: project}}}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	var out bytes.Buffer
	if code := cmdCanonicalizeScopes([]string{"--db", path}, &out); code != 0 {
		t.Fatalf("code = %d, output = %s", code, out.String())
	}
	if !strings.Contains(out.String(), "backup written and verified") {
		t.Errorf("no verified backup reported: %s", out.String())
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.SearchRequests(requestdomain.SearchFilter{
		Scope: scope.Axes{Project: "github.com/example/sampleproject"}, State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("open requests = %d, want 1 after merge", len(page.Results))
	}
}

// An owner change is invisible to normalisation, so the mapping comes from a
// file the operator writes by hand.
func TestCanonicalizeScopesReadsAliasFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "serial console",
		Body: "b", Scope: scope.Axes{Project: "github.com/Deadweight-Labs/SampleProject"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	aliases := filepath.Join(dir, "aliases.json")
	if err := os.WriteFile(aliases, []byte(`{"github.com/deadweight-labs/sampleproject":"github.com/example/sampleproject"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdCanonicalizeScopes([]string{"--db", path, "--aliases", aliases}, &out); code != 0 {
		t.Fatalf("code = %d, output = %s", code, out.String())
	}

	s, _ = store.Open(path)
	defer s.Close()
	ks, _ := s.SearchKnowledge("console", scope.Axes{Project: "github.com/example/sampleproject"}, 10)
	if len(ks) != 1 {
		t.Errorf("aliased knowledge = %d, want 1", len(ks))
	}
}

// A dry run is what makes this safe to point at production first.
func TestCanonicalizeScopesDryRunLeavesDataUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "serial console",
		Body: "b", Scope: scope.Axes{Project: "github.com/Example/SampleProject"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	var out bytes.Buffer
	if code := cmdCanonicalizeScopes([]string{"--db", path, "--dry-run"}, &out); code != 0 {
		t.Fatalf("code = %d, output = %s", code, out.String())
	}
	s, _ = store.Open(path)
	defer s.Close()
	ks, _ := s.SearchKnowledge("console", scope.Axes{Project: "github.com/Example/SampleProject"}, 10)
	if len(ks) != 1 {
		t.Errorf("dry run modified the database: %d rows left on the old spelling, want 1", len(ks))
	}
}
