package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestCoordDisplayTimestampUsesLocalTimeAndKeepsInvalidValues(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = previous })

	if got := coordDisplayTimestamp("2026-09-18T08:07:00Z"); got != "18.09.2026, 10:07" {
		t.Fatalf("display timestamp=%q", got)
	}
	if got := coordDisplayTimestamp("not-a-timestamp"); got != "not-a-timestamp" {
		t.Fatalf("invalid timestamp changed to %q", got)
	}
}

func TestBuildCoordSidebarSeparatesRoomKindsAndUnifiesAttentionSignals(t *testing.T) {
	summaries := []store.CoordRoomSummary{
		{Room: store.CoordRoom{Key: "group:g", Kind: store.RoomGroup, Label: "Release"}, Unread: 3, MentionUnread: 1, Attention: 2},
		{Room: store.CoordRoom{Key: "project:p", Kind: store.RoomProject}, Unread: 2},
		{Room: store.CoordRoom{Key: "machine:m", Kind: store.RoomMachine}},
		{Room: store.CoordRoom{Key: "direct:d", Kind: store.RoomDirect, Members: []string{"person:1", "sess-a"}}},
	}

	labels := map[string]string{"person:1": "Robin", "sess-a": "Build Agent"}
	got := buildCoordSidebar(summaries, "person:1", "group:g", labels)
	if len(got.Attention) != 2 || len(got.Machines) != 1 || len(got.Projects) != 1 || len(got.Private) != 2 {
		t.Fatalf("unexpected sections: %+v", got)
	}
	if got.Attention[0].Key != "group:g" || got.Attention[0].Attention != 2 || got.Attention[0].Mentions != 1 || got.Attention[0].Unread != 3 {
		t.Fatalf("room signals were not preserved in one attention row: %+v", got.Attention)
	}
	if !got.Private[0].Active || got.Private[0].Label != "Release" {
		t.Fatalf("active labelled group not projected: %+v", got.Private[0])
	}
	if got.Private[1].Label != "Build Agent" {
		t.Fatalf("direct label should name the peer, got %q", got.Private[1].Label)
	}
}

func TestBuildCoordMessageViewsMarksConsecutiveAuthorGroups(t *testing.T) {
	messages := []store.CoordMessagePresentation{
		{Message: store.CoordMessage{ID: 1, Sequence: 1, SenderExternalID: "human-a", AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, Body: "one"}, AuthorLabel: "Robin"},
		{Message: store.CoordMessage{ID: 2, Sequence: 2, SenderExternalID: "human-a", AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, Body: "two"}, AuthorLabel: "Robin"},
		{Message: store.CoordMessage{ID: 3, Sequence: 3, SenderExternalID: "human-b", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman, Body: "same label, different person"}, AuthorLabel: "Robin"},
		{Message: store.CoordMessage{ID: 4, Sequence: 4, SenderExternalID: "agent-a", AuthorKind: store.AuthorAgent, Body: "three"}, AuthorLabel: "Build Agent"},
		{Message: store.CoordMessage{ID: 5, Sequence: 5, SenderExternalID: "agent-a", AuthorKind: store.AuthorAgent, Body: "reply"}, AuthorLabel: "Build Agent", Reply: &store.CoordReplyPreview{Sequence: 1}},
	}
	got := buildCoordMessageViews(messages, "project:p", nil)
	if len(got) != 5 || !got[0].GroupStart || got[1].GroupStart || !got[2].GroupStart || !got[3].GroupStart || !got[4].GroupStart {
		t.Fatalf("room message grouping=%+v", got)
	}
	thread := buildCoordThreadMessageViews(messages, "project:p", 7, nil)
	if len(thread) != 5 || !thread[0].GroupStart || thread[1].GroupStart || !thread[2].GroupStart || !thread[3].GroupStart || !thread[4].GroupStart {
		t.Fatalf("thread message grouping=%+v", thread)
	}
}

func TestThreadAttentionURLCarriesServerCursorBeforeFragment(t *testing.T) {
	views, _ := buildCoordAttentionViews([]store.AttentionItem{{
		ID: 1, DestinationKind: store.DestinationDiscussion, DestinationID: "7",
		HomeRoomKey: "group:private room", Sequence: 3, State: store.AttentionOpen, IsRecipient: true,
	}}, "")
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
	}, "", map[string]string{"person:2": "Alex", "person:3": "Sam"})
	if len(incoming) != 1 || len(outgoing) != 2 {
		t.Fatalf("incoming=%+v outgoing=%+v", incoming, outgoing)
	}
	if outgoing[0].RecipientID == outgoing[1].RecipientID {
		t.Fatalf("outgoing recipients collapsed: %+v", outgoing)
	}
	if outgoing[0].RecipientID != "person:2" || outgoing[1].RecipientID != "person:3" {
		t.Fatalf("outgoing recipient IDs were replaced by labels: %+v", outgoing)
	}
	if outgoing[0].RecipientLabel != "Alex" || outgoing[1].RecipientLabel != "Sam" {
		t.Fatalf("outgoing recipient labels=%+v", outgoing)
	}
}

func TestBuildCoordMessageViewsPreservesUntrustedText(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = previous })

	got := buildCoordMessageViews([]store.CoordMessagePresentation{{
		Message: store.CoordMessage{ID: 9, Sequence: 4, SenderExternalID: "raw-agent", AuthorKind: store.AuthorAgent,
			Body: "<script>alert(1)</script>", CreatedAt: "2026-09-18T08:00:00Z"},
		AuthorLabel: "<agent>", Mentions: []string{"sess-b"},
	}}, "project:p", map[string]string{"sess-b": "Review Agent"})
	if len(got) != 1 || got[0].Body != "<script>alert(1)</script>" || got[0].Author != "<agent>" || len(got[0].Mentions) != 1 || got[0].Mentions[0] != "Review Agent" {
		t.Fatalf("presentation changed message content: %+v", got)
	}
	if got[0].Timestamp != "2026-09-18T08:00:00Z" || got[0].DisplayTimestamp != "18.09.2026, 10:00" {
		t.Fatalf("message timestamps=%+v", got[0])
	}
}

func TestBuildCoordParticipantsUsesCanonicalLabelsAndHonestPresence(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = previous })

	current := store.Principal{ID: "person:1", Label: "Robin"}
	room := store.CoordRoom{Key: "direct:d", Kind: store.RoomDirect, Members: []string{"person:1", "person:2"}}
	peers := []store.CoordAgent{{
		ExternalID: "sess-peer-secret", DisplayName: "Build Agent", Provider: "codex",
		Worktree: "/worktrees/ui", Branch: "feat/ui", LastSeenAt: "2026-09-18T10:00:00Z",
	}}
	got := buildCoordParticipants(room, peers, nil, current, map[string]string{"person:2": "Alex"})
	if len(got) != 3 {
		t.Fatalf("participants=%+v", got)
	}
	if got[0].Label != "Robin" || !got[0].Current {
		t.Fatalf("current participant=%+v", got[0])
	}
	if got[1].Label != "Alex" {
		t.Fatalf("person label=%+v", got[1])
	}
	if got[2].Label != "Build Agent" || got[2].Provider != "codex" || got[2].Worktree != "/worktrees/ui" || got[2].Branch != "feat/ui" || got[2].LastSeen != "2026-09-18T10:00:00Z" {
		t.Fatalf("agent presentation=%+v", got[2])
	}
	if got[2].Reachability != "unbekannt" || got[2].WorkState != "unbekannt" {
		t.Fatalf("presence must remain explicitly unknown: %+v", got[2])
	}
	if got[2].DisplayTimestamp != "18.09.2026, 12:00" {
		t.Fatalf("participant display timestamp=%q", got[2].DisplayTimestamp)
	}
}

func TestBuildCoordStandingViewsSeparatesRawAndDisplayTimestamp(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = previous })

	got := buildCoordStandingViews([]store.StandingInstruction{{
		MessageID: "9", Person: "Robin", Body: "Release stoppen", CreatedAt: "2026-09-18T08:30:00Z",
	}}, nil)
	if len(got) != 1 || got[0].CreatedAt != "2026-09-18T08:30:00Z" || got[0].DisplayTimestamp != "18.09.2026, 10:30" {
		t.Fatalf("standing timestamps=%+v", got)
	}
}

func TestUnknownPrincipalLabelsAreNotRenderedAsIdentifiers(t *testing.T) {
	room := store.CoordRoom{Key: "direct:d", Kind: store.RoomDirect, Members: []string{"person:1", "person:secret"}}
	if got := coordRoomLabel(room, "person:1", nil); strings.Contains(got, "person:secret") {
		t.Fatalf("raw principal leaked through room label: %q", got)
	}
	participants := buildCoordParticipants(room, nil, nil, store.Principal{ID: "person:1", Label: "Robin"}, nil)
	for _, participant := range participants {
		if strings.Contains(participant.Label, "person:") {
			t.Fatalf("raw principal leaked through participant: %+v", participant)
		}
	}
}
