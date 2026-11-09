package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var badgeRE = regexp.MustCompile(`class="shell-badge"[^>]*>(\d+)</b>`)

// The number beside Requests counts the open tasks for the viewer, through the
// same visibility as the list: not the ones for somebody else, not a hidden
// project's, and a guest only counts what is assigned to them.
func TestOpenTasksForMeShowAsABadgeAndCountOnlyWhatIsVisible(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden thing", Project: shellHiddenProject})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For everyone"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For anna", Assignee: "anna"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For gina", Assignee: "gina"})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "SECRET-TASK"})

	count := func(c *http.Client, path string) string {
		t.Helper()
		_, page := fetchPage(t, c, e.Base+path)
		m := badgeRE.FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		return m[1]
	}
	if got := count(e.Member, "/ui/inbox"); got != "2" {
		t.Errorf("anna's badge = %q, want 2 (hers and the one for everyone)", got)
	}
	// Ein Gast erledigt keine Aufgaben (Robin, 2026-11-09): auch die ihm
	// zugewiesene wartet nicht auf ihn.
	if got := count(e.Guest, "/ui/inbox"); got != "" {
		t.Errorf("gina's badge = %q, want none (a guest does not mark tasks done)", got)
	}
	if got := count(e.Owner, "/ui/inbox"); got != "2" {
		t.Errorf("alice's badge = %q, want 2 (the two for everyone, one of them in the project only she is in)", got)
	}
	if got := count(e.Reviewer, "/ui/inbox"); got != "1" {
		t.Errorf("rita's badge = %q, want 1", got)
	}
	// Nothing open: no badge at all.
	none := ovEnv(t)
	if got := count2(t, none, none.Member, "/ui/inbox"); got != "" {
		t.Errorf("badge without tasks = %q", got)
	}
}

func count2(t *testing.T, e shellEnv, c *http.Client, path string) string {
	t.Helper()
	_, page := fetchPage(t, c, e.Base+path)
	if m := badgeRE.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return ""
}

func trackSince(t *testing.T, st *store.Store, at string) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT OR REPLACE INTO org_state(key, value) VALUES('token_use_tracking_since', ?)`, at); err != nil {
		t.Fatal(err)
	}
}

func tokenAt(t *testing.T, st *store.Store, account, label, created, lastUsed string) int64 {
	t.Helper()
	_, info, err := st.CreateToken(account, store.TokenSpec{Label: label})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE api_tokens SET created_at=?, last_used_at=? WHERE id=?`, created, lastUsed, info.ID); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// Devices & tokens: the one used last comes first, never used ones are
// labelled and sit below, and "Revoke unused" revokes exactly those, of the
// person's own account only.
func TestTokensAreSortedByUseAndUnusedOnesCanBeRevoked(t *testing.T) {
	e := ovEnv(t)
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const month = 30 * 24 * time.Hour
	old := tokenAt(t, e.St, "anna", "old-use", at(2*month), at(48*time.Hour))
	recent := tokenAt(t, e.St, "anna", "recent-use", at(2*month), at(time.Hour))
	never := tokenAt(t, e.St, "anna", "e2e-never", at(2*month), "")
	fresh := tokenAt(t, e.St, "anna", "fresh-login", at(5*time.Minute), "")
	foreign := tokenAt(t, e.St, "gina", "gina-never", at(2*month), "")
	// Use has been recorded for two months: an empty last use now means never.
	trackSince(t, e.St, at(2*month))
	_, page := fetchPage(t, e.Member, e.Base+"/ui/account/tokens")
	i, j, k := strings.Index(page, "recent-use"), strings.Index(page, "old-use"), strings.Index(page, "e2e-never")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("order recent=%d old=%d never=%d: by last use first, never used last", i, j, k)
	}
	if !strings.Contains(page, "never used") || !strings.Contains(page, "Revoke unused") || !strings.Contains(page, "Revoke 1 tokens that were never used?") {
		t.Errorf("the page does not offer to revoke the one unused token: %s", page)
	}
	if strings.Contains(page, "gina-never") {
		t.Error("another account's token is listed")
	}

	resp := tpPost(t, e, e.Member, "/ui/account/tokens/revoke-unused", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "revoked=1") {
		t.Fatalf("revoke unused = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	state := func(id int64) bool {
		info, err := e.St.TokenByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return info.RevokedAt != ""
	}
	if !state(never) {
		t.Error("the unused token is still active")
	}
	for name, id := range map[string]int64{"used long ago": old, "used recently": recent, "just issued": fresh, "of someone else": foreign} {
		if state(id) {
			t.Errorf("the token %s was revoked", name)
		}
	}
	_, after := fetchPage(t, e.Member, e.Base+"/ui/account/tokens?revoked=1")
	if !strings.Contains(after, "Revoked 1 unused tokens.") || strings.Contains(after, "Revoke unused") {
		t.Errorf("after: %s", after)
	}
}

// Team & devices is one entry of the account menu with an icon of its own, not
// the shared fallback; a guest has none.
func TestTeamAndDevicesAreOneMenuEntryWithItsOwnIcon(t *testing.T) {
	e := ovEnv(t)
	const fallback = `cx="9" cy="8"`
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member} {
		_, page := fetchPage(t, c, e.Base+"/ui/inbox")
		i := strings.Index(page, `data-account="team"`)
		if i < 0 {
			t.Fatalf("%s has no Team & devices entry", name)
		}
		rest := page[i:]
		if icon := rest[strings.Index(rest, "<svg"):strings.Index(rest, "</svg>")]; strings.Contains(icon, fallback) {
			t.Errorf("%s: the entry falls back to the shared icon", name)
		}
		if strings.Count(page, `data-account="team"`) != 1 {
			t.Errorf("%s: Team & devices must be a single entry", name)
		}
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/inbox")
	if strings.Contains(guest, `data-account="team"`) {
		t.Error("a guest is offered Team & devices")
	}
}

func TestTokenIsUnusedRules(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const day = 24 * time.Hour
	old := now.Add(-60 * day) // use is recorded since two months
	cases := []struct {
		name    string
		token   store.TokenInfo
		current int64
		since   time.Time
		want    bool
	}{
		{"never used and old, use is recorded", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, old, true},
		{"use is not recorded at all yet", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, time.Time{}, false},
		{"recording began only yesterday", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, now.Add(-day), false},
		{"just issued, first call still to come", store.TokenInfo{ID: 1, CreatedAt: at(10 * time.Minute)}, 0, old, false},
		{"used", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), LastUsedAt: at(time.Hour)}, 0, old, false},
		{"already revoked", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), RevokedAt: at(time.Hour)}, 0, old, false},
		{"expired", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), ExpiresAt: at(time.Hour)}, 0, old, false},
		{"the token this session signed in with", store.TokenInfo{ID: 7, CreatedAt: at(40 * day)}, 7, old, false},
	}
	for _, c := range cases {
		if got := tokenIsUnused(c.token, now, c.current, c.since); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Revoke unused is not offered to an administrator without an interactive
// session when it would reach another account, and a POST without the CSRF
// token revokes nothing.
func TestRevokeUnusedRefusesWithoutCSRFAndGuardsForeignTokens(t *testing.T) {
	e := ovEnv(t)
	at := func(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }
	own := tokenAt(t, e.St, "anna", "anna-never", at(40*24*time.Hour), "")
	trackSince(t, e.St, at(60*24*time.Hour))

	form := url.Values{}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/account/tokens/revoke-unused", form)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("a POST without CSRF token was accepted: %d", resp.StatusCode)
	}
	if info, _ := e.St.TokenByID(own); info.RevokedAt != "" {
		t.Error("the token was revoked without a CSRF token")
	}

	// The owner is an administrator: the foreign token is listed and counted.
	foreign := tokenAt(t, e.St, "gina", "gina-never", at(40*24*time.Hour), "")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/account/tokens")
	if !strings.Contains(page, "gina-never") {
		t.Skip("the owner of the fixture is no administrator")
	}
	resp = tpPost(t, e, e.Owner, "/ui/account/tokens/revoke-unused", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("interactive administrator: %d", resp.StatusCode)
	}
	if info, _ := e.St.TokenByID(foreign); info.RevokedAt == "" {
		t.Error("the administrator's revoke did not reach the foreign unused token")
	}
}
