package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
)

// PAIN-67: ctx coord inbox in a repository with a remote the server does not
// know creates no project, does not join the room, and so does not bind the
// CLI identity to that room.
func TestCoordReadsInAnUnknownRepoCreateNothing(t *testing.T) {
	for _, sub := range []string{"inbox", "peers", "rooms"} {
		t.Run(sub, func(t *testing.T) {
			st, cfg := coordCLIEnv(t)
			const remote = "github.com/somebody/unknown-repo-xyz"
			unknown := coordRepo(t, "https://"+remote+".git")
			var out bytes.Buffer
			if code := run([]string{"coord", sub, unknown}, &out); code != 0 {
				t.Fatalf("coord %s: code %d: %s", sub, code, out.String())
			}
			if sub != "rooms" && !strings.Contains(out.String(), "not a project on the server") {
				t.Errorf("no word on the unknown project: %s", out.String())
			}
			if _, ok := st.ProjectByRemote(remote); ok {
				t.Fatalf("coord %s created the project", sub)
			}
			if _, registered, err := st.CoordAgentOwner("cli:testbox"); err != nil || registered {
				t.Fatalf("coord %s joined a room: registered=%v err=%v", sub, registered, err)
			}
			// The identity is still free for a repository that is a project: no
			// "already registered in another room", no --agent-name.
			if _, err := client.New(cfg).ClaimProject("github.com/x/real", ""); err != nil {
				t.Fatal(err)
			}
			real := coordRepo(t, "https://github.com/x/real.git")
			out.Reset()
			if code := run([]string{"coord", "send", "hello", real}, &out); code != 0 {
				t.Fatalf("send in the real project after a read elsewhere: %d %s", code, out.String())
			}
			if strings.Contains(out.String(), "another room") {
				t.Errorf("the identity is bound to the unknown room: %s", out.String())
			}
		})
	}
}

// Reading a project the server knows still works, and still joins (the server
// answers the inbox only to a registered identity).
func TestCoordInboxInAKnownProjectStillReads(t *testing.T) {
	_, cfg := coordCLIEnv(t)
	if _, err := client.New(cfg).ClaimProject("github.com/x/known", ""); err != nil {
		t.Fatal(err)
	}
	repo := coordRepo(t, "https://github.com/x/known.git")
	var out bytes.Buffer
	if code := run([]string{"coord", "inbox", repo}, &out); code != 0 {
		t.Fatalf("inbox: %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "nothing new in project:github.com/x/known") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// A write still works in a repository the server does not know yet: joining
// the room of an unknown remote creates no project (REQ-623), the write is the
// work that does.
func TestCoordSendInAnUnknownRepoStillWorks(t *testing.T) {
	coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/somebody/fresh-repo.git")
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "hello", repo}, &out); code != 0 {
		t.Fatalf("send: %d %s", code, out.String())
	}
}
