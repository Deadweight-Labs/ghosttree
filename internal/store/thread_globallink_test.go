package store

import (
	"errors"
	"strconv"
	"testing"
)

// A thread cannot link somebody else's global request: the answer is the one for
// a request that is not there, and the room list never shows its title (security
// review of PR #101, finding 2).
func TestThreadCannotLinkAnotherAuthorsGlobalRequest(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	mia := Principal{ID: "person:3", Label: "mia"}
	open := func(p Principal, request int64) (int64, error) {
		return f.st.CreateThreadByRole(p, Thread{Project: roleProject, Title: "t", Question: "q",
			Link: &ThreadLink{Kind: "request", ID: "REQ-" + strconv.FormatInt(request, 10)}})
	}
	// robin's global request: mia gets exactly what she gets for an unknown id.
	_, foreign := open(mia, f.robinR)
	_, unknown := open(mia, 99999)
	if !errors.Is(unknown, errLinkedRequestNotFound) || !errors.Is(foreign, errLinkedRequestNotFound) || foreign.Error() != unknown.Error() {
		t.Fatalf("foreign global request: %v, unknown: %v", foreign, unknown)
	}
	// The ownerless legacy request of the instance owner is not mia's either.
	if _, err := open(mia, f.legacyR); !errors.Is(err, errLinkedRequestNotFound) {
		t.Errorf("legacy global request: %v", err)
	}
	// Her own global request she may link, and nobody else sees its title.
	id, err := open(mia, f.miaR)
	if err != nil {
		t.Fatalf("own global request: %v", err)
	}
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, f.st, "a-mia", "person:3", room, "member")
	registerRoleAgent(t, f.st, "a-rex", "person:4", room, "member")
	for _, c := range []struct {
		p     Principal
		agent string
		want  bool
	}{{mia, "a-mia", true}, {Principal{ID: "person:4", Label: "rex"}, "a-rex", false}} {
		threads, err := f.st.CoordinationFor(c.p, c.agent).RoomThreads(room)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		defer func() {
			if !found {
				t.Errorf("%s does not see the thread at all, the check proves nothing", c.p.Label)
			}
		}()
		for _, th := range threads {
			if th.Thread.ID != id {
				continue
			}
			found = true
			if got := th.RequestTitle != ""; got != c.want {
				t.Errorf("%s reads the title of the global request in the room list: %q", c.p.Label, th.RequestTitle)
			}
		}
	}
}
