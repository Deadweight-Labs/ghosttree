package client

import (
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestArchiveGhostClientRoundTripAndRetry(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, err := st.AddPerson("operator")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: token})
	if _, err := c.PutGhost(store.GhostFile{Project: "p", Path: "gone.go", Description: "retained words"}); err != nil {
		t.Fatal(err)
	}
	candidate, err := c.PrepareGhostArchive("p", "gone.go")
	if err != nil {
		t.Fatal(err)
	}
	in := store.GhostArchiveInput{Project: "p", Targets: []store.GhostArchiveTarget{candidate.Target}, Reason: "confirmed gone", ConfirmDeleted: true}
	out, err := c.ArchiveGhosts(in)
	if err != nil || len(out.Archived) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = c.ArchiveGhosts(in)
	if err != nil || len(out.AlreadyArchived) != 1 {
		t.Fatalf("retry: %+v %v", out, err)
	}
	chain, err := c.GhostChain("p", "gone.go", 0)
	if err != nil || len(chain) != 1 || chain[0].Description != "retained words" || chain[0].ReplacedAt == "" {
		t.Fatalf("archived chain: %+v %v", chain, err)
	}
}
