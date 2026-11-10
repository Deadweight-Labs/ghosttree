package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A guest sees an agent's entry as "alice’s Claude", on the stamp and in the first bubble of a page, but never
// the model, the machine or the link to the session, not even when the session is shared with the project
// (a member sees those then, so the test would notice a page that leaves them out for everybody).
func TestGuestSeesNoModelMachineOrSessionOnStampsAndFirstBubble(t *testing.T) {
	e, pa := listEnv(t)
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-p3", AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "laptop-p3"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.AppendChunks(sid, []store.Chunk{{Seq: 0, Role: "assistant", Raw: `{"type":"assistant","timestamp":"2020-01-01T00:00:00Z","message":{"model":"claude-opus-5-5"}}`}}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetSessionShared(sid, "person:1", true); err != nil {
		t.Fatal(err)
	}
	agent := actor.Actor{Kind: actor.Agent, AccountID: 1, Harness: "claude-code", Machine: "laptop-p3", SessionID: sid}
	made, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Agent pain", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, made.Pain.ID, agent); err != nil {
		t.Fatal(err)
	}
	req := rqMake(t, e.St, rqSpec{Title: "Agent request", Desc: "Text."})
	if err := e.St.RecordActor(actor.Request, req.Request.ID, agent); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"pain":  "/ui/x/shell/pains/" + itoa(made.Pain.ID),
		"issue": "/ui/x/shell/issues/" + itoa(req.Request.ID),
	} {
		_, member := fetchPage(t, e.Member, e.Base+path)
		if !strings.Contains(member, "Opus 5.5") || !strings.Contains(member, "laptop-p3") {
			t.Errorf("%s: a member does not see model and machine of the shared session, the test proves nothing", name)
		}
		code, guest := fetchPage(t, e.Guest, e.Base+path)
		if code != http.StatusOK {
			t.Fatalf("%s: guest got %d", name, code)
		}
		if !strings.Contains(guest, "’s Claude") {
			t.Errorf("%s: a guest does not see whose agent wrote it", name)
		}
		for _, leak := range []string{"Opus 5.5", "claude-opus-5-5", "laptop-p3", `href="/ui/sessions/`, "s-p3"} {
			if strings.Contains(guest, leak) {
				t.Errorf("%s: a guest sees %q", name, leak)
			}
		}
		// The first bubble is cut out on its own, so a leak there is named as such.
		if open := strings.Index(guest, `<div class="cmt first">`); open >= 0 {
			bubble := guest[open:]
			if end := strings.Index(bubble, `class="cmt`+` `); end > 0 && end < len(bubble) {
				bubble = bubble[:end]
			}
			for _, leak := range []string{"Opus 5.5", "laptop-p3", "/ui/sessions/"} {
				if strings.Contains(bubble, leak) {
					t.Errorf("%s: the first bubble shows %q to a guest", name, leak)
				}
			}
		}
	}
}

// The report of a task leaves the address of the task page only for a task the viewer may open: with
// back=task a task that is not readable (another repository's) goes to the inbox, the readable one to its page.
func TestBackTaskOfAnUnreadableTaskGoesToTheInbox(t *testing.T) {
	e, shown, foreign := taskFixture(t)
	a := &app{store: e.St}
	member := store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}
	post := func(back string, id int64) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/ui/tasks/done", strings.NewReader(url.Values{"back": {back}, "id": {itoa(id)}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r.WithContext(context.WithValue(r.Context(), personKey{}, member))
	}
	if got, want := a.taskBack(post("task", shown.ID), []int64{shown.ID}, url.Values{}), "/ui/x/shell/tasks/"+itoa(shown.ID); got != want {
		t.Fatalf("a readable task leads to %q, want %q (the test proves nothing otherwise)", got, want)
	}
	if got := a.taskBack(post("task", foreign.ID), []int64{foreign.ID}, url.Values{}); got != "/ui/inbox" {
		t.Errorf("an unreadable task leads to %q, want /ui/inbox", got)
	}
	if got := a.taskBack(post("inbox", shown.ID), []int64{shown.ID}, url.Values{}); got != "/ui/inbox" {
		t.Errorf("back=inbox leads to %q, want /ui/inbox", got)
	}
	// Over HTTP the member gets no answer that names the other repository's task.
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(foreign.ID)}, "back": {"task"}, "note": {"token ghp_" + strings.Repeat("Ab1", 5)}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "/tasks/"+itoa(foreign.ID)) || strings.Contains(loc, "/ui/x/hidden") {
		t.Errorf("the refusal leads to %q, the address of a task the member cannot read", loc)
	}
}

// The note of a report is counted in characters, the way the field counts them, and a note that is too
// long goes back to the page with its message (the draft stays), not to a bare 400.
func TestTaskNoteIsCountedInCharactersAndTooLongGoesBackWithAMessage(t *testing.T) {
	e, task, _ := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	_, page := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page", page, `maxlength="500"`)
	send := func(note string) *http.Response {
		resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}, "note": {note}})
		resp.Body.Close()
		return resp
	}
	// Without a browser navigation the refusal is the message itself (with one, failFormTo carries it back to the page).
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}, "note": {strings.Repeat("ä", 501)}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "500") || !strings.Contains(string(body), "501") {
		t.Fatalf("501 characters -> %d %q, want 400 naming the limit and the length", resp.StatusCode, body)
	}
	// A task still open: 500 umlauts are 1000 bytes and fit, as the field says. A line break is one character.
	if r := send(strings.Repeat("ö", 250) + "\r\n" + strings.Repeat("ö", 249)); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != path {
		t.Errorf("500 characters with a line break -> %d %q, want done and back to %s", r.StatusCode, r.Header.Get("Location"), path)
	}
}

// The send button is held while the form is on its way (a double click would be refused, the text would come
// back into the field and a second send would write the comment twice), as ui-coord.js does for its forms.
func TestComposerHoldsTheSendButtonWhileTheFormIsSent(t *testing.T) {
	js, err := files.ReadFile("static/ui-parts.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	i := strings.Index(src, `form.addEventListener("submit"`)
	if i < 0 {
		t.Fatal("the composer form has no submit handler")
	}
	handler := src[i : i+min(len(src)-i, 900)]
	for _, want := range []string{"dataset.sending", "e.preventDefault()", "sendBtn.disabled = true", "e.defaultPrevented"} {
		if !strings.Contains(handler, want) {
			t.Errorf("the submit handler lacks %q", want)
		}
	}
	if !strings.Contains(src, `"pageshow"`) || !strings.Contains(src, "sendBtn.disabled = false") {
		t.Error("a page restored from the history keeps the button disabled")
	}
}
