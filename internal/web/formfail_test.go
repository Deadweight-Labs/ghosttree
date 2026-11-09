package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// followRefusal opens the page a refused form was sent back to. The test jar keeps no
// cookies, so the cookie of the answer (the note and the typed text, once) goes along by hand.
func followRefusal(t *testing.T, c *http.Client, base string, resp *http.Response) (int, string) {
	t.Helper()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || loc == "" {
		t.Fatalf("the answer %d is no redirect to the form", resp.StatusCode)
	}
	req, err := http.NewRequest(http.MethodGet, base+loc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == flashCookie {
			req.AddCookie(ck)
		}
	}
	got, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	return got.StatusCode, body(t, got)
}

type navigationTransport struct{ base http.RoundTripper }

func (n navigationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	base := n.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// asBrowser is the client as a browser sends a form: as a change of page. Only then does a
// refused form come back to its page (formNavigation); the other tests keep the plain answers.
func asBrowser(c *http.Client) *http.Client {
	copied := *c
	copied.Transport = navigationTransport{base: c.Transport}
	return &copied
}

func TestAFormFromAnotherOriginIsRefusedPlainAndWritesNoFieldsBack(t *testing.T) {
	e := ovEnv(t)
	post := func(origin string) *http.Response {
		form := url.Values{"csrf_token": {"stale"}, "mode": {"comment"}, "reason": {"hello there"}, backField: {"/ui/inbox"}}
		req, err := http.NewRequest(http.MethodPost, e.Base+"/ui/requests/1/close", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		resp, err := asBrowser(e.Owner).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	foreign := post("https://evil.example")
	foreign.Body.Close()
	if foreign.StatusCode != http.StatusForbidden || !strings.HasPrefix(foreign.Header.Get("Content-Type"), "text/plain") || len(foreign.Cookies()) != 0 {
		t.Errorf("a form from another origin = %d %q, cookies %v: want the plain refusal", foreign.StatusCode, foreign.Header.Get("Content-Type"), foreign.Cookies())
	}
	// The same page with an old token (the page was open too long) comes back with its text.
	own := post(e.Base)
	own.Body.Close()
	if own.StatusCode != http.StatusSeeOther || own.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("a stale form of this site = %d %q", own.StatusCode, own.Header.Get("Location"))
	}
	_, page := followRefusal(t, e.Owner, e.Base, own)
	if !strings.Contains(page, "hello there") || !strings.Contains(page, msg("err.form.expired")) {
		t.Errorf("the page does not carry the note and the text: %.200s", page)
	}
}

func TestATaskFormNamesNoRepositoryForATaskTheViewerCannotRead(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Hidden", Project: shellHiddenProject})
	task := tpTask(t, e, shellHiddenProject, req.Request.ID, humantask.CreateInput{Title: "Secret chore"})
	resp := tpPost(t, e, asBrowser(e.Member), "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(task.ID, 10)}, "back": {"task"}, "note": {strings.Repeat("x", taskNoteChars+1)}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(loc, "hidden") || strings.Contains(loc, strconv.FormatInt(task.ID, 10)) {
		t.Errorf("the refusal leads to %q, which names the hidden task or its repository", loc)
	}
}

// A guest reads; the pages offer no button that the server would refuse (#192).
func TestAGuestSeesNoWriteControlOnTheRepositoryPages(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Guest reads", Criteria: []string{"One"}})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Chore"})
	pages := []string{
		"/ui/x/shell/issues", "/ui/x/shell/issues/" + strconv.FormatInt(req.Request.ID, 10), "/ui/x/shell/runbooks",
		"/ui/x/shell/tasks", "/ui/x/shell/tasks/" + strconv.FormatInt(task.ID, 10), "/ui/x/shell/knowledge",
	}
	for _, path := range pages {
		code, page := fetchPage(t, e.Guest, e.Base+path)
		if code != http.StatusOK {
			continue
		}
		for _, write := range []string{`action="/ui/requests/`, `action="/ui/labels/`, `action="/ui/tasks/done`, `action="/ui/comments/`, `action="/ui/review/`, `/runbooks/new"`, `details class="cfm"`} {
			if strings.Contains(page, write) {
				t.Errorf("%s offers a guest %q", path, write)
			}
		}
	}
}
