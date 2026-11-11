package forge

import (
	"errors"
	"net/http"
	"testing"
)

func pullBody(action string, merged bool, state string) string {
	m := "false"
	if merged {
		m = "true"
	}
	return `{"action":"` + action + `","number":7,"installation":{"id":5},
	 "repository":{"full_name":"Deadweight-Labs/App","default_branch":"main"},
	 "pull_request":{"number":7,"title":"Fix","state":"` + state + `","merged":` + m + `,"merge_commit_sha":"abc1234def",
	  "head":{"ref":"feat/a","repo":{"full_name":"Deadweight-Labs/App"}},"base":{"ref":"main"}}}`
}

func TestWebhookTranslatesPullRequestEvents(t *testing.T) {
	f := newFakeApp(t)
	var got []PullEvent
	h := f.app().WebhookHandlerWithPulls(func(int64, []string, []string) error { t.Fatal("repo sync must not run"); return nil },
		func(e PullEvent) error { got = append(got, e); return nil }, nil)
	cases := []struct {
		body, state string
	}{
		{pullBody("opened", false, "open"), "open"},
		{pullBody("reopened", false, "open"), "open"},
		{pullBody("synchronize", false, "open"), "open"},
		{pullBody("edited", false, "open"), "open"},
		{pullBody("closed", false, "closed"), "closed"},
		{pullBody("closed", true, "closed"), "merged"},
	}
	for _, c := range cases {
		got = nil
		if rec := deliver(t, h, "pull_request", "whsec", c.body); rec.Code != http.StatusNoContent || len(got) != 1 {
			t.Fatalf("%s: %d %v", c.body, rec.Code, got)
		}
		e := got[0]
		if e.State != c.state || e.InstallationID != 5 || e.Remote != "github.com/deadweight-labs/app" || e.HeadRemote != e.Remote ||
			e.Number != 7 || e.Head != "feat/a" || e.Base != "main" || e.DefaultBranch != "main" || e.MergeSHA != "abc1234def" {
			t.Fatalf("event: %+v", e)
		}
	}
	// Ein Fork-PR trägt das Head-Repo, ein gelöschter Fork keines: beides ist vom Store abzulehnen.
	got = nil
	fork := `{"action":"opened","installation":{"id":5},"repository":{"full_name":"A/app"},"pull_request":{"number":1,"state":"open","head":{"ref":"main","repo":{"full_name":"evil/app"}},"base":{"ref":"main"}}}`
	deliver(t, h, "pull_request", "whsec", fork)
	if len(got) != 1 || got[0].HeadRemote != "github.com/evil/app" || got[0].Remote == got[0].HeadRemote {
		t.Fatalf("fork: %+v", got)
	}
	got = nil
	gone := `{"action":"opened","installation":{"id":5},"repository":{"full_name":"A/app"},"pull_request":{"number":1,"state":"open","head":{"ref":"x","repo":null},"base":{"ref":"main"}}}`
	deliver(t, h, "pull_request", "whsec", gone)
	if len(got) != 1 || got[0].HeadRemote != "" {
		t.Fatalf("deleted fork: %+v", got)
	}
}

func TestWebhookPullRequestNeedsTheSignatureAndIgnoresOtherActions(t *testing.T) {
	f := newFakeApp(t)
	var got []PullEvent
	h := f.app().WebhookHandlerWithPulls(nil, func(e PullEvent) error { got = append(got, e); return nil }, nil)
	if rec := deliver(t, h, "pull_request", "wrong", pullBody("opened", false, "open")); rec.Code != http.StatusUnauthorized || len(got) != 0 {
		t.Fatalf("bad signature: %d %v", rec.Code, got)
	}
	for _, c := range [][2]string{
		{"pull_request", pullBody("labeled", false, "open")},
		{"pull_request", pullBody("assigned", false, "open")},
		{"pull_request", "not json"},
		{"pull_request_review", pullBody("opened", false, "open")},
	} {
		if rec := deliver(t, h, c[0], "whsec", c[1]); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", c[0], rec.Code)
		}
	}
	if len(got) != 0 {
		t.Fatalf("ignored events reached the store: %v", got)
	}
	// Ohne Empfänger (alter Aufruf) bleibt es bei 204.
	plain := f.app().WebhookHandler(func(int64, []string, []string) error { return nil }, nil)
	if rec := deliver(t, plain, "pull_request", "whsec", pullBody("opened", false, "open")); rec.Code != http.StatusNoContent {
		t.Fatalf("plain handler: %d", rec.Code)
	}
	// Ein Fehler beim Anwenden wird gemeldet, ohne den Körper preiszugeben.
	var logged error
	failing := f.app().WebhookHandlerWithPulls(nil, func(PullEvent) error { return errors.New("db down") }, func(err error) { logged = err })
	rec := deliver(t, failing, "pull_request", "whsec", pullBody("opened", false, "open"))
	if rec.Code != http.StatusInternalServerError || logged == nil || rec.Body.Len() > 40 {
		t.Fatalf("failing: %d %v %q", rec.Code, logged, rec.Body.String())
	}
}

// REQ-601 AC-13: the text of the pull request comes with the event; a null is a
// pull request without a text, a missing field is an event that does not say it.
func TestWebhookCarriesThePullRequestText(t *testing.T) {
	f := newFakeApp(t)
	var got []PullEvent
	h := f.app().WebhookHandlerWithPulls(func(int64, []string, []string) error { return nil },
		func(e PullEvent) error { got = append(got, e); return nil }, nil)
	for _, c := range []struct {
		field string
		want  *string
	}{
		{`"body":"## Was\nein Text",`, ptr("## Was\nein Text")},
		{`"body":null,`, ptr("")},
		{``, nil},
	} {
		got = nil
		body := `{"action":"edited","number":7,"installation":{"id":5},"repository":{"full_name":"A/app"},
		 "pull_request":{"number":7,"title":"Fix",` + c.field + `"state":"open","head":{"ref":"feat/a","repo":{"full_name":"A/app"}},"base":{"ref":"main"}}}`
		if rec := deliver(t, h, "pull_request", "whsec", body); rec.Code != http.StatusNoContent || len(got) != 1 {
			t.Fatalf("%s: %d %v", c.field, rec.Code, got)
		}
		switch {
		case c.want == nil && got[0].Body != nil:
			t.Errorf("%q: the event says %q, want nothing", c.field, *got[0].Body)
		case c.want != nil && (got[0].Body == nil || *got[0].Body != *c.want):
			t.Errorf("%q: the event says %v, want %q", c.field, got[0].Body, *c.want)
		}
	}
}

func ptr(s string) *string { return &s }
