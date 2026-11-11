package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Route zum Melden von Git-Vorgängen prüft wie appendChunks (Review von PR #242):
// Maschinenbindung des Tokens, die Drossel für große Körper, die Grenze des Körpers,
// und ein Konto über seiner Tagesgrenze bekommt 429.

func TestGitEventsFromATokenBoundToAnotherMachineAreRefused(t *testing.T) {
	f := newOwnershipFixture(t)
	// anna legt zwei Sessions an: eine auf ihrer Box, eine auf einer anderen Maschine.
	if code, _ := f.post(t, "/api/sessions", f.annaLegacy, session("annabox", "on-box")); code != 200 {
		t.Fatalf("session on annabox: %d", code)
	}
	if code, _ := f.post(t, "/api/sessions", f.annaLegacy, session("elsewhere", "on-other")); code != 200 {
		t.Fatalf("session elsewhere: %d", code)
	}
	event := func(ext string) map[string]any {
		return map[string]any{"events": []store.SessionGitEvent{{Harness: "claude-code", SessionExternalID: ext, Seq: 1, Kind: "commit", Op: "commit",
			StartedAt: "2026-11-08T10:00:00Z", EndedAt: "2026-11-08T10:00:01Z", OK: true}}}
	}
	if code, body := f.post(t, "/api/session-git-events", f.annaBound, event("on-box")); code != 200 {
		t.Fatalf("own machine: %d %v", code, body)
	}
	code, body := f.post(t, "/api/session-git-events", f.annaBound, event("on-other"))
	if code != 403 || body["code"] != "machine_bound" {
		t.Fatalf("a token bound to annabox reported for a session of another machine: %d %v", code, body)
	}
	// Ohne Bindung geht es.
	if code, body := f.post(t, "/api/session-git-events", f.annaLegacy, event("on-other")); code != 200 {
		t.Fatalf("legacy token: %d %v", code, body)
	}
}

func TestGitEventsWithoutAKnownLengthWaitForAnUploadSlot(t *testing.T) {
	f := accessAPI(t, true)
	for i := 0; i < cap(bigBodies); i++ {
		bigBodies <- struct{}{}
	}
	released := false
	release := func() {
		if !released {
			released = true
			for i := 0; i < cap(bigBodies); i++ {
				<-bigBodies
			}
		}
	}
	t.Cleanup(release)
	body, _ := json.Marshal(gitEvents(miaCommit()))
	// Eine Länge ist bekannt und klein: kein Platz nötig.
	f.expect(t, "mia", 200, "POST", "/api/session-git-events", gitEvents(miaCommit()))
	// Ohne bekannte Länge (chunked) wartet der Aufruf auf einen Platz.
	rq, _ := http.NewRequest("POST", f.srv.URL+"/api/session-git-events", io.NopCloser(bytes.NewReader(body)))
	rq.Header.Set("Authorization", "Bearer "+f.tok["mia"])
	if rq.ContentLength != 0 && rq.ContentLength != -1 {
		t.Fatalf("want an unknown length, got %d", rq.ContentLength)
	}
	rq.ContentLength = -1
	waiting := &http.Client{Timeout: 400 * time.Millisecond}
	if resp, err := waiting.Do(rq); err == nil {
		resp.Body.Close()
		t.Fatalf("an upload of unknown size was served while both slots were taken: %d", resp.StatusCode)
	}
	// Sind die Plätze frei, geht derselbe Aufruf durch.
	release()
	rq, _ = http.NewRequest("POST", f.srv.URL+"/api/session-git-events", io.NopCloser(bytes.NewReader(body)))
	rq.Header.Set("Authorization", "Bearer "+f.tok["mia"])
	rq.ContentLength = -1
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("after the slots are free: %d", resp.StatusCode)
	}
}

func TestAnOversizedGitEventBodyIsTooLargeNotAnInvalidJSON(t *testing.T) {
	f := accessAPI(t, true)
	e := miaCommit()
	e.Branch = strings.Repeat("x", maxSessionGitBody+1)
	code, out := f.call(t, "mia", "POST", "/api/session-git-events", gitEvents(e))
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(out, "body_too_large") {
		t.Fatalf("%d %s", code, strings.TrimSpace(out))
	}
}

func TestAnInvalidEventInABatchLeavesTheRestRecorded(t *testing.T) {
	f := accessAPI(t, true)
	future := miaCommit()
	future.Seq = 5
	future.StartedAt = time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339)
	future.EndedAt = ""
	out := f.expect(t, "mia", 200, "POST", "/api/session-git-events", gitEvents(miaCommit(), future))
	got := decode[store.SessionGitResult](t, out)
	if got.Recorded != 1 || got.Rejected != 1 || !strings.Contains(got.Reason, "future") {
		t.Fatalf("%+v", got)
	}
}

func TestAnAccountOverItsDailyLimitGetsATooManyRequests(t *testing.T) {
	f := accessAPI(t, true)
	// mia ist person:3; ihre Tagesgrenze ist erreicht.
	day := time.Now().UTC().Format("2006-01-02")
	if _, err := f.st.DB().Exec(`INSERT INTO session_git_quota(account_id, day, events) VALUES(3, ?, 1000000)`, day); err != nil {
		t.Fatal(err)
	}
	resp := req(t, "POST", f.srv.URL+"/api/session-git-events", f.tok["mia"], gitEvents(miaCommit()))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("%d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// Ein anderes Konto ist nicht betroffen.
	f.expect(t, "rex", 200, "POST", "/api/session-git-events", gitEvents(store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-rex", Seq: 1,
		Kind: "commit", StartedAt: "2026-11-08T10:00:00Z", EndedAt: "2026-11-08T10:00:01Z", OK: true}))
}
