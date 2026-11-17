package web

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-687 AC-1 (N16): a picture on an issue is a tile that names itself, ATT-n, the name a text writes as
// ![](ATT-n), and carries the button that puts that line into the text field (the script shows it).
func TestPictureTileNamesItsAttNumberAndOffersToInsertIt(t *testing.T) {
	e, _ := listEnv(t)
	rq := rqMake(t, e.St, rqSpec{Title: "Needs a picture", Criteria: []string{"one"}})
	route := "/ui/requests/" + strconv.FormatInt(rq.Request.ID, 10) + "/attach"
	if r := upload(t, e, e.Member, route, map[string]string{"owner": "request", "alt": "The broken button"}, pngBytes(t, 3, 2), "image/png", true); r.StatusCode != 303 {
		t.Fatalf("upload -> %d", r.StatusCode)
	}
	_, page := fetchPage(t, e.Member, e.Base+issuePagePath(rq.Request.ID))
	src := attachSrcRE.FindStringSubmatch(page)
	if src == nil {
		t.Fatal("no picture in the page")
	}
	n := strings.TrimPrefix(src[1], "/ui/attachments/")
	for _, want := range []string{
		`<figure class="ws-fig">`,
		`<code class="ws-att">ATT-` + n + `</code>`,
		`data-att-insert="ATT-` + n + `" data-desc="The broken button"`,
		"Insert into text",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the picture tile lacks %q", want)
		}
	}
	// What the script of the page gets after an upload in the background is the same tile.
	_, body := uploadAsking(t, e, e.Member, route, map[string]string{"owner": "request"}, pngBytes(t, 4, 4), "image/png", true, "application/json")
	if !strings.Contains(body, `data-att-insert=\"ATT-`) || !strings.Contains(body, `ws-att`) {
		t.Errorf("the answer of a background upload carries no tile: %s", body)
	}
}

// moveForm is the form of the "Move" panel of an issue page ("" when the page has none).
func moveForm(page string) string {
	i := strings.Index(page, `action="/ui/requests/`)
	for i >= 0 {
		rest := page[i:]
		end := strings.Index(rest, "</form>")
		if end < 0 {
			return ""
		}
		if strings.Contains(rest[:end], `/move"`) {
			return rest[:end]
		}
		next := strings.Index(rest[len(`action="`):], `action="/ui/requests/`)
		if next < 0 {
			return ""
		}
		i += len(`action="`) + next
	}
	return ""
}

// REQ-687 AC-2 (N18): the move panel offers the other projects only. Where there is no other one, the page has neither
// the button nor the form (an entry that moves to where it is changes nothing and was refused with a 409).
func TestMoveOffersOnlyOtherProjectsAndNothingWhenThereIsNone(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Misfiled request"})
	_, page := fetchPage(t, e.Owner, e.Base+rqPath(d.Request.ID, ""))
	form := moveForm(page)
	if form == "" {
		t.Fatal("the owner has a second project but the page offers no move")
	}
	if !strings.Contains(form, `value="`+shellHiddenProject+`"`) {
		t.Errorf("the other project is not offered:\n%s", form)
	}
	if strings.Contains(form, `value="`+shellProject+`"`) {
		t.Errorf("the project the issue lies in is offered as the place to move it to:\n%s", form)
	}
	if !strings.Contains(form, `value="`+shellHiddenProject+`" selected`) {
		t.Errorf("the only target is not preselected:\n%s", form)
	}

	// The same issue once it lies in the other project: now the first one is the target.
	if resp := rqPost(t, e.shellEnv, e.Owner, d.Request.ID, "/move", url.Values{"project": {shellHiddenProject}, "reason": {"wrong repo"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("move -> %d", resp.StatusCode)
	}
	_, page = fetchPage(t, e.Owner, e.Base+rqPath(d.Request.ID, ""))
	if form := moveForm(page); !strings.Contains(form, `value="`+shellProject+`"`) || strings.Contains(form, `value="`+shellHiddenProject+`"`) {
		t.Errorf("after the move the targets are wrong:\n%s", form)
	}
}

func TestMoveIsNotOfferedWhenTheViewerMayWriteToOneProjectOnly(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Stays here"})
	// The lead may edit the issue (the Edit button is there) but writes to the one project only: no other place to move to.
	_, page := fetchPage(t, e.Lead, e.Base+rqPath(d.Request.ID, ""))
	if !strings.Contains(page, "panel=edit") {
		t.Fatal("the lead cannot edit the issue: the test would show nothing")
	}
	if strings.Contains(page, "panel=move") || moveForm(page) != "" {
		t.Error("a viewer with no other project to move to is offered the move")
	}
}

// REQ-687 AC-2 (N19): when "Done" is shut, the page says why in words beside the status row: how many of the
// requirements are still open. Once all are met, the note is gone and "Done" can be chosen.
func TestDoneNamesWhatKeepsItShut(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Two to meet", Criteria: []string{"first", "second"}})
	_, page := fetchPage(t, e.Owner, e.Base+rqPath(d.Request.ID, ""))
	if !strings.Contains(page, `<span class="cx-note">Done is possible once all requirements are met (2 of 2 still open)</span>`) {
		t.Errorf("the shut Done has no reason in words:\n%.400s", page[strings.Index(page, `cx-st`):])
	}
	if !strings.Contains(page, `class="cx-opt off"`) {
		t.Error("Done is not shown as shut")
	}
	for _, c := range d.Criteria {
		if resp := rqPost(t, e.shellEnv, e.Owner, d.Request.ID, "/criteria/"+strconv.FormatInt(c.ID, 10), url.Values{"state": {"met"}, "evidence_kind": {"url"}, "evidence_ref": {"https://example.com/x"}}); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("meet criterion -> %d", resp.StatusCode)
		}
	}
	_, page = fetchPage(t, e.Owner, e.Base+rqPath(d.Request.ID, ""))
	if strings.Contains(page, "cx-note") {
		t.Error("the note stays although every requirement is met")
	}
}

// REQ-687 AC-2 (N19): whoever may make a label finds the way: a link under the labels of an issue, and the page it
// leads to works for a member (it was a 404 for everybody but the leads), without the parts that are for leads.
func TestMemberFindsTheWayToMakeALabel(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Needs a label"})
	way := `href="` + repoHref(shellProject) + `/settings#labels"`
	_, page := fetchPage(t, e.Member, e.Base+rqPath(d.Request.ID, ""))
	if !strings.Contains(page, `class="lbnew"`) || !strings.Contains(page, way) || !strings.Contains(page, "Create a label") {
		t.Errorf("the labels of the issue carry no way to make one:\n%s", page[strings.Index(page, `id="labels"`):][:600])
	}
	code, settings := fetchPage(t, e.Member, e.Base+repoHref(shellProject)+"/settings")
	if code != http.StatusOK || !strings.Contains(settings, `action="/ui/labels/create"`) {
		t.Fatalf("the member does not get the page that makes labels: %d", code)
	}
	if strings.Contains(settings, `id="access"`) || strings.Contains(settings, "/ui/labels/") && strings.Contains(settings, "/update") {
		t.Error("the member sees what is for the leads (access section, editing of labels)")
	}
	if code, _ := fetchPage(t, e.Guest, e.Base+repoHref(shellProject)+"/settings"); code != http.StatusNotFound {
		t.Errorf("a guest on the label page -> %d, want 404", code)
	}
	_, lead := fetchPage(t, e.Lead, e.Base+repoHref(shellProject)+"/settings")
	if !strings.Contains(lead, `id="access"`) {
		t.Error("the lead lost the access section")
	}
	_, guestIssue := fetchPage(t, e.Guest, e.Base+rqPath(d.Request.ID, ""))
	if strings.Contains(guestIssue, `class="lbnew"`) {
		t.Error("a guest is offered to make a label")
	}
}

// REQ-687 AC-3 (N21): review, context and agents of the chosen repository carry its band (header with the repository
// and its tabs), also when the repository is the remembered choice and the address does not name it. With "all
// projects" they are the global pages and carry none.
func TestReviewContextAndAgentsOfTheChosenRepoCarryItsBand(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	c := remembering(t, base, owner)
	fetchPage(t, c, base+"/ui/knowledge?project="+shellProject)
	for _, path := range []string{"/ui/review", "/ui/context", "/ui/agents"} {
		_, page := fetchPage(t, c, base+path)
		for _, want := range []string{`class="rband`, `data-tab="issues"`, `data-tab="code"`, `data-suggest-project="` + shellProject + `"`} {
			if !strings.Contains(page, want) {
				t.Errorf("GET %s lacks %s: the repository the page is about has no band", path, want)
			}
		}
	}
	// The agents page keeps its own tabs under the band, and they keep the repository.
	_, agents := fetchPage(t, c, base+"/ui/agents")
	if !strings.Contains(agents, `href="/ui/sessions?project=`+url.QueryEscape(shellProject)+`"`) {
		t.Errorf("the tabs of the agents page are gone or lose the repository (%d bytes)", len(agents))
	}
	// All projects: no band.
	for _, path := range []string{"/ui/review?project=", "/ui/agents?project="} {
		if _, page := fetchPage(t, c, base+path); strings.Contains(page, `class="rband`) {
			t.Errorf("GET %s with all projects carries the band of a repository", path)
		}
	}
}

// REQ-687 AC-3 (N21): the context view stands on the repository the viewer came from instead of asking which one.
func TestContextStandsOnTheRepoTheViewerCameFrom(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	noFollow := &http.Client{Jar: owner.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(referer string) (*http.Response, string) {
		req, err := http.NewRequest(http.MethodGet, base+"/ui/context", nil)
		if err != nil {
			t.Fatal(err)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	resp, _ := get(base + "/ui/x/shell/issues")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "project="+url.QueryEscape(shellProject)) {
		t.Errorf("from a page of the repository: %d -> %q, want the context of that repository", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := get(base + "/ui/x/hidden"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), url.QueryEscape(shellHiddenProject)) {
		t.Errorf("from the other repository: %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Without a page of a repository behind the visit there is nothing to preselect: the list of repositories stays.
	for _, ref := range []string{"", base + "/ui/inbox", "https://elsewhere.example/ui/x/shell/issues"} {
		if resp, body := get(ref); resp.StatusCode != http.StatusOK || !strings.Contains(body, `class="row opt-row" href="?project=`) {
			t.Errorf("referer %q: %d, want the list to pick from", ref, resp.StatusCode)
		}
	}
}

// REQ-687 AC-4 (N23): what is written about a folder belongs to the files in it. A pitfall that names
// internal/spool shows at internal/spool/spool.go (the file page, and the counters of the folder list).
func TestKnowledgeOfAFolderShowsAtItsFiles(t *testing.T) {
	e := ovEnv(t)
	put := func(g store.GhostFile) {
		t.Helper()
		g.Project = shellProject
		if _, err := e.St.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	put(store.GhostFile{Path: "internal", Kind: "dir", Description: "All server code."})
	put(store.GhostFile{Path: "internal/spool", Kind: "dir", Description: "Handover between the trust zones."})
	put(store.GhostFile{Path: "internal/spool/spool.go", Kind: "file", Description: "Takes a file from the spool."})
	put(store.GhostFile{Path: "internal/other/other.go", Kind: "file", Description: "Something else."})
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Refuse symlinks in spool", Body: "b", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: "trusted",
		OriginContext: &originctx.Context{Paths: []originctx.Path{{Path: "internal/spool", Kind: originctx.KindNamed, Weight: 10}}}}); err != nil {
		t.Fatal(err)
	}
	_, dir := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tree/main/internal/spool")
	row := dir[strings.Index(dir, "spool.go"):]
	if end := strings.Index(row, "</div>"); end > 0 {
		row = row[:end]
	}
	if !strings.Contains(dir, `title="1 pitfall"`) {
		t.Errorf("the file in the folder the pitfall names shows no pitfall in the folder list:\n%s", dir)
	}
	_, blob := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/main/internal/spool/spool.go")
	for _, want := range []string{"Refuse symlinks in spool", "Area: internal/spool"} {
		if !strings.Contains(blob, want) {
			t.Errorf("the file page lacks %q", want)
		}
	}
	// A file elsewhere is not touched by it.
	_, other := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/main/internal/other/other.go")
	if strings.Contains(other, "Refuse symlinks in spool") {
		t.Error("the pitfall of internal/spool shows at a file in another folder")
	}
}

// REQ-687 AC-4 (N23): the context view names the described files and folders, under what an agent is given at the start.
func TestContextViewListsTheDescribedPaths(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.PutGhostFile(store.GhostFile{Project: shellProject, Path: "internal/spool/spool.go", Kind: "file", Description: "Takes a file from the spool."}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.PutGhostFile(store.GhostFile{Project: shellProject, Path: "internal/blank.go", Kind: "file", Description: ""}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/context")
	for _, want := range []string{"Descriptions of files and folders", "internal/spool/spool.go", "Takes a file from the spool.", `href="/ui/x/shell/blob/main/internal/spool/spool.go"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the context view lacks %q", want)
		}
	}
	if strings.Contains(page, "internal/blank.go") {
		t.Error("a path without a description is listed as described")
	}
}

// REQ-687 AC-4 (N25): the field that makes a project has a label, the empty state does not say the opposite of the
// form below it, and the project says in words who sees it.
func TestProjectFormHasALabelAndTheEmptyStateAgreesWithIt(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	resp, _ := anna.Get(base + "/ui/orgs?org=alpha")
	page := body(t, resp)
	for _, want := range []string{`<div class="fl grow"><label for="pnew-name">Repository address or name</label>`, `placeholder="github.com/owner/name"`, "Create one below, or connect an agent in a repo"} {
		if !strings.Contains(page, want) {
			t.Errorf("the projects of an empty organization lack %q", want)
		}
	}
	if strings.Contains(page, "A project appears once an agent runs in a repo") {
		t.Error("the empty state says a project only appears with an agent, above a form that makes one")
	}
	if r := postOrg(t, alice, base, "/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {"github.com/nurrobin/deploy-hook"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", r.StatusCode)
	}
	resp, _ = alice.Get(base + "/ui/orgs?org=alpha")
	if page := body(t, resp); !strings.Contains(page, "Only people with a role here see it") {
		t.Error("a private project does not say who sees it")
	}
	// The head of the repository names the organization that holds it, not the owner on the host.
	resp, _ = alice.Get(base + repoHref("github.com/nurrobin/deploy-hook"))
	head := body(t, resp)
	if !strings.Contains(head, `<a class="org" href="/ui/alpha">Alpha</a>`) || strings.Contains(head, `>nurrobin</a>`) {
		t.Errorf("the head of the repository does not name the organization:\n%.600s", head[strings.Index(head, `class="hcrumb"`):])
	}
}
