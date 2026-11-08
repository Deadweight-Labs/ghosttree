package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func shaOf(c byte) string { return strings.Repeat(string(c), 40) }

// seedCommits legt fest, was der Server von Commits weiß:
//
//	sha 1  Anker feat/x (Wissen, Wissen "staged", Pain, Kommentar mit zwei Fassungen) und main (Wissen); im PR 12
//	sha 2  Anker feat/x (Issue)
//	sha 3  Anker main, nur ein Wissenseintrag
//	sha 4  nur im PR 12 (feat/x), kein Eintrag
//	sha 5  Anker feat/x, nur ein Wissenseintrag "staged" mit Pfad secret/dir
//
// Dazu ein Wissenseintrag eines anderen Projekts am selben Commit 1, der nie
// auftauchen darf.
func (f *accessAPIFixture) seedCommits(t *testing.T) {
	t.Helper()
	know := func(project, title, confidence string, sha byte, branch string, paths ...originctx.Path) int64 {
		k := store.Knowledge{Type: "note", Title: title, Body: "text", Person: "robin", Confidence: confidence,
			Scope: scope.Axes{Project: project, Branch: branch}, GitAnchor: &gitanchor.Anchor{Commit: shaOf(sha), Branch: branch}}
		if len(paths) > 0 {
			k.OriginContext = &originctx.Context{SessionRef: "s-1", Paths: paths}
		}
		id, err := f.st.InsertKnowledge(k)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	know(accProject, "geprüft auf feat/x", "trusted", '1', "feat/x",
		originctx.Path{Path: "internal/store/a.go", Kind: originctx.KindWrite, Weight: 3}, originctx.Path{Path: "internal/store/sub/b.go", Kind: originctx.KindRead, Weight: 1})
	know(accProject, "ungeprüft auf feat/x", "staged", '1', "feat/x")
	know(accProject, "geprüft auf main", "trusted", '1', "main")
	know(accProject, "nur auf main", "trusted", '3', "main")
	know(accProject, "geheimer Pfad", "staged", '5', "feat/x", originctx.Path{Path: "secret/dir/x.go", Kind: originctx.KindNamed, Weight: 10})
	know(accOther, "anderes Projekt", "trusted", '1', "feat/x", originctx.Path{Path: "internal/store/a.go", Kind: originctx.KindWrite, Weight: 3})

	atCommit := func(sha byte, branch string) map[string]string {
		return map[string]string{gitanchor.Header: gitanchor.Encode(gitanchor.Anchor{Commit: shaOf(sha), Branch: branch})}
	}
	code, out := f.callWith(t, "mia", "POST", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz am Commit"}, atCommit('1', "feat/x"))
	if code/100 != 2 {
		t.Fatalf("pain: %d %s", code, out)
	}
	if _, err := f.st.CreateRequest(requestdomainInput("Issue an Commit 2", shaOf('2'))); err != nil {
		t.Fatal(err)
	}
	// Ein Kommentar mit zwei Fassungen am selben Commit zählt einmal.
	code, out = f.callWith(t, "mia", "POST", "/api/comments", map[string]any{"kind": "request", "id": f.id["r-mia"], "body": "erste Fassung"}, atCommit('1', "feat/x"))
	if code/100 != 2 {
		t.Fatalf("comment: %d %s", code, out)
	}
	cid := decode[store.Comment](t, out).ID
	if code, out = f.callWith(t, "mia", "PATCH", fmt.Sprintf("/api/comments/%d", cid), map[string]any{"body": "zweite Fassung"}, atCommit('1', "feat/x")); code != 200 {
		t.Fatalf("edit: %d %s", code, out)
	}

	f.seedPull(t) // PR 12: feat/x → main, Installation 77
	if res, err := f.st.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: accProject, Number: 12, Commits: []store.PullCommit{
		{SHA: shaOf('1'), Author: "mia-gh", AuthorName: "Mia", Title: "erster Commit des PRs", CommittedAt: "2026-11-07T08:00:00Z"},
		{SHA: shaOf('4'), Author: "lena-gh", Title: "nur im PR", CommittedAt: "2026-11-07T09:00:00Z"},
	}}); err != nil || res.Recorded != 2 {
		t.Fatalf("pr commits: %+v %v", res, err)
	}
}

func requestdomainInput(title, sha string) requestdomain.CreateInput {
	return requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: accProject}, Person: "mia"},
		GitAnchor: &gitanchor.Anchor{Commit: sha, Branch: "feat/x"}}
}

func commitsURL(extra string) string {
	return "/api/commits?project=" + url.QueryEscape(accProject) + extra
}

func commitURL(sha string) string {
	return "/api/commits/" + sha + "?project=" + url.QueryEscape(accProject)
}

func (f *accessAPIFixture) commitList(t *testing.T, who, extra string) client.CommitListOut {
	t.Helper()
	return decode[client.CommitListOut](t, f.expect(t, who, 200, "GET", commitsURL(extra), nil))
}

func byShort(list client.CommitListOut) map[byte]client.CommitEntryOut {
	m := map[byte]client.CommitEntryOut{}
	for _, c := range list.Items {
		m[c.SHA[0]] = c
	}
	return m
}

func TestCommitsMergeAnchorsAndPullRequestCommitsByHash(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	list := f.commitList(t, "mia", "")
	got := byShort(list)
	if list.Total != 5 || len(list.Items) != 5 {
		t.Fatalf("a member sees five commits: %+v", list)
	}
	c1 := got['1']
	// Eine Zeile je Hash, auch wenn beide Quellen ihn kennen.
	if strings.Join(c1.Sources, ",") != "ledger,pr" || c1.Title != "erster Commit des PRs" || c1.Author != "mia-gh" || c1.AuthorName != "Mia" ||
		strings.Join(c1.Branches, ",") != "feat/x,main" || len(c1.PRs) != 1 || c1.PRs[0] != 12 || c1.At != "2026-11-07T08:00:00Z" {
		t.Fatalf("commit 1: %+v", c1)
	}
	// Wissen (2 auf feat/x, 1 auf main), Pain und Kommentar: der Kommentar mit zwei Fassungen zählt einmal.
	if c1.Entries != 5 {
		t.Errorf("entries at commit 1 = %d, want 5", c1.Entries)
	}
	if c4 := got['4']; strings.Join(c4.Sources, ",") != "pr" || c4.Entries != 0 || c4.Title != "nur im PR" || c4.Author != "lena-gh" {
		t.Errorf("commit only in the pull request: %+v", c4)
	}
	if c3 := got['3']; strings.Join(c3.Sources, ",") != "ledger" || c3.Entries != 1 || c3.Title != "" || c3.At == "" {
		t.Errorf("commit only in the ledger: %+v", c3)
	}
	for _, c := range list.Items {
		if c.Short != c.SHA[:12] || !strings.HasSuffix(c.Link, "/ui/dw/p/commit/"+c.SHA) {
			t.Errorf("short/link: %+v", c)
		}
	}
	// Das jüngste zuerst.
	for i := 1; i < len(list.Items); i++ {
		if list.Items[i-1].At < list.Items[i].At {
			t.Errorf("order: %s before %s", list.Items[i-1].At, list.Items[i].At)
		}
	}

	// Nach Branch: die Anker auf dem Branch und die Commits seiner PRs; die Zahl zählt alle Einträge am Commit.
	x := byShort(f.commitList(t, "mia", "&branch=feat/x"))
	if len(x) != 4 || x['3'].SHA != "" || x['1'].Entries != 5 || x['4'].SHA == "" || x['5'].SHA == "" || x['2'].SHA == "" {
		t.Errorf("branch feat/x: %+v", x)
	}
	m := byShort(f.commitList(t, "mia", "&branch=main"))
	if len(m) != 2 || m['1'].Entries != 5 || m['3'].Entries != 1 {
		t.Errorf("branch main: %+v", m)
	}
	if none := f.commitList(t, "mia", "&branch=gibt-es-nicht"); none.Total != 0 || len(none.Items) != 0 {
		t.Errorf("unknown branch: %+v", none)
	}

	// Seiten.
	p1, p2 := f.commitList(t, "mia", "&limit=2"), f.commitList(t, "mia", "&limit=2&offset=4")
	if len(p1.Items) != 2 || len(p2.Items) != 1 || p1.Total != 5 || p2.Total != 5 {
		t.Errorf("pages: %d %d", len(p1.Items), len(p2.Items))
	}
	if got := f.commitList(t, "mia", "&limit=9999"); got.Limit != store.CommitMaxLimit {
		t.Errorf("limit: %d", got.Limit)
	}
	f.expect(t, "mia", 400, "GET", commitsURL("&offset=-1"), nil)
	f.expect(t, "mia", 400, "GET", "/api/commits", nil)
}

// Einträge zählen und erscheinen nur, soweit der Betrachter sie lesen darf: ein
// Gast sieht weder den ungeprüften Eintrag noch den Commit, an dem nur so einer hängt.
func TestCommitEntriesAreCountedOnlyAsFarAsTheViewerMayRead(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	member, guest := byShort(f.commitList(t, "mia", "")), byShort(f.commitList(t, "gus", ""))
	if member['1'].Entries != 5 || guest['1'].Entries != 4 {
		t.Errorf("commit 1: member %d, guest %d (want 5 and 4)", member['1'].Entries, guest['1'].Entries)
	}
	if _, ok := guest['5']; ok {
		t.Errorf("the guest sees a commit that only a hidden entry hangs on: %+v", guest['5'])
	}
	if _, ok := member['5']; !ok {
		t.Errorf("the member lost commit 5")
	}
	// Der Wissenseintrag des anderen Projekts hängt am selben Hash und zählt nie mit.
	for who, c := range map[string]client.CommitEntryOut{"mia": member['1'], "robin": byShort(f.commitList(t, "robin", ""))['1']} {
		if c.Entries != 5 {
			t.Errorf("%s: entries %d, another project's entry must not count", who, c.Entries)
		}
	}
	d := decode[client.CommitDetailOut](t, f.expect(t, "gus", 200, "GET", commitURL(shaOf('1')), nil))
	titles := []string{}
	for _, e := range d.Said {
		titles = append(titles, e.Title)
	}
	joined := strings.Join(titles, "|")
	if strings.Contains(joined, "ungeprüft") || strings.Contains(joined, "anderes Projekt") || len(d.Said) != 4 {
		t.Errorf("the guest's view of commit 1: %v", titles)
	}
	// Ein Commit, an dem für den Gast nichts lesbar hängt, ist für ihn unbekannt, und zwar genauso wie ein erfundener.
	hiddenCode, hiddenBody := f.call(t, "gus", "GET", commitURL(shaOf('5')), nil)
	unknownCode, unknownBody := f.call(t, "gus", "GET", commitURL(shaOf('9')), nil)
	if hiddenCode != 404 || hiddenCode != unknownCode || hiddenBody != unknownBody {
		t.Fatalf("oracle: hidden %d %s / unknown %d %s", hiddenCode, hiddenBody, unknownCode, unknownBody)
	}
	f.expect(t, "mia", 200, "GET", commitURL(shaOf('5')), nil)
}

func TestCommitDetailSaysWhatWasWrittenThereAndTakesAPrefix(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	d := decode[client.CommitDetailOut](t, f.expect(t, "mia", 200, "GET", commitURL(shaOf('1')[:7]), nil))
	if d.SHA != shaOf('1') || d.Title != "erster Commit des PRs" || d.Project != accProject {
		t.Fatalf("detail: %+v", d.CommitInfo)
	}
	kinds := map[string]int{}
	for _, e := range d.Said {
		kinds[e.Kind]++
		if e.Ref == "" || e.Title == "" || e.At == "" || e.Link == "" {
			t.Errorf("entry without ref, title, time or link: %+v", e)
		}
	}
	if kinds["knowledge"] != 3 || kinds["pain"] != 1 || kinds["comment"] != 1 || len(d.Said) != 5 {
		t.Errorf("entries by kind: %v", kinds)
	}
	for _, e := range d.Said {
		if e.Kind == "comment" && (e.ObjectKind != "request" || e.ObjectID != f.id["r-mia"] || !strings.Contains(e.Link, "#comment-")) {
			t.Errorf("comment entry: %+v", e)
		}
	}
	if d.Entries != len(d.Said) {
		t.Errorf("count %d differs from the list %d", d.Entries, len(d.Said))
	}
	// Link und Karte gehören zu genau diesem Commit.
	if !strings.HasSuffix(d.Link, "/ui/dw/p/commit/"+shaOf('1')) || d.Card.Kind != "Commit" || d.Card.Ref != shaOf('1')[:12] || d.Card.URL != d.Link || d.Card.Title != "erster Commit des PRs" {
		t.Errorf("link %q card %+v", d.Link, d.Card)
	}
	// Eingaben, die nichts bedeuten.
	f.expect(t, "mia", 400, "GET", commitURL("abc123"), nil)
	f.expect(t, "mia", 400, "GET", commitURL("zzzzzzzz"), nil)
	f.expect(t, "mia", 404, "GET", commitURL(shaOf('7')), nil)
	f.expect(t, "mia", 400, "GET", "/api/commits/"+shaOf('1'), nil)
}

func TestCommitPrefixThatMatchesTwoCommitsIsAnError(t *testing.T) {
	f := accessAPI(t, true)
	for _, sha := range []string{"abcdef0" + strings.Repeat("1", 33), "abcdef0" + strings.Repeat("2", 33)} {
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: "x " + sha[7:9], Body: "t", Person: "robin", Confidence: "trusted",
			Scope: scope.Axes{Project: accProject}, GitAnchor: &gitanchor.Anchor{Commit: sha, Branch: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	code, body := f.call(t, "mia", "GET", commitURL("abcdef0"), nil)
	if code != 400 || !strings.Contains(body, "commit_ambiguous") {
		t.Fatalf("ambiguous prefix: %d %s", code, body)
	}
	f.expect(t, "mia", 200, "GET", commitURL("abcdef01"), nil)
}

func TestPullCommitsListTheCommitsOfThePullRequestInOrder(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	list := decode[client.PullCommitsOut](t, f.expect(t, "gus", 200, "GET", "/api/pulls/12/commits?project="+url.QueryEscape(accProject), nil))
	if list.PR != 12 || len(list.Items) != 2 || list.Items[0].SHA != shaOf('1') || list.Items[1].SHA != shaOf('4') {
		t.Fatalf("commits of the pull request: %+v", list)
	}
	if list.Items[0].Entries != 4 || list.Items[1].Entries != 0 || list.Items[0].Title != "erster Commit des PRs" || !strings.HasSuffix(list.Link, "/pulls/12") {
		t.Errorf("guest view: %+v", list.Items)
	}
	// Ein PR, dessen Commits nie abgeglichen wurden, hat eine leere Liste, kein 404.
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: accProject, HeadRemote: accProject, Number: 20, Action: "opened", Head: "feat/q", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	empty := decode[client.PullCommitsOut](t, f.expect(t, "mia", 200, "GET", "/api/pulls/20/commits?project="+url.QueryEscape(accProject), nil))
	if len(empty.Items) != 0 {
		t.Errorf("empty: %+v", empty)
	}
	f.expect(t, "mia", 404, "GET", "/api/pulls/99/commits?project="+url.QueryEscape(accProject), nil)
	f.expect(t, "mia", 400, "GET", "/api/pulls/0/commits?project="+url.QueryEscape(accProject), nil)
}

func pathURL(path, extra string) string {
	return "/api/paths/commits?project=" + url.QueryEscape(accProject) + "&path=" + url.QueryEscape(path) + extra
}

func TestCommitsForAPathFollowTheOriginContextOfTheEntries(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	type pathAnswer struct {
		Path  string
		Total int
		Items []struct {
			SHA     string
			Kinds   []string
			Weight  int
			Matched []struct{ Kind, Title string }
		}
	}
	ask := func(who, path string, want int) pathAnswer {
		t.Helper()
		out := f.expect(t, who, want, "GET", pathURL(path, ""), nil)
		if want != 200 {
			return pathAnswer{}
		}
		return decode[pathAnswer](t, out)
	}
	// Eine Datei: der Commit, an dem der Eintrag mit diesem Pfad hängt.
	a := ask("mia", "internal/store/a.go", 200)
	if a.Path != "internal/store/a.go" || a.Total != 1 || a.Items[0].SHA != shaOf('1') || strings.Join(a.Items[0].Kinds, ",") != "write" || a.Items[0].Weight != 3 ||
		len(a.Items[0].Matched) != 1 || a.Items[0].Matched[0].Title != "geprüft auf feat/x" {
		t.Fatalf("file: %+v", a)
	}
	// Ein Ordner trägt alles darunter; ein Namensanfang ohne Segmentgrenze trifft nichts.
	dir := ask("mia", "internal/store/", 200)
	if dir.Total != 1 || strings.Join(dir.Items[0].Kinds, ",") != "write,read" || dir.Items[0].Weight != 4 {
		t.Fatalf("folder: %+v", dir)
	}
	if sub := ask("mia", "internal/store/sub", 200); sub.Total != 1 || strings.Join(sub.Items[0].Kinds, ",") != "read" {
		t.Fatalf("sub folder: %+v", sub)
	}
	if none := ask("mia", "internal/sto", 200); none.Total != 0 || len(none.Items) != 0 {
		t.Fatalf("a name start is not a path: %+v", none)
	}
	if none := ask("mia", "internal/store/a.g", 200); none.Total != 0 {
		t.Fatalf("a file name start is not a path: %+v", none)
	}
	// Das Zeichen % und _ stehen für sich.
	if none := ask("mia", "internal/%", 200); none.Total != 0 {
		t.Fatalf("wildcard: %+v", none)
	}
	// Der Eintrag des anderen Projekts mit demselben Pfad gehört nicht dazu: nur ein Commit, nur ein Eintrag.
	if all := ask("robin", "internal/store/a.go", 200); all.Total != 1 || len(all.Items[0].Matched) != 1 {
		t.Fatalf("another project leaked: %+v", all)
	}
	// Sichtbarkeit: der ungeprüfte Eintrag mit geheimem Pfad ist für den Gast nicht da.
	if guest := ask("gus", "secret/dir", 200); guest.Total != 0 {
		t.Fatalf("the guest sees a hidden entry's path: %+v", guest)
	}
	if member := ask("mia", "secret/dir", 200); member.Total != 1 || member.Items[0].SHA != shaOf('5') {
		t.Fatalf("the member: %+v", member)
	}
	// Eingaben.
	ask("mia", "../etc", 400)
	ask("mia", "", 400)
	ask("mia", "/abs/path", 400)
	f.expect(t, "mia", 400, "GET", "/api/paths/commits?path=x", nil)
}

// Fremder, ehemaliges Mitglied und Token ohne Anmeldung: nichts, und "gibt es nicht"
// sieht aus wie "darfst du nicht".
func TestCommitReadRightsAndOracle(t *testing.T) {
	f := accessAPI(t, true)
	f.seedCommits(t)
	paths := []string{commitsURL(""), commitURL(shaOf('1')), pathURL("internal/store", "")}
	for _, path := range paths {
		f.expect(t, "gus", 200, "GET", path, nil)
		noToken := req(t, "GET", f.srv.URL+path, "", nil)
		noToken.Body.Close()
		if noToken.StatusCode != 401 {
			t.Errorf("no token %s: %d", path, noToken.StatusCode)
		}
		unknown := strings.Replace(path, url.QueryEscape(accProject), url.QueryEscape("github.com/dw/none"), 1)
		a, ab := f.call(t, "nora", "GET", path, nil)
		u, ub := f.call(t, "nora", "GET", unknown, nil)
		if a != 404 || a != u || ab != ub {
			t.Errorf("%s: outsider %d %s, unknown project %d %s", path, a, ab, u, ub)
		}
	}
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(paths, "/api/pulls/12/commits?project="+url.QueryEscape(accProject)) {
		if code, out := f.call(t, "mia", "GET", path, nil); code != 404 {
			t.Errorf("former member %s: %d %s", path, code, out)
		}
	}
}

// Ein Objekt, das in ein anderes Projekt gezogen ist, gehört nicht mehr an die
// Commits des alten.
func TestAMovedIssueLeavesTheCommitsOfItsOldProject(t *testing.T) {
	f := accessAPI(t, true)
	d, err := f.st.CreateRequest(requestdomainInput("wandert", shaOf('6')))
	if err != nil {
		t.Fatal(err)
	}
	if got := decode[client.CommitDetailOut](t, f.expect(t, "mia", 200, "GET", commitURL(shaOf('6')), nil)); len(got.Said) != 1 || got.Said[0].Kind != "request" {
		t.Fatalf("before the move: %+v", got.Said)
	}
	f.expect(t, "robin", 200, "POST", fmt.Sprintf("/api/requests/%d/move", d.Request.ID), map[string]any{"project": accOther, "reason": "gehört dorthin"})
	// Der Commit hat dort keinen lesbaren Eintrag mehr: unbekannt, wie ein erfundener.
	f.expect(t, "mia", 404, "GET", commitURL(shaOf('6')), nil)
	if list := f.commitList(t, "mia", ""); list.Total != 0 {
		t.Errorf("the old project still lists the commit: %+v", list)
	}
}

// Ein Präfix wird nur gegen Commits aufgelöst, die der Betrachter lesen kann: ein
// zweiter, für ihn unsichtbarer Treffer macht ihn weder "mehrdeutig" noch sonst
// anders als bei einem Präfix, das nichts trifft.
func TestCommitPrefixIsResolvedOnlyAmongCommitsTheViewerCanSee(t *testing.T) {
	f := accessAPI(t, true)
	add := func(sha, title, confidence string) {
		t.Helper()
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "t", Person: "robin", Confidence: confidence,
			Scope: scope.Axes{Project: accProject}, GitAnchor: &gitanchor.Anchor{Commit: sha, Branch: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	shown, hidden, onlyHidden := "abcdef0"+strings.Repeat("1", 33), "abcdef0"+strings.Repeat("2", 33), "1234567"+strings.Repeat("3", 33)
	add(shown, "sichtbar", "trusted")
	add(hidden, "unsichtbar für Gäste", "staged")
	add(onlyHidden, "ebenfalls unsichtbar", "staged")

	// Das Mitglied sieht beide: mehrdeutig. Der Gast sieht einen: der Präfix trifft ihn.
	if code, body := f.call(t, "mia", "GET", commitURL("abcdef0"), nil); code != 400 || !strings.Contains(body, "commit_ambiguous") {
		t.Fatalf("member: %d %s", code, body)
	}
	d := decode[client.CommitDetailOut](t, f.expect(t, "gus", 200, "GET", commitURL("abcdef0"), nil))
	if d.SHA != shown || len(d.Said) != 1 || d.Said[0].Title != "sichtbar" {
		t.Fatalf("guest: %+v", d)
	}
	// Ein Präfix, das nur Unsichtbares trifft, sieht aus wie einer, der nichts trifft.
	hiddenCode, hiddenBody := f.call(t, "gus", "GET", commitURL("1234567"), nil)
	noneCode, noneBody := f.call(t, "gus", "GET", commitURL("7654321"), nil)
	if hiddenCode != 404 || hiddenCode != noneCode || hiddenBody != noneBody {
		t.Fatalf("oracle: hidden %d %s / none %d %s", hiddenCode, hiddenBody, noneCode, noneBody)
	}
}

// Unsichtbare Treffer eines Präfixes dürfen den sichtbaren nicht verdrängen:
// die Grenze gilt für die Kandidaten, nicht für das, was der Betrachter sieht.
func TestManyHiddenCommitsDoNotCrowdOutTheVisibleOneOfAPrefix(t *testing.T) {
	f := accessAPI(t, true)
	add := func(sha, title, confidence string) {
		t.Helper()
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "t", Person: "robin", Confidence: confidence,
			Scope: scope.Axes{Project: accProject}, GitAnchor: &gitanchor.Anchor{Commit: sha, Branch: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 450; i++ {
		add(fmt.Sprintf("abcdef0%03x", i)+strings.Repeat("0", 30), "unsichtbar", "staged")
	}
	shown := "abcdef0fff" + strings.Repeat("1", 30)
	add(shown, "sichtbar", "trusted")
	d := decode[client.CommitDetailOut](t, f.expect(t, "gus", 200, "GET", commitURL("abcdef0"), nil))
	if d.SHA != shown {
		t.Fatalf("guest: %+v", d)
	}
}
