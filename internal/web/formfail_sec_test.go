package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The room and logout forms carry the same body limit as every other form: a body of
// several megabytes is refused as too large before ParseForm has read it.
func TestCoordFormsAndLogoutRefuseABodyOverTheFormLimit(t *testing.T) {
	e := shellWebAll(t)
	room, _ := memberReadSeed(t, e)
	token := renderedCSRFToken(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	huge := strings.Repeat("a", maxFormBody+1024)
	for _, path := range []string{"/ui/coord/send", "/ui/coord/thread/create", "/ui/coord/thread/post", "/ui/coord/read", "/ui/coord/group/create", "/ui/logout"} {
		resp := sameOriginPostForm(t, e.Member, e.Base+path, url.Values{"csrf_token": {token}, "room": {room}, "body": {huge}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with a body over the limit = %d, want 413", path, resp.StatusCode)
		}
	}
	// With a wrong token the oversized body is refused the same way and nothing comes back.
	resp := sameOriginPostForm(t, asBrowser(e.Member), e.Base+"/ui/coord/send", url.Values{"csrf_token": {"stale"}, "room": {room}, "body": {huge}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a stale token with a huge body = %d, want 413", resp.StatusCode)
	}
}

// A room message up to CoordMaxBody still goes through the limit of the form; over it
// the page says the limit in KB (not the character limit of the long texts).
func TestRoomMessageLimitSurvivesTheBodyLimitAndSaysItsReasonOnThePage(t *testing.T) {
	e := shellWebAll(t)
	room, _ := memberReadSeed(t, e)
	post := func(c *http.Client, body string) (int, string) {
		form := url.Values{"room": {room}, "body": {body}, "csrf_token": {renderedCSRFToken(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))}}
		resp := sameOriginPostForm(t, c, e.Base+"/ui/coord/send", form)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if status, text := post(e.Member, strings.Repeat("a", store.CoordMaxBody)); status != http.StatusSeeOther {
		t.Errorf("a message of the limit: %d %.100s", status, text)
	}
	// Four bytes per character as a form: still under the form limit at the message limit.
	if status, text := post(e.Member, strings.Repeat("ä", store.CoordMaxBody/2)); status != http.StatusSeeOther {
		t.Errorf("a message of the limit in two-byte characters: %d %.100s", status, text)
	}
	status, page := post(asBrowser(e.Member), strings.Repeat("a", store.CoordMaxBody+1))
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(page, "64 KB") || strings.Contains(page, "64,000") || strings.Contains(page, "64.000") {
		t.Errorf("a message over the limit in the browser: %d, want 413 with the KB limit: %.300s", status, page)
	}
	// What was typed is on the page again while it is under the memory limit of a note.
	if !strings.Contains(page, strings.Repeat("a", 1000)) {
		t.Errorf("the refused message is not on the page")
	}
}

// Over flashMaxBytes the fields are not kept; the refusal still arrives.
func TestARefusedFormOverTheMemoryLimitKeepsNoFieldsButSaysWhy(t *testing.T) {
	e := shellWebAll(t)
	room, _ := memberReadSeed(t, e)
	form := url.Values{"room": {room}, "body": {strings.Repeat("a", flashMaxBytes+1)}, "csrf_token": {renderedCSRFToken(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))}}
	resp := sameOriginPostForm(t, asBrowser(e.Member), e.Base+"/ui/coord/send", form)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	page := string(b)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(page, "64 KB") {
		t.Fatalf("the refusal = %d %.200s", resp.StatusCode, page)
	}
	if strings.Contains(page, strings.Repeat("a", 1000)) {
		t.Errorf("a body over the memory limit is written onto the page")
	}
	if n := len(keptFields(form)); n != 0 {
		t.Errorf("keptFields keeps %d fields of a form over the limit", n)
	}
	small := url.Values{"a": {strings.Repeat("x", flashMaxBytes-1)}}
	if len(keptFields(small)) != 1 {
		t.Errorf("a form under the limit is not kept")
	}
}

// A refused token keeps nothing: the page says the form expired and does not carry
// what was typed, and the server holds no note of it.
func TestARefusedTokenKeepsNothingOfTheForm(t *testing.T) {
	e := ovEnv(t)
	form := url.Values{"csrf_token": {"stale"}, "mode": {"comment"}, "reason": {"hello there"}, backField: {"/ui/inbox"}}
	req, err := http.NewRequest(http.MethodPost, e.Base+"/ui/requests/1/close", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.Base)
	resp, err := asBrowser(e.Owner).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a stale form = %d", resp.StatusCode)
	}
	_, page := followRefusal(t, e.Owner, e.Base, resp)
	if !strings.Contains(page, msg("err.form.expired")) {
		t.Errorf("the page does not say the form expired: %.200s", page)
	}
	if strings.Contains(page, "hello there") {
		t.Errorf("a form with a refused token is written back onto the page")
	}
}

// The page that shows a note of a refused form is not cached anywhere.
func TestThePageWithAFlashIsNotCached(t *testing.T) {
	e := ovEnv(t)
	form := url.Values{"csrf_token": {"stale"}, "reason": {"x"}, backField: {"/ui/inbox"}}
	req, _ := http.NewRequest(http.MethodPost, e.Base+"/ui/requests/1/close", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.Base)
	resp, err := asBrowser(e.Owner).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	get, _ := http.NewRequest(http.MethodGet, e.Base+resp.Header.Get("Location"), nil)
	for _, ck := range resp.Cookies() {
		if ck.Name == flashCookie {
			get.AddCookie(ck)
		}
	}
	page, err := e.Owner.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	text := body(t, page)
	if !strings.Contains(text, msg("err.form.expired")) {
		t.Fatalf("the page shows no note: %.200s", text)
	}
	if cc := page.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control of the page with a note = %q, want no-store", cc)
	}
}

func TestFlashesKeepFewPerPersonAndEvictOnlyTheirOwnOldest(t *testing.T) {
	var f flashes
	mk := func(person string) string { return f.put(&flashEntry{person: person, Text: person}) }
	other := mk("other")
	var mine []string
	for i := 0; i < flashPerPerson+3; i++ {
		mine = append(mine, mk("mine"))
		// Older notes expire first: make the order of the loop the order of age.
		f.mu.Lock()
		f.m[mine[i]].expires = time.Now().Add(time.Duration(i+1) * time.Minute)
		f.mu.Unlock()
	}
	f.mu.Lock()
	n := f.count("mine")
	_, otherThere := f.m[other]
	f.mu.Unlock()
	if n != flashPerPerson || !otherThere {
		t.Errorf("mine = %d (want %d), other still there = %v", n, flashPerPerson, otherThere)
	}
	if f.take(mine[0], "mine") != nil || f.take(mine[1], "mine") != nil {
		t.Errorf("the oldest notes of the person survived")
	}
	if f.take(mine[len(mine)-1], "mine") == nil {
		t.Errorf("the newest note is gone")
	}
	if f.take(other, "other") == nil {
		t.Errorf("a note of another person was evicted")
	}
}

func TestFlashesAtTheGlobalLimitEvictOwnFirst(t *testing.T) {
	var f flashes
	var victim string
	for i := 0; i < flashMaxEntries/flashPerPerson; i++ {
		person := "p" + itoa(int64(i))
		for j := 0; j < flashPerPerson; j++ {
			id := f.put(&flashEntry{person: person})
			if i == 0 && j == 0 {
				victim = id
			}
		}
	}
	f.mu.Lock()
	full := len(f.m)
	f.mu.Unlock()
	if full != flashMaxEntries {
		t.Fatalf("setup holds %d notes, want %d", full, flashMaxEntries)
	}
	// One of them keeps asking: it evicts its own, p0's oldest note survives.
	for i := 0; i < 10; i++ {
		f.put(&flashEntry{person: "p5"})
	}
	f.mu.Lock()
	holds, all := f.count("p5"), len(f.m)
	f.mu.Unlock()
	if holds != flashPerPerson || all != flashMaxEntries {
		t.Errorf("p5 holds %d, all %d", holds, all)
	}
	if f.take(victim, "p0") == nil {
		t.Errorf("a person with a full set lost a note to the requests of another")
	}
}

func TestTakeSweepsExpiredNotes(t *testing.T) {
	var f flashes
	old := f.put(&flashEntry{person: "a"})
	keep := f.put(&flashEntry{person: "b"})
	f.mu.Lock()
	f.m[old].expires = time.Now().Add(-time.Minute)
	f.mu.Unlock()
	if f.take(keep, "b") == nil {
		t.Fatal("the fresh note was not taken")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.m) != 0 {
		t.Errorf("%d notes left after a take; the expired one should be swept", len(f.m))
	}
}

func TestKeptFieldsDropSecretNamesAsSubstring(t *testing.T) {
	v := url.Values{}
	for _, name := range []string{"password", "New_Password", "token", "csrf_token", "api_TOKEN", "code", "user_code", "confirm_code", "Secret", "client_secret", "key", "api_key", "KEY_ID", "otp", "totp_code", "pairing", "pairing_id", "nonce", "NONCE2", backField} {
		v.Set(name, "x")
	}
	for _, name := range []string{"title", "body", "reason", "note", "keywords", "mode"} {
		v.Set(name, "y")
	}
	got := keptFields(v)
	for name := range got {
		if secretName(name) {
			t.Errorf("%q is kept", name)
		}
	}
	for _, want := range []string{"title", "body", "reason", "note", "keywords", "mode"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q is dropped", want)
		}
	}
	if len(got) != 6 {
		t.Errorf("kept %d fields: %v", len(got), got)
	}
}
