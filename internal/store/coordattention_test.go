package store

import (
	"errors"
	"testing"
	"time"
)

func attentionGroupFixture(t *testing.T) (*Store, CoordAccess, CoordAccess, CoordAccess, string) {
	t.Helper()
	s := newCoordAccessStore(t)
	group, err := s.CreateCoordGroup(GroupInput{
		Label: "release", Creator: "person:1",
		Members: []string{"person:1", "person:2", "person:3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s,
		s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, ""),
		s.CoordinationFor(Principal{ID: "person:2", Label: "Alex"}, ""),
		s.CoordinationFor(Principal{ID: "person:3", Label: "Sam"}, ""),
		group.Key
}

func TestQuestionStaysOpenAfterReadAndResolvesOnlyByRecipientAction(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	messageID, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		ClientID: "question", Body: "Can we ship?", Intent: IntentQuestion,
		Mentions: []string{"person:2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
	if items[0].MessageID != messageID || items[0].Reason != AttentionQuestion || items[0].State != AttentionOpen {
		t.Fatalf("item=%+v", items[0])
	}
	if err := recipient.MarkRead(DestinationRoom, room, 1); err != nil {
		t.Fatal(err)
	}
	items, err = recipient.Attention()
	if err != nil || len(items) != 1 || items[0].State != AttentionOpen {
		t.Fatalf("read changed attention: %+v err=%v", items, err)
	}
	if err := recipient.ResolveAttention(items[0].ID, AttentionActionAnswer); err != nil {
		t.Fatal(err)
	}
	items, err = recipient.Attention()
	if err != nil || len(items) != 1 || items[0].State != AttentionResolved || items[0].ResolvedAt == "" {
		t.Fatalf("resolved attention=%+v err=%v", items, err)
	}
}

func TestAttentionActionsAreBoundToRecipientAndAuthor(t *testing.T) {
	_, author, recipient, outsider, room := attentionGroupFixture(t)
	_, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "blocker",
		Body: "Deployment is blocked", Intent: IntentBlocker, Mentions: []string{"person:2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
	id := items[0].ID
	if err := outsider.ResolveAttention(id, AttentionActionResolve); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("outsider action leaked item: %v", err)
	}
	if err := author.ResolveAttention(id, AttentionActionDismiss); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("author dismissed recipient item: %v", err)
	}
	if err := recipient.ResolveAttention(id, AttentionActionWithdraw); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("recipient withdrew author item: %v", err)
	}
	if err := author.ResolveAttention(id, AttentionActionWithdraw); err != nil {
		t.Fatalf("author could not withdraw: %v", err)
	}
}

func TestAttentionRequiresExplicitIntentAndAckIsNeverWakeCandidate(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	messages := []CoordMessage{
		{ClientID: "plain", Body: "Can you approve this?", Mentions: []string{"person:2"}},
		{ClientID: "ack", Body: "ack", Intent: IntentAck, Mentions: []string{"person:2"}},
		{ClientID: "ack-kind", Body: "ack", Kind: " ack ", Mentions: []string{"person:2"}},
	}
	for i, message := range messages {
		message.DestinationKind, message.DestinationID = DestinationRoom, room
		if _, err := author.Send(message); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	wake := func(m CoordMessage) bool {
		return ShouldWake("person:2", RoomDirect, m, m.Mentions, WakeParentNotOwn, time.Now())
	}
	if !wake(messages[0]) {
		t.Fatal("direct mention did not become a wake candidate")
	}
	if wake(messages[1]) {
		t.Fatal("ack became a wake candidate")
	}
	if wake(messages[2]) {
		t.Fatal("ack kind became a wake candidate")
	}
	// In a project room a mention decides, so an ack with a mention still wakes.
	if !ShouldWake("person:2", RoomProject, messages[1], messages[1].Mentions, WakeParentNotOwn, time.Now()) {
		t.Fatal("ack with a mention in a project room must still wake")
	}
	items, err := recipient.Attention()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("implicit/ack attention=%+v", items)
	}
}

func TestAttentionExpiryIsDerivedFromMessageExpiry(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	_, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "expired",
		Body: "Old approval", Intent: IntentApproval, Mentions: []string{"person:2"},
		ExpiresAt: "2000-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err := recipient.Store.DB().QueryRow(`SELECT id FROM coord_attention`).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := recipient.ResolveAttention(itemID, AttentionActionApprove); !errors.Is(err, ErrAttentionClosed) {
		t.Fatalf("resolve expired before read: want closed, got %v", err)
	}
	var stored string
	if err := recipient.Store.DB().QueryRow(`SELECT state FROM coord_attention WHERE id=?`, itemID).Scan(&stored); err != nil || stored != AttentionExpired {
		t.Fatalf("expired state was not made monotonic: state=%q err=%v", stored, err)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 || items[0].State != AttentionExpired {
		t.Fatalf("expired attention=%+v err=%v", items, err)
	}
}

func TestThreadMentionRollsUpWithoutRoomUnread(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	anchor, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		ClientID: "anchor", Body: "Investigate release",
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := author.PromoteRoomMessageToTaskThread(anchor, "Investigate", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := recipient.MarkRead(DestinationRoom, room, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := author.ThreadPost(threadID, CoordMessage{
		ClientID: "thread-question", Body: "What did you find?", Intent: IntentQuestion,
		Mentions: []string{"person:2"},
	}); err != nil {
		t.Fatal(err)
	}
	summaries, err := recipient.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Unread != 0 || summaries[0].MentionUnread != 1 || summaries[0].Attention != 1 {
		t.Fatalf("summary=%+v", summaries)
	}
	if err := recipient.MarkRead(DestinationDiscussion, ThreadDestinationID(threadID), 1); err != nil {
		t.Fatal(err)
	}
	summaries, err = recipient.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if summaries[0].Unread != 0 || summaries[0].MentionUnread != 0 || summaries[0].Attention != 1 {
		t.Fatalf("thread read should clear only mention rollup: %+v", summaries[0])
	}
}

func TestAttentionUsesDynamicThreadHomeACLAndPublicProjectionIsReadOnly(t *testing.T) {
	s, author, recipient, _, room := attentionGroupFixture(t)
	anchor, err := author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "anchor-private", Body: "Private task"})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := author.PromoteRoomMessageToTaskThread(anchor, "Private", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := author.ThreadPost(threadID, CoordMessage{ClientID: "handoff", Body: "Take this", Intent: IntentHandoff, Mentions: []string{"person:2"}}); err != nil {
		t.Fatal(err)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
	if err := s.LeaveCoordRoom(room, "person:2", "person:1"); err != nil {
		t.Fatal(err)
	}
	if got, err := recipient.Attention(); err != nil || len(got) != 0 {
		t.Fatalf("removed member attention=%+v err=%v", got, err)
	}
	if err := recipient.ResolveAttention(items[0].ID, AttentionActionAccept); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("removed member action leaked item: %v", err)
	}
	public := s.CoordinationPublicFor(Principal{ID: "person:public"})
	if _, err := public.Attention(); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("public attention read: %v", err)
	}
	if err := public.ResolveAttention(items[0].ID, AttentionActionResolve); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("public attention mutation: %v", err)
	}
}

func TestLegacyPrivateThreadMentionsUseThreadVisibilityNotProjectRoom(t *testing.T) {
	s := newCoordAccessStore(t)
	projectRoom := RoomKeyForProject("github.com/x/private")
	registerAccessAgent(t, s, "person:3", "sess-project", projectRoom)
	direct := RoomKeyForDirect([]string{"person:1", "person:2"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: direct, Kind: RoomDirect, Members: []string{"person:1", "person:2"}}); err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: direct, SenderExternalID: "person:1",
		AuthorPrincipalID: "person:1", AuthorKind: AuthorHuman, ClientID: "legacy-source", Body: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := s.PromoteMessagesToThread(direct, []int64{source}, Thread{Project: "github.com/x/private", Title: "legacy private"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	author := s.CoordinationFor(Principal{ID: "person:1"}, "")
	if _, err := author.ThreadPost(promoted.ThreadID, CoordMessage{
		ClientID: "private-mention", Body: "Please answer", Intent: IntentQuestion,
		Mentions: []string{"person:2"},
	}); err != nil {
		t.Fatalf("visible thread member rejected: %v", err)
	}
	if _, err := author.ThreadPost(promoted.ThreadID, CoordMessage{
		ClientID: "project-mention", Body: "Should stay private", Intent: IntentQuestion,
		Mentions: []string{"sess-project"},
	}); !errors.Is(err, ErrCoordUnknownRecipient) {
		t.Fatalf("project participant accepted in private thread: %v", err)
	}
}

func TestAttentionIntentRequiresTargetAndDirectMessageExpandsItsPeer(t *testing.T) {
	s, author, recipient, _, group := attentionGroupFixture(t)
	if _, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: group,
		ClientID: "untargeted", Body: "Who owns this?", Intent: IntentQuestion,
	}); !errors.Is(err, ErrAttentionRecipientRequired) {
		t.Fatalf("untargeted group attention: %v", err)
	}
	direct := RoomKeyForDirect([]string{"person:1", "person:2"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: direct, Kind: RoomDirect, Members: []string{"person:1", "person:2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := author.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: direct,
		ClientID: "direct-question", Body: "Can you answer?", Intent: IntentQuestion,
	}); err != nil {
		t.Fatal(err)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 || items[0].RecipientID != "person:2" {
		t.Fatalf("direct attention=%+v err=%v", items, err)
	}
}

func TestAttentionRetryKeepsOriginalRecipientsAndReason(t *testing.T) {
	_, author, recipient, outsider, room := attentionGroupFixture(t)
	original := CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "stable-retry",
		Body: "Please answer", Intent: IntentQuestion, Mentions: []string{"person:2"},
	}
	first, err := author.Send(original)
	if err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.Intent = IntentApproval
	retry.Mentions = []string{"person:3"}
	second, err := author.Send(retry)
	if err != nil || second != first {
		t.Fatalf("retry=(%d,%v), want original %d", second, err, first)
	}
	items, err := recipient.Attention()
	if err != nil || len(items) != 1 || items[0].Reason != AttentionQuestion {
		t.Fatalf("original attention=%+v err=%v", items, err)
	}
	if items, err := outsider.Attention(); err != nil || len(items) != 0 {
		t.Fatalf("retry added recipient: %+v err=%v", items, err)
	}
}
