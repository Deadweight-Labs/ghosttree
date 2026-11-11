package server

import (
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Sessions an Commits und PRs über HTTP: wer melden darf und wer was sieht.
//
// accessAPI: robin (Owner), lena (Lead), mia und rex (Mitglieder), gus (Gast),
// nora (nicht in der Organisation). Die Sessions s-robin, s-mia und s-rex im
// Projekt accProject sind privat und tragen den Prompt "secretterm <name>".

const (
	gitCommitA = "a1b2c3d4e5f60718293a4b5c6d7e8f9001122334"
	gitCommitB = "b1b2c3d4e5f60718293a4b5c6d7e8f9001122334"
)

func gitEvents(events ...store.SessionGitEvent) map[string]any {
	return map[string]any{"events": events}
}

func miaCommit() store.SessionGitEvent {
	return store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-mia", Seq: 4, Kind: "commit", Op: "commit",
		StartedAt: "2026-11-07T07:59:59.800Z", EndedAt: "2026-11-07T08:00:00.200Z", OK: true, Branch: "feat/x"}
}

// seedSessionGit: PR 12 (feat/x) mit zwei Commits; mia committet leise (zugeordnet
// über das Zeitfenster), rex legt den PR an, robin merkt ihn nicht an. rex' Session
// ist für das Projekt freigegeben.
func (f *accessAPIFixture) seedSessionGit(t *testing.T) {
	t.Helper()
	f.seedPull(t)
	if res, err := f.st.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: accProject, Number: 12, Commits: []store.PullCommit{
		{SHA: gitCommitA, Title: "erster Commit", CommittedAt: "2026-11-07T08:00:00Z"},
		{SHA: gitCommitB, Title: "zweiter Commit", CommittedAt: "2026-11-07T09:00:00Z"},
	}}); err != nil || res.Recorded != 2 {
		t.Fatalf("pr commits: %+v %v", res, err)
	}
	f.expect(t, "mia", 200, "POST", "/api/session-git-events", gitEvents(miaCommit()))
	f.expect(t, "rex", 200, "POST", "/api/session-git-events", gitEvents(store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-rex",
		Seq: 9, Kind: "pr_create", StartedAt: "2026-11-07T09:30:00Z", EndedAt: "2026-11-07T09:30:03Z", OK: true, PR: 12, PRRemote: accProject}))
	if err := f.st.SetSessionShared(f.id["s-rex"], "person:4", true); err != nil {
		t.Fatal(err)
	}
}

func prSessionsURL(project string) string {
	return "/api/pulls/12/sessions?project=" + url.QueryEscape(project)
}

func commitSessionsURL(project, sha string) string {
	return "/api/commits/" + sha + "/sessions?project=" + url.QueryEscape(project)
}

func TestSessionGitEventsAreAcceptedOnlyForTheOwnersSessions(t *testing.T) {
	f := accessAPI(t, true)
	f.seedPull(t)
	// mia meldet für ihre eigene Session.
	out := f.expect(t, "mia", 200, "POST", "/api/session-git-events", gitEvents(miaCommit()))
	if got := decode[store.SessionGitResult](t, out); got.Recorded != 1 {
		t.Fatalf("recorded: %+v", got)
	}
	// Dieselbe Meldung noch einmal: nichts Neues.
	out = f.expect(t, "mia", 200, "POST", "/api/session-git-events", gitEvents(miaCommit()))
	if got := decode[store.SessionGitResult](t, out); got.Recorded != 0 {
		t.Fatalf("repeat: %+v", got)
	}
	// Eine fremde Session: nicht für das Mitglied, nicht für den Lead, nicht für
	// den Projekt-Owner, und eine unbekannte sieht gleich aus.
	for _, who := range []string{"rex", "lena", "robin", "gus", "nora"} {
		f.expect(t, who, 403, "POST", "/api/session-git-events", gitEvents(miaCommit()))
	}
	unknown := miaCommit()
	unknown.SessionExternalID = "nobody"
	f.expect(t, "mia", 403, "POST", "/api/session-git-events", gitEvents(unknown))
	// Ohne Token gar nicht.
	if code, _ := f.callNoToken(t, "POST", "/api/session-git-events", gitEvents(miaCommit())); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// Ungültiges ist ein 400, kein 500.
	bad := miaCommit()
	bad.Kind = "reset"
	f.expect(t, "mia", 400, "POST", "/api/session-git-events", gitEvents(bad))
	f.expect(t, "mia", 400, "POST", "/api/session-git-events", "not an object")
}

func (f *accessAPIFixture) callNoToken(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	resp := req(t, method, f.srv.URL+path, "", body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPullSessionsAreShownInThreeLevelsByRole(t *testing.T) {
	f := accessAPI(t, true)
	f.seedSessionGit(t)
	get := func(who string) store.GitSessionLinks {
		t.Helper()
		return decode[store.GitSessionLinks](t, f.expect(t, who, 200, "GET", prSessionsURL(accProject), nil))
	}
	persons := func(l store.GitSessionLinks) string {
		var p []string
		for _, it := range l.Items {
			p = append(p, it.Person+":"+it.Level)
		}
		return strings.Join(p, ",")
	}

	// mia: ihre eigene Session (privat) mit Titel und Adresse, dazu die für das
	// Projekt freigegebene von rex.
	mia := get("mia")
	if mia.Hidden != 0 || len(mia.Items) != 2 {
		t.Fatalf("mia: %+v", mia)
	}
	for _, it := range mia.Items {
		if it.Level != store.GitSessionLinkFull || it.SessionRef == "" || !strings.Contains(it.Title, "secretterm") {
			t.Errorf("mia sees title and address of both: %+v", it)
		}
	}
	// rex: seine eigene und die private von mia nur als Zahl.
	rex := get("rex")
	if len(rex.Items) != 1 || rex.Hidden != 1 || rex.Items[0].Person != "rex" {
		t.Fatalf("rex: %+v", rex)
	}
	// lena (Lead) und robin (Owner): "privat heißt privat", auch für sie.
	for _, who := range []string{"lena", "robin"} {
		l := get(who)
		if l.Hidden != 1 || len(l.Items) != 1 || l.Items[0].Person != "rex" {
			t.Errorf("%s: %+v", who, l)
		}
	}
	// gus (Gast): nur die Zahl, kein Name, kein Datum, kein Titel.
	gus := get("gus")
	if len(gus.Items) != 0 || gus.Hidden != 2 {
		t.Fatalf("a guest sees a number only: %+v", gus)
	}
	// nora (nicht in der Organisation) bekommt dieselbe Antwort wie auf ein
	// Projekt, das es nicht gibt: kein Name, keine Zahl.
	code, out := f.call(t, "nora", "GET", prSessionsURL(accProject), nil)
	_, missing := f.call(t, "nora", "GET", prSessionsURL("github.com/dw/does-not-exist"), nil)
	if code != 404 || out != missing {
		t.Fatalf("an outsider: %d %s, want the answer for a project that does not exist: %s", code, out, missing)
	}
	// Anonym: 401.
	if code, _ := f.callNoToken(t, "GET", prSessionsURL(accProject), nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// Was ein Betrachter nicht sehen darf, steht auch nirgends im Rohtext.
	for _, who := range []string{"rex", "lena", "robin", "gus"} {
		body := f.expect(t, who, 200, "GET", prSessionsURL(accProject), nil)
		if strings.Contains(body, "secretterm mia") || strings.Contains(body, "s-mia") {
			t.Errorf("%s: the private session leaks: %s", who, body)
		}
	}
	if got := persons(mia); !strings.Contains(got, "mia:full") || !strings.Contains(got, "rex:full") {
		t.Errorf("persons: %s", got)
	}
}

func TestCommitSessionsFollowTheSameRulesAndResolveSquashMerges(t *testing.T) {
	f := accessAPI(t, true)
	f.seedSessionGit(t)
	// Der Commit A liegt im Fenster von mias leisem Commit: Stufe 1, direkt.
	l := decode[store.GitSessionLinks](t, f.expect(t, "mia", 200, "GET", commitSessionsURL(accProject, gitCommitA), nil))
	if len(l.Items) != 1 || l.Items[0].Person != "mia" || l.Items[0].Via != "" || strings.Join(l.Items[0].Sources, ",") != "matched" {
		t.Fatalf("direct: %+v", l)
	}
	// Auch mit zwölf Zeichen.
	l = decode[store.GitSessionLinks](t, f.expect(t, "mia", 200, "GET", commitSessionsURL(accProject, gitCommitA[:12]), nil))
	if len(l.Items) != 1 {
		t.Fatalf("prefix: %+v", l)
	}
	// rex sieht die direkte Session von mia nur als Zahl.
	l = decode[store.GitSessionLinks](t, f.expect(t, "rex", 200, "GET", commitSessionsURL(accProject, gitCommitA), nil))
	if len(l.Items) != 0 || l.Hidden != 1 {
		t.Fatalf("rex: %+v", l)
	}
	// Squash-Merge: die merge_sha ist ein Commit von GitHub; die Frage nach ihr
	// nennt die Sessions des PRs, als "über PR".
	mergeSHA := strings.Repeat("e", 40)
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: accProject, HeadRemote: accProject, Number: 12,
		Action: "closed", Title: "Der PR", Head: "feat/x", Base: "main", State: "merged", MergeSHA: mergeSHA}); err != nil {
		t.Fatal(err)
	}
	l = decode[store.GitSessionLinks](t, f.expect(t, "mia", 200, "GET", commitSessionsURL(accProject, mergeSHA), nil))
	if len(l.Items) != 2 {
		t.Fatalf("squash: %+v", l)
	}
	for _, it := range l.Items {
		if it.Via != store.LinkViaPR || it.PR != 12 {
			t.Errorf("squash must say via PR 12: %+v", it)
		}
	}
	// Gast und Fremder wie beim PR.
	if g := decode[store.GitSessionLinks](t, f.expect(t, "gus", 200, "GET", commitSessionsURL(accProject, mergeSHA), nil)); len(g.Items) != 0 || g.Hidden != 2 {
		t.Fatalf("guest: %+v", g)
	}
	f.expect(t, "nora", 404, "GET", commitSessionsURL(accProject, mergeSHA), nil)
	// Ein ungültiger Hash ist ein 400.
	f.expect(t, "mia", 400, "GET", commitSessionsURL(accProject, "xyz"), nil)
}

func TestSessionsOfAnotherProjectDoNotAppearAtAnotherProjectsPullRequest(t *testing.T) {
	f := accessAPI(t, true)
	f.seedSessionGit(t)
	// robin hat im anderen Projekt eine Session und meldet einen PR 12 "in
	// accProject": das ist nicht sein Projekt, die Zuordnung wird nicht angelegt.
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-robin-other", AccountID: 1,
		Scope: scope.Axes{Project: accOther}})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	f.expect(t, "robin", 200, "POST", "/api/session-git-events", gitEvents(store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-robin-other",
		Seq: 1, Kind: "pr_create", StartedAt: "2026-11-07T09:30:00Z", EndedAt: "2026-11-07T09:30:03Z", OK: true, PR: 12, PRRemote: accProject}))
	// Auch ein Commit im Fenster aus dem anderen Projekt ordnet nichts zu.
	f.expect(t, "robin", 200, "POST", "/api/session-git-events", gitEvents(store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-robin-other",
		Seq: 2, Kind: "commit", Op: "commit", StartedAt: "2026-11-07T08:59:59.500Z", EndedAt: "2026-11-07T09:00:00.100Z", OK: true, Branch: "feat/x"}))
	l := decode[store.GitSessionLinks](t, f.expect(t, "robin", 200, "GET", prSessionsURL(accProject), nil))
	if l.Hidden != 1 || len(l.Items) != 1 {
		t.Fatalf("PR 12 in %s must still show two sessions (one visible to robin, one as a number): %+v", accProject, l)
	}
	// Im anderen Projekt gibt es den PR nicht, also auch keine Session.
	l = decode[store.GitSessionLinks](t, f.expect(t, "robin", 200, "GET", prSessionsURL(accOther), nil))
	if len(l.Items) != 0 || l.Hidden != 0 {
		t.Fatalf("the other project: %+v", l)
	}
	// Commit B kennt keine Session; er liegt im PR, also gelten dessen Sessions
	// "über PR": mia nur als Zahl, rex mit Titel, nie die des anderen Projekts.
	lb := decode[store.GitSessionLinks](t, f.expect(t, "robin", 200, "GET", commitSessionsURL(accProject, gitCommitB), nil))
	if len(lb.Items) != 1 || lb.Hidden != 1 || lb.Items[0].Via != store.LinkViaPR || lb.Items[0].Person != "rex" {
		t.Fatalf("commit B through the PR: %+v", lb)
	}
}
