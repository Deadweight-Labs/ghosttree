package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestSendResolvesAnAtNameInTheBody(t *testing.T) {
	a, _, _ := twoSessions(t)
	res, _, err := a.handleCoordSend(context.Background(), nil, CoordSendInput{Body: "@sess-codex please look at the diff"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text(t, res), "mentions: sess-codex") {
		t.Fatalf("the sender should see whom the name reached: %s", text(t, res))
	}
	plain, _, err := a.handleCoordSend(context.Background(), nil, CoordSendInput{Body: "no name here"})
	if err != nil || strings.Contains(text(t, plain), "mentions:") {
		t.Fatalf("a plain message must not claim mentions: %v %s", err, text(t, plain))
	}
}

func TestSendRefusesAnAmbiguousAtNameWithCandidates(t *testing.T) {
	a, _, _ := twoSessions(t)
	registerThirdSession(t, a)
	_, _, err := a.handleCoordSend(context.Background(), nil, CoordSendInput{Body: "@sess take this"})
	if err == nil || !strings.Contains(err.Error(), "fits") || !strings.Contains(err.Error(), "sess-codex") {
		t.Fatalf("want an ambiguity hint naming candidates, got %v", err)
	}
}

func TestThreadReplyResolvesAnAtName(t *testing.T) {
	a, _, _ := twoSessions(t)
	ctx := context.Background()
	if _, _, err := a.handleThreadOpen(ctx, nil, ThreadOpenInput{Title: "Is the cursor sealed?"}); err != nil {
		t.Fatal(err)
	}
	res, _, err := a.handleThreadReply(ctx, nil, ThreadReplyInput{ID: 1, Body: "@sess-codex what do you think"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text(t, res), "mentions: sess-codex") {
		t.Fatalf("thread reply should resolve the name: %s", text(t, res))
	}
}

func TestPeersRefusalReadsAsAnExplanationNotARawHTTPError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		err := peersError(&client.StatusError{Method: "GET", Path: "/api/coord/agents", Status: status, Body: `{"error":"coordination target not found"}`})
		if err == nil || strings.Contains(err.Error(), "/api/") || !strings.Contains(err.Error(), "guests of a project") {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	raw := &client.StatusError{Method: "GET", Path: "/api/coord/agents", Status: 500, Body: "boom"}
	if peersError(raw) != raw {
		t.Fatal("a server error must stay what it is")
	}
}

func TestGuestPeersRefusalGoesThroughTheTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/coord/agents") && r.Method == http.MethodGet {
			http.Error(w, `{"error":"coordination target not found"}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	s := &Server{client: client.New(config.Config{ServerURL: srv.URL, Token: "t"}), sessionRef: "g",
		ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	_, _, err := s.handleCoordPeers(context.Background(), nil, CoordPeersInput{})
	if err == nil || strings.Contains(err.Error(), "/api/") {
		t.Fatalf("raw error leaked: %v", err)
	}
}

func TestInboxMarksWhatIsAddressedToYou(t *testing.T) {
	a, b, _ := twoSessions(t)
	registerThirdSession(t, a)
	ctx := context.Background()
	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{Body: "room chatter"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{Body: "@sess-codex a word", Mention: b.sessionRef, Intent: "question"}); err != nil {
		t.Fatal(err)
	}
	res, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	lines := strings.Split(got, "\n")
	var chatter, ask string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "room chatter"):
			chatter = l
		case strings.Contains(l, "a word"):
			ask = l
		}
	}
	if strings.Contains(chatter, "[to you") || !strings.Contains(ask, "[to you: mentions you, question]") {
		t.Fatalf("marking wrong:\nchatter=%q\nask=%q", chatter, ask)
	}
}
