package web

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestBuildCoordSidebarSeparatesRoomKindsAndKeepsAttention(t *testing.T) {
	summaries := []store.CoordRoomSummary{
		{Room: store.CoordRoom{Key: "group:g", Kind: store.RoomGroup, Label: "Release"}, Unread: 3, MentionUnread: 1},
		{Room: store.CoordRoom{Key: "project:p", Kind: store.RoomProject}, Unread: 2},
		{Room: store.CoordRoom{Key: "machine:m", Kind: store.RoomMachine}},
		{Room: store.CoordRoom{Key: "direct:d", Kind: store.RoomDirect, Members: []string{"person:1", "sess-a"}}},
	}

	got := buildCoordSidebar(summaries, "person:1", "group:g")
	if len(got.Attention) != 1 || len(got.Machines) != 1 || len(got.Projects) != 1 || len(got.Private) != 2 {
		t.Fatalf("unexpected sections: %+v", got)
	}
	if !got.Private[0].Active || got.Private[0].Label != "Release" {
		t.Fatalf("active labelled group not projected: %+v", got.Private[0])
	}
	if got.Private[1].Label != "sess-a" {
		t.Fatalf("direct label should name the peer, got %q", got.Private[1].Label)
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
