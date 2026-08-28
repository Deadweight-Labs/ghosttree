package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestDocNewCreatesOnlyLocalDraft(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if code := docNew(root, []string{"spec", "example"}, &out); code != 0 {
		t.Fatalf("docNew = %d: %s", code, out.String())
	}
	state, err := docwork.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := state["example"]
	if !ok || entry.DocumentID != 0 || entry.BaseRevision != 0 {
		t.Fatalf("draft state = %+v", state)
	}
	if _, err := os.Stat(filepath.Join(docwork.Dir(root), entry.Path)); err != nil {
		t.Fatalf("draft file: %v", err)
	}
}

func TestDocHasChangedUsesBaseDigest(t *testing.T) {
	root := t.TempDir()
	entry := docwork.Entry{Path: "specs/2026-08-26-x.md", BaseDigest: store.Digest("unchanged\n")}
	if err := docwork.WriteFile(root, entry.Path, "unchanged\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := docHasChanged(root, entry)
	if err != nil || changed {
		t.Fatalf("unchanged = %v, %v", changed, err)
	}
	if err := docwork.WriteFile(root, entry.Path, "changed\n"); err != nil {
		t.Fatal(err)
	}
	changed, err = docHasChanged(root, entry)
	if err != nil || !changed {
		t.Fatalf("changed = %v, %v", changed, err)
	}
}

func TestDocPushPullRoundTripPreservesBytes(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	root := t.TempDir()
	body := "# Design\r\n\ttab\tä ö ü 🌳\r\n"
	if code := docNew(root, []string{"spec", "design"}, &bytes.Buffer{}); code != 0 {
		t.Fatal("new failed")
	}
	state, _ := docwork.LoadState(root)
	entry := state["design"]
	if err := docwork.WriteFile(root, entry.Path, body); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := docPush(root, "github.com/x/y", c, "design", "initial", false, &out); code != 0 {
		t.Fatalf("push = %d: %s", code, out.String())
	}
	state, _ = docwork.LoadState(root)
	entry = state["design"]
	if err := os.Remove(filepath.Join(docwork.Dir(root), entry.Path)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := docPull(root, "github.com/x/y", c, "design", false, &out); code != 0 {
		t.Fatalf("pull = %d: %s", code, out.String())
	}
	got, err := docwork.ReadFile(root, state["design"].Path)
	if err != nil || got != body {
		t.Fatalf("round trip = %q, %v; want %q", got, err, body)
	}
}

func TestDocPullRefusesDirtyWorktreeWithoutForce(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	d, err := c.CreateDocument(store.Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "remote\n", "initial")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel, _ := docwork.RelPath(d.Kind, d.CreatedAt, d.Slug)
	docwork.WriteFile(root, rel, "local\n")
	docwork.SaveState(root, docwork.State{"a": {
		DocumentID: d.ID, BaseRevision: 1, BaseDigest: store.Digest("remote\n"), Path: rel,
	}})
	var out bytes.Buffer
	if code := docPull(root, "p", c, "a", false, &out); code == 0 || !strings.Contains(out.String(), "modified") {
		t.Fatalf("pull output = %q, code = %d", out.String(), code)
	}
}

func TestDocPullRefusesInvalidUTF8WithoutForce(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	d, err := c.CreateDocument(store.Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "remote\n", "initial")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel, _ := docwork.RelPath(d.Kind, d.CreatedAt, d.Slug)
	if err := docwork.WriteFile(root, rel, "local\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docwork.Dir(root), rel), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	docwork.SaveState(root, docwork.State{"a": {
		DocumentID: d.ID, BaseRevision: 1, BaseDigest: store.Digest("remote\n"), Path: rel,
	}})
	var out bytes.Buffer
	if code := docPull(root, "p", c, "a", false, &out); code == 0 || !strings.Contains(out.String(), "refusing to overwrite") {
		t.Fatalf("pull output = %q, code = %d", out.String(), code)
	}
	raw, err := os.ReadFile(filepath.Join(docwork.Dir(root), rel))
	if err != nil || len(raw) != 2 || raw[0] != 0xff {
		t.Fatalf("invalid local bytes changed: %x, %v", raw, err)
	}
}

func TestDocumentedPushAndShowArgumentOrder(t *testing.T) {
	slug, message, clean, ok := parseDocPushArgs([]string{"design", "-m", "publish", "--clean"})
	if !ok || slug != "design" || message != "publish" || !clean {
		t.Fatalf("push args = %q, %q, %v, %v", slug, message, clean, ok)
	}
	slug, revision, ok := parseDocShowArgs([]string{"design", "--rev", "3"})
	if !ok || slug != "design" || revision != 3 {
		t.Fatalf("show args = %q, %d, %v", slug, revision, ok)
	}
}

func TestDocImportRecordsProofBeforeClean(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	root := t.TempDir()
	source := filepath.Join(root, "existing-design.md")
	body := "# Existing design\r\n\ttab 🌳\n"
	if err := os.WriteFile(source, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := docImport(root, "p", c, []string{source, "--kind", "spec", "--slug", "existing-design", "--clean"}, &out); code != 0 {
		t.Fatalf("import = %d: %s", code, out.String())
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists or stat failed unexpectedly: %v", err)
	}
	document, err := findDocument(c, "p", "existing-design")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := c.DocumentRevision(document.ID, 1)
	if err != nil || revision.Body != body || revision.Digest != store.Digest(body) {
		t.Fatalf("revision = %+v, %v", revision, err)
	}
	proof, err := c.CompletedDocumentArtifacts("p")
	if err != nil || len(proof["existing-design.md"]) != 1 || proof["existing-design.md"][0] != store.Digest(body) {
		t.Fatalf("proof = %+v, %v", proof, err)
	}
}
