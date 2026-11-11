package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// say schreibt eine Nachricht mit Anker in einen Raum.
func say(t *testing.T, st *store.Store, room, client, body string, anchor *gitanchor.Anchor) {
	t.Helper()
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room,
		AuthorKind: store.AuthorAgent, ClientID: client, Body: body, GitAnchor: anchor}); err != nil {
		t.Fatal(err)
	}
}

// Nachrichten des Projektraums mit Anker stehen auf der Commit-Seite und im
// Gespräch des PRs; Nachrichten anderer Räume und Räume, die der Betrachter nicht
// lesen darf, nicht.
func TestRoomMessagesWithAnAnchorShowOnTheCommitAndPullPage(t *testing.T) {
	e, _ := pullsEnv(t)
	room := store.RoomKeyForProject(shellProject)
	materializeWebRoom(t, e.St, room)
	say(t, e.St, room, "m1", "Build is green at this commit", &gitanchor.Anchor{Commit: pullShaB, Branch: "feat/x"})
	say(t, e.St, room, "m2", "Said at the first commit of the pull", &gitanchor.Anchor{Commit: pullShaA, Branch: "feat/x"})
	say(t, e.St, room, "m3", "No anchor, said nowhere", nil)

	// A private conversation at the same commit does not belong to the project.
	for _, m := range []string{"sess-a", "sess-b"} {
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: m, PrincipalID: "person:1", Person: "alice", Provider: "test", RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	direct := store.RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := e.St.EnsureCoordRoom(store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	say(t, e.St, direct, "d1", "PRIVATE-DIRECT at the commit", &gitanchor.Anchor{Commit: pullShaB, Branch: "feat/x"})
	// The room of a project nobody here may read, anchored at the same commit.
	hidden := store.RoomKeyForProject(shellHiddenProject)
	materializeWebRoom(t, e.St, hidden)
	say(t, e.St, hidden, "h1", "PRIVATE-HIDDEN-PROJECT at the commit", &gitanchor.Anchor{Commit: pullShaB, Branch: "feat/x"})

	for _, client := range []*http.Client{e.Owner, e.Member} {
		status, page := fetchPage(t, client, e.Base+"/ui/x/shell/commits/"+pullShaB)
		if status != http.StatusOK {
			t.Fatalf("commit page: %d", status)
		}
		mustContain(t, "commit page", page, "Build is green at this commit", "said in the room", `href="/ui/coord?room=project%3Agithub.com%2Fx%2Fshell"`)
		for _, not := range []string{"PRIVATE-DIRECT", "PRIVATE-HIDDEN-PROJECT", "Said at the first commit", "No anchor"} {
			if strings.Contains(page, not) {
				t.Errorf("commit page shows %q", not)
			}
		}
		_, pull := fetchPage(t, client, e.Base+"/ui/x/shell/pulls/12")
		mustContain(t, "pull page", pull, "Build is green at this commit", "Said at the first commit of the pull")
		for _, not := range []string{"PRIVATE-DIRECT", "PRIVATE-HIDDEN-PROJECT", "No anchor"} {
			if strings.Contains(pull, not) {
				t.Errorf("pull page shows %q", not)
			}
		}
	}

	// A guest has no right to the room: nothing of it on either page.
	for _, path := range []string{"/ui/x/shell/commits/" + pullShaB, "/ui/x/shell/pulls/12"} {
		_, page := fetchPage(t, e.Guest, e.Base+path)
		for _, not := range []string{"Build is green", "Said at the first commit", "PRIVATE-"} {
			if strings.Contains(page, not) {
				t.Errorf("a guest sees %q at %s", not, path)
			}
		}
	}
}

// Eine Nachricht, deren Anker erst über den PR-Webhook die PR-Nummer bekommt,
// erscheint auch dann am PR, wenn ihr Commit nicht zu den Commits des PRs gehört.
func TestRoomMessageReachesThePullThroughTheAnchorNumber(t *testing.T) {
	e, _ := pullsEnv(t)
	room := store.RoomKeyForProject(shellProject)
	materializeWebRoom(t, e.St, room)
	say(t, e.St, room, "m1", "Said at a later commit of the branch", &gitanchor.Anchor{Commit: "dddddddd44444444444444444444444444444444", Branch: "feat/x"})
	if res, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: 12, Action: "synchronize",
		Title: "feat(web): Pull requests tab", Head: "feat/x", Base: "main", DefaultBranch: "main", State: "open"}); err != nil || !res.Applied {
		t.Fatalf("synchronize: %+v %v", res, err)
	}
	_, pull := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pulls/12")
	mustContain(t, "pull page", pull, "Said at a later commit of the branch")
	_, commit := fetchPage(t, e.Member, e.Base+"/ui/x/shell/commits/dddddddd44444444444444444444444444444444")
	mustContain(t, "commit page", commit, "Said at a later commit of the branch", `/pulls/12`)
}
