package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestNotifyKindOfFollowsTheFiltersOfTheInbox(t *testing.T) {
	for name, c := range map[string]struct {
		row  forYouRow
		want string
	}{
		"task":             {forYouRow{taskID: 3}, store.NotifyTask},
		"decision":         {forYouRow{decisionID: 4}, store.NotifyDecision},
		"gardener":         {forYouRow{Mark: "garden"}, store.NotifyGardener},
		"question":         {forYouRow{attentionID: 5, Mark: "ask"}, store.NotifyQuestion},
		"approval":         {forYouRow{attentionID: 5, Mark: "approve"}, store.NotifyQuestion},
		"blocker":          {forYouRow{attentionID: 5, Mark: "warn"}, store.NotifyQuestion},
		"handoff":          {forYouRow{attentionID: 5, Mark: "handoff"}, store.NotifyTask},
		"knowledge bundle": {forYouRow{Mark: "book"}, store.NotifyTask},
	} {
		if got := notifyKindOf(c.row); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func notifySwitch(t *testing.T, e shellEnv, c *http.Client, kind string, on bool) *http.Response {
	t.Helper()
	v := "0"
	if on {
		v = "1"
	}
	return postForm(t, e, c, "/ui/profile/notifications", url.Values{"kind": {kind}, "on": {v}})
}

func TestProfileSwitchesTurnAKindOffInTheInboxAndAtTheBell(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Second thing"})
	bell := regexp.MustCompile(`class="top-count"[^>]*>2<`)

	_, profile := fetchPage(t, e.Member, e.Base+"/ui/profile")
	mustContain(t, "profile", profile, `id="benachrichtigungen"`, "Notifications", "Mentions", "Questions to me", "Decisions", "Tasks", "Gardener", "Tasks in the inbox and at the bell")
	if n := strings.Count(profile, `aria-checked="true"`); n != 5 {
		t.Errorf("all five kinds start on, %d are", n)
	}
	mustNotContain(t, "profile", profile, "E-mail", "Browser")
	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !bell.MatchString(inbox) || !strings.Contains(inbox, "First thing") {
		t.Fatal("the tasks wait before anything is switched off")
	}

	// Another kind off changes nothing for the tasks.
	if resp := notifySwitch(t, e, e.Member, store.NotifyGardener, false); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/profile#benachrichtigungen" {
		t.Fatalf("switch -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, inbox = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !bell.MatchString(inbox) || !strings.Contains(inbox, "First thing") {
		t.Error("switching the gardener off hid the tasks")
	}

	notifySwitch(t, e, e.Member, store.NotifyTask, false)
	_, inbox = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(inbox, "First thing") || strings.Contains(inbox, "Second thing") || strings.Contains(inbox, `class="top-count"`) {
		t.Errorf("tasks are off but still show: %s", inbox)
	}
	for _, path := range []string{"/ui/knowledge", "/ui/x/shell/issues"} {
		if _, page := fetchPage(t, e.Member, e.Base+path); strings.Contains(page, `class="top-count"`) {
			t.Errorf("%s: the bell still counts the tasks", path)
		}
	}
	_, profile = fetchPage(t, e.Member, e.Base+"/ui/profile")
	if n := strings.Count(profile, `aria-checked="true"`); n != 3 {
		t.Errorf("two kinds are off, %d of five are on", n)
	}

	// Nothing of this is another person's: the owner still sees the tasks, and a task
	// that waited does not vanish, it is back as soon as the switch is.
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/profile")
	if n := strings.Count(owner, `aria-checked="true"`); n != 5 {
		t.Errorf("the owner's switches follow the member's: %d on", n)
	}
	notifySwitch(t, e, e.Member, store.NotifyTask, true)
	_, inbox = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !bell.MatchString(inbox) || !strings.Contains(inbox, "First thing") {
		t.Error("switching tasks on again must bring them back")
	}
}

func TestProfileSwitchRefusesWhatIsNoKindAndNeedsTheToken(t *testing.T) {
	e := ovEnv(t)
	if resp := notifySwitch(t, e, e.Member, "nonsense", false); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown kind -> %d, want 400", resp.StatusCode)
	}
	form := url.Values{"kind": {store.NotifyTask}, "on": {"0"}}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/profile/notifications", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("without the CSRF token -> %d, want it refused", resp.StatusCode)
	}
	if off, _ := e.St.NotifyOff("person:2"); len(off) != 0 {
		t.Errorf("a refused post switched something off: %v", off)
	}
	// A guest sets their own switches too: it is their inbox.
	if resp := notifySwitch(t, e, e.Guest, store.NotifyMention, false); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("guest -> %d", resp.StatusCode)
	}
}
