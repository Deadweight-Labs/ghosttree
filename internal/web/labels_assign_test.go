package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func labelNames(t *testing.T, e shellEnv, kind string, id int64) []string {
	t.Helper()
	list, err := tpOwner(e).LabelsOf(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range list {
		out = append(out, l.Name)
	}
	return out
}

// Labels lassen sich an Issue, Pain und Idee im Web setzen und entfernen, ab
// member, mit CSRF-Token; ein Gast sieht sie und bekommt kein Formular (REQ-600 K10).
func TestLabelsAreSetAndRemovedOnIssuePainAndIdeaFromTheSidebar(t *testing.T) {
	e := ovEnv(t)
	owner := tpOwner(e)
	for _, n := range []string{"ui", "git"} {
		if _, err := owner.CreateLabel(store.LabelInput{Project: shellProject, Name: n, Color: "0969da", By: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	req := rqMake(t, e.St, rqSpec{Title: "Needs a label"})
	pn, err := owner.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Hard to label", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := owner.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Label it", Actor: store.IdeaActorFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind string
		id   int64
		page string
	}{
		{"request", req.Request.ID, "/ui/x/shell/issues/" + itoa(req.Request.ID)},
		{"pain", pn.Pain.ID, "/ui/x/shell/pains/" + itoa(pn.Pain.ID)},
		{"idea", id.ID, "/ui/x/shell/ideas/" + itoa(id.ID)},
	} {
		_, page := fetchPage(t, e.Member, e.Base+c.page)
		for _, want := range []string{`action="/ui/labels/assign"`, `name="csrf_token"`, `<option value="ui">`, `<option value="git">`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s page lacks %q", c.kind, want)
			}
		}
		resp := postForm(t, e, e.Member, "/ui/labels/assign", url.Values{"kind": {c.kind}, "id": {itoa(c.id)}, "add": {"ui", "git"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), c.page) {
			t.Fatalf("%s: set -> %d %q", c.kind, resp.StatusCode, resp.Header.Get("Location"))
		}
		if got := labelNames(t, e, c.kind, c.id); len(got) != 2 {
			t.Fatalf("%s: labels after setting: %v", c.kind, got)
		}
		_, page = fetchPage(t, e.Guest, e.Base+c.page)
		if !strings.Contains(page, ">ui<") || strings.Contains(page, `action="/ui/labels/assign"`) {
			t.Errorf("%s: a guest must see the labels without the form", c.kind)
		}
		_, page = fetchPage(t, e.Member, e.Base+c.page)
		if !strings.Contains(page, `name="remove" value="ui"`) {
			t.Errorf("%s: no button takes a label off", c.kind)
		}
		resp = postForm(t, e, e.Member, "/ui/labels/assign", url.Values{"kind": {c.kind}, "id": {itoa(c.id)}, "remove": {"ui"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: remove -> %d", c.kind, resp.StatusCode)
		}
		if got := labelNames(t, e, c.kind, c.id); len(got) != 1 || got[0] != "git" {
			t.Errorf("%s: labels after removing: %v", c.kind, got)
		}
	}
}

func TestLabelAssignRefusesGuestsStrangersMissingTokensAndForgedInput(t *testing.T) {
	e := ovEnv(t)
	owner := tpOwner(e)
	if _, err := owner.CreateLabel(store.LabelInput{Project: shellProject, Name: "ui", Color: "0969da", By: "alice"}); err != nil {
		t.Fatal(err)
	}
	req := rqMake(t, e.St, rqSpec{Title: "Guarded"})
	form := func() url.Values {
		return url.Values{"kind": {"request"}, "id": {itoa(req.Request.ID)}, "add": {"ui"}}
	}
	if resp := postForm(t, e, e.Guest, "/ui/labels/assign", form()); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a guest sets a label -> %d, want 403", resp.StatusCode)
	}
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	got := postForm(t, e, stranger, "/ui/labels/assign", form())
	missing := postForm(t, e, stranger, "/ui/labels/assign", url.Values{"kind": {"request"}, "id": {"9999"}, "add": {"ui"}})
	if got.StatusCode != http.StatusNotFound || missing.StatusCode != http.StatusNotFound {
		t.Errorf("stranger -> %d, a missing issue -> %d, want 404 for both", got.StatusCode, missing.StatusCode)
	}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/labels/assign", form())
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a token -> %d, want 403", resp.StatusCode)
	}
	for name, f := range map[string]url.Values{
		"unknown kind":  {"kind": {"knowledge"}, "id": {itoa(req.Request.ID)}, "add": {"ui"}},
		"unknown label": {"kind": {"request"}, "id": {itoa(req.Request.ID)}, "add": {"nope"}},
		"nothing":       {"kind": {"request"}, "id": {itoa(req.Request.ID)}},
		"bad id":        {"kind": {"request"}, "id": {"x"}, "add": {"ui"}},
	} {
		if r := postForm(t, e, e.Member, "/ui/labels/assign", f); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", name, r.StatusCode)
		}
	}
	if got := labelNames(t, e, "request", req.Request.ID); len(got) != 0 {
		t.Errorf("a refused request set labels: %v", got)
	}
}
