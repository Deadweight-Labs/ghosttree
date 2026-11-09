package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestIdeaPageShowsTheWishAndDismissesWithAReason(t *testing.T) {
	e, pa := listEnv(t)
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Voice in the inbox", Body: "I want to answer by voice.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := pa.CreateLabel(store.LabelInput{Project: shellProject, Name: "agenten-ux", Color: "f9d0c4", By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetLabels("idea", i.ID, []string{lb.Name}, nil); err != nil {
		t.Fatal(err)
	}
	path := "/ui/x/shell/ideas/" + strconv.FormatInt(i.ID, 10)
	code, page := fetchPage(t, e.Owner, e.Base+path)
	if code != http.StatusOK {
		t.Fatalf("idea page = %d", code)
	}
	for _, want := range []string{"Voice in the inbox", "I want to answer by voice.", "wished for this", `class="rband`, "agenten-ux", `action="/ui/ideas/` + strconv.FormatInt(i.ID, 10) + `/state"`, "Dismiss idea"} {
		if !strings.Contains(page, want) {
			t.Errorf("idea page lacks %q", want)
		}
	}
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/ideas/"+strconv.FormatInt(i.ID, 10)); code != http.StatusNotFound {
		t.Errorf("idea under a foreign project = %d", code)
	}
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/ideas/9999"); code != http.StatusNotFound {
		t.Errorf("unknown idea = %d", code)
	}
	_, guest := fetchPage(t, e.Guest, e.Base+path)
	if strings.Contains(guest, `/state"`) {
		t.Error("a guest gets the state field")
	}
	post := func(form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
		return sameOriginPostForm(t, e.Owner, e.Base+"/ui/ideas/"+strconv.FormatInt(i.ID, 10)+"/state", form)
	}
	resp := post(url.Values{"mode": {"dismissed"}, "reason": {" "}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=reason_required") {
		t.Fatalf("dismiss without a reason -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, failed := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(failed, "Say why the idea is dismissed.") {
		t.Error("the refusal is not shown")
	}
	resp = post(url.Values{"mode": {"dismissed"}, "reason": {"Out of scope for now"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(resp.Header.Get("Location"), "?done=state") {
		t.Fatalf("dismiss -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, after := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	for _, want := range []string{"State saved.", "Dismissed because", "Out of scope for now", "Reopen idea"} {
		if !strings.Contains(after, want) {
			t.Errorf("after dismissing the page lacks %q", want)
		}
	}
	resp = post(url.Values{"mode": {"accepted"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("accepting through the field -> %d, want 400", resp.StatusCode)
	}
	resp = post(url.Values{"mode": {"open"}})
	resp.Body.Close()
	if got, err := pa.IdeaView(i.ID); err != nil || got.State != idea.Open {
		t.Errorf("reopened: %v %v", got.State, err)
	}
}

func TestPainPageHistoryRunsOldestFirstWithTheStatesToGoTo(t *testing.T) {
	e, pa := listEnv(t)
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Restart is slow", Body: "Takes minutes.", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(d.Pain.ID, "mitigated", "Over 400 sessions it is slow again.", pain.Evidence{Kind: "commit", Ref: "3f9a2c1"}, "", pain.Actor{AccountID: 1, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	created := strings.Index(page, `data-act="pain.created"`)
	state := strings.Index(page, `data-act="pain.state"`)
	if created < 0 || state < 0 || created > state {
		t.Errorf("history not oldest first: created %d, state %d", created, state)
	}
	for _, want := range []string{"Stays open", "Over 400 sessions it is slow again.", "3f9a2c1", `data-val="resolved"`, `data-val="obsolete"`, `data-val="active"`, `name="remaining"`, `name="evidence_ref"`, "Set to Fixed"} {
		if !strings.Contains(page, want) {
			t.Errorf("pain page lacks %q", want)
		}
	}
	// A pain that is open can go to mitigated, resolved or obsolete, not to itself.
	d2, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Second", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	_, p2 := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d2.Pain.ID, 10))
	if strings.Contains(p2, `data-val="active"`) || !strings.Contains(p2, `data-val="mitigated"`) {
		t.Error("the offered states do not follow the transition table")
	}
}
