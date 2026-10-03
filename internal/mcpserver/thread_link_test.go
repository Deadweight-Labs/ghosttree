package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestARefusedLinkLeavesNoThreadBehind(t *testing.T) {
	a, _, st := twoSessions(t)
	ctx := context.Background()
	foreign, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "elsewhere", Scope: scope.Axes{Project: "github.com/other/repo"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"9999", fmt.Sprint(foreign.Request.ID), "REQ-x"} {
		if _, _, err := a.handleThreadOpen(ctx, nil, ThreadOpenInput{Title: "Q " + id, LinkKind: "request", LinkID: id}); err == nil {
			t.Fatalf("link %q: want an error", id)
		}
	}
	res, _, err := a.handleThreadFind(ctx, nil, ThreadFindInput{Query: "Q"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "no threads match") {
		t.Fatalf("refused opens left threads behind: %s", got)
	}
}

func TestFindSeesBothLinkForms(t *testing.T) {
	a, _, st := twoSessions(t)
	ctx := context.Background()
	created, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "work", Scope: scope.Axes{Project: "github.com/deadweight-labs/ghosttree"}}})
	if err != nil {
		t.Fatal(err)
	}
	n := created.Request.ID
	if _, _, err := a.handleThreadOpen(ctx, nil, ThreadOpenInput{Title: "canonical one", LinkKind: "request", LinkID: fmt.Sprint(n)}); err != nil {
		t.Fatal(err)
	}
	legacy, err := a.client.CreateThread(storeThread("legacy bare"), a.coordRef())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO thread_links(thread_id,object_kind,object_id,created_at) VALUES(?,?,?,'t0')`,
		legacy, "request", fmt.Sprint(n)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fmt.Sprint(n), fmt.Sprintf("REQ-%d", n)} {
		res, _, err := a.handleThreadFind(ctx, nil, ThreadFindInput{ObjectKind: "request", ObjectID: id})
		if err != nil {
			t.Fatal(err)
		}
		got := text(t, res)
		if !strings.Contains(got, "canonical one") || !strings.Contains(got, "legacy bare") {
			t.Fatalf("find by %q misses a form: %s", id, got)
		}
	}
}

func storeThread(title string) store.Thread {
	return store.Thread{Project: "github.com/deadweight-labs/ghosttree", Title: title}
}
