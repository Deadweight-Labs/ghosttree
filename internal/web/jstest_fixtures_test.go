package web

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scripts of the interface are tested under node (jstest/*.test.mjs) against
// small pages. The pages are not written by hand where they can come from the
// templates: the fixtures in jstest/fixtures are what the real templates render,
// and this test fails when a template changes so that the script and its test
// would be looking at markup that no longer exists. To accept a change:
//
//	UPDATE_GOLDEN=1 go test -p 1 -run TestJSFixturesAreWhatTheTemplatesRender ./internal/web/
func goldenFixture(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("jstest", "fixtures", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; write it with UPDATE_GOLDEN=1", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from what the template renders now; run UPDATE_GOLDEN=1 go test -p 1 -run TestJSFixturesAreWhatTheTemplatesRender ./internal/web/ and check the node tests\n--- fixture\n%s\n--- template\n%s", path, want, got)
	}
}

func TestJSFixturesAreWhatTheTemplatesRender(t *testing.T) {
	// The rows of /ui/search/suggest: an object, a key, a value with a quote in it
	// (html/template writes it as &#34;) and the line that searches for it all.
	var rows bytes.Buffer
	err := langEN.page().ExecuteTemplate(&rows, "searchsuggest", suggestView{Rows: []suggestRow{
		{Kind: "hit", Href: "/ui/x/shell/issues/7", Title: "Fix the deploy", RefKind: "request", RefN: 7},
		{Kind: "hit", Href: "/ui/x/shell/pulls/9", Title: "Deploy script", RefKind: "pr", RefN: 9, PRState: "open"},
		{Kind: "qual", Insert: "is:", Title: "is:", Lead: "State"},
		{Kind: "qual", Insert: "is:open", Title: "is:open"},
		{Kind: "qual", Insert: `branch:"feat/a,b"`, Title: `branch:"feat/a,b"`},
		{Kind: "all", Href: "/ui/search?q=" + url.QueryEscape("deploy is:o"), Mark: "search", Title: langEN.msg("search.suggest_for", "deploy is:o")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	goldenFixture(t, "suggest-rows.html", rows.Bytes())

	// The search field of the header on a repository page, as the layout writes it.
	x := seedExact(t)
	_, page := fetchPage(t, x.Owner, x.Base+"/ui/x/shell")
	field := regexp.MustCompile(`(?s)<div class="hs"><form class="hsf".*?</form></div>`).FindString(page)
	if field == "" || !strings.Contains(field, `data-suggest="/ui/search/suggest"`) || !strings.Contains(field, `data-suggest-project="`+shellProject+`"`) {
		t.Fatalf("the header field is not on the page:\n%.600s", page)
	}
	goldenFixture(t, "suggest-field.html", []byte(field+"\n"))
}
