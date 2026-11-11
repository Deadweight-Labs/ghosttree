package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var digits = regexp.MustCompile(`\d+`)

func createWithLinks(t *testing.T, f *accessAPIFixture, who string, extra map[string]any) (int, string) {
	t.Helper()
	body := map[string]any{"type": "feature", "title": "Kind", "project": accProject, "criteria": []string{"eins"}}
	for k, v := range extra {
		body[k] = v
	}
	return f.call(t, who, "POST", "/api/requests", body)
}

func requestCount(t *testing.T, f *accessAPIFixture) int {
	t.Helper()
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateRequestWithParentAndDerivedFromIsOneStep(t *testing.T) {
	f := accessAPI(t, true)
	parent := f.id["r-robin"]
	source := f.id["r-mia"]
	pn := f.post(t, "mia", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz"}, "pain.id")
	own := f.post(t, "mia", "/api/ideas", map[string]any{"project": accProject, "title": "Meine Idee"}, "id")
	foreign := f.post(t, "robin", "/api/ideas", map[string]any{"project": accProject, "title": "Idee von robin"}, "id")

	// An idea mia may read but not edit cannot be a source: tying a request to
	// it is a write on the idea, the same one LinkIdeaRequest asks the right for.
	before := requestCount(t, f)
	if code, out := createWithLinks(t, f, "mia", map[string]any{"derived_from": []string{fmt.Sprintf("IDEA-%d", foreign)}}); code != 403 {
		t.Fatalf("a read-only idea as a source = %d %s, want 403", code, out)
	}
	if got := requestCount(t, f); got != before {
		t.Fatalf("a refused call created %d issues", got-before)
	}
	code, out := createWithLinks(t, f, "mia", map[string]any{
		"parent":       fmt.Sprintf("REQ-%d", parent),
		"derived_from": []string{fmt.Sprintf("IDEA-%d", own), fmt.Sprintf("PAIN-%d", pn), fmt.Sprintf("REQ-%d", source)},
	})
	if code != 201 {
		t.Fatalf("create = %d %s", code, out)
	}
	var d requestdomain.Detail
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	id := d.Request.ID
	kinds := map[string]int64{}
	for _, r := range d.Relations {
		kinds[r.Kind] = r.OtherRequestID
	}
	if kinds["parent"] != parent || kinds["derived"] != source {
		t.Errorf("relations of the new request: %+v", d.Relations)
	}
	// Idee: verknüpft und angenommen; die fremde (nur lesbar) blieb unberührt.
	for ideaID, wantState := range map[int64]string{own: "accepted"} {
		var i idea.Idea
		if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", fmt.Sprintf("/api/ideas/%d", ideaID), nil)), &i); err != nil {
			t.Fatal(err)
		}
		if len(i.Requests) != 1 || i.Requests[0] != id || i.State != wantState {
			t.Errorf("idea %d: requests %v state %q, want [%d] %q", ideaID, i.Requests, i.State, id, wantState)
		}
	}
	// Pain: motiviert den neuen Auftrag.
	if body := f.expect(t, "mia", 200, "GET", fmt.Sprintf("/api/pains/%d", pn), nil); !strings.Contains(body, fmt.Sprintf(`"request_id":%d`, id)) {
		t.Errorf("pain does not motivate the request: %s", body)
	}
	// Die Verlaufszeile steht am Auftrag.
	acts := 0
	for _, a := range d.Activity {
		if a.Kind == "relation.added" {
			acts++
		}
	}
	if acts < 3 {
		t.Errorf("relation.added entries: %d in %+v", acts, d.Activity)
	}
}

func TestCreateRequestWithABadLinkCreatesNothing(t *testing.T) {
	f := accessAPI(t, true)
	pn := f.post(t, "mia", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz"}, "pain.id")
	before := requestCount(t, f)
	for name, extra := range map[string]map[string]any{
		"parent unknown":    {"parent": "REQ-99999"},
		"second source bad": {"derived_from": []string{fmt.Sprintf("PAIN-%d", pn), "IDEA-99999"}},
		"parent as an idea": {"parent": "IDEA-1"},
		"no reference":      {"derived_from": []string{"nonsense"}},
		"too many sources":  {"derived_from": manyRefs(21)},
	} {
		if code, out := createWithLinks(t, f, "mia", extra); code < 400 {
			t.Errorf("%s: created (%d %s)", name, code, out)
		}
	}
	if got := requestCount(t, f); got != before {
		t.Errorf("requests: %d, want %d: a refused link left an issue behind", got, before)
	}
	// Die Pain ist nicht verknüpft worden, auch nicht halb.
	if body := f.expect(t, "mia", 200, "GET", fmt.Sprintf("/api/pains/%d", pn), nil); strings.Contains(body, `"request_id"`) {
		t.Errorf("pain linked by a refused call: %s", body)
	}
}

func manyRefs(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("REQ-%d", i))
	}
	return out
}

// Ein Ziel, das es nicht gibt, eines in einem fremden Projekt und eines, das der
// Anlegende nicht lesen darf, antworten gleich; ein Gast legt nichts an, ein
// ehemaliges Mitglied auch nicht.
func TestCreateRequestLinksRightsAndOracle(t *testing.T) {
	f := accessAPI(t, true)
	otherReq := f.id["r-other"]
	otherIdea := f.post(t, "robin", "/api/ideas", map[string]any{"project": accOther, "title": "fremde Idee"}, "id")
	otherPain := f.post(t, "robin", "/api/pains", map[string]any{"project": accOther, "title": "fremder Schmerz"}, "pain.id")
	otherThread := f.post(t, "robin", "/api/threads?by_role=1", map[string]any{"project": accOther, "title": "fremder Thread"}, "id")
	type linkCase struct {
		key, hidden, unknown string
	}
	cases := map[string]linkCase{
		"parent":  {"parent", fmt.Sprintf("REQ-%d", otherReq), "REQ-99999"},
		"request": {"derived_from", fmt.Sprintf("REQ-%d", otherReq), "REQ-99999"},
		"idea":    {"derived_from", fmt.Sprintf("IDEA-%d", otherIdea), "IDEA-99999"},
		"pain":    {"derived_from", fmt.Sprintf("PAIN-%d", otherPain), "PAIN-99999"},
		"thread":  {"derived_from", fmt.Sprintf("thread:%d", otherThread), "thread:99999"},
	}
	body := func(c linkCase, ref string) map[string]any {
		if c.key == "parent" {
			return map[string]any{"parent": ref}
		}
		return map[string]any{"derived_from": []string{ref}}
	}
	before := requestCount(t, f)
	for name, c := range cases {
		codeH, bodyH := createWithLinks(t, f, "mia", body(c, c.hidden))
		codeU, bodyU := createWithLinks(t, f, "mia", body(c, c.unknown))
		if codeH != 404 || codeU != 404 {
			t.Errorf("%s: hidden %d %s, unknown %d %s", name, codeH, bodyH, codeU, bodyU)
			continue
		}
		// Bis auf die genannte Nummer ist die Antwort dieselbe.
		same := func(s string) string { return digits.ReplaceAllString(s, "N") }
		if same(bodyH) != same(bodyU) {
			t.Errorf("%s: a hidden object answers differently from an unknown one:\n%s\n%s", name, bodyH, bodyU)
		}
	}
	if got := requestCount(t, f); got != before {
		t.Errorf("a refused call created %d issues", got-before)
	}
	// Gast: legt nichts an.
	if code, _ := createWithLinks(t, f, "gus", map[string]any{"parent": fmt.Sprintf("REQ-%d", f.id["r-mia"])}); code != 403 {
		t.Errorf("guest = %d, want 403", code)
	}
	// Ehemaliges Mitglied.
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	if code, out := createWithLinks(t, f, "mia", map[string]any{"parent": fmt.Sprintf("REQ-%d", f.id["r-mia"])}); code < 400 {
		t.Errorf("former member created an issue: %d %s", code, out)
	}
	if got := requestCount(t, f); got != before {
		t.Errorf("requests after the refused calls: %d, want %d", got, before)
	}
}

// Ein Thread als Quelle: das Konto muss ihn ändern dürfen, wie bei
// CoordAccess.LinkThread. Ein Token ohne Mitgliedschaft im Raum darf es nicht, ein
// Browser-Nutzer mit Projektrolle schon; ein fremder Thread bleibt tabu.
func TestCreateRequestFromAThreadNeedsTheRightToChangeIt(t *testing.T) {
	f := accessAPI(t, true)
	own := f.post(t, "mia", "/api/threads?by_role=1", map[string]any{"project": accProject, "title": "Diskussion"}, "id")
	foreign := f.post(t, "lena", "/api/threads?by_role=1", map[string]any{"project": accProject, "title": "Thread von lena"}, "id")
	before := requestCount(t, f)
	// Über ein Token: mia ist kein Mitglied des Projektraums, das Anlegen scheitert.
	if code, out := createWithLinks(t, f, "mia", map[string]any{"derived_from": []string{fmt.Sprintf("thread:%d", own)}}); code != 404 {
		t.Errorf("token without room membership = %d %s", code, out)
	}
	if got := requestCount(t, f); got != before {
		t.Fatalf("a refused thread link left %d issues behind", got-before)
	}
	create := func(p store.Principal, ref string) (requestdomain.Detail, error) {
		pa := f.st.Access(p)
		links, err := pa.ResolveRequestLinks(accProject, "", []string{ref})
		if err != nil {
			return requestdomain.Detail{}, err
		}
		return f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Aus Thread", Person: p.Label,
			Scope: scope.Axes{Project: accProject}}, Links: links, AuthorID: 3})
	}
	web := store.Principal{ID: "person:3", Label: "mia", TokenKind: store.WebSessionKind}
	d, err := create(web, fmt.Sprintf("thread:%d", own))
	if err != nil {
		t.Fatalf("browser session, own thread: %v", err)
	}
	links, err := f.st.ThreadLinks(own)
	if err != nil || len(links) != 1 || links[0].Kind != "request" || links[0].ID != d.Request.HumanID() {
		t.Errorf("thread links: %+v %v", links, err)
	}
	// Der Thread eines anderen: auch mit Projektrolle nicht (nur Autor und Raumleitung ändern ihn).
	before = requestCount(t, f)
	if _, err := create(web, fmt.Sprintf("thread:%d", foreign)); err == nil {
		t.Error("mia linked lena's thread")
	}
	if got := requestCount(t, f); got != before {
		t.Errorf("a refused thread link left %d issues behind", got-before)
	}
	// Ein Fremder und ein Gast kommen gar nicht erst an den Thread.
	for _, p := range []store.Principal{{ID: "person:6", Label: "nora", TokenKind: store.WebSessionKind}, {ID: "person:5", Label: "gus", TokenKind: store.WebSessionKind}} {
		if _, err := create(p, fmt.Sprintf("thread:%d", own)); err == nil {
			t.Errorf("%s linked a thread", p.Label)
		}
	}
}
