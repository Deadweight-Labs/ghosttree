package mcpserver

import (
	"context"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// request_get nennt die Sessions hinter einem Request, auch ohne start_work (IDEA-31
// P4): über den PR im Paket und über einen Commit-Nachweis. Der Agent liest mit der
// Adresse gezielt nach.
func TestRequestGetNamesTheSessionsBehindThePullRequestOfThePackage(t *testing.T) {
	c, st := newTestClient(t)
	org, err := st.CreateOrg("person:1", "X", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "x", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", linkProject, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 5, Remote: linkProject, HeadRemote: linkProject, Number: 9, Action: "opened",
		Title: "Der PR", Head: "feat/a", Base: "main", State: "open", Author: "alice-gh"}); err != nil {
		t.Fatal(err)
	}
	ax := scope.Axes{Project: linkProject, Branch: "feat/a"}
	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-alice", AccountID: 1, Scope: ax, })
	if err != nil {
		t.Fatal(err)
	}
	said := "Den Export bauen"
	if err := st.AppendChunks(sid, []store.Chunk{{Seq: 1, Role: "user", Text: said, Raw: promptRaw("claude-code", said)}}); err != nil {
		t.Fatal(err)
	}
	sess, err := st.SessionByID(sid)
	if err != nil || sess.PublicID == "" {
		t.Fatalf("session: %+v %v", sess, err)
	}
	if _, err := st.RecordSessionGitEvents(1, "", []store.SessionGitEvent{{Harness: "claude-code", SessionExternalID: "s-alice", Seq: 3, Kind: "pr_create",
		StartedAt: "2026-11-07T09:30:00Z", EndedAt: "2026-11-07T09:30:03Z", OK: true, PR: 9, PRRemote: linkProject}}); err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Export", Scope: scope.Axes{Project: linkProject}, Person: "alice"},
		Criteria: []string{"Der Export läuft"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Ohne Paket", Scope: scope.Axes{Project: linkProject}, Person: "alice"},
		Criteria: []string{"x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.CreatePackage(requestdomain.PackageInput{Project: linkProject, Branch: "feat/a", Name: "Export",
		Members: []requestdomain.PackageMember{{RequestID: d.Request.ID}}, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	pr := int64(9)
	if _, err := st.UpdatePackage(pkg.ID, requestdomain.PackagePatch{PR: &pr}); err != nil {
		t.Fatal(err)
	}

	s := &Server{client: c, ctxAxes: scope.Axes{Project: linkProject, Branch: "main", Machine: "workstation-a"}}
	for _, format := range []string{"", "detailed"} {
		res, _, err := s.handleRequestGet(context.Background(), nil, RequestGetInput{RequestID: d.Request.ID, ResponseFormat: format})
		if err != nil {
			t.Fatal(err)
		}
		got := text(t, res)
		for _, want := range []string{"Sessions behind the work", "- @" + sess.PublicID, `"Den Export bauen"`, "PR #9", "context_search action=sessions session_ref="} {
			if !strings.Contains(got, want) {
				t.Errorf("format %q lacks %q:\n%s", format, want, got)
			}
		}
		// Der PR-Autor "alice-gh" ist nicht gegen ein GitHub-Konto belegt: ein
		// Kandidat, und der Text sagt es.
		if !strings.Contains(got, "weak") {
			t.Errorf("format %q: an assignment that could not be checked must say so:\n%s", format, got)
		}
	}

	// Ein Request ohne Paket und ohne Nachweis nennt keine Sessions.
	res, _, err := s.handleRequestGet(context.Background(), nil, RequestGetInput{RequestID: other.Request.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "Sessions behind the work") {
		t.Errorf("nothing derived, nothing said:\n%s", got)
	}
}

// Ohne Transkriptrecht steht keine Adresse da, und mehr als fünf werden gezählt.
func TestRenderRequestSessionsHoldsBackAddressesAndCaps(t *testing.T) {
	if renderRequestSessions(store.GitSessionLinks{}) != "" {
		t.Fatal("no sessions, no section")
	}
	links := store.GitSessionLinks{Hidden: 2}
	for i := 0; i < 7; i++ {
		links.Items = append(links.Items, store.GitSessionLinkView{Level: store.GitSessionLinkFull, SessionRef: "ref" + string(rune('a'+i)), Title: "t", Origins: []string{store.RequestOriginEvidence}})
	}
	links.Items = append(links.Items, store.GitSessionLinkView{Level: store.GitSessionLinkMeta, Person: "mia", Date: "2026-11-07"})
	got := renderRequestSessions(links)
	if strings.Count(got, "- @ref") != 5 || !strings.Contains(got, "- 3 more sessions you may see") || !strings.Contains(got, "- 2 more sessions not shared with you") {
		t.Errorf("cap and counts:\n%s", got)
	}
	meta := renderRequestSessions(store.GitSessionLinks{Items: []store.GitSessionLinkView{{Level: store.GitSessionLinkMeta, Person: "mia", Date: "2026-11-07"}}})
	if strings.Contains(meta, "@") || !strings.Contains(meta, "session of mia on 2026-11-07") {
		t.Errorf("a session the caller may not read has no address:\n%s", meta)
	}
}
