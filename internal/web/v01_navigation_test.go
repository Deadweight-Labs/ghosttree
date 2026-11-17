package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// WP8 of v0.1 (REQ-681): labelled repository tabs in one order, an account menu that
// fits and leads nowhere dead, and an empty state with a next step on every list.

// follow reads a page and follows redirects the way a browser does.
func follow(t *testing.T, c *http.Client, target string) (int, string) {
	t.Helper()
	base := target[:strings.Index(target, "/ui/")]
	for i := 0; i < 6; i++ {
		resp, err := c.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
			resp.Body.Close()
			if strings.HasPrefix(loc, "/") {
				loc = base + loc
			}
			target = loc
			continue
		}
		return resp.StatusCode, body(t, resp)
	}
	t.Fatalf("too many redirects from %s", target)
	return 0, ""
}

var bandTabRe = regexp.MustCompile(`<li data-tab="([a-z]+)"[^>]*><a [^>]*>(?:<svg.*?</svg>)?<span>([^<]*)</span>`)

// bandOf is "key:Label" of every tab of the first band on the page, in order.
func bandOf(t *testing.T, page string) []string {
	t.Helper()
	i := strings.Index(page, `class="utabs"`)
	if i < 0 {
		t.Fatalf("the page has no tab band: %.300s", page)
	}
	end := strings.Index(page[i:], `</ul>`)
	var out []string
	for _, m := range bandTabRe.FindAllStringSubmatch(page[i:i+end], -1) {
		out = append(out, m[1]+":"+m[2])
	}
	return out
}

// Every tab of a repository carries its name, and the row looks the same on every
// page of the repository: same tabs, same order, nothing moves when one opens.
func TestRepoTabsCarryTheirNameAndKeepOneOrderOnEveryPage(t *testing.T) {
	e := ovEnv(t)
	pages := []string{"", "/issues", "/pulls", "/knowledge", "/runbooks", "/sessions", "/features", "/commits", "/tasks", "/packages", "/settings"}
	var want []string
	for _, p := range pages {
		status, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell"+p)
		if status != http.StatusOK {
			t.Fatalf("%q answers %d", p, status)
		}
		got := bandOf(t, page)
		if want == nil {
			want = got
			if len(want) < 11 {
				t.Fatalf("the band of a lead lacks tabs: %v", want)
			}
			for _, tab := range want {
				if _, label, _ := strings.Cut(tab, ":"); strings.TrimSpace(label) == "" {
					t.Errorf("tab %q has no name", tab)
				}
			}
			for _, key := range []string{"rooms", "sessions", "features", "commits", "tasks", "packages", "settings"} {
				if !strings.Contains(strings.Join(want, " "), key+":") {
					t.Errorf("the band lacks %s: %v", key, want)
				}
			}
			continue
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: the band reads %v, want the same as the code page: %v", p, got, want)
		}
		if strings.Contains(page, `data-ic="`) || strings.Contains(page, `class="icx"`) {
			t.Errorf("%q: a tab shrinks to an icon", p)
		}
	}
}

// The account menu: "Issues" instead of "Requests", every entry leads to a page that
// exists, "Sign out" stands in the menu's own footer, and the menu is bounded by the
// window so that it never grows its own scrollbar on the way down to it.
func TestAccountMenuHasNoDeadEntryAndKeepsSignOutInView(t *testing.T) {
	e := ovEnv(t)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/inbox")
		start := strings.Index(page, `class="pop r account-menu"`)
		if start < 0 {
			t.Fatalf("%s: no account menu", name)
		}
		menu := page[start : start+strings.Index(page[start:], `</details>`)]
		if strings.Contains(menu, ">Requests<") {
			t.Errorf("%s: the menu still says Requests", name)
		}
		if !strings.Contains(menu, "<span>Issues</span>") {
			t.Errorf("%s: the menu lacks Issues", name)
		}
		if !regexp.MustCompile(`class="account-foot">.*action="/ui/logout".*Sign out</button>`).MatchString(menu) {
			t.Errorf("%s: sign out is not in the footer of the menu", name)
		}
		if !strings.Contains(menu, " data-nofilter") {
			t.Errorf("%s: the menu would get a filter field of its own", name)
		}
		seen := 0
		for _, m := range regexp.MustCompile(`<a class="opt[^"]*" (?:[a-z-]+="[^"]*" )*?href="(/ui/[^"]*)"`).FindAllStringSubmatch(menu, -1) {
			seen++
			if status, _ := follow(t, c, e.Base+m[1]); status != http.StatusOK {
				t.Errorf("%s: the menu entry %s answers %d", name, m[1], status)
			}
		}
		if seen < 4 {
			t.Errorf("%s: only %d entries were checked: %s", name, seen, menu)
		}
	}
	css, err := files.ReadFile("static/ui-shell.css")
	if err != nil {
		t.Fatal(err)
	}
	// The menu is bounded by the window below the header; a long list of organizations
	// scrolls in itself and not the whole menu.
	menuRule := regexp.MustCompile(`\.gh-header \.account-menu \{[^}]*max-height: calc\(100dvh - 64px\)`)
	orgRule := regexp.MustCompile(`\.account-menu \.org-list \{[^}]*max-height: \d+px; overflow-y: auto`)
	if !menuRule.Match(css) || !orgRule.Match(css) {
		t.Error("the stylesheet does not bound the account menu by the window and its organization list by its own height")
	}
}

// emptyBlocks are the inner HTML of every empty box of a page, depth-aware, hidden
// ones (the filter's "nothing matches") left out.
func emptyBlocks(page string) []string {
	var out []string
	open := regexp.MustCompile(`<div class="(?:[^"]*\s)?(?:box-empty|empty)(?:\s[^"]*)?"([^>]*)>`)
	for _, loc := range open.FindAllStringSubmatchIndex(page, -1) {
		if strings.Contains(page[loc[2]:loc[3]], "hidden") {
			continue
		}
		depth, i := 1, loc[1]
		for depth > 0 {
			next := strings.Index(page[i:], "div")
			if next < 0 {
				break
			}
			i += next
			switch {
			case strings.HasPrefix(page[i-1:], "<div"):
				depth++
			case strings.HasPrefix(page[i-2:], "</div"):
				depth--
			}
			i += 3
		}
		out = append(out, page[loc[1]:i])
	}
	return out
}

var (
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	sentenceRe = regexp.MustCompile(`[.!?](\s|$)`)
)

// Every empty list says in one sentence what shows up there and offers a next step: a
// link or button, or a command to copy.
func TestEveryEmptyStateHasOneSentenceAndANextStep(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	nora := loginInteractive(t, e.Srv, e.St, "nora")
	cases := []struct {
		name, path string
		c          *http.Client
	}{
		{"packages", "/ui/x/shell/packages", e.Owner},
		{"packages, closed", "/ui/x/shell/packages?state=closed", e.Owner},
		{"packages, one branch", "/ui/x/shell/packages?branch=main", e.Owner},
		{"features", "/ui/x/shell/features", e.Owner},
		{"features, retired", "/ui/x/shell/features?state=retired", e.Owner},
		{"runbooks", "/ui/x/shell/runbooks", e.Owner},
		{"runbooks, reader", "/ui/x/shell/runbooks", e.Guest},
		{"review", "/ui/review", e.Owner},
		{"context", "/ui/context?project=" + shellProject, e.Owner},
		{"context, with the waiting", "/ui/context?project=" + shellProject + "&preview=1", e.Owner},
		{"rooms", "/ui/coord", nora},
		{"agents", "/ui/agents", e.Owner},
		{"sessions", "/ui/sessions", e.Owner},
	}
	for _, tc := range cases {
		status, page := follow(t, tc.c, e.Base+tc.path)
		if status != http.StatusOK {
			t.Errorf("%s: %s answers %d", tc.name, tc.path, status)
			continue
		}
		blocks := emptyBlocks(page)
		if len(blocks) == 0 {
			t.Errorf("%s: no empty state on %s", tc.name, tc.path)
			continue
		}
		block := blocks[0]
		if !strings.Contains(block, `<a class="gbtn`) && !strings.Contains(block, `class="g-cmd"`) && !strings.Contains(block, "<button") {
			if tc.name == "runbooks, reader" {
				continue // a reader may not create one: the sentence stands alone
			}
			t.Errorf("%s: the empty state has no next step: %s", tc.name, block)
		}
		text := strings.TrimSpace(tagRe.ReplaceAllString(strings.SplitN(block, `<a class="gbtn`, 2)[0], " "))
		if text == "" {
			t.Errorf("%s: the empty state says nothing: %s", tc.name, block)
		}
		if n := len(sentenceRe.FindAllString(text, -1)); n > 1 {
			t.Errorf("%s: %d sentences, at most one: %q", tc.name, n, text)
		}
	}
}
