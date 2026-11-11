package web

import (
	"encoding/json"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The reference pill (UI review P2): one building block for everything a page
// points at. The kind is a coloured icon, the number or name stands quiet next
// to it, and in the long form the title follows, separated from the number.
//
//	{{refPill "request" 602 .Href}}              icon #602                 short
//	{{refPillT "request" 602 .Href .Title}}      icon #602 Title           long
//	{{prPill 184 .Href "merged"}}                icon #184, tone by state
//	{{branchPill .Name .Href}}                   icon feat/long-name…      truncates
//	{{shaPill .Sha .Href}}                       icon 1a2b3c4
//	{{pillx "org" .Name .Href .Name .Brief}}     any other kind with a card
//
// One shape (.ref.pill) in every list, sidebar and sentence; ui-pill.js builds
// the same markup for the Markdown renderer. Numbered kinds carry data-ref and
// get their card from the server (refcards.go); every other pill carries its own
// card in data-pc, because the page that shows the pill knows what it is.
// A pill never sets a title attribute: the card says it, the browser must not
// say it twice.

// pillCard is what the hover card of a pill without a server card shows. The
// short keys keep the attribute small; ui-refs.js reads the same names.
type pillCard struct {
	Label string `json:"l,omitempty"` // what it is: "Pull request"
	State string `json:"s,omitempty"` // state word, shown as a pill
	Tone  string `json:"t,omitempty"` // colour of the state: open, done, gray, bad, amber4
	Title string `json:"h,omitempty"` // the name or title
	Brief string `json:"b,omitempty"` // one or two lines
	Who   string `json:"w,omitempty"` // author
	When  string `json:"a,omitempty"` // when
}

func (c pillCard) empty() bool { return c == pillCard{} }

// pillKind is the look of a pill that has no number of its own: the class that
// colours the icon, the icon and the catalog key of its name.
type pillKind struct {
	Class, Icon, Key string
	// Mono sets the text in the code font (branch, commit, path).
	Mono bool
}

var pillKinds = map[string]pillKind{
	"pr":      {"k-pr", "pr", "ref.kind.pull", false},
	"commit":  {"k-commit", "commit", "ref.kind.commit", true},
	"branch":  {"k-branch", "branch", "ref.kind.branch", true},
	"know":    {"k-know", "book", "ref.kind.knowledge", false},
	"org":     {"k-org", "people", "ref.kind.org", false},
	"machine": {"k-machine", "server", "ref.kind.machine", false},
	"url":     {"k-url", "ext", "ref.kind.link", false},
	"file":    {"k-file", "file", "ref.kind.file", true},
	"folder":  {"k-file", "folder", "ref.kind.folder", true},
	"lock":    {"k-lock", "lock", "ref.kind.hidden", false},
}

// pillSpec is everything one pill needs.
type pillSpec struct {
	// Class is the kind class (k-req, k-pr, ...), Icon the svg body of the kind.
	Class string
	Icon  template.HTML
	// Text is the number or name that stands next to the icon; Title is the
	// long form and follows it.
	Text, Title string
	Href        string
	// Ref is the key of a numbered kind ("REQ-5"); its card comes from the server.
	Ref string
	// Card is the card of a pill without a Ref.
	Card pillCard
	// Mono sets Text in the code font. External marks a link that leaves ghosttree.
	Mono, External bool
	// Extra classes, for the state of a pull request (s-merged, ...).
	Extra string
	// Name is what a screen reader says instead of the text ("Issue 602").
	Name string
	// Long asks the script for the title: in a sentence of the history the pill
	// is written out (ui-refs.js fills it from the card of the page or /ui/refs).
	Long bool
}

// pillHTML writes one pill; with an empty Href it is a span, not a link. The
// href of an identifier may come from data a person wrote, so it goes through
// safeRefHref.
func pillHTML(s pillSpec) template.HTML {
	href := safeRefHref(s.Href)
	cls := "ref pill " + s.Class
	if s.Mono {
		cls += " mono"
	}
	if s.Extra != "" {
		cls += " " + s.Extra
	}
	var b strings.Builder
	tag := "span"
	if href != "" {
		tag = "a"
	}
	b.WriteString("<" + tag + ` class="` + html.EscapeString(cls) + `"`)
	if href != "" {
		b.WriteString(` href="` + html.EscapeString(href) + `"`)
		if s.External {
			b.WriteString(` rel="noopener noreferrer"`)
		}
	}
	if s.Ref != "" {
		b.WriteString(` data-ref="` + html.EscapeString(s.Ref) + `"`)
		if s.Long {
			b.WriteString(` data-long`)
		}
	} else if !s.Card.empty() {
		if raw, err := json.Marshal(s.Card); err == nil {
			b.WriteString(` data-pc="` + html.EscapeString(string(raw)) + `"`)
		}
	}
	if s.Name != "" {
		b.WriteString(` aria-label="` + html.EscapeString(s.Name) + `"`)
	}
	b.WriteString(`><svg class="i" viewBox="0 0 16 16" aria-hidden="true">` + string(s.Icon) + `</svg>`)
	b.WriteString(`<span class="num">` + html.EscapeString(s.Text) + `</span>`)
	if s.Title != "" {
		b.WriteString(`<span class="pt">` + html.EscapeString(s.Title) + `</span>`)
	}
	b.WriteString("</" + tag + ">")
	return template.HTML(b.String())
}

// numberedPill is the pill of a numbered kind (request, pain, idea, task,
// decision, feature): its short form "#602", with the title when it is known.
func numberedPill(l lang, kind string, n int64, href, title string) template.HTML {
	return numberedPillOf(l, kind, n, href, title, false)
}

// longPill is the pill of a numbered kind for a sentence: icon, number and the
// title, which the script adds. A title too long for the line is cut with an
// ellipsis; the card says it whole.
func longPill(l lang, kind string, n int64, href string) template.HTML {
	return numberedPillOf(l, kind, n, href, "", true)
}

func numberedPillOf(l lang, kind string, n int64, href, title string, long bool) template.HTML {
	k, ok := refKinds[kind]
	if !ok {
		k = refKinds["request"]
	}
	return pillHTML(pillSpec{Class: k.Class, Icon: template.HTML(k.Icon), Text: pillNum(k.Prefix, n),
		Title: title, Href: href, Ref: k.Prefix + "-" + strconv.FormatInt(n, 10), Long: long,
		Name: strings.TrimSpace(l.msg(k.Key) + " " + strconv.FormatInt(n, 10) + " " + title)})
}

// kindPill is the pill of a kind without a number, with the card the page can
// give it. The name of the kind heads the card.
func kindPill(l lang, kind, text, href string, card pillCard) template.HTML {
	k, ok := pillKinds[kind]
	if !ok {
		k = pillKinds["url"]
	}
	if card.Label == "" && !card.empty() {
		card.Label = l.msg(k.Key)
	}
	return pillHTML(pillSpec{Class: k.Class, Icon: mockIconBody(k.Icon), Text: text, Href: href, Card: card, Mono: k.Mono,
		External: strings.HasPrefix(href, "http")})
}

// mockIconBody is the svg body of a mock icon, "" for an unknown name.
func mockIconBody(name string) template.HTML { return template.HTML(mockIcons[name]) }

// prStates names the look of a pull request: open is green, merged purple,
// closed red. A pull request of unknown state is grey.
var prStates = map[string]struct{ Extra, Key, Tone string }{
	"open":   {"s-open", "pulls.state.open", "open"},
	"merged": {"s-merged", "pulls.state.merged", "done"},
	"closed": {"s-closed", "pulls.state.closed", "bad"},
}

// prPill is a pull request as one pill: its icon takes the colour of its state,
// so a merged one is no longer green. title, who and brief fill the card.
func prPill(l lang, n int64, href, state, title, brief string) template.HTML {
	return prPillOf(l, n, href, state, title, brief, false)
}

// prPillOf is prPill; external marks an address outside ghosttree (a GitHub link
// in a text), whose state nobody here has read.
func prPillOf(l lang, n int64, href, state, title, brief string, external bool) template.HTML {
	card := pillCard{Label: l.msg("ref.kind.pull") + " #" + strconv.FormatInt(n, 10), Title: title, Brief: brief}
	st, ok := prStates[state]
	if ok {
		card.State, card.Tone = l.msg(st.Key), st.Tone
	} else {
		st.Extra = "s-unknown"
	}
	k := pillKinds["pr"]
	return pillHTML(pillSpec{Class: k.Class, Icon: mockIconBody(k.Icon), Text: "#" + strconv.FormatInt(n, 10), Href: href, Card: card, Extra: st.Extra, External: external})
}

// pullPillFor reads a pull request as the viewer and writes its pill: the colour
// of its state and a card with title, branches and author. A pull request the
// viewer cannot read, or that GitHub never reported, is a plain pill without a
// state; the branch names stay out of it when the viewer may not see them.
func pullPillFor(l lang, pa *store.ProjectAccess, project, repoHref string, n int64) template.HTML {
	href := repoHref + "/pulls/" + strconv.FormatInt(n, 10)
	p, err := pa.PullRequestView(project, n)
	if err != nil {
		return prPill(l, n, href, "", "", "")
	}
	head, base := pa.BranchView(project, p.Head), pa.BranchView(project, p.Base)
	brief := head
	switch {
	case head == "":
		brief = base
	case base != "":
		brief += " → " + base
	}
	return prPill(l, n, href, p.State, p.Title, brief)
}

// branchPill is a branch name that never breaks the page: the text is cut with an
// ellipsis, the card carries the whole name.
func branchPill(l lang, name, href string) template.HTML {
	return kindPill(l, "branch", name, href, pillCard{Title: name})
}

// shaPill is a commit by its short hash.
func shaPill(l lang, sha, href string) template.HTML {
	return kindPill(l, "commit", sha, href, pillCard{Title: sha})
}

// urlLabel is how an address reads in a pill: the host and the end of the path
// ("github.com/…/pull/163"), never the whole string.
func urlLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return host
	}
	parts := strings.Split(path, "/")
	if len(parts) > 2 {
		parts = append([]string{"…"}, parts[len(parts)-2:]...)
	}
	return host + "/" + strings.Join(parts, "/")
}

// githubPull reads "https://github.com/org/repo/pull/163": the number, true.
func githubPull(raw string) (int64, bool) {
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(strings.TrimPrefix(u.Host, "www.")) != "github.com" {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return 0, false
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	return n, err == nil && n > 0
}

// urlPill is an outside address as a pill: a GitHub pull request as the pull
// request it is, any other address by host and the end of its path.
func urlPill(l lang, raw string) template.HTML {
	if n, ok := githubPull(raw); ok {
		return prPillOf(l, n, raw, "", "", urlLabel(raw), true)
	}
	return kindPill(l, "url", urlLabel(raw), raw, pillCard{Title: urlLabel(raw), Brief: raw})
}

// pillFuncs are the template functions of the pill, bound to a language.
func pillFuncs(l lang) template.FuncMap {
	return template.FuncMap{
		"refPill": func(kind string, n int64, href string) template.HTML { return numberedPill(l, kind, n, href, "") },
		"refPillT": func(kind string, n int64, href, title string) template.HTML {
			return numberedPill(l, kind, n, href, title)
		},
		"prPill": func(n int64, href, state string) template.HTML {
			return prPill(l, n, href, state, "", "")
		},
		"prPillT": func(n int64, href, state, brief string) template.HTML {
			return prPill(l, n, href, state, "", brief)
		},
		"branchPill": func(name, href string) template.HTML { return branchPill(l, name, href) },
		"shaPill":    func(sha, href string) template.HTML { return shaPill(l, sha, href) },
		"urlPill":    func(raw string) template.HTML { return urlPill(l, raw) },
		"titleRefs":  func(text string) template.HTML { return titleRefs(l, text) },
		// pillx is the generic one: kind from pillKinds, text, address, then the
		// title and the line of the card.
		"pillx": func(kind, text, href, title, brief string) template.HTML {
			return kindPill(l, kind, text, href, pillCard{Title: title, Brief: brief})
		},
		// keyPill is the pill of a reference key ("REQ-5", "PAIN-2") for a short
		// form in a sidebar: the kind and the number come from the key.
		"keyPill": func(key, href string) template.HTML { return keyPill(l, key, href) },
	}
}

func init() {
	// A knowledge entry is written #1970 in text and keyed KN-1970 on the page.
	refKinds["knowledge"] = refKind{"KN", "k-know", "ref.kind.knowledge", mockIcons["book"]}
}

// keyKinds maps the prefix of a reference key to its kind.
var keyKinds = map[string]string{"REQ": "request", "PAIN": "pain", "IDEA": "idea", "TASK": "task", "DEC": "decision", "FEAT": "feature", "KN": "knowledge"}

// keyPill is the pill of "REQ-5"; a key it does not know gives "".
func keyPill(l lang, key, href string) template.HTML {
	prefix, num, ok := strings.Cut(key, "-")
	kind, known := keyKinds[prefix]
	n, err := strconv.ParseInt(num, 10, 64)
	if !ok || !known || err != nil {
		return ""
	}
	return numberedPill(l, kind, n, href, "")
}

// titleRefRE finds "REQ-599" in a title, with the parentheses around it that
// commit and pull request titles put there: "Tabs (REQ-599)".
var titleRefRE = regexp.MustCompile(`(\s*\(\s*)?\b((?:REQ|PAIN|IDEA|TASK|DEC|FEAT)-([0-9]{1,15}))\b(\s*\))?`)

// titleRefs writes a title with its references as pills. A pill in a title is
// no link (a title is mostly a link itself); the card on hover says what it is.
// "(REQ-599)" becomes the pill without its parentheses.
func titleRefs(l lang, text string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range titleRefRE.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(html.EscapeString(text[last:m[0]]))
		last = m[1]
		open, closing := m[2] >= 0, m[8] >= 0
		prefix, _, _ := strings.Cut(text[m[4]:m[5]], "-")
		kind := keyKinds[prefix]
		n, err := strconv.ParseInt(text[m[6]:m[7]], 10, 64)
		if kind == "" || err != nil {
			b.WriteString(html.EscapeString(text[m[0]:m[1]]))
			continue
		}
		switch {
		case open && closing:
			if strings.TrimLeft(text[m[2]:m[3]], " \t") != text[m[2]:m[3]] || m[0] > 0 {
				b.WriteString(" ")
			}
		case open:
			b.WriteString(html.EscapeString(text[m[2]:m[3]]))
		}
		b.WriteString(string(numberedPill(l, kind, n, "", "")))
		if closing && !open {
			b.WriteString(html.EscapeString(text[m[8]:m[9]]))
		}
	}
	b.WriteString(html.EscapeString(text[last:]))
	return template.HTML(b.String())
}
