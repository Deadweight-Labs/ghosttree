package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Mit einer gültigen Einladung ließen sich sonst beliebig viele Kontonamen
// prüfen: ein abgelehnter Name verbraucht die Einladung nicht.
func TestInviteNameOracleIsLockedAfterFiveRejections(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleGuest)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := func(name string) (int, string, string) {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "join": {"1"}, "name": {name}})
		retry := resp.Header.Get("Retry-After")
		return resp.StatusCode, body(t, resp), retry
	}
	for i := 1; i < inviteNameMisses; i++ {
		status, page, _ := probe("alice")
		if status != http.StatusUnprocessableEntity || !strings.Contains(page, "taken or not allowed") {
			t.Fatalf("rejection %d: %d %s", i, status, page)
		}
	}
	status, page, retry := probe("alice")
	if status != http.StatusTooManyRequests || retry == "" || !strings.Contains(page, "Too many names") || strings.Contains(page, "taken or not allowed") {
		t.Fatalf("fifth rejection: %d retry=%q %s", status, retry, page)
	}
	// Gesperrt wird gar nicht mehr geprüft: ein freier Name bekommt dieselbe Antwort
	// wie ein vergebener, sonst bliebe die Sperre ein Orakel.
	freeStatus, freePage, _ := probe("Neu Person")
	takenStatus, takenPage, _ := probe("alice")
	// (Die Seiten unterscheiden sich nur im zurückgegebenen Namen im Feld.)
	if freeStatus != http.StatusTooManyRequests || takenStatus != freeStatus {
		t.Fatalf("locked: free %d, taken %d", freeStatus, takenStatus)
	}
	for _, p := range []string{freePage, takenPage} {
		if !strings.Contains(p, "Too many names") || strings.Contains(p, "taken or not allowed") {
			t.Fatalf("locked page says the wrong thing: %s", p)
		}
	}
	// Die Einladung bleibt gültig, nur der Weg für Namen ist zu.
	if _, err := st.PreviewInvitation(code, true); err != nil {
		t.Errorf("the invitation was used up or revoked: %v", err)
	}
	// Eine andere Einladung ist nicht betroffen.
	other := projectInvite(t, st, org, store.RoleGuest)
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {other}, "join": {"1"}, "name": {"Zweite Person"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("another invitation: %d", resp.StatusCode)
	}
}

// Dasselbe auf der Einmalcode-Seite (ohne join=1).
func TestInviteNameLockAlsoHoldsOnTheCodePage(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleGuest)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var status int
	var retry string
	for i := 0; i < inviteNameMisses; i++ {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"alice"}})
		status, retry = resp.StatusCode, resp.Header.Get("Retry-After")
		resp.Body.Close()
	}
	if status != http.StatusTooManyRequests || retry == "" {
		t.Fatalf("after %d rejections: %d retry=%q", inviteNameMisses, status, retry)
	}
}

// Die Namensprüfung zählt gegen dieselbe Grenze wie die übrigen Beitrittswege.
func TestInviteNameCheckGoesThroughTheJoinGate(t *testing.T) {
	_, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleGuest)
	h := New(st)
	do := func(method, target string, form url.Values) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://example.com")
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		req.RemoteAddr = "198.51.100.9:1000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < joinLimit; i++ {
		do(http.MethodGet, "http://example.com/join/"+strings.Repeat("cd", 32), nil)
	}
	rec := do(http.MethodPost, "http://example.com/ui/login/code", url.Values{"code": {code}, "join": {"1"}, "name": {"alice"}})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the join limit: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "taken or not allowed") {
		t.Error("the name was checked although the address is over the limit")
	}
	// Eine andere Adresse kommt durch und bekommt die Antwort auf den Namen.
	req := httptest.NewRequest(http.MethodPost, "http://example.com/ui/login/code", strings.NewReader(url.Values{"code": {code}, "join": {"1"}, "name": {"alice"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.RemoteAddr = "198.51.100.10:1000"
	other := httptest.NewRecorder()
	h.ServeHTTP(other, req)
	if other.Code != http.StatusUnprocessableEntity {
		t.Errorf("other address: %d", other.Code)
	}
}

func TestInviteNameGuardUnlocksAfterTheLockAndForgetsOldMisses(t *testing.T) {
	g := newInviteNameGuard()
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return at }
	for i := 1; i < inviteNameMisses; i++ {
		if g.miss("c") || g.locked("c") {
			t.Fatalf("locked after %d misses", i)
		}
	}
	if !g.miss("c") || !g.locked("c") {
		t.Fatal("not locked after the fifth miss")
	}
	if g.locked("other") {
		t.Error("another code is locked")
	}
	at = at.Add(inviteNameLockFor - time.Second)
	if !g.locked("c") {
		t.Error("unlocked too early")
	}
	at = at.Add(2 * time.Second)
	if g.locked("c") {
		t.Fatal("still locked after the lock ran out")
	}
	// Nach der Sperre beginnt das Zählen von vorn.
	for i := 1; i < inviteNameMisses; i++ {
		if g.miss("c") {
			t.Fatalf("locked again after %d misses", i)
		}
	}
	// Vereinzelte Fehlversuche über Stunden sperren nicht.
	for i := 0; i < 3*inviteNameMisses; i++ {
		at = at.Add(inviteNameLockFor)
		if g.miss("slow") {
			t.Fatal("slow typos locked the code")
		}
	}
	g.accepted("c")
	if g.locked("c") || len(g.counts) != 1 {
		t.Errorf("accepted did not clear the code: %d entries", len(g.counts))
	}
}
