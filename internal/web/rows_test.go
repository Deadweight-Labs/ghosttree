package web

import (
	"os"
	"strings"
	"testing"
)

// The title link of a list row stretches over the row (class rl); what the row
// holds with its own target lies above it. The rule is one stylesheet for every
// list, and every list marks its link.
func TestListRowsStretchTheTitleLinkOverTheRow(t *testing.T) {
	css, err := os.ReadFile("static/ui-rows.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".rl::after", "position: absolute", "inset: 0", ":not(.rl) { position: relative; z-index: 1; }"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("ui-rows.css lacks %q", want)
		}
	}
	head, err := os.ReadFile("templates/layout.html")
	if err != nil || !strings.Contains(string(head), "/static/ui-rows.css") {
		t.Error("the head does not load ui-rows.css")
	}
	// Each list marks the primary link of its row.
	for file, want := range map[string]string{
		"templates/components.html": `<a class="title rl"`, "templates/sessions.html": `<a class="title rl"`, "templates/runbooks.html": `class="rbname rl mono"`,
		"templates/knowledge.html": `<a class="rl" href="{{.Href}}">{{.Title}}</a></div>`, "templates/pulls.html": `<a class="rl" href="{{.Href}}">`,
		"templates/codetree.html": `<a class="rl" href="{{.Href}}" title="{{.Name}}">`, "templates/agents.html": `<a class="title rl"`,
		"templates/docs.html": `class="dcname rl"`, "templates/org.html": `<a class="rl" href="{{.Href}}">{{.Name}}</a>`, "templates/search.html": `class="ht rl"`,
		"templates/machines.html": `<a class="title rl"`,
	} {
		b, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: the row link lacks the class rl (%s)", file, want)
		}
	}
}

func TestMarkWordsMarksEveryWordInAnyCaseAndEscapesTheRest(t *testing.T) {
	cases := []struct {
		text  string
		words []string
		want  string
	}{
		{"Gärtner-Hinweise im Posteingang", []string{"gärtner"}, "<mark>Gärtner</mark>-Hinweise im Posteingang"},
		{"a <b> & Stripes", []string{"stripes", "a"}, "<mark>a</mark> &lt;b&gt; &amp; <mark>Stripes</mark>"},
		{"nothing here", []string{"zzz"}, "nothing here"},
		{"plain <tag>", nil, "plain &lt;tag&gt;"},
	}
	for _, c := range cases {
		if got := string(markWords(c.text, c.words)); got != c.want {
			t.Errorf("markWords(%q, %v) = %q, want %q", c.text, c.words, got, c.want)
		}
	}
}
