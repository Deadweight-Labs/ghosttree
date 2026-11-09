package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-616 im Web: der Zustand "Akzeptiert" hat ein Pflichtfeld "Warum", zählt als
// geschlossen, zeigt den Grund in der Liste und auf der Seite, und der Wechsel
// ist als Web gekennzeichnet.
func TestAcceptedPainNeedsAReasonAndShowsItInListAndPage(t *testing.T) {
	e := ovEnv(t)
	d, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Flaky Sortierung", Body: "b",
		Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/pains/" + strconv.FormatInt(d.Pain.ID, 10) + "/state"

	_, before := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	for _, want := range []string{`value="accepted"`, `name="note"`, `&#34;need&#34;:true`} {
		if !strings.Contains(before, want) {
			t.Errorf("composer lacks %q", want)
		}
	}

	resp := tpPost(t, e, asBrowser(e.Member), path, url.Values{"state": {"accepted"}})
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "?") {
		t.Fatalf("accepted without a reason = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, failed := followRefusal(t, e.Member, e.Base, resp)
	if !strings.Contains(failed, "Say why you live with it.") || !strings.Contains(failed, `"field":"why"`) {
		t.Error("the missing reason is not explained on the page")
	}

	// The composer of the page names the state "mode".
	resp = tpPost(t, e, e.Member, path, url.Values{"mode": {"accepted"}, "why": {"Kostet mehr als es bringt"}, "note": {"Siehe Messung"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=state") {
		t.Fatalf("accept = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, err := tpOwner(e).PainDetailView(d.Pain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pain.State != pain.Accepted || !strings.HasPrefix(got.Pain.AcceptedReason, "Kostet mehr als es bringt") || !strings.Contains(got.Pain.AcceptedReason, "Siehe Messung") {
		t.Errorf("pain after accepting: %q %q", got.Pain.State, got.Pain.AcceptedReason)
	}
	last := got.Events[len(got.Events)-1]
	if last.ToState != pain.Accepted || last.Via != "web" || last.GitAnchor != nil {
		t.Errorf("accepting from the browser: %+v", last)
	}

	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	for _, want := range []string{"Accepted", "Kostet mehr als es bringt"} {
		if !strings.Contains(page, want) {
			t.Errorf("pain page lacks %q", want)
		}
	}
	if !strings.Contains(page, `<div class="k">Why</div>`) {
		t.Error("pain page lacks the box 'Why'")
	}

	_, open := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:open"))
	if strings.Contains(open, "Flaky Sortierung") {
		t.Error("an accepted pain is still listed as open")
	}
	_, closed := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:closed"))
	for _, want := range []string{"Flaky Sortierung", "Accepted", "Kostet mehr als es bringt", `<b class="xtl">Why</b>`} {
		if !strings.Contains(closed, want) {
			t.Errorf("closed pains lack %q", want)
		}
	}
}

func TestPainStateNote(t *testing.T) {
	for _, c := range []struct{ to, why, note, want string }{
		{pain.Accepted, "Grund", "", "Grund"},
		{pain.Accepted, "Grund", "mehr", "Grund\n\nmehr"},
		{pain.Obsolete, " Grund ", " ", "Grund"},
		{pain.Accepted, "", "nur Notiz", "nur Notiz"},
		{pain.Resolved, "Grund", "Notiz", "Notiz"},
	} {
		if got := painStateNote(c.to, c.why, c.note); got != c.want {
			t.Errorf("painStateNote(%q,%q,%q) = %q, want %q", c.to, c.why, c.note, got, c.want)
		}
	}
}
