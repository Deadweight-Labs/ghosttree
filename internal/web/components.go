package web

import (
	"encoding/json"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Components of the GitHub-like interface (REQ-599, phase 1). Each one is a
// partial in templates/components.html that takes one of the structs below, or
// a template function from identity.go. Pages call them like this:
//
//	{{template "uiband" .Repo.Band}}               tab band of a repository
//	{{template "uirow" .}}                         list row (uiRow)
//	{{template "uitimeline" .Items}}               timeline of tlItem entries
//	{{template "uicomposer" .Composer}}            composer (composerView)
//	{{template "uireactions" .Reactions}}          reactions (reactionsView)
//	{{template "uihc" .Chip}}                      number chip with hover card
//	{{template "uicmd" .Command}}                  copyable command, macOS and Linux
//	{{refPill "pain" 55 .Href}}  {{ref "request" 7 .Href}}  {{waitStack .Names}}
//
// The CSS is ui-parts.css, the behaviour ui-parts.js and ui-tabs.js.

// ---- Tab band ----------------------------------------------------------------

// bandTab is one tab of a band. Icon is the name of a mock icon. Count is shown
// beside the label ("" shows nothing); Mentions is the number of mentions for
// the viewer behind the tab (the count then turns into a blue "@n").
type bandTab struct {
	Key, Label, Href, Icon, Count string
	Mentions                      int
	Current                       bool
}

// bandView is the band under the header: Tabs are the row, Extras the second
// group behind a thin separator. An extra that is open or has a mention moves
// to the front of that group, the others show as icon with number. Label is the
// catalog key of the aria-label. External is a link "on the host" for the band.
type bandView struct {
	Label    string
	Tabs     []bandTab
	Extras   []bandTab
	External string
}

// Promoted are the extras that show with their name: the open one and those with a mention.
func (b *bandView) Promoted() []bandTab {
	var out []bandTab
	for _, t := range b.Extras {
		if t.Current || t.Mentions > 0 {
			out = append(out, t)
		}
	}
	return out
}

// Icons are the other extras: icon, number, name as tooltip.
func (b *bandView) Icons() []bandTab {
	var out []bandTab
	for _, t := range b.Extras {
		if !t.Current && t.Mentions == 0 {
			out = append(out, t)
		}
	}
	return out
}

// Separator is true when the second group has any tab.
func (b *bandView) Separator() bool { return len(b.Extras) > 0 }

// MentionKey is the catalog key of the tooltip of the mention count.
func (t bandTab) MentionKey() string {
	if t.Mentions == 1 {
		return "ui.band.mentions_one"
	}
	return "ui.band.mentions"
}

// orgBand is the band of an organisation page: repositories, agents, people,
// rooms and, for owners, settings. base is the address of the organisation
// ("/ui/acme"); counts maps a tab key to its number.
func orgBand(base, current string, counts map[string]int, owner bool) *bandView {
	tab := func(key, label, path, icon string) bandTab {
		t := bandTab{Key: key, Label: label, Href: base + path, Icon: icon, Current: key == current}
		if n, ok := counts[key]; ok {
			t.Count = strconv.Itoa(n)
		}
		return t
	}
	v := &bandView{Label: "ui.org.tabs", Tabs: []bandTab{
		tab("repos", "ui.org.tab.repos", "", "repo"),
		tab("agents", "ui.org.tab.agents", "/agents", "bot"),
		tab("people", "ui.org.tab.people", "/people", "people"),
		tab("rooms", "ui.org.tab.rooms", "/rooms", "chat"),
		tab("machines", "ui.org.tab.machines", "/machines", "server"),
	}}
	if owner {
		v.Tabs = append(v.Tabs, tab("settings", "ui.org.tab.settings", "/settings", "gear"))
	}
	return v
}

// ---- List row ----------------------------------------------------------------

// rowProgress is the progress column: "3/5" and the bar.
type rowProgress struct {
	Done, Total int
	// Warn paints it amber (everything met, a proof is missing).
	Warn  bool
	Title string
}

// Percent is the bar width, 0..100.
func (p rowProgress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	return p.Done * 100 / p.Total
}

// Text is "3/5".
func (p rowProgress) Text() string { return strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total) }

// rowLabel is a label pill in line one.
// Class is the colour class of a label (labelClass); empty draws it grey.
type rowLabel struct{ Text, Href, Class string }

// uiRow is one row of a list of issues, pains or ideas: state icon once,
// "#nr" muted, title; line two kind, short time, person and further pieces;
// at the right the fixed columns, those waiting left of the progress.
type uiRow struct {
	// Icon is the mock icon of the state, Tone its colour class (open, closed,
	// pain, idea, merged, drop), StateLabel its text for screen readers.
	Icon, Tone, StateLabel string
	Num                    string
	Title, Href            string
	Labels                 []rowLabel
	Kind                   string
	// Age is the short time ("4 d"), AgeTitle the long one for the tooltip.
	Age, AgeTitle string
	Person        string
	// Actor is the agent behind Person, if one wrote it (REQ-620).
	Actor actorView
	// Meta are further pieces of line two (branch, PR, sub-issues), ready as HTML.
	Meta     []template.HTML
	Waiting  []string
	WaitText string
	// Excerpt is a line of text under the title (an idea's first words, what a
	// pain leaves open); ExLead is its bold start. Pill is a state word at the
	// right ("offen") with its tone class (ok, warn, done, out). Chip is what the
	// row points at, at the right, ready as a pill (an issue "#597", a pull request).
	Excerpt, ExLead, Pill, PillTone string
	Chip                            template.HTML
	Progress                        *rowProgress
	// Live is the live dot of an agent at work, ready as HTML; LiveID names the
	// issue so the live stream can replace it.
	Live   template.HTML
	LiveID string
}

// HasRight says whether the row has anything for its right column; a row without
// one has no column, so no empty slot pushes the others.
func (r uiRow) HasRight() bool {
	return r.Chip != "" || r.Pill != "" || len(r.Waiting) > 0 || r.Progress != nil || r.Live != "" || r.LiveID != ""
}

// ---- Timeline ----------------------------------------------------------------

// tlItem is one entry of the timeline. With Who the avatar sits on the line and
// the action icon beside the name; without it (a system event) a coloured mark
// with the icon takes the place of the avatar. Head is what follows the name
// (ready as HTML), Body the box under the line.
type tlItem struct {
	ID, Act, Who, Kind, Owner string
	Icon, Tone                string
	Head                      template.HTML
	Age                       string
	Body                      template.HTML
	// Right stands at the right end of the head, in line with the anchor: the
	// commits an entry is about, not part of the sentence.
	Right template.HTML
	// Git is the anchor of the entry as links (commit, pull request, branch).
	Git *anchorView
	// Comment makes the entry a comment box instead of a line of the history;
	// at is the moment the entry sorts by when comments are mixed in.
	Comment *commentView
	at      time.Time
}

// withActor makes the entry the work of the agent or person the stamp names:
// the name the page writes, the avatar kind and the owner in its corner.
func (it *tlItem) withActor(v actorView) {
	if v.Label == "" {
		return
	}
	it.Who, it.Kind, it.Owner = v.Label, v.Kind, v.Owner()
}

// firstHead is the head of the first object of a page: the one bubble that
// opens the timeline of an issue, a pain, an idea, a task or a feature. Actor
// says who made it (an agent with the mark of its owner, a person by name);
// Who is the fallback name when no stamp is known.
type firstHead struct {
	Actor     actorView
	Who       string
	Verb      string
	Age, Long string
	Mark      string
	EditHref  string
	EditTip   string
	// Git is the anchor the first object was written at (commit, pull request,
	// branch); it stands at the right end of the head.
	Git *anchorView
}

// firstOf is the head of the first object from what the page knows.
func firstOf(l lang, av actorView, who, verb, age, mark, editHref string) firstHead {
	h := firstHead{Actor: av, Who: who, Verb: verb, Age: age, Mark: mark, EditHref: editHref}
	if editHref != "" {
		h.EditTip = l.msg("issue.action.edit")
	}
	return h
}

// ---- Composer ----------------------------------------------------------------

// composerMode is one choice of the mode select (the status of an issue, say):
// it sets the button label and class, the placeholder and shows its own fields.
type composerMode struct {
	Value, Label string
	// Hint is the small text beside the entry in the menu.
	Hint string
	// Submit is the button text, SubmitText the text when the field has content.
	Submit, SubmitText string
	// Class is the button class after "gbtn": "pri", "dangerf", "".
	Class, Placeholder, Tip string
	Selected                bool
}

// composerOption is a choice in a field or in the signal select.
type composerOption struct {
	Value, Label string
	Selected     bool
}

// composerField is a field between the text and the footer. Show lists the
// modes in which it is visible (empty: always). Kind is "text" or "select".
type composerField struct {
	Name, Label, Kind, Value string
	Show                     string
	Wide                     bool
	Options                  []composerOption
}

// composerSelect is a select in the footer, left of the buttons (the signal slot).
type composerSelect struct {
	Name, Label string
	Options     []composerOption
}

// Value is the value of the chosen option.
func (s composerSelect) Value() string {
	for _, o := range s.Options {
		if o.Selected {
			return o.Value
		}
	}
	if len(s.Options) > 0 {
		return s.Options[0].Value
	}
	return ""
}

// Current is the label of the chosen option.
func (s composerSelect) Current() string {
	for _, o := range s.Options {
		if o.Selected {
			return o.Label
		}
	}
	if len(s.Options) > 0 {
		return s.Options[0].Label
	}
	return ""
}

// composerView is the one input component: tabs write and preview, toolbar,
// text field and footer. Optional: a mode select (Modes with ModeLabel and
// Fields), a signal select (Signal) and an avatar (Avatar is the name of the
// writer; the composer then stands in the timeline).
type composerView struct {
	ID, Action, Token, Name string
	Placeholder, Value      string
	Submit, Cancel          string
	// CancelHref makes "Cancel" a link (a comment that is being edited) instead
	// of a button that closes the field.
	CancelHref      string
	NoHint, NoTools bool
	// NoButtons leaves out the submit and cancel buttons (the page has its own).
	NoButtons bool
	Avatar    string
	ModeLabel string
	Modes     []composerMode
	Fields    []composerField
	Signal    *composerSelect
	// Hidden are further fixed fields of the form (the room a reply goes to).
	Hidden []hiddenField
}

// hiddenField is a fixed field of a composer's form.
type hiddenField struct{ Name, Value string }

// FieldName is the name of the text field, "body" by default.
func (c composerView) FieldName() string {
	if c.Name == "" {
		return "body"
	}
	return c.Name
}

// Mode is the mode that is selected, or the first one.
func (c composerView) Mode() composerMode {
	for _, m := range c.Modes {
		if m.Selected {
			return m
		}
	}
	if len(c.Modes) > 0 {
		return c.Modes[0]
	}
	return composerMode{}
}

// Config is the data-cfg of the composer: per mode the button, the placeholder.
func (c composerView) Config() string {
	type entry struct {
		Label     string `json:"label"`
		LabelText string `json:"labelText,omitempty"`
		Cls       string `json:"cls"`
		Tip       string `json:"tip,omitempty"`
		Ph        string `json:"ph,omitempty"`
	}
	out := map[string]entry{}
	for _, m := range c.Modes {
		out[m.Value] = entry{Label: m.Submit, LabelText: m.SubmitText, Cls: m.Class, Tip: m.Tip, Ph: m.Placeholder}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// Tools are the buttons of the toolbar: icon name and catalog key of the tooltip.
func (composerView) Tools() [][2]string {
	return [][2]string{{"bold", "ui.cx.tool.bold"}, {"italic", "ui.cx.tool.italic"}, {"quote", "ui.cx.tool.quote"},
		{"code", "ui.cx.tool.code"}, {"link", "ui.cx.tool.link"}, {"list", "ui.cx.tool.list"},
		{"tasks", "ui.cx.tool.tasks"}, {"at", "ui.cx.tool.mention"}}
}

// ---- Reactions ----------------------------------------------------------------

// reaction is "👍 3". Mine marks that the viewer reacted (blue). Value is what a
// click sends.
type reaction struct {
	Emoji, Value string
	Count        int
	Mine         bool
	// Tip is the tooltip: who reacted and what a click does.
	Tip string
}

// reactionChoice is one of the eight in the picker of the add button.
type reactionChoice struct {
	Value, Emoji, Label string
	Mine                bool
}

// reactionsView is the row of reactions and the button to add one. With an
// Action the buttons are forms that act on the object Kind and ID name (and the
// add button opens Choices); without one the row only shows.
type reactionsView struct {
	Items         []reaction
	Choices       []reactionChoice
	Action, Token string
	Kind, ID      string
}

// ---- Hover card, command block --------------------------------------------------

// hoverChip is a number chip ("#172") with a card (title and a short text) on
// hover and focus. Show opens the card without the pointer (for tests and shots).
type hoverChip struct {
	Num, Title, Brief, Href string
	Show                    bool
}

// cmdBlock is a command to copy with a tab per operating system; the tab of the
// visitor's platform is open at the start. Only macOS and Linux exist.
type cmdBlock struct {
	ID, Mac, Linux string
}

// copyBtn is the one copy button of the interface ("uicopy"). From is a CSS
// selector of the element whose text is copied, Text is a literal. With Tip the
// button is a bare icon (SHA, path); without it the button says "Copy" and,
// for a moment after a success, "Copied", in the same width.
type copyBtn struct {
	From, Text, Tip string
	Label           string // the word on the button when it is not "Copy"
	Page            bool   // copies the address of the page itself
}

// cmdURLRE finds an address in a command.
var cmdURLRE = regexp.MustCompile(`https?://[^\s"'<>]+`)

// cmdText writes a command as HTML: the text is escaped, an address in it stands in the
// accent colour (span.u). The copied text is the plain text of the block, unchanged.
func cmdText(s string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range cmdURLRE.FindAllStringIndex(s, -1) {
		b.WriteString(template.HTMLEscapeString(s[last:m[0]]))
		b.WriteString(`<span class="u">` + template.HTMLEscapeString(s[m[0]:m[1]]) + `</span>`)
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(s[last:]))
	return template.HTML(b.String())
}

// ---- Template functions ---------------------------------------------------------

// waitStack writes the waiting people as one stack: one red outline around
// everything, a dot at the last avatar. Without names it writes nothing.
func waitStack(l lang, names []string) template.HTML {
	if len(names) == 0 {
		return ""
	}
	return avatarsWaiting(l, names)
}

// issueUIRow turns a row of the issue list into the list row component: the
// number muted before the title, kind, short time and person in line two, the
// branch as a further piece, the waiting person left of the progress.
func issueUIRow(l lang, r issueRow) uiRow {
	row := uiRow{Icon: r.Icon, Tone: r.Tone, StateLabel: r.StateLabel, Num: "#" + strconv.FormatInt(r.N, 10), Title: r.Title, Href: r.Href,
		Kind: r.TypeLabel, Age: r.Age, AgeTitle: r.Opened, Person: r.Person, Actor: r.Actor, Labels: r.Labels,
		Excerpt: r.Excerpt, ExLead: r.ExLead, Pill: r.Pill, PillTone: r.PillTone}
	if r.Branch != "" {
		b := html.EscapeString(r.Branch)
		row.Meta = append(row.Meta, template.HTML(`<a class="bn" href="?q=branch%3A`+url.QueryEscape(r.Branch)+`">`+string(mockIcon("branch"))+b+`</a>`))
	}
	if r.Waiting != "" {
		row.Waiting = []string{r.Waiting}
	}
	if r.Total > 0 {
		row.Progress = &rowProgress{Done: r.Done, Total: r.Total, Title: l.msg("requests.progress")}
	}
	if r.Kind == "request" {
		row.LiveID = strconv.FormatInt(r.N, 10)
		row.Live = liveDotHTML(l, r.Live)
	}
	return row
}
