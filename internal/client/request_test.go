package client

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRequestClientRoundtripAndStructuredError(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: token})

	detail, err := c.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "client", Description: "body"},
		Criteria: []string{"works"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchRequests(requestdomain.SearchFilter{Query: "client", Limit: 10})
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("page = %+v, err = %v", page, err)
	}
	err = c.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: "commit", Ref: "abc"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "open_criteria" || apiErr.Resolution == "" {
		t.Fatalf("error = %#v", err)
	}
	sessionID, _ := c.UpsertSession(store.Session{Harness: "codex", ExternalID: "client-work"})
	work, _, err := c.StartRequestWork(detail.Request.ID, sessionID, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRequestCriterion(detail.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FinishRequestWork(work.ID, "completed", "client flow verified"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: "commit", Ref: "abc"}); err != nil {
		t.Fatal(err)
	}
}
