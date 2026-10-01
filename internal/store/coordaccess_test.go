package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newCoordAccessStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func registerAccessAgent(t *testing.T, s *Store, principal, agent, room string) {
	t.Helper()
	if _, err := s.RegisterCoordAgent(CoordAgent{
		ExternalID: agent, PrincipalID: principal, Person: principal,
		Provider: "test", DisplayName: agent, RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordAccessRequiresAnOwnedRegisteredAgent(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)

	for _, agent := range []string{"sess-missing", "sess-owner"} {
		principal := Principal{ID: "person:2", Label: "other"}
		if agent == "sess-missing" {
			principal = Principal{ID: "person:1", Label: "owner"}
		}
		_, err := s.CoordinationFor(principal, agent).Messages(DestinationRoom, room, 0, 10)
		if !errors.Is(err, ErrCoordForbidden) {
			t.Fatalf("agent %q: want forbidden, got %v", agent, err)
		}
	}
}

func TestCoordAccessPublicRoomNeedsMaterializationAndAgentMembership(t *testing.T) {
	s := newCoordAccessStore(t)
	missing := RoomKeyForProject("github.com/x/missing")
	if _, err := s.CoordinationFor(Principal{ID: "person:1"}, "").Room(missing); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("unmaterialized prefix: want not found, got %v", err)
	}

	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-member", room)
	registerAccessAgent(t, s, "person:2", "sess-outsider", RoomKeyForProject("github.com/x/z"))
	if _, err := s.CoordinationFor(Principal{ID: "person:2"}, "sess-outsider").Room(room); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("materialized public room outside agent scope: want forbidden, got %v", err)
	}
	if _, err := s.CoordinationFor(Principal{ID: "person:1"}, "").Room(room); err != nil {
		t.Fatalf("authenticated browser principal should read materialized public room: %v", err)
	}
}

func TestPrivateThreadIsInvisibleThroughEveryCoordAccessSurface(t *testing.T) {
	s := newCoordAccessStore(t)
	project := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", project)
	registerAccessAgent(t, s, "person:1", "sess-b", project)
	registerAccessAgent(t, s, "person:2", "sess-other", project)
	direct := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: direct, Kind: RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	messageID, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: direct,
		SenderExternalID: "sess-a", ClientID: "source", Body: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := s.PromoteMessagesToThread(direct, []int64{messageID}, Thread{Project: "github.com/x/y", Title: "secret thread"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	other := s.CoordinationFor(Principal{ID: "person:2"}, "sess-other")
	destination := ThreadDestinationID(promoted.ThreadID)
	for name, call := range map[string]func() error{
		"thread":       func() error { _, err := other.Thread(promoted.ThreadID); return err },
		"messages":     func() error { _, err := other.Messages(DestinationDiscussion, destination, 0, 10); return err },
		"cursor read":  func() error { _, err := other.Cursor(DestinationDiscussion, destination); return err },
		"cursor write": func() error { return other.SetCursor(DestinationDiscussion, destination, 4) },
	} {
		if err := call(); !errors.Is(err, ErrCoordNotFound) {
			t.Errorf("%s leaked private target: %v", name, err)
		}
	}
}

func TestCoordAccessPrivateRoomHidesPeersAndRoomMetadata(t *testing.T) {
	s := newCoordAccessStore(t)
	project := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", project)
	registerAccessAgent(t, s, "person:1", "sess-b", project)
	registerAccessAgent(t, s, "person:2", "sess-other", project)
	direct := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: direct, Kind: RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	other := s.CoordinationFor(Principal{ID: "person:2"}, "sess-other")
	if _, err := other.Room(direct); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("room metadata: want not found, got %v", err)
	}
	if _, err := other.Peers(direct, ""); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("peer metadata: want not found, got %v", err)
	}
}

func TestCoordAccessRejectsReplyOutsideDestination(t *testing.T) {
	s := newCoordAccessStore(t)
	a := RoomKeyForProject("github.com/x/a")
	b := RoomKeyForProject("github.com/x/b")
	registerAccessAgent(t, s, "person:1", "sess-a", a)
	registerAccessAgent(t, s, "person:1", "sess-b", b)
	id, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: a, SenderExternalID: "sess-a", ClientID: "a-1", Body: "a"})
	if err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-b")
	_, err = access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: b, ClientID: "b-1", Body: "reply", ReplyTo: id})
	if !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("cross-destination reply: want not found, got %v", err)
	}
}

func TestThreadMutationUsesStableAuthorPrincipalAcrossRename(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	registerAccessAgent(t, s, "person:2", "sess-other", room)
	owner := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "sess-owner")
	id, err := owner.CreateThread(Thread{Project: "github.com/x/y", Title: "stable owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CoordinationFor(Principal{ID: "person:1", Label: "Renamed"}, "sess-owner").SetThreadState(id, ThreadResolved); err != nil {
		t.Fatalf("rename broke stable ownership: %v", err)
	}
	if err := s.CoordinationFor(Principal{ID: "person:2", Label: "Robin"}, "sess-other").SetThreadState(id, ThreadDeferred); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("same label gained mutation rights: %v", err)
	}
}

func TestLegacyThreadWithoutStableOwnerIsReadAndPostOnly(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	id, err := s.CreateThread(Thread{Project: "github.com/x/y", Title: "legacy", Person: "old label"})
	if err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "old label"}, "sess-a")
	if _, err := access.Thread(id); err != nil {
		t.Fatalf("legacy thread must remain readable: %v", err)
	}
	if _, err := access.ThreadPost(id, CoordMessage{ClientID: "legacy-post", Body: "still discussable"}); err != nil {
		t.Fatalf("legacy thread must remain postable: %v", err)
	}
	if err := access.SetThreadState(id, ThreadResolved); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("creatorless legacy mutation: want forbidden, got %v", err)
	}
}

func TestThreadObjectListingFiltersPublicThreadsOutsideAgentScope(t *testing.T) {
	s := newCoordAccessStore(t)
	registerAccessAgent(t, s, "person:1", "sess-a", RoomKeyForProject("github.com/x/a"))
	id, err := s.CreateThread(Thread{Project: "github.com/x/b", Title: "other project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkThread(ThreadLink{ThreadID: id, Kind: "knowledge", ID: "shared"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a").ThreadsForObject("knowledge", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-project thread leaked through object listing: %+v", got)
	}
}

func TestBrowserPrincipalOnlySeesPublicRoomsItOwnsThroughMembership(t *testing.T) {
	s := newCoordAccessStore(t)
	a := RoomKeyForProject("github.com/x/a")
	b := RoomKeyForProject("github.com/x/b")
	registerAccessAgent(t, s, "person:1", "sess-a", a)
	registerAccessAgent(t, s, "person:2", "sess-b", b)

	access := s.CoordinationFor(Principal{ID: "person:1"}, "")
	if _, err := access.Room(a); err != nil {
		t.Fatalf("owner lost public room: %v", err)
	}
	if _, err := access.Room(b); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("other person's public room: want forbidden, got %v", err)
	}
	rooms, err := access.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].Key != a {
		t.Fatalf("browser room scope = %+v", rooms)
	}
}

func TestPublicProjectionFailsClosedForEveryMutation(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	id, err := s.CreateThread(Thread{Project: "github.com/x/y", Title: "public"})
	if err != nil {
		t.Fatal(err)
	}
	public := s.CoordinationPublicFor(Principal{ID: "person:1"})
	checks := map[string]func() error{
		"send": func() error {
			_, err := public.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "x", Body: "x"})
			return err
		},
		"cursor":   func() error { return public.SetCursor(DestinationDiscussion, ThreadDestinationID(id), 1) },
		"delivery": func() error { return public.MarkDelivery(1, DeliveryFetched) },
		"claim": func() error {
			_, err := public.ClaimDelivery(1)
			return err
		},
		"group": func() error {
			_, err := public.CreateGroup(GroupInput{Members: []string{"sess-a", "sess-b"}})
			return err
		},
		"direct": func() error {
			return public.EnsureDirect(CoordRoom{Kind: RoomDirect, Members: []string{"sess-a", "sess-b"}})
		},
		"thread": func() error {
			_, err := public.CreateThread(Thread{Project: "github.com/x/y", Title: "x"})
			return err
		},
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrCoordForbidden) {
			t.Errorf("%s mutation: want forbidden, got %v", name, err)
		}
	}
}

func TestRuntimePublicProjectionCallbacksRemainReadOnly(t *testing.T) {
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "coord.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	messageID, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "existing", Body: "existing",
	})
	if err != nil {
		t.Fatal(err)
	}
	public := s.CoordinationPublicFor(Principal{ID: "person:1"})
	if _, err := public.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "forbidden", Body: "forbidden"}); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("runtime public send = %v", err)
	}
	if err := public.SetCursor(DestinationRoom, room, messageID); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("runtime public cursor = %v", err)
	}
	if err := public.MarkDelivery(messageID, DeliveryFetched); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("runtime public delivery = %v", err)
	}
	var messages, cursors, deliveries int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM coord_messages WHERE client_id='forbidden'`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM coord_cursors`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM coord_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || cursors != 0 || deliveries != 0 {
		t.Fatalf("public callback wrote messages=%d cursors=%d deliveries=%d", messages, cursors, deliveries)
	}
}

func TestBrowserThreadAccessRequiresOwnedProjectRoom(t *testing.T) {
	s := newCoordAccessStore(t)
	ownedProject := "github.com/x/owned"
	otherProject := "github.com/x/other"
	registerAccessAgent(t, s, "person:1", "sess-owned", RoomKeyForProject(ownedProject))
	registerAccessAgent(t, s, "person:2", "sess-other", RoomKeyForProject(otherProject))
	otherID, err := s.CreateThread(Thread{Project: otherProject, Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	browser := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "")
	if _, err := browser.Thread(otherID); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("browser read outside room = %v", err)
	}
	if threads, err := browser.SearchThreads(otherProject, "", false, 20); !errors.Is(err, ErrCoordForbidden) || threads != nil {
		t.Fatalf("browser search outside room = %+v, %v", threads, err)
	}
	if _, err := browser.CreateThread(Thread{Project: otherProject, Title: "cross-project"}); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("browser create outside room = %v", err)
	}
	if _, err := s.CoordinationPublicFor(Principal{ID: "person:1"}).Thread(otherID); err != nil {
		t.Fatalf("explicit public projection lost public thread: %v", err)
	}
}

func TestPromoteRequiresMatchingPublicProjectRoom(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "sess-a")
	messageID, err := access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "source-project", Body: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.PromoteMessagesToThread(room, []int64{messageID}, Thread{Project: "github.com/x/other", Title: "relabel"}); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("cross-project promotion = %v", err)
	}
	machine := RoomKeyForMachine("host")
	registerAccessAgent(t, s, "person:1", "sess-machine", machine)
	machineAccess := s.CoordinationFor(Principal{ID: "person:1"}, "sess-machine")
	machineMessageID, err := machineAccess.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: machine, ClientID: "source-machine", Body: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machineAccess.PromoteMessagesToThread(machine, []int64{machineMessageID}, Thread{Project: "github.com/x/y", Title: "machine"}); !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("machine promotion = %v", err)
	}
}

func TestLegacyThreadOwnerBackfillsOnlyFromUniquePersonName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateThread(Thread{Project: "p", Title: "legacy", Person: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	thread, err := s.ThreadByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.AuthorPrincipalID != "person:1" {
		t.Fatalf("unique legacy owner = %q", thread.AuthorPrincipalID)
	}
}

func TestAmbiguousLegacyThreadOwnerStaysUnowned(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE persons(id INTEGER PRIMARY KEY,name TEXT NOT NULL);
		CREATE TABLE threads(id INTEGER PRIMARY KEY,person TEXT NOT NULL,author_principal_id TEXT NOT NULL DEFAULT '');
		INSERT INTO persons(id,name) VALUES(1,'same'),(2,'same');
		INSERT INTO threads(id,person) VALUES(1,'same')`); err != nil {
		t.Fatal(err)
	}
	if err := ensureThreadAuthorPrincipalID(db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRow(`SELECT author_principal_id FROM threads WHERE id=1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "" {
		t.Fatalf("ambiguous legacy owner was claimed by %q", owner)
	}
}

func TestPromotedAndSplitThreadsCarryStableOwner(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "sess-a")
	messageID, err := access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "source", Body: "source"})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := access.PromoteMessagesToThread(room, []int64{messageID}, Thread{
		Project: "github.com/x/y", Title: "promoted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := access.SetThreadState(promoted.ThreadID, ThreadResolved); err != nil {
		t.Fatalf("promoted owner could not mutate: %v", err)
	}

	sourceID, err := access.CreateThread(Thread{Project: "github.com/x/y", Title: "source thread"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.ThreadPost(sourceID, CoordMessage{ClientID: "post", Body: "split me"}); err != nil {
		t.Fatal(err)
	}
	split, err := s.SplitThread(sourceID, []int64{1}, Thread{Project: "github.com/x/y", Title: "split"}, "Robin")
	if err != nil {
		t.Fatal(err)
	}
	if err := access.SetThreadState(split.ThreadID, ThreadResolved); err != nil {
		t.Fatalf("split owner was not inherited: %v", err)
	}
}

func TestCoordAccessMutationsUseRuntimeWriterAdmission(t *testing.T) {
	cfg := DefaultWriterConfig()
	cfg.MaxOperations = 1
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "coord.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	release := holdRuntimeWriter(t, s.writer, 0)
	defer release()
	checks := map[string]func() error{
		"send": func() error {
			_, err := access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "queued", Body: "x"})
			return err
		},
		"cursor":   func() error { return access.SetCursor(DestinationRoom, room, 1) },
		"delivery": func() error { return access.MarkDelivery(1, DeliveryFetched) },
		"create thread": func() error {
			_, err := access.CreateThread(Thread{Project: "github.com/x/y", Title: "queued"})
			return err
		},
		"promote": func() error {
			_, err := access.PromoteMessagesToThread(room, []int64{1}, Thread{Project: "github.com/x/y", Title: "queued"})
			return err
		},
		"thread state":   func() error { return access.SetThreadState(1, ThreadResolved) },
		"thread archive": func() error { return access.SetThreadArchived(1, true) },
		"thread touch":   func() error { return access.TouchThread(1) },
		"thread link":    func() error { return access.LinkThread(ThreadLink{ThreadID: 1, Kind: "knowledge", ID: "x"}) },
		"thread summary": func() error { _, err := access.PutThreadSummary(ThreadSummary{ThreadID: 1, Body: "x"}); return err },
		"thread outcome": func() error { return access.PutThreadOutcome(ThreadOutcome{ThreadID: 1, Note: "x"}) },
		"group": func() error {
			_, err := access.CreateGroup(GroupInput{Members: []string{"sess-a", "sess-b"}})
			return err
		},
		"direct": func() error {
			return access.EnsureDirect(CoordRoom{Kind: RoomDirect, Members: []string{"sess-a", "sess-b"}})
		},
		"group update": func() error { return access.UpdateGroup(GroupUpdate{RoomKey: "group:x", Actor: "sess-a"}) },
		"leave":        func() error { return access.LeaveRoom(room, "sess-a") },
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrWriterOperationsFull) {
			t.Errorf("%s bypassed writer admission: %v", name, err)
		}
	}
}

func TestQueuedCreateAndPromoteRecheckAccessAfterRevocation(t *testing.T) {
	for _, operation := range []string{"create", "promote"} {
		t.Run(operation, func(t *testing.T) {
			s, err := OpenRuntime(filepath.Join(t.TempDir(), "coord.db"), DefaultWriterConfig())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			room := RoomKeyForProject("github.com/x/y")
			registerAccessAgent(t, s, "person:1", "sess-a", room)
			access := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "sess-a")
			messageID, err := access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "before-revoke", Body: "source"})
			if err != nil {
				t.Fatal(err)
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			revoked, err := s.writer.admit(t.Context(), 0, func() error {
				return s.direct().LeaveCoordRoom(room, "sess-a", "sess-a")
			})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			wg.Add(1)
			result := make(chan error, 1)
			go func() {
				defer wg.Done()
				var operationErr error
				if operation == "create" {
					_, operationErr = access.CreateThread(Thread{Project: "github.com/x/y", Title: "after revoke"})
				} else {
					_, operationErr = access.PromoteMessagesToThread(room, []int64{messageID}, Thread{Project: "github.com/x/y", Title: "after revoke"})
				}
				result <- operationErr
			}()
			deadline := time.Now().Add(2 * time.Second)
			for {
				s.writer.mu.Lock()
				queued := s.writer.operations
				s.writer.mu.Unlock()
				if queued == 3 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s never entered writer queue; operations=%d", operation, queued)
				}
				time.Sleep(time.Millisecond)
			}
			release()
			if err := <-revoked.done; err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			if err := <-result; !errors.Is(err, ErrCoordForbidden) {
				t.Fatalf("%s after queued revocation = %v", operation, err)
			}
		})
	}
}

func TestRecipientsResolveAgentPrincipalToAuthorizedDisplayName(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	if _, err := s.RegisterCoordAgent(CoordAgent{
		ExternalID: "sess-peer", PrincipalID: "agent:sess-peer", Provider: "test",
		DisplayName: "Backend Agent", RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}

	recipients, err := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "").Recipients()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipient := range recipients {
		if recipient.PrincipalID == "agent:sess-peer" {
			if recipient.Label != "Backend Agent" || recipient.Kind != "agent" {
				t.Fatalf("agent recipient = %+v", recipient)
			}
			return
		}
	}
	t.Fatalf("agent principal missing from recipients: %+v", recipients)
}

// human gibt es nur für einen Menschen in einer interaktiven Browser-Sitzung.
// Bearer-Tokens (kein TokenKind web), Sitzungen aus eingefügtem Token und jeder
// Aufruf mit Agent-Kennung sind agent.
func TestAuthorKindIsHumanOnlyForInteractiveBrowserSessions(t *testing.T) {
	s := newCoordAccessStore(t)
	cases := []struct {
		name      string
		principal Principal
		agent     string
		want      string
	}{
		{"interactive browser session", Principal{ID: "person:1", TokenKind: WebSessionKind}, "", AuthorHuman},
		{"interactive session acting as an agent", Principal{ID: "person:1", TokenKind: WebSessionKind}, "sess-a", AuthorAgent},
		{"cli token", Principal{ID: "person:1", TokenKind: "cli"}, "", AuthorAgent},
		{"legacy token (pasted into the web UI)", Principal{ID: "person:1", TokenKind: "legacy"}, "", AuthorAgent},
		{"device token", Principal{ID: "person:1", TokenKind: "device"}, "", AuthorAgent},
		{"no token kind", Principal{ID: "person:1"}, "", AuthorAgent},
	}
	for _, c := range cases {
		if got := s.CoordinationFor(c.principal, c.agent).authorKind(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
