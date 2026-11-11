package store

import (
	"strings"
	"testing"
)

// REQ-601 AC-13: the text of a pull request is kept as a clean text and as the
// newest one GitHub said.

func bodyOf(t *testing.T, st *Store, n int64) string {
	t.Helper()
	p, err := st.PullRequestOf(prProject, n)
	if err != nil {
		t.Fatal(err)
	}
	return p.Body
}

func withBody(c PullRequestChange, body string, at string) PullRequestChange {
	c.Body, c.HeadAt = &body, at
	return c
}

func TestPullRequestBodyIsKeptCleanedAndShortened(t *testing.T) {
	st := prFixture(t)
	dirty := "## Was\r\n\r\n- eins\r\n- <script>alert(1)</script>‮\x00\x07zwei\r\n\ttabbed\n\n" + "x"
	if res, err := st.RecordPullRequest(withBody(prEvent(7, "opened", "open", "feat/a"), dirty, "")); err != nil || !res.Applied {
		t.Fatalf("opened: %+v %v", res, err)
	}
	got := bodyOf(t, st, 7)
	for _, bad := range []string{"\r", "\x00", "\x07", "‮"} {
		if strings.Contains(got, bad) {
			t.Errorf("the body keeps %q: %q", bad, got)
		}
	}
	// Markup stays text: the page escapes it; here it is just kept as it was written.
	if !strings.Contains(got, "<script>alert(1)</script>zwei") || !strings.Contains(got, "\ttabbed") || !strings.HasPrefix(got, "## Was\n\n- eins") {
		t.Errorf("the body lost its text: %q", got)
	}
	// Only so much is kept.
	long := strings.Repeat("ä", maxPullBody+500)
	if _, err := st.RecordPullRequest(withBody(prEvent(8, "opened", "open", "feat/b"), long, "")); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(bodyOf(t, st, 8))); n != maxPullBody {
		t.Errorf("a long body is kept with %d characters, want %d", n, maxPullBody)
	}
}

func TestPullRequestBodyHasSecretsBlackedOut(t *testing.T) {
	st := prFixture(t)
	token := "ghp_" + strings.Repeat("a1B2c3D4e5", 3) + "xyz123"
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "opened", "open", "feat/a"), "Deploy with token "+token+" please", "")); err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, st, 7); strings.Contains(got, token) || !strings.Contains(got, "Deploy with token") {
		t.Errorf("the token stays in the body: %q", got)
	}
}

// A event that does not say the body leaves it; an empty one clears it; an older
// event never replaces a newer text (webhooks arrive out of order).
func TestPullRequestBodyIsOnlyChangedByWhatSaysIt(t *testing.T) {
	st := prFixture(t)
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "opened", "open", "feat/a"), "first", "2026-11-11T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	// An event without the text (a synchronize from a client that left it out, a seed).
	if _, err := st.RecordPullRequest(prEvent(7, "synchronize", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, st, 7); got != "first" {
		t.Errorf("an event without a text changed it: %q", got)
	}
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "edited", "open", "feat/a"), "second", "2026-11-11T11:00:00Z")); err != nil {
		t.Fatal(err)
	}
	// The edit before it arrives late.
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "edited", "open", "feat/a"), "stale", "2026-11-11T10:30:00Z")); err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, st, 7); got != "second" {
		t.Errorf("a late older event replaced the newer text: %q", got)
	}
	// The catch-up has no time and says what GitHub has now, also nothing at all.
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "sync", "open", "feat/a"), "", "")); err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, st, 7); got != "" {
		t.Errorf("a pull request whose text was taken away still has %q", got)
	}
}

// The text of a pull request of a project the viewer cannot read is not read by
// the detail view either.
func TestPullRequestBodyFollowsTheRightToReadTheProject(t *testing.T) {
	st := prFixture(t)
	if _, err := st.RecordPullRequest(withBody(prEvent(7, "opened", "open", "feat/a"), "private words", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("sara"); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})
	stranger := st.Access(Principal{ID: "person:2", Label: "sara", TokenKind: WebSessionKind})
	if d, err := stranger.PullRequestDetail(prProject, 7); err == nil || d.Body != "" {
		t.Errorf("a stranger reads the pull request: %+v %v", d, err)
	}
	owner := st.Access(Principal{ID: "person:1", Label: "robin", TokenKind: WebSessionKind})
	if d, err := owner.PullRequestDetail(prProject, 7); err != nil || d.Body != "private words" {
		t.Errorf("the owner reads %+v %v", d, err)
	}
}
