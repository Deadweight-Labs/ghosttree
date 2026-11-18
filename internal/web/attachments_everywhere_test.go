package web

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func painPagePath(id int64) string  { return "/ui/x/shell/pains/" + strconv.FormatInt(id, 10) }
func issuePagePath(id int64) string { return "/ui/x/shell/issues/" + strconv.FormatInt(id, 10) }

func mustPain(t *testing.T, pa *store.ProjectAccess, project, title string) pain.Detail {
	t.Helper()
	d, err := pa.CreatePain(store.CreatePainInput{Project: project, Title: title, Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// REQ-671 AC-1 and AC-5: a pain and an issue show their images, whoever may work on them
// gets the form, a guest gets the images without the form and a refusal from the route.
func TestPainAndIssuePagesShowTheirImagesAndTheFormOnlyToThoseWhoMayWork(t *testing.T) {
	e, pa := listEnv(t)
	pn := mustPain(t, pa, shellProject, "Screenshot wanted")
	rq := rqMake(t, e.St, rqSpec{Title: "Needs a picture", Criteria: []string{"one"}})
	cases := []struct {
		name, route, page, owner string
		id                       int64
	}{
		{"pain", "/ui/pains/" + strconv.FormatInt(pn.Pain.ID, 10) + "/attach", painPagePath(pn.Pain.ID), "pain", pn.Pain.ID},
		{"issue", "/ui/requests/" + strconv.FormatInt(rq.Request.ID, 10) + "/attach", issuePagePath(rq.Request.ID), "request", rq.Request.ID},
	}
	data := pngBytes(t, 3, 2)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Before any image: the member has the form, the page names no image.
			_, before := fetchPage(t, e.Member, e.Base+c.page)
			for _, want := range []string{`id="images"`, `action="` + c.route + `"`, `name="owner" value="` + c.owner + `"`, `enctype="multipart/form-data"`, `data-attach`, `type="file"`, `/static/ui-attach.js`, `/static/ui-workshop.js`} {
				if !strings.Contains(before, want) {
					t.Errorf("the %s page lacks %q for a member", c.name, want)
				}
			}
			if strings.Contains(before, `class="ws-zoom"`) {
				t.Error("a picture is shown before one was attached")
			}
			// The browser claims HTML: what counts are the bytes.
			resp := upload(t, e, e.Member, c.route, map[string]string{"owner": c.owner, "alt": "The broken button"}, data, "text/html", true)
			if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=image") || !strings.HasPrefix(resp.Header.Get("Location"), c.page) {
				t.Fatalf("upload -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
			_, member := fetchPage(t, e.Member, e.Base+c.page+"?done=image")
			for _, want := range []string{"The image was attached.", `alt="The broken button"`, `class="ws-zoom"`, `<dialog class="ws-lb" id="ws-lb"`} {
				if !strings.Contains(member, want) {
					t.Errorf("the %s page lacks %q after the upload", c.name, want)
				}
			}
			src := attachSrcRE.FindStringSubmatch(member)
			if src == nil {
				t.Fatal("no picture address in the page")
			}
			// A guest reads the page with the image and gets the image, but has no form.
			_, guest := fetchPage(t, e.Guest, e.Base+c.page)
			if !strings.Contains(guest, `alt="The broken button"`) || strings.Contains(guest, `enctype="multipart/form-data"`) || strings.Contains(guest, "data-attach") {
				t.Errorf("a guest on the %s page: the picture must show, the form must not", c.name)
			}
			if got, err := e.Guest.Get(e.Base + src[1]); err != nil || got.StatusCode != http.StatusOK {
				t.Errorf("a guest reads the picture: %v %v", got, err)
			}
			// The route refuses a guest, and a refused upload leaves nothing behind.
			if r := upload(t, e, e.Guest, c.route, map[string]string{"owner": c.owner}, pngBytes(t, 5, 5), "image/png", true); r.StatusCode != http.StatusForbidden {
				t.Errorf("a guest uploads -> %d, want 403", r.StatusCode)
			}
			subject := map[string]string{"pain": idea.SubjectPain, "request": idea.SubjectRequest}[c.owner]
			if metas, err := tpOwner(e).AttachmentsOf(subject, c.id); err != nil || len(metas) != 1 || metas[0].Owner != c.owner {
				t.Errorf("stored pictures: %+v (%v), want the one of the member", metas, err)
			}
			// A refusal of the picture comes back as a banner on the page itself.
			bad := upload(t, e, e.Member, c.route, map[string]string{"owner": c.owner}, []byte("<svg onload=alert(1)/>"), "image/png", true)
			loc := bad.Header.Get("Location")
			if bad.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "fail=img_type") {
				t.Fatalf("an SVG -> %d %q", bad.StatusCode, loc)
			}
			_, failed := fetchPage(t, e.Member, e.Base+strings.TrimSuffix(loc, "#images"))
			if !strings.Contains(failed, "Only PNG, JPEG and WebP images can be attached.") {
				t.Errorf("the %s page does not tell why the picture was refused", c.name)
			}
			// Without a token the route does not answer.
			if r := upload(t, e, e.Member, c.route, map[string]string{"owner": c.owner}, data, "image/png", false); r.StatusCode != http.StatusForbidden {
				t.Errorf("without a token -> %d, want 403", r.StatusCode)
			}
		})
	}
}

// REQ-671 AC-5: the image of a pain nobody may read answers like a number that does not
// exist, and so does the upload route.
func TestImageOfAPainTheViewerMayNotReadAnswersLikeAnUnknownOne(t *testing.T) {
	e, pa := listEnv(t)
	hidden := mustPain(t, pa, shellHiddenProject, "Not for you")
	if _, err := pa.Attach(store.AttachInput{Subject: idea.SubjectPain, SubjectID: hidden.Pain.ID, Owner: idea.OwnerPain, Data: pngBytes(t, 2, 2), Actor: idea.Actor{Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	var src string
	if metas, err := pa.AttachmentsOf(idea.SubjectPain, hidden.Pain.ID); err != nil || len(metas) != 1 {
		t.Fatalf("%+v %v", metas, err)
	} else {
		src = attachmentSrc(metas[0].ID)
	}
	if got, err := e.Owner.Get(e.Base + src); err != nil || got.StatusCode != http.StatusOK {
		t.Fatalf("the owner reads the picture: %v %v", got, err)
	}
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "stranger": loginInteractive(t, e.Srv, e.St, "nora")} {
		read, _ := c.Get(e.Base + src)
		missing, _ := c.Get(e.Base + "/ui/attachments/9999")
		if read.StatusCode != http.StatusNotFound || missing.StatusCode != http.StatusNotFound {
			t.Errorf("%s: the picture of the hidden pain -> %d, a missing picture -> %d, want 404 for both", name, read.StatusCode, missing.StatusCode)
		}
		up := upload(t, e, c, "/ui/pains/"+strconv.FormatInt(hidden.Pain.ID, 10)+"/attach", map[string]string{"owner": "pain"}, pngBytes(t, 4, 4), "image/png", true)
		none := upload(t, e, c, "/ui/pains/987654/attach", map[string]string{"owner": "pain"}, pngBytes(t, 4, 4), "image/png", true)
		if up.StatusCode != http.StatusNotFound || none.StatusCode != http.StatusNotFound {
			t.Errorf("%s: upload to the hidden pain -> %d, to a missing one -> %d, want 404 for both", name, up.StatusCode, none.StatusCode)
		}
	}
}

// The idea page uses the same box and the same script as the pain and the issue page.
func TestIdeaPagePastesAndDropsThroughTheSameFormAsThePainPage(t *testing.T) {
	e, pa := listEnv(t)
	i, _ := workedIdea(t, pa, shellProject)
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	for _, want := range []string{`data-attach`, `name="owner" value="idea"`, `/static/ui-attach.js`, `/static/ui-workshop.js`} {
		if !strings.Contains(page, want) {
			t.Errorf("the idea page lacks %q", want)
		}
	}
	// One form per page takes the pasted picture; the forms of the variants do not.
	if n := strings.Count(page, "data-attach"); n != 1 {
		t.Errorf("%d forms marked data-attach, want 1", n)
	}
	_, guest := fetchPage(t, e.Guest, e.Base+ideaPath(i))
	if strings.Contains(guest, "data-attach") {
		t.Error("a guest gets the form that takes the pasted picture")
	}
	js, err := e.Owner.Get(e.Base + "/static/ui-attach.js")
	if err != nil || js.StatusCode != http.StatusOK {
		t.Fatalf("the script is not served: %v", err)
	}
	body, _ := io.ReadAll(js.Body)
	js.Body.Close()
	if !regexp.MustCompile(`addEventListener\("paste"`).Match(body) || !regexp.MustCompile(`addEventListener\("drop"`).Match(body) {
		t.Error("the script does not listen for paste and drop")
	}
}

// REQ-685 AC-1: the script of the page uploads in the background. Asked for JSON, the route does not redirect
// (a page that is loaded again takes a begun comment with it): it answers with the markup of the new picture, or,
// when the picture is refused, with the words the page would show, and nothing is attached.
func TestUploadAskedForJSONAnswersWithThePictureAndNeverRedirects(t *testing.T) {
	e, pa := listEnv(t)
	pn := mustPain(t, pa, shellProject, "Screenshot wanted")
	route := "/ui/pains/" + strconv.FormatInt(pn.Pain.ID, 10) + "/attach"
	fields := map[string]string{"owner": "pain", "alt": "The broken button"}

	resp, body := uploadAsking(t, e, e.Member, route, fields, pngBytes(t, 3, 2), "text/html", true, "application/json")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("upload -> %d %q, want 200 JSON (a redirect would load the page again): %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	var got struct {
		OK    bool   `json:"ok"`
		ID    int64  `json:"id"`
		HTML  string `json:"html"`
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("answer is no JSON: %v\n%s", err, body)
	}
	if !got.OK || got.ID == 0 || got.Owner != "pain" {
		t.Errorf("answer = %+v", got)
	}
	for _, want := range []string{`class="ws-zoom"`, `data-zoom`, `alt="The broken button"`, `src="/ui/attachments/` + strconv.FormatInt(got.ID, 10) + `"`} {
		if !strings.Contains(got.HTML, want) {
			t.Errorf("the markup of the picture lacks %q: %s", want, got.HTML)
		}
	}
	// The same markup is what a loaded page shows for it.
	_, page := fetchPage(t, e.Member, e.Base+painPagePath(pn.Pain.ID))
	if !strings.Contains(page, got.HTML) {
		t.Error("the picture of the answer is not the markup the page draws for it")
	}

	// A refused picture: the words of the page, status 422, nothing attached.
	bad, text := uploadAsking(t, e, e.Member, route, fields, []byte("not an image"), "image/png", true, "application/json")
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused picture -> %d, want 422: %s", bad.StatusCode, text)
	}
	var refused struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &refused); err != nil || refused.OK || !strings.Contains(refused.Message, "PNG, JPEG and WebP") {
		t.Errorf("a refused picture answers %q (%v), want the words of ideas.result.img_type", text, err)
	}
	none, text := uploadAsking(t, e, e.Member, route, fields, nil, "", true, "application/json")
	if none.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text, "Choose an image file first") {
		t.Errorf("no file -> %d %s", none.StatusCode, text)
	}
	// A guest is refused as before.
	if r, _ := uploadAsking(t, e, e.Guest, route, fields, pngBytes(t, 5, 5), "image/png", true, "application/json"); r.StatusCode != http.StatusForbidden {
		t.Errorf("a guest -> %d, want 403", r.StatusCode)
	}
	// Without the Accept header it stays the redirect of the form (no script).
	if r := upload(t, e, e.Member, route, fields, pngBytes(t, 4, 4), "image/png", true); r.StatusCode != http.StatusSeeOther {
		t.Errorf("the form without script -> %d, want 303", r.StatusCode)
	}
}
