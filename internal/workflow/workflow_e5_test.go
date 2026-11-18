package workflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ownPrompt = `---
on: idea.created
inputs: [idea.title]
tools: [knowledge.search, issue.read]
rounds: 6
results:
  comment: 1
model: mittel
budget:
  run_usd: 0.40
---
Prüfe, ob es schon Wissen zu dieser Idee gibt.
`

// AC-698.1 at the check: an own prompt is a valid stage 3 workflow; the check
// shows its tools and rounds, and research where the server has a search
// service; it refuses unknown tools, research tools in the list, tools on a
// block, a description as result, and an own prompt the policy forbids.
func TestCheckAcceptsAnOwnPromptWithItsTools(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	d, rep := Check(ownPrompt, Env{Alias: func(string) bool { return true }, Research: yes})
	if !rep.OK() || d.Stage() != StagePrompt || d.TaskPrompt() != "Prüfe, ob es schon Wissen zu dieser Idee gibt." || rep.Rounds != 6 ||
		strings.Join(rep.Tools, ",") != "knowledge.search,issue.read" || rep.MaxRunUSD != 0.40 || rep.Unbounded {
		t.Fatalf("check: %+v", rep)
	}
	out := FormatReport(rep)
	for _, want := range []string{"stage: 3", "tools: knowledge.search, issue.read (read only)", "rounds: at most 6", "comment (at most 1, as proposals)", "0.40 USD a run"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	research := strings.Replace(ownPrompt, "rounds: 6", "research: true", 1)
	if _, rep := Check(research, Env{Research: yes}); !rep.OK() || !strings.Contains(strings.Join(rep.Tools, ","), "web.search,web.fetch") || len(rep.Warnings) > 0 {
		t.Fatalf("research with a search service: %+v", rep)
	}
	if _, rep := Check(research, Env{Research: no}); !rep.OK() || !hasProblem(rep.Warnings, "no search service") || strings.Contains(strings.Join(rep.Tools, ","), "web.") {
		t.Fatalf("research without a search service: %+v", rep)
	}
	if _, rep := Check(research, Env{Research: yes, Policy: Policy{DenyResearch: true}}); !hasProblem(rep.Errors, "forbids research") {
		t.Fatalf("research against the policy: %+v", rep.Errors)
	}
	if _, rep := Check(ownPrompt, Env{Policy: Policy{DenyPrompt: true}}); !hasProblem(rep.Errors, "forbids an own prompt") {
		t.Fatalf("own prompt against the policy: %+v", rep.Errors)
	}
	if _, rep := Check(strings.Replace(ownPrompt, "budget:\n  run_usd: 0.40\n", "", 1), Env{}); !rep.Unbounded {
		t.Fatalf("an own prompt without a cap is unbounded: %+v", rep)
	}
	for text, want := range map[string]string{
		strings.Replace(ownPrompt, "issue.read]", "issue.write]", 1):                                     "unknown tool issue.write",
		strings.Replace(ownPrompt, "issue.read]", "web.fetch]", 1):                                       "comes with research: true",
		strings.Replace(ownPrompt, "issue.read]", "knowledge.search]", 1):                                "named twice",
		strings.Replace(ownPrompt, "comment: 1", "description: 1", 1):                                    "gardener",
		strings.Replace(ownPrompt, "rounds: 6", "rounds: 30", 1):                                         "rounds: a number from 1 to 24",
		strings.Replace(ownPrompt, "model: mittel\n", "", 1):                                             "name a model alias",
		strings.Replace(ownPrompt, "results:\n  comment: 1\n", "", 1):                                    "has to declare what it may propose",
		strings.Replace(ownPrompt, "model: mittel\n", "model: mittel\nprompt: kurz\n", 1):                "not both",
		"---\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\ntools: [knowledge.search]\n---\n":        "tools belong to an own prompt",
		"---\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\nrounds: 3\n---\n":                        "rounds belongs to an own prompt",
		"---\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\nprompt: \"Fasse zusammen\"\n---\n":       "prompt belongs to a workflow without uses",
		"---\non: idea.created\ninputs: [idea.title]\nresults:\n  comment: 1\nmodel: mittel\n---\n   \n": "needs its text",
	} {
		if _, rep := Check(text, Env{}); !hasProblem(rep.Errors, want) {
			t.Errorf("%q: want %q, got %v", text, want, rep.Errors)
		}
	}
}

// The search service: a SearXNG answer becomes hits (only http(s) addresses),
// and the query the server sends is the one given.
func TestResearchSearchesTheOperatorsService(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("q") + "|" + r.URL.Query().Get("format")
		fmt.Fprint(w, `{"results":[{"url":"https://example.org/a","title":"A","content":"erster"},{"url":"javascript:alert(1)","title":"x"},{"url":"http://example.org/b","title":"B","content":"zweiter"}]}`)
	}))
	defer srv.Close()
	web, err := NewWebResearch(srv.URL + "/search")
	if err != nil {
		t.Fatal(err)
	}
	hits, err := web.Search(context.Background(), "doppelte webhooks", 8)
	if err != nil || len(hits) != 2 || hits[0].URL != "https://example.org/a" || asked != "doppelte webhooks|json" {
		t.Fatalf("hits %+v, asked %q, %v", hits, asked, err)
	}
	if _, err := NewWebResearch("http://example.org/search"); err == nil {
		t.Fatal("plain http to another machine was accepted as the search address")
	}
	t.Setenv(ResearchURLEnv, "")
	if web, err := ResearchFromEnv(); web != nil || err != nil || ResearchConfigured() {
		t.Fatal("research without a search address")
	}
}

// web.fetch reads only what a web.search of the run returned, and the client
// for pages reaches public addresses only: loopback and the metadata address
// are refused at the dial, also when the address came from a search.
func TestResearchFetchesOnlyFoundPublicPages(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Seite &amp; Titel</title><style>p{}</style></head><body><script>alert(1)</script><p>Erster Absatz.</p><p>Zweiter&nbsp;Absatz.</p></body></html>`)
	}))
	defer page.Close()
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[{"url":%q,"title":"Seite","content":"x"},{"url":"http://169.254.169.254/latest","title":"Meta","content":"x"}]}`, page.URL+"/doc")
	}))
	defer search.Close()
	web, err := NewWebResearch(search.URL)
	if err != nil {
		t.Fatal(err)
	}
	tb := &Toolbox{web: web, found: map[string]bool{}, searchable: map[string]bool{}}
	tb.allowSearchWords("Doppelte Webhooks abfangen")
	if _, err := toolWebFetch(context.Background(), tb, toolArgs{URL: page.URL + "/doc"}); err == nil || !strings.Contains(err.Error(), "only an address a web.search") {
		t.Fatalf("fetch before a search: %v", err)
	}
	// What a read tool returned may not leave in a query: only words of the
	// inputs, the task and earlier hits.
	if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: "webhooks Kundenliste-Passwort"}); err == nil || !strings.Contains(err.Error(), "kundenliste, passwort") {
		t.Fatalf("a query with foreign words: %v", err)
	}
	if out, err := toolWebSearch(context.Background(), tb, toolArgs{Query: strings.Repeat("webhooks ", 60)}); err != nil || !strings.Contains(out, page.URL+"/doc") {
		t.Fatalf("search: %q %v", out, err)
	}
	if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: "Doppelte Seite Meta"}); err != nil {
		t.Fatalf("words of earlier hits: %v", err)
	}
	// The default client for pages: the test page on loopback is refused.
	if _, err := toolWebFetch(context.Background(), tb, toolArgs{URL: page.URL + "/doc"}); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("loopback page: %v", err)
	}
	if _, err := toolWebFetch(context.Background(), tb, toolArgs{URL: "http://169.254.169.254/latest"}); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("metadata address: %v", err)
	}
	// With a client that may reach the test page, the page arrives as text.
	web.Fetcher = page.Client()
	out, err := toolWebFetch(context.Background(), tb, toolArgs{URL: page.URL + "/doc"})
	if err != nil || !strings.Contains(out, "Seite & Titel") || !strings.Contains(out, "Erster Absatz.\n\nZweiter\u00a0Absatz.") || strings.Contains(out, "alert") || strings.Contains(out, "<p>") {
		t.Fatalf("page: %q %v", out, err)
	}
}

// The tools of a run are the declared ones; web.search and web.fetch come only
// with research: true and a search service.
func TestToolboxOffersExactlyTheDeclaredTools(t *testing.T) {
	d, _ := Parse(ownPrompt)
	web := &WebResearch{}
	if got := strings.Join(newToolbox(&assembler{}, &d, web).Names(), ","); got != "knowledge.search,issue.read" {
		t.Fatalf("without research: %s", got)
	}
	d.Research = true
	if got := strings.Join(newToolbox(&assembler{}, &d, nil).Names(), ","); got != "knowledge.search,issue.read" {
		t.Fatalf("research without a search service: %s", got)
	}
	tb := newToolbox(&assembler{}, &d, web)
	if got := strings.Join(tb.Names(), ","); got != "knowledge.search,issue.read,web.search,web.fetch" {
		t.Fatalf("with research: %s", got)
	}
	for _, w := range tb.wire() {
		if strings.Contains(w.Name, ".") {
			t.Fatalf("wire name %q", w.Name)
		}
	}
	if _, ok := tb.lookup("pain_read"); ok {
		t.Fatal("an undeclared tool was found")
	}
}
