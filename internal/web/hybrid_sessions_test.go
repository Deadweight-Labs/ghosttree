package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var sessionHref = regexp.MustCompile(`href="/ui/sessions/([a-z0-9]{12})[?"#]`)

// addresses are the sessions a page links to, by name.
func (e sessEnv) addresses(page string) []string {
	byPID := map[string]string{}
	for name, pid := range e.pid {
		byPID[pid] = name
	}
	seen := map[string]bool{}
	for _, m := range sessionHref.FindAllStringSubmatch(page, -1) {
		if name, ok := byPID[m[1]]; ok {
			seen[name] = true
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// The web search of sessions finds a session by what it says in other words, for
// whoever may read it and for nobody else, on the page of the sessions and in the
// search of everything.
func TestSessionPagesFindByMeaningForWhoMayReadThem(t *testing.T) {
	e := seedSessions(t)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range e.id {
		if err := e.St.AppendChunks(id, []store.Chunk{{Seq: 100, Raw: uLine("wagen kaputt hier und dort in " + strings.ReplaceAll(name, "-", " "))}}); err != nil {
			t.Fatal(err)
		}
	}
	job := embedjob.New(e.St, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: []string{store.KindSession}, SessionWindow: embedjob.Window{Always: true}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	const meaning, words = "/ui/sessions?q=auto+defekt", "/ui/sessions?q=zebrafish"
	// What each viewer may read is what the search by words gives him.
	viewers := []struct {
		name   string
		client *http.Client
	}{{"owner", e.Owner}, {"member", e.Member}, {"guest", e.Guest}, {"lead", e.Lead}, {"reviewer", e.Reviewer}}
	readable := map[string][]string{}
	for _, v := range viewers {
		_, page := e.get(t, v.client, words)
		readable[v.name] = e.addresses(page)
	}
	if got := strings.Join(readable["guest"], ","); got != "anna-guests" {
		t.Fatalf("the guest reads %s", got)
	}
	// Without the switch the words of the query are in no session.
	e.St.SetEmbedder(store.EmbedSearchConfig{Embedder: c, Kinds: []string{store.KindRequest}})
	for _, v := range viewers {
		if _, page := e.get(t, v.client, meaning); len(e.addresses(page)) != 0 {
			t.Fatalf("%s: a session is found without the switch: %v", v.name, e.addresses(page))
		}
	}
	e.St.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2, Kinds: []string{store.KindSession}})
	for _, v := range viewers {
		code, page := e.get(t, v.client, meaning)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", v.name, code)
		}
		got := e.addresses(page)
		if strings.Join(got, ",") != strings.Join(readable[v.name], ",") || len(got) == 0 {
			t.Errorf("%s finds %v by meaning, may read %v", v.name, got, readable[v.name])
		}
		for _, hidden := range []string{"hidden-project"} {
			if strings.Contains(page, e.pid[hidden]) != slicesContain(readable[v.name], hidden) {
				t.Errorf("%s: %s", v.name, hidden)
			}
		}
		// The hit of a session found by meaning is the text that came closest.
		if !strings.Contains(page, "wagen kaputt hier und dort") {
			t.Errorf("%s: the hit shows no text of the session:\n%s", v.name, page)
		}
	}
	// A kind of text that is not a message is answered by words.
	if _, page := e.get(t, e.Owner, meaning+"&kind=output"); len(e.addresses(page)) != 0 {
		t.Errorf("the output of commands is searched by meaning: %v", e.addresses(page))
	}
	// A search by time is not ranked.
	if _, page := e.get(t, e.Owner, meaning+"&sort=newest"); len(e.addresses(page)) != 0 {
		t.Errorf("the sort by time is ranked by meaning: %v", e.addresses(page))
	}

	// The search of everything has a group of sessions, for members only.
	_, owner := e.get(t, e.Owner, "/ui/search?q=auto+defekt")
	_, guest := e.get(t, e.Guest, "/ui/search?q=auto+defekt")
	if !strings.Contains(owner, `href="/ui/sessions/`) || strings.Contains(guest, `href="/ui/sessions/`) {
		t.Errorf("the group of sessions: owner %v, guest %v", strings.Contains(owner, `href="/ui/sessions/`), strings.Contains(guest, `href="/ui/sessions/`))
	}

	// With the embedding server gone the page is the page of words, with no error.
	fake.Fail.Store(true)
	code, page := e.get(t, e.Owner, meaning)
	if code != http.StatusOK || len(e.addresses(page)) != 0 {
		t.Fatalf("fallback: status %d, %v", code, e.addresses(page))
	}
	if code, page = e.get(t, e.Owner, words); code != http.StatusOK || len(e.addresses(page)) == 0 {
		t.Fatalf("the search by words: status %d", code)
	}
}

func slicesContain(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
