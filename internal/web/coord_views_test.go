package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestBuildCoordSidebarSeparatesRoomKindsAndKeepsAttention(t *testing.T) {
	summaries := []store.CoordRoomSummary{
		{Room: store.CoordRoom{Key: "group:g", Kind: store.RoomGroup, Label: "Release"}, Unread: 3, MentionUnread: 1, Attention: 2},
		{Room: store.CoordRoom{Key: "project:p", Kind: store.RoomProject}, Unread: 2},
		{Room: store.CoordRoom{Key: "machine:m", Kind: store.RoomMachine}},
		{Room: store.CoordRoom{Key: "direct:d", Kind: store.RoomDirect, Members: []string{"person:1", "sess-a"}}},
	}

	got := buildCoordSidebar(summaries, "person:1", "group:g")
	if len(got.NeedsAttention) != 1 || len(got.Mentions) != 1 || len(got.Unread) != 2 || len(got.Machines) != 1 || len(got.Projects) != 1 || len(got.Private) != 2 {
		t.Fatalf("unexpected sections: %+v", got)
	}
	if got.NeedsAttention[0].Attention != 2 || got.Mentions[0].Mentions != 1 {
		t.Fatalf("attention signals merged: %+v", got)
	}
	if !got.Private[0].Active || got.Private[0].Label != "Release" {
		t.Fatalf("active labelled group not projected: %+v", got.Private[0])
	}
	if got.Private[1].Label != "sess-a" {
		t.Fatalf("direct label should name the peer, got %q", got.Private[1].Label)
	}
}

func TestThreadAttentionURLCarriesServerCursorBeforeFragment(t *testing.T) {
	views, _ := buildCoordAttentionViews([]store.AttentionItem{{
		ID: 1, DestinationKind: store.DestinationDiscussion, DestinationID: "7",
		HomeRoomKey: "group:private room", Sequence: 3, State: store.AttentionOpen, IsRecipient: true,
	}})
	if len(views) != 1 {
		t.Fatalf("views=%+v", views)
	}
	parts := strings.SplitN(views[0].URL, "#", 2)
	parsed, err := url.Parse(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[1] != "thread-message-3" || parsed.Query().Get("thread") != "7" || parsed.Query().Get("thread_around") != "3" {
		t.Fatalf("thread attention URL=%q", views[0].URL)
	}
}

func TestAttentionViewsSeparateIncomingAndNameEveryOutgoingRecipient(t *testing.T) {
	incoming, outgoing := buildCoordAttentionViews([]store.AttentionItem{
		{ID: 1, State: store.AttentionOpen, RecipientID: "person:2", IsRecipient: true, Reason: store.AttentionQuestion},
		{ID: 2, State: store.AttentionOpen, RecipientID: "person:2", CanWithdraw: true, Reason: store.AttentionHandoff},
		{ID: 3, State: store.AttentionOpen, RecipientID: "person:3", CanWithdraw: true, Reason: store.AttentionHandoff},
		{ID: 4, State: store.AttentionResolved, RecipientID: "person:2", IsRecipient: true, Reason: store.AttentionQuestion},
	})
	if len(incoming) != 1 || len(outgoing) != 2 {
		t.Fatalf("incoming=%+v outgoing=%+v", incoming, outgoing)
	}
	if outgoing[0].RecipientID == outgoing[1].RecipientID {
		t.Fatalf("outgoing recipients collapsed: %+v", outgoing)
	}
}

func TestBuildCoordMessageViewsPreservesUntrustedText(t *testing.T) {
	got := buildCoordMessageViews([]store.CoordMessagePresentation{{
		Message: store.CoordMessage{ID: 9, Sequence: 4, SenderExternalID: "raw-agent", AuthorKind: store.AuthorAgent,
			Body: "<script>alert(1)</script>", CreatedAt: "2026-09-18T08:00:00Z"},
		AuthorLabel: "<agent>", Mentions: []string{"sess-b"},
	}}, "project:p")
	if len(got) != 1 || got[0].Body != "<script>alert(1)</script>" || got[0].Author != "<agent>" || len(got[0].Mentions) != 1 {
		t.Fatalf("presentation changed message content: %+v", got)
	}
}
