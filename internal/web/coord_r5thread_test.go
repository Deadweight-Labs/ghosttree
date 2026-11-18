package web

import (
	"net/url"
	"strings"
	"testing"
)

// REQ-706 AC-1 (S08): a member who answered in a thread resolves it, puts it on hold and opens it again from
// the status menu, each with a line in the thread that says who did it; a member who never wrote in it sees the
// status as a pill that says who may change it, and the server refuses the change.
func TestThreadStateIsChangedByWhoeverAnsweredInIt(t *testing.T) {
	e := ovEnv(t)
	room, thread := threadMention(t, e, "@anna can you take the release?") // started by alice
	at := e.Base + "/ui/coord?room=" + url.QueryEscape(room) + "&thread=" + itoa(thread)

	page := fetchBody(t, e.Member, at)
	if strings.Contains(page, `action="/ui/coord/thread/state"`) {
		t.Fatalf("anna never wrote in the thread and gets the status menu:\n%.1500s", page)
	}
	lock := `title="` + messages["coord.thread_state_lock"] + `"`
	if !strings.Contains(page, lock) {
		t.Errorf("the status of a thread that cannot be changed does not say who may change it (%s)", lock)
	}
	resp := tpPost(t, e, e.Member, "/ui/coord/thread/state", url.Values{"thread_id": {itoa(thread)}, "state": {"resolved"}, "room": {room}})
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("anna resolves a thread she never wrote in: %d", resp.StatusCode)
	}

	reply := tpPost(t, e, e.Member, "/ui/coord/thread/post", url.Values{"thread_id": {itoa(thread)}, "body": {"Ten minutes are enough."}, "room": {room}})
	reply.Body.Close()
	if reply.StatusCode >= 400 {
		t.Fatalf("anna answers in the thread: %d", reply.StatusCode)
	}
	page = fetchBody(t, e.Member, at)
	menu := segment(t, page, `<details class="dd top-menu rtstate"`, "</details>")
	for _, v := range []string{"open", "resolved", "deferred"} {
		if !strings.Contains(menu, `name="state" value="`+v+`"`) {
			t.Errorf("anna answered and the menu does not offer %s", v)
		}
	}

	steps := []struct{ state, line string }{
		{"resolved", "anna marked this thread resolved"},
		{"deferred", "anna put this thread on hold"},
		{"open", "anna reopened this thread"},
	}
	for _, step := range steps {
		resp := tpPost(t, e, e.Member, "/ui/coord/thread/state", url.Values{"thread_id": {itoa(thread)}, "state": {step.state}, "room": {room}})
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Fatalf("anna sets %s: %d", step.state, resp.StatusCode)
		}
		page = fetchBody(t, e.Member, at)
		if !strings.Contains(page, step.line) {
			t.Errorf("after %s the thread lacks the line %q", step.state, step.line)
		}
		if !strings.Contains(segment(t, page, `<details class="dd top-menu rtstate"`, "</details>"), `value="`+step.state+`" aria-current="true"`) {
			t.Errorf("after %s the menu does not mark it as the current status", step.state)
		}
	}

	// The status alone also goes with the reply: one send.
	both := tpPost(t, e, e.Member, "/ui/coord/thread/post", url.Values{"thread_id": {itoa(thread)}, "body": {"Done, thanks."}, "state": {"resolved"}, "state_was": {"open"}, "room": {room}})
	both.Body.Close()
	if both.StatusCode >= 400 {
		t.Fatalf("reply with the status: %d", both.StatusCode)
	}
	if page = fetchBody(t, e.Member, at); !strings.Contains(page, "Done, thanks.") || !strings.Contains(page, "anna marked this thread resolved") {
		t.Error("the reply that resolves does not show as a reply and a status line")
	}

	// Somebody who wrote nothing is still outside, also with the reply.
	other := tpPost(t, e, e.Reviewer, "/ui/coord/thread/state", url.Values{"thread_id": {itoa(thread)}, "state": {"open"}, "room": {room}})
	other.Body.Close()
	if other.StatusCode < 400 {
		t.Errorf("rita never wrote in the thread and reopens it: %d", other.StatusCode)
	}
}
