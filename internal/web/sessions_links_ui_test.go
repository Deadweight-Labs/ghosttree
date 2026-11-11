package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Der Block „Entstanden in“ an PR, Commit und Request (IDEA-31 P4): wer das
// Transkript lesen darf, sieht Titel und Link; alle anderen eine Zahl; eine schwache
// Zuordnung trägt ihr Etikett.

type sessLinksEnv struct {
	shellEnv
	Stranger *http.Client
	// Die Sessions: alice (Besitzerin, Github-Konto bewiesen, Commit A, stark) und anna
	// (Mitglied, ohne Verknüpfung, Commit B, schwach). Beide privat.
	AliceRef, AnnaRef string
	RequestPath       string
}

const (
	aliceTitle = "Alices Plan fuer den PR-Reiter"
	annaTitle  = "Annas Notizen zur Seite"
)

func sessLinksFixture(t *testing.T) sessLinksEnv {
	t.Helper()
	e, _ := pullsEnv(t)
	if err := e.St.SetAccountGitHub("person:1", "alice-gh", 1001); err != nil {
		t.Fatal(err)
	}
	mk := func(account int64, external, title string) (int64, string) {
		sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: external, AccountID: account,
			Scope: scope.Axes{Project: shellProject, Branch: "feat/x", Machine: "build-1"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.St.AppendChunks(sid, []store.Chunk{{Seq: 1, Role: "user", Text: title, Raw: uLine(title)}}); err != nil {
			t.Fatal(err)
		}
		s, err := e.St.SessionByID(sid)
		if err != nil || s.PublicID == "" {
			t.Fatalf("session: %+v %v", s, err)
		}
		return sid, s.PublicID
	}
	_, aliceRef := mk(1, "sl-alice", aliceTitle)
	_, annaRef := mk(2, "sl-anna", annaTitle)
	commit := func(account int64, external string, start, end string) {
		t.Helper()
		res, err := e.St.RecordSessionGitEvents(account, "", []store.SessionGitEvent{{Harness: "claude-code", SessionExternalID: external, Seq: 4, Kind: "commit", Op: "commit",
			StartedAt: start, EndedAt: end, OK: true, Branch: "feat/x"}})
		if err != nil || res.Recorded != 1 {
			t.Fatalf("event of %s: %+v %v", external, res, err)
		}
	}
	// Commit A (10:00:00) von alice-gh, Commit B (11:00:00) von "claude": der erste
	// ist gegen alices bewiesenes Konto prüfbar, der zweite für anna nicht.
	commit(1, "sl-alice", "2026-11-08T09:59:59.800Z", "2026-11-08T10:00:00.200Z")
	commit(2, "sl-anna", "2026-11-08T10:59:59.800Z", "2026-11-08T11:00:00.200Z")
	if _, err := e.St.AddPerson("sara"); err != nil {
		t.Fatal(err)
	}
	pkg, err := e.St.PackageForPR(shellProject, 12)
	if err != nil || len(pkg.Members) == 0 {
		t.Fatalf("package of PR 12: %+v %v", pkg, err)
	}
	return sessLinksEnv{shellEnv: e, Stranger: loginInteractive(t, e.Srv, e.St, "sara"), AliceRef: aliceRef, AnnaRef: annaRef,
		RequestPath: "/ui/x/shell/issues/" + itoa(pkg.Members[0].RequestID)}
}

func TestPullPageShowsSessionsByWhatTheViewerMayRead(t *testing.T) {
	e := sessLinksFixture(t)
	page := func(c *http.Client) string {
		t.Helper()
		status, body := fetchPage(t, c, e.Base+"/ui/x/shell/pulls/12")
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		return body
	}
	// alice: ihre eigene Session mit Titel und Link; die von anna (privat) nur als Zahl.
	owner := page(e.Owner)
	mustContain(t, "owner", owner, "Built in", aliceTitle, `href="/ui/sessions/`+e.AliceRef+`"`, "1 more session, not shared")
	mustNotContain(t, "owner", owner, annaTitle, e.AnnaRef)
	// anna: ihre Session, mit dem Etikett "weak", denn der Autor von Commit B ließ
	// sich nicht gegen ihr Konto prüfen. Alices Session bleibt eine Zahl.
	member := page(e.Member)
	mustContain(t, "member", member, annaTitle, `href="/ui/sessions/`+e.AnnaRef+`"`, ">weak<", "1 more session, not shared")
	mustNotContain(t, "member", member, aliceTitle, e.AliceRef)
	// Eine starke Zuordnung trägt das Etikett nicht.
	if strings.Contains(owner, ">weak<") {
		t.Error("alice's assignment is proven and must not be called weak")
	}
	// Lead und Prüfer: auch sie sehen fremde private Sessions nicht.
	for name, c := range map[string]*http.Client{"lead": e.Lead, "reviewer": e.Reviewer} {
		body := page(c)
		mustContain(t, name, body, "2 more sessions, not shared")
		mustNotContain(t, name, body, aliceTitle, annaTitle, e.AliceRef, e.AnnaRef)
	}
	// Gast: nur die Zahl, kein Titel, kein Name, keine Adresse.
	guest := page(e.Guest)
	mustContain(t, "guest", guest, "Built in", "2 more sessions, not shared")
	mustNotContain(t, "guest", guest, aliceTitle, annaTitle, e.AliceRef, e.AnnaRef, `href="/ui/sessions/`)
	// Ein Fremder bekommt nicht einmal die Seite.
	status, body := fetchPage(t, e.Stranger, e.Base+"/ui/x/shell/pulls/12")
	if status == http.StatusOK {
		t.Errorf("a stranger reads the page: %d", status)
	}
	mustNotContain(t, "stranger", body, "Built in", aliceTitle, annaTitle)
}

func TestSharingASessionShowsItsTitleToTheProject(t *testing.T) {
	e := sessLinksFixture(t)
	s, err := e.St.SessionByPublicID(e.AliceRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetSessionShared(s.ID, "person:1", true); err != nil {
		t.Fatal(err)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pulls/12")
	mustContain(t, "member after the share", member, aliceTitle, e.AliceRef)
}

func TestCommitPageShowsTheSessionThatMadeTheCommit(t *testing.T) {
	e := sessLinksFixture(t)
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits/"+pullShaA)
	mustContain(t, "owner at commit A", owner, "Built in", aliceTitle, `href="/ui/sessions/`+e.AliceRef+`"`)
	mustNotContain(t, "owner at commit A", owner, annaTitle, ">weak<")
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/commits/"+pullShaB)
	mustContain(t, "member at commit B", member, annaTitle, ">weak<")
	mustNotContain(t, "member at commit B", member, aliceTitle)
	// An einem Commit ohne eigene Session gelten die des PRs, mit dem Hinweis darauf.
	_, viaPR := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits/"+pullShaB)
	mustContain(t, "owner at commit B", viaPR, "1 more session, not shared")
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/commits/"+pullShaA)
	mustContain(t, "guest at commit A", guest, "1 more session, not shared")
	mustNotContain(t, "guest at commit A", guest, aliceTitle, e.AliceRef)
	if status, _ := fetchPage(t, e.Stranger, e.Base+"/ui/x/shell/commits/"+pullShaA); status == http.StatusOK {
		t.Error("a stranger reads the commit page")
	}
}

func TestRequestPageShowsTheSessionsBehindThePackageAndTheEvidence(t *testing.T) {
	e := sessLinksFixture(t)
	status, owner := fetchPage(t, e.Owner, e.Base+e.RequestPath)
	if status != http.StatusOK {
		t.Fatalf("request page: %d", status)
	}
	// Der Nachweis der Fixture nennt Commit A; der PR 12 des Pakets trägt beide Commits.
	mustContain(t, "owner at the request", owner, "Built in", aliceTitle, `href="/ui/sessions/`+e.AliceRef+`"`, "PR #12", "evidence")
	mustNotContain(t, "owner at the request", owner, annaTitle)
	_, member := fetchPage(t, e.Member, e.Base+e.RequestPath)
	mustContain(t, "member at the request", member, annaTitle, ">weak<")
	mustNotContain(t, "member at the request", member, aliceTitle, e.AliceRef)
	_, guest := fetchPage(t, e.Guest, e.Base+e.RequestPath)
	mustNotContain(t, "guest at the request", guest, aliceTitle, annaTitle, e.AliceRef, e.AnnaRef)
	if status, _ := fetchPage(t, e.Stranger, e.Base+e.RequestPath); status == http.StatusOK {
		t.Error("a stranger reads the request page")
	}
}

// Ein Titel, der Markup enthält, wird als Text gesetzt.
func TestSessionTitlesAreEscapedInTheBlock(t *testing.T) {
	links := store.GitSessionLinks{Items: []store.GitSessionLinkView{{Level: store.GitSessionLinkFull, SessionRef: "abc", Title: `<img src=x onerror=alert(1)>`, Person: "alice"}}}
	b := sessionBlockOf(langEN, links, "pr")
	if b == nil || len(b.Lines) != 1 || b.Lines[0].Href != "/ui/sessions/abc" {
		t.Fatalf("block: %+v", b)
	}
	// Das Template maskiert: html/template gibt den Titel nur als Text aus.
	html := renderPart(t, langEN, "sessblock", b)
	if strings.Contains(html, "<img") || !strings.Contains(html, "&lt;img") {
		t.Errorf("the title must be escaped: %s", html)
	}
}
