package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type commentFixture struct {
	*accessAPIFixture
	targets map[string]int64
}

func newCommentFixture(t *testing.T) commentFixture {
	t.Helper()
	f := commentFixture{accessAPIFixture: accessAPI(t, true), targets: map[string]int64{}}
	f.targets["request"] = f.id["r-mia"]
	f.targets["idea"] = f.post(t, "mia", "/api/ideas", map[string]any{"project": accProject, "title": "Idee"}, "id")
	f.targets["pain"] = f.post(t, "mia", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz"}, "pain.id")
	return f
}

func (f commentFixture) add(t *testing.T, who, kind string, body string) (int, string) {
	t.Helper()
	return f.call(t, who, "POST", "/api/comments", map[string]any{"kind": kind, "id": f.targets[kind], "body": body})
}

func (f commentFixture) list(t *testing.T, who, kind string) (int, string) {
	t.Helper()
	return f.call(t, who, "GET", fmt.Sprintf("/api/comments?kind=%s&id=%d", kind, f.targets[kind]), nil)
}

func decodeComment(t *testing.T, body string) store.Comment {
	t.Helper()
	var c store.Comment
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return c
}

func decodeComments(t *testing.T, body string) []store.Comment {
	t.Helper()
	var c []store.Comment
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return c
}

func TestCommentsOnIssueIdeaAndPainWithAuthorAndTime(t *testing.T) {
	f := newCommentFixture(t)
	for _, kind := range []string{"request", "idea", "pain"} {
		code, out := f.add(t, "mia", kind, "Erster Kommentar zu "+kind)
		if code != 201 {
			t.Fatalf("%s: add = %d %s", kind, code, out)
		}
		first := decodeComment(t, out)
		if first.Person != "mia" || first.CreatedAt == "" || first.Body != "Erster Kommentar zu "+kind || first.ObjectKind != kind || first.ObjectID != f.targets[kind] || !first.CanEdit || !first.CanDelete {
			t.Errorf("%s: %+v", kind, first)
		}
		if code, out := f.add(t, "lena", kind, "Antwort"); code != 201 {
			t.Fatalf("%s: lena add = %d %s", kind, code, out)
		}
		_, body := f.list(t, "rex", kind)
		list := decodeComments(t, body)
		if len(list) != 2 || list[0].Person != "mia" || list[1].Person != "lena" || list[0].ID >= list[1].ID {
			t.Errorf("%s list: %+v", kind, list)
		}
		// rex (Mitglied, weder Autor noch Lead) liest, darf aber nichts ändern.
		if list[0].CanEdit || list[0].CanDelete {
			t.Errorf("%s: a stranger to the comment may %+v", kind, list[0])
		}
		// Kommentare der anderen Objekte stehen nicht in der Liste.
	}
	if _, body := f.list(t, "mia", "idea"); strings.Contains(body, "zu pain") || strings.Contains(body, "zu request") {
		t.Errorf("comments of another object in the list: %s", body)
	}
}

func TestCommentRightsGuestReadsStrangerAndFormerMemberSeeNothing(t *testing.T) {
	f := newCommentFixture(t)
	if code, out := f.add(t, "mia", "idea", "für alle Leser"); code != 201 {
		t.Fatal(code, out)
	}
	// Gast: liest, schreibt nicht.
	if code, out := f.list(t, "gus", "idea"); code != 200 || !strings.Contains(out, "für alle Leser") {
		t.Errorf("guest list = %d %s", code, out)
	}
	if code, out := f.add(t, "gus", "idea", "darf nicht"); code != 403 {
		t.Errorf("guest add = %d %s", code, out)
	}
	for _, c := range decodeComments(t, func() string { _, b := f.list(t, "gus", "idea"); return b }()) {
		if c.CanEdit || c.CanDelete {
			t.Errorf("guest may %+v", c)
		}
	}
	// Fremder (nicht in der Org): lesen und schreiben antworten wie bei einer
	// Nummer, die es nicht gibt.
	for _, kind := range []string{"request", "idea", "pain"} {
		codeL, bodyL := f.list(t, "nora", kind)
		codeA, bodyA := f.add(t, "nora", kind, "x")
		codeU, bodyU := f.call(t, "nora", "GET", "/api/comments?kind="+kind+"&id=999999", nil)
		codeUA, bodyUA := f.call(t, "nora", "POST", "/api/comments", map[string]any{"kind": kind, "id": 999999, "body": "x"})
		if codeL != 404 || codeA != 404 || codeU != 404 || codeUA != 404 {
			t.Errorf("%s: stranger %d %d, unknown %d %d", kind, codeL, codeA, codeU, codeUA)
		}
		if digits.ReplaceAllString(bodyL, "N") != digits.ReplaceAllString(bodyU, "N") || digits.ReplaceAllString(bodyA, "N") != digits.ReplaceAllString(bodyUA, "N") {
			t.Errorf("%s: hidden and unknown differ:\n%s\n%s\n%s\n%s", kind, bodyL, bodyU, bodyA, bodyUA)
		}
	}
	// Das Objekt eines anderen Projekts: auch für ein Mitglied hier unlesbar.
	otherPain := f.post(t, "robin", "/api/pains", map[string]any{"project": accOther, "title": "fremd"}, "pain.id")
	cOther := f.post(t, "robin", "/api/comments", map[string]any{"kind": "pain", "id": otherPain, "body": "intern"}, "id")
	if code, out := f.call(t, "mia", "GET", fmt.Sprintf("/api/comments?kind=pain&id=%d", otherPain), nil); code != 404 {
		t.Errorf("member of another project lists comments: %d %s", code, out)
	}
	for _, c := range []struct{ method, path string }{{"PATCH", "/api/comments/%d"}, {"DELETE", "/api/comments/%d"}, {"GET", "/api/comments/%d/versions"}} {
		hidden := f.call
		code, body := hidden(t, "mia", c.method, fmt.Sprintf(c.path, cOther), map[string]any{"body": "x"})
		codeU, bodyU := hidden(t, "mia", c.method, fmt.Sprintf(c.path, 999999), map[string]any{"body": "x"})
		if code != 404 || codeU != 404 || digits.ReplaceAllString(body, "N") != digits.ReplaceAllString(bodyU, "N") {
			t.Errorf("%s %s: hidden %d %s, unknown %d %s", c.method, c.path, code, body, codeU, bodyU)
		}
	}
	// Ehemaliges Mitglied.
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	if code, out := f.list(t, "mia", "idea"); code != 404 {
		t.Errorf("former member lists: %d %s", code, out)
	}
	if code, out := f.add(t, "mia", "idea", "ehemalig"); code != 404 {
		t.Errorf("former member writes: %d %s", code, out)
	}
	// Der Kommentar, den das ehemalige Mitglied schrieb, ist für es nicht mehr
	// zu ändern oder zu löschen.
	first := decodeComments(t, func() string { _, b := f.list(t, "robin", "idea"); return b }())[0]
	f.expect(t, "mia", 404, "PATCH", fmt.Sprintf("/api/comments/%d", first.ID), map[string]any{"body": "x"})
	f.expect(t, "mia", 404, "DELETE", fmt.Sprintf("/api/comments/%d", first.ID), nil)
}

func TestCommentEditOnlyByTheAuthorWithVersionsAndSoftDelete(t *testing.T) {
	f := newCommentFixture(t)
	_, out := f.add(t, "mia", "request", "Version eins")
	c := decodeComment(t, out)
	path := fmt.Sprintf("/api/comments/%d", c.ID)

	// Ändern: nur der Autor. Der Lead sieht den Kommentar, ändert ihn aber nicht.
	f.expect(t, "lena", 403, "PATCH", path, map[string]any{"body": "fremd"})
	f.expect(t, "robin", 403, "PATCH", path, map[string]any{"body": "fremd"})
	f.expect(t, "rex", 403, "PATCH", path, map[string]any{"body": "fremd"})
	f.expect(t, "gus", 403, "PATCH", path, map[string]any{"body": "fremd"})
	f.expect(t, "nora", 404, "PATCH", path, map[string]any{"body": "fremd"})
	edited := decodeComment(t, f.expect(t, "mia", 200, "PATCH", path, map[string]any{"body": "Version zwei"}))
	if edited.Body != "Version zwei" || edited.Revision != 2 || edited.EditedAt == "" {
		t.Errorf("edited: %+v", edited)
	}
	// Derselbe Text noch einmal ist keine neue Fassung.
	if again := decodeComment(t, f.expect(t, "mia", 200, "PATCH", path, map[string]any{"body": "Version zwei"})); again.Revision != 2 {
		t.Errorf("same text, new revision: %+v", again)
	}
	var versions []store.CommentVersion
	if err := json.Unmarshal([]byte(f.expect(t, "rex", 200, "GET", path+"/versions", nil)), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Body != "Version eins" || versions[1].Body != "Version zwei" {
		t.Errorf("versions: %+v", versions)
	}
	f.expect(t, "mia", 400, "PATCH", path, map[string]any{"body": "   "})
	f.expect(t, "mia", 400, "PATCH", path, map[string]any{"body": strings.Repeat("x", store.MaxCommentChars+1)})

	// Löschen: Autor und Lead; der andere Member, der Gast und der Fremde nicht.
	f.expect(t, "rex", 403, "DELETE", path, nil)
	f.expect(t, "gus", 403, "DELETE", path, nil)
	f.expect(t, "nora", 404, "DELETE", path, nil)
	del := decodeComment(t, f.expect(t, "lena", 200, "DELETE", path, nil))
	if !del.Deleted || del.Body != "" || del.DeletedBy != "lena" {
		t.Errorf("deleted: %+v", del)
	}
	// Weich: der Platzhalter bleibt in der Reihe, ohne Text, für jeden Leser;
	// die Fassungen geben nichts mehr her, der Text steht noch in der Datenbank.
	for _, who := range []string{"mia", "gus"} {
		_, body := f.list(t, who, "request")
		list := decodeComments(t, body)
		if len(list) != 1 || !list[0].Deleted || list[0].Body != "" || strings.Contains(body, "Version") || list[0].CanEdit || list[0].CanDelete {
			t.Errorf("%s sees %s", who, body)
		}
	}
	f.expect(t, "mia", 404, "GET", path+"/versions", nil)
	f.expect(t, "mia", 404, "PATCH", path, map[string]any{"body": "wieder da"})
	f.expect(t, "mia", 404, "DELETE", path, nil)
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM comment_versions WHERE comment_id=?`, c.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("versions kept in the database: %d %v", n, err)
	}
	// Die Fassungen sind angehängt, nie geändert.
	if _, err := f.st.DB().Exec(`UPDATE comment_versions SET body='x' WHERE comment_id=?`, c.ID); err == nil {
		t.Error("a version was rewritten")
	}
	if _, err := f.st.DB().Exec(`DELETE FROM comment_versions WHERE comment_id=?`, c.ID); err == nil {
		t.Error("a version was deleted")
	}
}

func TestCommentAuthorWhoBecameAGuestCannotEditOrDelete(t *testing.T) {
	f := newCommentFixture(t)
	_, out := f.add(t, "mia", "pain", "vor der Herabstufung")
	c := decodeComment(t, out)
	if err := f.st.SetProjectRole("person:1", accProject, "person:3", store.RoleGuest, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/comments/%d", c.ID)
	f.expect(t, "mia", 403, "PATCH", path, map[string]any{"body": "geändert"})
	f.expect(t, "mia", 403, "DELETE", path, nil)
	f.expect(t, "mia", 200, "GET", fmt.Sprintf("/api/comments?kind=pain&id=%d", f.targets["pain"]), nil)
}

// Der Git-Anker aus dem Header bleibt am Kommentar und an jeder Fassung; ein
// Kommentar aus dem Browser trägt statt eines Ankers die Kennzeichnung "web".
func TestCommentCarriesTheGitAnchorAndTheWebMark(t *testing.T) {
	f := newCommentFixture(t)
	c := client.New(config.Config{ServerURL: f.srv.URL, Token: f.tok["mia"]})
	const first, second = "aaaaaaaa1111111111111111111111111111aaaa", "bbbbbbbb2222222222222222222222222222bbbb"
	head := first
	c.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{Commit: head, Branch: "feat/x"} })
	made, err := c.AddComment("request", f.targets["request"], "mit Anker", 0)
	if err != nil {
		t.Fatal(err)
	}
	if made.GitAnchor == nil || made.GitAnchor.Commit != first || made.GitAnchor.Branch != "feat/x" || made.Via != "" {
		t.Fatalf("created: %+v", made)
	}
	head = second
	edited, err := c.EditComment(made.ID, "mit Anker, neu")
	if err != nil {
		t.Fatal(err)
	}
	if edited.GitAnchor == nil || edited.GitAnchor.Commit != second {
		t.Fatalf("edited: %+v", edited)
	}
	versions, err := c.CommentVersions(made.ID)
	if err != nil || len(versions) != 2 || versions[0].GitAnchor.Commit != first || versions[1].GitAnchor.Commit != second {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	// Ein Anker im Rumpf ist nie die Quelle.
	forged := f.expect(t, "mia", 201, "POST", "/api/comments", map[string]any{"kind": "request", "id": f.targets["request"], "body": "x",
		"git_anchor": map[string]any{"commit": "cccccccc3333333333333333333333333333cccc"}})
	if got := decodeComment(t, forged); got.GitAnchor != nil {
		t.Errorf("an anchor from the body: %+v", got.GitAnchor)
	}
	// Aus dem Browser: Store-Aufruf mit gitanchor.Web().
	pa := f.st.Access(store.Principal{ID: "person:3", Label: "mia"})
	web, err := pa.AddComment("idea", f.targets["idea"], "aus dem Browser", store.CommentActor{Person: "mia", GitAnchor: gitanchor.Web()})
	if err != nil {
		t.Fatal(err)
	}
	if web.Via != "web" || web.GitAnchor != nil {
		t.Fatalf("web comment: %+v", web)
	}
	list, _ := pa.Comments("idea", f.targets["idea"])
	if len(list) != 1 || list[0].Via != "web" {
		t.Errorf("web mark lost in the list: %+v", list)
	}
	if web, err = pa.EditComment(web.ID, "aus dem Browser, bearbeitet", store.CommentActor{Person: "mia", GitAnchor: gitanchor.Web()}); err != nil || web.Via != "web" || web.Revision != 2 {
		t.Errorf("web edit: %+v %v", web, err)
	}
}

// Ein Kommentar folgt dem Projekt seines Objekts: wird der Issue in ein anderes
// Projekt verschoben, gelten dort die Rechte, nicht die des alten Projekts, in
// dem der Kommentar geschrieben wurde.
func TestCommentRightsFollowTheObjectIntoAnotherProject(t *testing.T) {
	f := newCommentFixture(t)
	_, out := f.add(t, "mia", "request", "geschrieben im alten Projekt")
	c := decodeComment(t, out)
	path := fmt.Sprintf("/api/comments/%d", c.ID)
	// robin (Owner beider Projekte) verschiebt den Issue nach accOther, wo weder
	// mia noch lena eine Rolle haben.
	f.expect(t, "robin", 200, "POST", fmt.Sprintf("/api/requests/%d/move", f.targets["request"]), map[string]any{"project": accOther, "reason": "gehört dorthin"})
	for _, who := range []string{"mia", "lena", "rex"} {
		f.expect(t, who, 404, "PATCH", path, map[string]any{"body": "geändert"})
		f.expect(t, who, 404, "DELETE", path, nil)
		f.expect(t, who, 404, "GET", path+"/versions", nil)
		if code, _ := f.list(t, who, "request"); code != 404 {
			t.Errorf("%s lists comments of an issue of another project: %d", who, code)
		}
	}
	// Der Besitzer des neuen Projekts sieht ihn, mit dem neuen Projekt am Kommentar.
	code, body := f.list(t, "robin", "request")
	list := decodeComments(t, body)
	if code != 200 || len(list) != 1 || list[0].Project != accOther || !list[0].CanDelete || list[0].CanEdit {
		t.Errorf("owner of the new project: %d %+v", code, list)
	}
}
