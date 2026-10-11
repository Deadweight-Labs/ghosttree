package client

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestClientRoundtrip(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)

	c := New(config.Config{ServerURL: srv.URL, Token: token, Machine: "workstation-a"})
	if err := c.Health(); err != nil {
		t.Fatal(err)
	}
	ctx := scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}
	saved, err := c.Remember(store.Knowledge{Type: "decision", Title: "sqlite", Body: "single writer ok"}, ctx)
	if err != nil || saved.Scope.Project != "github.com/x/y" {
		t.Fatalf("saved = %+v err = %v", saved, err)
	}
	ks, _ := c.Knowledge(ctx)
	if len(ks) != 1 {
		t.Errorf("knowledge = %d, want 1", len(ks))
	}
	bad := New(config.Config{ServerURL: srv.URL, Token: "wrong"})
	if err := bad.Health(); err != nil {
		t.Fatal("health is public, must not fail")
	}
	if _, err := bad.Knowledge(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: err = %v, want 401", err)
	}
}

func TestDocumentClientRoundTrip(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: token})

	d, err := c.CreateDocument(store.Document{
		Project: "github.com/x/y", Slug: "architecture", Kind: "spec", Title: "Architecture",
	}, "one\r\n\ttwo 🌳\n", "initial")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := c.Documents("github.com/x/y", "spec", false)
	if err != nil || len(listed) != 1 || listed[0].ID != d.ID {
		t.Fatalf("documents = %+v, err = %v", listed, err)
	}
	got, err := c.Document(d.ID)
	if err != nil || got.Slug != d.Slug {
		t.Fatalf("document = %+v, err = %v", got, err)
	}
	updated, err := c.PushDocumentRevision(d.ID, 1, "two\n", "second")
	if err != nil || updated.HeadRevision != 2 {
		t.Fatalf("push = %+v, err = %v", updated, err)
	}
	revs, err := c.DocumentRevisions(d.ID)
	if err != nil || len(revs) != 2 || revs[0].Body != "" {
		t.Fatalf("revisions = %+v, err = %v", revs, err)
	}
	rev, err := c.DocumentRevision(d.ID, 1)
	if err != nil || rev.Body != "one\r\n\ttwo 🌳\n" {
		t.Fatalf("revision = %+v, err = %v", rev, err)
	}
	patched, err := c.PatchDocument(d.ID, map[string]string{"slug": "design"})
	if err != nil || patched.Slug != "design" {
		t.Fatalf("patch = %+v, err = %v", patched, err)
	}
}

func TestDocumentClientDecodesRevisionConflict(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: token})

	d, err := c.CreateDocument(store.Document{
		Project: "p", Slug: "a", Kind: "spec", Title: "A",
	}, "one", "initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PushDocumentRevision(d.ID, 1, "two", "winner"); err != nil {
		t.Fatal(err)
	}
	_, err = c.PushDocumentRevision(d.ID, 1, "three", "stale")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T %v, want *ConflictError", err, err)
	}
	if conflict.HeadRevision != 2 || conflict.Person != "alice" || conflict.Message != "winner" || conflict.At == "" {
		t.Fatalf("conflict = %+v", conflict)
	}
}

func TestClientRawSession(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)

	c := New(config.Config{ServerURL: srv.URL, Token: token, Machine: "workstation-a"})
	id, err := c.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "r1", StartedAt: "2026-08-23T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AppendChunks(id, []store.Chunk{
		{Seq: 0, Role: "user", Text: "a", Raw: `{"n":0}`},
		{Seq: 1, Role: "assistant", Text: "b", Raw: `{"n":1}`},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := c.RawSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if raw != "{\"n\":0}\n{\"n\":1}\n" {
		t.Errorf("raw = %q", raw)
	}
}

func TestClientSessionsAndSearch(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: token, Machine: "workstation-a"})

	id, err := c.UpsertSession(store.Session{Harness: "codex", ExternalID: "s1",
		Scope: scope.Axes{Machine: "workstation-a"}, StartedAt: "2026-08-23T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "livekit sfu keeps dropping", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Search("livekit", "sessions", scope.Axes{}, 10)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("search = %+v err = %v", res, err)
	}
	sessions, err := c.Sessions(scope.Axes{Machine: "workstation-a"}, 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %+v err = %v", sessions, err)
	}
	chunks, err := c.ReadSession(id, 0, 10)
	if err != nil || len(chunks) != 1 || chunks[0].Text == "" {
		t.Fatalf("chunks = %+v err = %v", chunks, err)
	}
	md, err := c.Bootstrap(scope.Axes{Machine: "workstation-a"}, activation.Context{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if md != "" {
		t.Errorf("no knowledge yet, bootstrap should be empty: %q", md)
	}
}

// Die Kette geht ueber dieselbe Route wie die Historie und unterscheidet sich
// nur im Kopf. Genau dieser Kopf ist der Unterschied zwischen "was stand da
// mal" und "was hat sich geaendert" — er darf beim Durchreichen nicht
// verlorengehen.
func TestClientGhostChainCarriesTheCurrentVersion(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)

	c := New(config.Config{ServerURL: srv.URL, Token: token, Machine: "workstation-a"})
	g := store.GhostFile{Project: "p", Path: "a.go", Kind: "file", Description: "erste Fassung"}
	if _, err := c.PutGhost(g); err != nil {
		t.Fatal(err)
	}
	g.Description = "zweite Fassung"
	if _, err := c.PutGhost(g); err != nil {
		t.Fatal(err)
	}

	chain, err := c.GhostChain("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].Description != "zweite Fassung" || chain[1].Description != "erste Fassung" {
		t.Fatalf("Kette = %+v", chain)
	}
	hist, err := c.GhostHistory("p", "a.go", 0)
	if err != nil || len(hist) != 1 {
		t.Fatalf("die Historie bleibt ohne Kopf: %+v (%v)", hist, err)
	}
}
