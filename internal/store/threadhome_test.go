package store

import (
	"errors"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func taskThreadGroupFixture(t *testing.T) (*Store, CoordAccess, CoordAccess, string, int64) {
	t.Helper()
	s := newCoordAccessStore(t)
	room, err := s.CreateCoordGroup(GroupInput{
		Label: "release", Creator: "person:1", Members: []string{"person:1", "person:2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room.Key,
		SenderExternalID: "person:1", AuthorPrincipalID: "person:1", AuthorKind: AuthorHuman,
		ClientID: "anchor", Body: "Investigate the failed release",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s,
		s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, ""),
		s.CoordinationFor(Principal{ID: "person:2", Label: "Alex"}, ""),
		room.Key, messageID
}

func TestTaskThreadUsesDynamicHomeRoomACL(t *testing.T) {
	s, owner, member, room, anchor := taskThreadGroupFixture(t)
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Investigate", "Why did it fail?", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Thread(threadID); err != nil {
		t.Fatalf("active room member cannot read task thread: %v", err)
	}
	if err := s.LeaveCoordRoom(room, "person:2", "person:1"); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"thread": func() error { _, err := member.Thread(threadID); return err },
		"posts": func() error {
			_, err := member.Messages(DestinationDiscussion, ThreadDestinationID(threadID), 0, 10)
			return err
		},
		"room listing": func() error {
			_, err := member.RoomThreads(room)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrCoordNotFound) {
			t.Errorf("%s leaked after room removal: %v", name, err)
		}
	}
}

func TestAnchorCanOwnOnlyOneTaskThread(t *testing.T) {
	_, owner, _, _, anchor := taskThreadGroupFixture(t)
	first, err := owner.PromoteRoomMessageToTaskThread(anchor, "First", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.PromoteRoomMessageToTaskThread(anchor, "First", "", "")
	if err != nil || second != first {
		t.Fatalf("idempotent promotion = (%d,%v), want existing %d", second, err, first)
	}
	for name, payload := range map[string][2]string{
		"title": {"Different", ""}, "question": {"First", "Different"},
	} {
		if _, err := owner.PromoteRoomMessageToTaskThread(anchor, payload[0], payload[1], ""); !errors.Is(err, ErrAnchorAlreadyThreaded) {
			t.Errorf("different %s: want ErrAnchorAlreadyThreaded, got %v", name, err)
		}
	}
	request, err := owner.Store.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Different link"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PromoteRoomMessageToTaskThread(anchor, "First", "", request.Request.HumanID()); !errors.Is(err, ErrAnchorAlreadyThreaded) {
		t.Fatalf("different request: %v", err)
	}
	outsider := owner.Store.CoordinationFor(Principal{ID: "person:outside"}, "")
	if _, err := outsider.PromoteRoomMessageToTaskThread(anchor, "First", "", ""); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("unauthorized duplicate anchor leaked existence: %v", err)
	}
}

func TestTaskThreadMayHaveHomeWithoutAnchor(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "owner"}, "sess-a")
	threadID, err := access.CreateTaskThreadInRoom(room, "Room task", "No source message", "")
	if err != nil {
		t.Fatal(err)
	}
	home, err := access.ThreadHome(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if home.RoomKey != room || home.AnchorMessageID != 0 {
		t.Fatalf("home=%+v", home)
	}
	listed, err := access.RoomThreads(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].AnchorSequence != 0 {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestTaskThreadHomeAndRequestLinkAreCreatedAtomically(t *testing.T) {
	s, owner, _, room, anchor := taskThreadGroupFixture(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Ship release"}})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Release", "What remains?", detail.Request.HumanID())
	if err != nil {
		t.Fatal(err)
	}
	home, err := owner.ThreadHome(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if home.RoomKey != room || home.AnchorMessageID != anchor {
		t.Fatalf("home=%+v", home)
	}
	links, err := owner.ThreadLinks(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Kind != "request" || links[0].ID != detail.Request.HumanID() {
		t.Fatalf("links=%+v", links)
	}
	threads, err := owner.RoomThreads(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].Thread.ID != threadID || threads[0].Home.AnchorMessageID != anchor {
		t.Fatalf("room threads=%+v", threads)
	}
}

func TestClosingThreadDoesNotCloseLinkedRequest(t *testing.T) {
	s, owner, _, _, anchor := taskThreadGroupFixture(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Ship release"}})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Release", "", detail.Request.HumanID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.SetThreadState(threadID, ThreadResolved); err != nil {
		t.Fatal(err)
	}
	got, err := s.RequestByID(detail.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.State != "open" {
		t.Fatalf("request state=%s", got.Request.State)
	}
}

func TestHomeRoomManagerMayMutateTaskThread(t *testing.T) {
	s, owner, member, room, anchor := taskThreadGroupFixture(t)
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: room, Actor: "person:1", AddManagers: []string{"person:2"}}); err != nil {
		t.Fatal(err)
	}
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Release", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := member.SetThreadState(threadID, ThreadResolved); err != nil {
		t.Fatalf("active home-room manager cannot mutate thread: %v", err)
	}
	if err := s.LeaveCoordRoom(room, "person:2", "person:1"); err != nil {
		t.Fatal(err)
	}
	if err := member.SetThreadState(threadID, ThreadDeferred); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("removed manager retained mutation rights: %v", err)
	}
}

func TestTaskThreadDoesNotLeakThroughProjectSearch(t *testing.T) {
	s, owner, _, _, anchor := taskThreadGroupFixture(t)
	project := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:3", "sess-project", project)
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Secret release title", "private details", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE threads SET project='github.com/x/y' WHERE id=?`, threadID); err != nil {
		t.Fatal(err)
	}
	got, err := s.CoordinationFor(Principal{ID: "person:3"}, "sess-project").SearchThreads("github.com/x/y", "Secret", false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("private home thread leaked through search: %+v", got)
	}
}

func TestRequestLinkDoesNotBroadenTaskThreadACL(t *testing.T) {
	s, owner, _, _, anchor := taskThreadGroupFixture(t)
	project := "github.com/x/y"
	projectRoom := RoomKeyForProject(project)
	registerAccessAgent(t, s, "person:3", "sess-project", projectRoom)
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Public request", Scope: scope.Axes{Project: project}}})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Private task", "", detail.Request.HumanID())
	if err != nil {
		t.Fatal(err)
	}
	outsider := s.CoordinationFor(Principal{ID: "person:3"}, "sess-project")
	if _, err := outsider.Thread(threadID); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("request link broadened detail ACL: %v", err)
	}
	listed, err := outsider.ThreadsForObject("request", detail.Request.HumanID())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("request backlink leaked task thread: %+v", listed)
	}
}

func TestTaskThreadPublicProjectionAndLegacyHomeFallback(t *testing.T) {
	s := newCoordAccessStore(t)
	project := "github.com/x/y"
	room := RoomKeyForProject(project)
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	owner := s.CoordinationFor(Principal{ID: "person:1", Label: "owner"}, "sess-a")
	anchor, err := owner.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "public-anchor", Body: "public"})
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Public task", "", "")
	if err != nil {
		t.Fatal(err)
	}
	public := s.CoordinationPublicFor(Principal{ID: "projection"})
	if _, err := public.Thread(taskID); err != nil {
		t.Fatalf("public project home unreadable: %v", err)
	}
	if _, err := public.RoomThreads(room); err != nil {
		t.Fatalf("public project task list unreadable: %v", err)
	}
	if _, err := public.ThreadPost(taskID, CoordMessage{ClientID: "forged", Body: "write"}); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("public projection posted: %v", err)
	}

	legacyID, err := s.CreateThread(Thread{Project: project, Title: "Legacy standalone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Thread(legacyID); err != nil {
		t.Fatalf("standalone legacy thread stopped working: %v", err)
	}
	if _, err := owner.ThreadHome(legacyID); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("standalone thread acquired a fake home: %v", err)
	}
}

func TestPublicProjectionCannotSeePrivateHomeThread(t *testing.T) {
	s, owner, _, room, anchor := taskThreadGroupFixture(t)
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Private", "", "")
	if err != nil {
		t.Fatal(err)
	}
	public := s.CoordinationPublicFor(Principal{ID: "projection"})
	if _, err := public.Thread(threadID); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("private thread detail leaked: %v", err)
	}
	if _, err := public.RoomThreads(room); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("private thread list leaked: %v", err)
	}
}

func TestTaskThreadAllowsIdempotentRequestLinkButRejectsSecondRequest(t *testing.T) {
	s, owner, _, _, anchor := taskThreadGroupFixture(t)
	first, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "First"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Second"}})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := owner.PromoteRoomMessageToTaskThread(anchor, "Task", "", first.Request.HumanID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.LinkThread(ThreadLink{ThreadID: threadID, Kind: "request", ID: first.Request.HumanID()}); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if err := owner.LinkThread(ThreadLink{ThreadID: threadID, Kind: "request", ID: second.Request.HumanID()}); err == nil {
		t.Fatal("second request link was accepted")
	}
	_, owner2, _, _, anchor2 := taskThreadGroupFixture(t)
	threadID2, err := owner2.PromoteRoomMessageToTaskThread(anchor2, "Unlinked", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner2.LinkThread(ThreadLink{ThreadID: threadID2, Kind: "request", ID: "REQ-999999"}); err == nil {
		t.Fatal("unknown request link was accepted")
	}
}
