package web

import (
	"bytes"
	stdhtml "html"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func renderPart(t *testing.T, l lang, name string, data any) string {
	t.Helper()
	var out bytes.Buffer
	if err := l.page().ExecuteTemplate(&out, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out.String()
}

func partText(s string) string {
	s = regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func TestRefPillIsTheLongFormAndTheRefTheShortOne(t *testing.T) {
	for kind, want := range map[string]string{"pain": "#55", "request": "#599", "task": "#124", "idea": "#7"} {
		n := map[string]int64{"pain": 55, "request": 599, "task": 124, "idea": 7}[kind]
		long := string(refPillHTML(langDE, kind, n, "/x"))
		if !strings.Contains(long, `class="ref pill `) || !strings.Contains(long, `<svg class="i"`) || !strings.Contains(long, `<span class="num">`+want+`</span>`) {
			t.Errorf("%s long form: %s", kind, long)
		}
		short := string(refHTML(langDE, kind, n, "/x"))
		if strings.Contains(short, "pill") || !strings.Contains(short, `<span class="num">#`) || !strings.Contains(short, `<svg class="i"`) {
			t.Errorf("%s short form: %s", kind, short)
		}
	}
	if got := string(refPillHTML(langDE, "pain", 55, "")); !strings.HasPrefix(got, "<span") {
		t.Errorf("without an address the pill is no link: %s", got)
	}
}

func TestAvatarsOfPersonAgentGardenerAndSystem(t *testing.T) {
	person := string(avatarMarkup(langDE, avatarSpec{Name: "Mara", Size: "lg"}))
	if !strings.Contains(person, `class="gav hc-`) || !strings.Contains(person, ">MA<") {
		t.Errorf("person: %s", person)
	}
	agent := string(avatarMarkup(langDE, avatarSpec{Kind: avClaude, Name: "claude", Owner: "Robin", Size: "lg"}))
	if !strings.Contains(agent, "gav agent a-claude lg") || !strings.Contains(agent, `class="own hc-`) || !strings.Contains(agent, "Agent von Robin") {
		t.Errorf("agent with owner badge: %s", agent)
	}
	if small := string(avatarMarkup(langDE, avatarSpec{Kind: avClaude, Owner: "Robin"})); strings.Contains(small, `class="own hc-`) {
		t.Errorf("the small agent avatar has no badge: %s", small)
	}
	if g := string(avatarOfName("Gärtner")); !strings.Contains(g, "a-gardener") {
		t.Errorf("gardener: %s", g)
	}
	if s := string(avatarOfName("ghosttree")); !strings.Contains(s, "a-system") {
		t.Errorf("system: %s", s)
	}
}

func avatarOfName(name string) template.HTML { return avatarHTML(langDE, "", name, false) }

func TestWaitStackHasOneOutlineAndTheDotAtTheLastAvatar(t *testing.T) {
	one := string(waitStack(langDE, []string{"Robin"}))
	if !strings.Contains(one, `class="waits"`) || strings.Count(one, `class="ring"`) != 1 || !strings.Contains(one, `Wartet auf Robin`) {
		t.Errorf("one waiting person: %s", one)
	}
	two := string(waitStack(langDE, []string{"Robin", "Mara"}))
	if !strings.Contains(two, `class="waits multi"`) || strings.Count(two, `class="ring"`) != 2 || strings.Count(two, `class="wstack"`) != 1 || !strings.Contains(two, "Robin, Mara") {
		t.Errorf("two waiting people: %s", two)
	}
	if got := waitStack(langDE, nil); got != "" {
		t.Errorf("nobody waits: %q", got)
	}
}

func TestBandPutsActiveAndMentionedTabsBehindTheSeparatorAndTheRestAsIcons(t *testing.T) {
	b := &bandView{Label: "repo.tabs", Tabs: []bandTab{
		{Key: "code", Label: "repo.tab.code", Href: "/c", Icon: "code", Current: true},
		{Key: "issues", Label: "repo.tab.issues", Href: "/i", Icon: "issue", Count: "9"},
	}, Extras: []bandTab{
		{Key: "rooms", Label: "repo.tab.rooms", Href: "/r", Icon: "chat", Mentions: 2},
		{Key: "sessions", Label: "repo.tab.sessions", Href: "/s", Icon: "term", Count: "2"},
		{Key: "commits", Label: "repo.tab.commits", Href: "/m", Icon: "commit"},
	}}
	page := renderPart(t, langDE, "uiband", b)
	sep := strings.Index(page, `class="tsep"`)
	if sep < 0 || strings.Index(page, `data-tab="issues"`) > sep {
		t.Fatalf("the row stands before the separator: %s", page)
	}
	behind := page[sep:]
	if !strings.Contains(behind, `data-tab="rooms" class="att aux"`) || !strings.Contains(behind, `<span class="counter att" title="2 Erwähnungen für dich">@2</span>`) {
		t.Errorf("a tab with mentions moves behind the separator with a blue @n: %s", behind)
	}
	if !strings.Contains(behind, `class="icx" data-ic="sessions"`) || !strings.Contains(behind, `data-tip="Sessions"`) || !strings.Contains(behind, `aria-label="Sessions, 2"`) {
		t.Errorf("the others show as icon with number and tooltip: %s", behind)
	}
	if strings.Contains(page, `data-tab="sessions"`) {
		t.Errorf("an extra without a mention has no text tab: %s", page)
	}
	b.Extras[2].Current = true
	if page := renderPart(t, langDE, "uiband", b); !strings.Contains(page[strings.Index(page, `class="tsep"`):], `data-tab="commits" class="sel aux"`) {
		t.Error("the open extra shows with its name")
	}
	one := &bandView{Label: "repo.tabs", Tabs: []bandTab{{Key: "code", Label: "repo.tab.code", Href: "/c", Icon: "code"}}}
	if page := renderPart(t, langDE, "uiband", one); strings.Contains(page, `class="tsep"`) {
		t.Errorf("without a second group there is no separator: %s", page)
	}
}

// A band can scroll: the row stands in a box with the fades and the "+n" mark at its
// ends (ui-tabs.js fills them), and every tab carries its name for the tooltip it
// gets when it shrinks to an icon.
func TestBandHasABoxWithFadesAndAMoreMarkAndEveryTabNamesItself(t *testing.T) {
	b := &bandView{Label: "repo.tabs", Tabs: []bandTab{
		{Key: "code", Label: "repo.tab.code", Href: "/c", Icon: "code", Current: true},
		{Key: "issues", Label: "repo.tab.issues", Href: "/i", Icon: "issue", Count: "9"},
	}}
	page := renderPart(t, langDE, "uiband", b)
	for _, want := range []string{`<div class="utabs-box"><nav class="utabs-nav" data-fit`, `class="utabs-fade l" aria-hidden="true" hidden`, `class="utabs-fade r" hidden><button type="button" class="utabs-more" data-more>`, `href="/i" data-tip="Issues"`} {
		if !strings.Contains(page, want) {
			t.Errorf("band lacks %q: %s", want, page)
		}
	}
	if strings.Index(page, `</nav>`) > strings.Index(page, `utabs-fade r`) || strings.Index(page, `utabs-fade r`) > strings.Index(page, `</div></div>`) {
		t.Errorf("the fades stand behind the nav and inside the box: %s", page)
	}
}

func TestOrgBandHasTheTabsOfAnOrganisation(t *testing.T) {
	b := orgBand("/ui/acme", "agents", map[string]int{"repos": 5, "agents": 3}, true)
	page := renderPart(t, langDE, "uiband", b)
	for _, want := range []string{`data-tab="repos"`, `data-tab="agents" class="sel"`, `href="/ui/acme/agents" aria-current="page"`, `data-tab="people"`, `data-tab="rooms"`, `data-tab="settings"`, `aria-label="Organisation"`, `<span class="counter">5</span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("org band lacks %q: %s", want, page)
		}
	}
	if page := renderPart(t, langDE, "uiband", orgBand("/ui/acme", "repos", nil, false)); strings.Contains(page, `data-tab="settings"`) {
		t.Error("settings are for owners")
	}
}

func TestListRowHasTheIconOnceTheMutedNumberAndFixedColumns(t *testing.T) {
	row := uiRow{Icon: "issue", Tone: "open", StateLabel: "Issue, offen", Num: "#602", Title: "Git-natives ghosttree", Href: "/i/602",
		Kind: "Feature", Age: "4 T.", AgeTitle: "geöffnet vor 4 Tagen", Person: "Robin", Waiting: []string{"Robin"}, Progress: &rowProgress{Done: 3, Total: 5, Title: "3 von 5"}}
	page := renderPart(t, langDE, "uirow", row)
	for _, want := range []string{`<span class="st open" role="img" aria-label="Issue, offen">`, `<span class="rn">#602</span>`, `<a class="title" href="/i/602">`, `<span class="art">Feature</span>`,
		`<span class="age" title="geöffnet vor 4 Tagen">4 T.</span>`, `<span class="rw"><span class="waits"`, `<span class="rp"><span class="prog lead"`, `<span class="pt">3/5</span>`, `data-pct="60"`} {
		if !strings.Contains(page, want) {
			t.Errorf("row lacks %q: %s", want, page)
		}
	}
	if n := strings.Count(page, `<svg class="i"`); n != 1 {
		t.Errorf("the state icon stands once in the row, found %d icons", n)
	}
	if strings.Index(page, `class="rw"`) > strings.Index(page, `class="rp"`) {
		t.Error("the waiting column stands left of the progress")
	}
	row.Progress.Warn = true
	if page := renderPart(t, langDE, "uirow", row); !strings.Contains(page, "prog lead amber") {
		t.Error("a missing proof paints the progress amber")
	}
	row.Waiting, row.Progress = nil, nil
	if page := renderPart(t, langDE, "uirow", row); !strings.Contains(page, `<span class="rw"></span><span class="rp"></span>`) {
		t.Errorf("the columns stay in place without content: %s", page)
	}
}

func TestTimelineEntryHasTheAvatarOnTheLineAndASmallActionIcon(t *testing.T) {
	items := []tlItem{
		{ID: "e1", Who: "claude", Kind: avClaude, Owner: "Robin", Icon: "check", Tone: "ok", Head: template.HTML("hat <a href=\"#k1\">Kriterium 1</a> erfüllt"), Age: "vor 2 T.", Body: "Text"},
		{Icon: "link", Head: "System sagt etwas"},
	}
	page := renderPart(t, langDE, "uitimeline", items)
	for _, want := range []string{`<div class="tlx">`, `id="e1"`, `<div class="tav">`, `gav agent a-claude lg`, `<b>claude</b><span class="aic ok">`, `<span class="tm">vor 2 T.</span>`, `<div class="bd2">Text</div>`, `<span class="bd ">`} {
		if !strings.Contains(page, want) {
			t.Errorf("timeline lacks %q: %s", want, page)
		}
	}
	if strings.Count(page, `class="cmt ev tev"`) != 2 {
		t.Error("two entries")
	}
}

func TestComposerHasTabsToolbarFieldAndFooter(t *testing.T) {
	page := renderPart(t, langDE, "uicomposer", composerView{Placeholder: "Kommentar schreiben"})
	for _, want := range []string{`class="cx"`, `data-wtab="w"`, `data-wtab="p"`, `>Schreiben<`, `>Vorschau<`, `class="wtools"`, `aria-label="Fett"`, `<textarea name="body" placeholder="Kommentar schreiben"`, `class="cx-pv prose"`, `class="cx-hint"`, `Markdown, @ erwähnt`, `data-submit>Kommentieren</button>`} {
		if !strings.Contains(page, want) {
			t.Errorf("composer lacks %q", want)
		}
	}
	if strings.Count(page, `class="gbtn sm icon quiet"`) != 8 {
		t.Errorf("eight tools: %d", strings.Count(page, `class="gbtn sm icon quiet"`))
	}
	if strings.Contains(page, "<form") || strings.Contains(page, "data-cfg") {
		t.Error("no form without an action, no mode without modes")
	}
}

func TestComposerWithModesSignalAndFields(t *testing.T) {
	c := composerView{Action: "/ui/x", Token: "tok", Placeholder: "Kommentar", Avatar: "Robin", ModeLabel: "Status",
		Modes: []composerMode{
			{Value: "open", Label: "Offen", Submit: "Kommentieren", Class: "pri", Selected: true},
			{Value: "closed", Label: "Geschlossen", Submit: "Schließen", SubmitText: "Schließen und kommentieren", Class: "dangerf", Placeholder: "Grund"},
		},
		Fields: []composerField{{Name: "evidence", Label: "Nachweis", Kind: "text", Show: "closed", Wide: true}},
		Signal: &composerSelect{Name: "signal", Label: "Signal", Options: []composerOption{{Value: "none", Label: "Keins"}, {Value: "ask", Label: "Frage", Selected: true}}},
	}
	page := renderPart(t, langDE, "uicomposer", c)
	for _, want := range []string{`<form method="post" action="/ui/x">`, `name="csrf_token" value="tok"`, `class="cmt cx-cmt"`, `data-mode="open"`, `data-cfg="`, `&#34;labelText&#34;:&#34;Schließen und kommentieren&#34;`,
		`<span class="muted ddl">Status</span> <span class="ddv">Offen</span>`, `data-show="closed"`, `class="cx-fl wide"`, `<span class="muted ddl">Signal</span> <span class="ddv">Frage</span>`, `name="signal" value="ask"`, `name="mode" value="open"`, `class="gbtn pri" data-submit>Kommentieren`} {
		if !strings.Contains(page, want) {
			t.Errorf("composer lacks %q: %s", want, page)
		}
	}
	if !strings.Contains(c.Config(), `"cls":"dangerf"`) {
		t.Errorf("config: %s", c.Config())
	}
}

func TestReactionsAreBlueWhenMineAndTheAddButtonIsASmileyWithPlus(t *testing.T) {
	page := renderPart(t, langDE, "uireactions", reactionsView{Items: []reaction{{Emoji: "👍", Value: "up", Count: 3, Mine: true, Tip: "Mit 👍 reagiert: Du"}, {Emoji: "🎉", Value: "tada", Count: 1}}, Choices: []reactionChoice{{Value: "up", Emoji: "👍", Label: "Daumen hoch", Mine: true}, {Value: "tada", Emoji: "🎉", Label: "Hurra"}}, Action: "/ui/react", Token: "t", Kind: "request", ID: "7"})
	for _, want := range []string{`class="react on" aria-pressed="true" title="Mit 👍 reagiert: Du"><span class="em">👍</span>3`, `class="react" aria-pressed="false"><span class="em">🎉</span>1`, `class="react add"`, `Reaktion hinzufügen`, `name="reaction" value="up"`, `name="kind" value="request"`, `name="id" value="7"`, `class="rc on" aria-pressed="true" title="Daumen hoch"`, `class="rpick"`} {
		if !strings.Contains(page, want) {
			t.Errorf("reactions lack %q: %s", want, page)
		}
	}
	if strings.Count(page, "<path") < 3 {
		t.Error("the add button has a smiley icon with a plus")
	}
	if page := renderPart(t, langDE, "uireactions", reactionsView{Items: []reaction{{Emoji: "👍", Count: 2}}}); strings.Contains(page, "<button") || strings.Contains(page, "<form") {
		t.Errorf("without an action the row only shows: %s", page)
	}
}

func TestHoverCardBelongsToTheNumberChip(t *testing.T) {
	page := renderPart(t, langDE, "uihc", hoverChip{Num: "#172", Title: "Git-natives ghosttree", Brief: "Offen · 23 Commits", Href: "/pr/172"})
	for _, want := range []string{`class="hc"`, `<a class="nchip" href="/pr/172">#172</a>`, `role="tooltip"`, `<b>Git-natives ghosttree</b>`, `Offen · 23 Commits`} {
		if !strings.Contains(page, want) {
			t.Errorf("chip lacks %q: %s", want, page)
		}
	}
}

func TestCommandBlockOffersMacOSAndLinuxOnly(t *testing.T) {
	page := renderPart(t, langDE, "uicmd", cmdBlock{ID: "ov", Mac: "curl -fsSL x | sh", Linux: "curl -fsSL y | sh"})
	if strings.Count(page, `role="tab"`) != 2 || !strings.Contains(page, `data-os="mac"`) || !strings.Contains(page, `data-os="lin"`) || strings.Contains(strings.ToLower(page), "windows") {
		t.Errorf("only macOS and Linux: %s", page)
	}
	for _, want := range []string{`id="ov-mac"`, `id="ov-lin"`, `data-copy-from="#ov-lin"`, `>Kopiert<`, `curl -fsSL y | sh`} {
		if !strings.Contains(page, want) {
			t.Errorf("command block lacks %q", want)
		}
	}
}

func TestFooterHasPrivacyHelpAndServerVersion(t *testing.T) {
	page := renderPart(t, langDE, "uifoot", pageData{ServerVersion: "v0.1.0"})
	if !strings.Contains(partText(page), "Datenschutz · Hilfe · ghosttree v0.1.0") {
		t.Errorf("footer: %s", partText(page))
	}
	if page := renderPart(t, langEN, "uifoot", pageData{}); strings.Contains(partText(page), "version") {
		t.Errorf("without a version the footer names none: %s", partText(page))
	}
}

// GT_GALLERY=dir writes all components, light and dark in one file, for the
// screenshots of the base (go test ./internal/web -run Gallery).
func TestComponentGalleryRenders(t *testing.T) {
	l := langDE
	var b strings.Builder
	part := func(title, html string) {
		b.WriteString(`<section class="gal"><h2 class="gal-h">` + title + `</h2>` + html + `</section>`)
	}
	band := &bandView{Label: "repo.tabs", Tabs: []bandTab{
		{Key: "code", Label: "repo.tab.code", Href: "#", Icon: "code"},
		{Key: "issues", Label: "repo.tab.issues", Href: "#", Icon: "issue", Count: "9", Current: true},
		{Key: "pulls", Label: "repo.tab.pulls", Href: "#", Icon: "pr", Count: "2"},
		{Key: "knowledge", Label: "repo.tab.knowledge", Href: "#", Icon: "book", Count: "48"},
		{Key: "runbooks", Label: "repo.tab.runbooks", Href: "#", Icon: "play"},
	}, Extras: []bandTab{
		{Key: "rooms", Label: "repo.tab.rooms", Href: "#", Icon: "chat", Mentions: 2},
		{Key: "sessions", Label: "repo.tab.sessions", Href: "#", Icon: "term", Count: "2"},
		{Key: "commits", Label: "repo.tab.commits", Href: "#", Icon: "commit"},
	}}
	part("Band", renderPart(t, l, "uiband", band))
	part("Band Organisation", renderPart(t, l, "uiband", orgBand("#", "repos", map[string]int{"repos": 5, "agents": 5, "people": 3, "rooms": 4}, true)))
	part("Verweise", `<p class="gal-p">Im Text <b>`+string(refPillHTML(l, "pain", 55, "#"))+`</b> und `+string(refPillHTML(l, "request", 599, "#"))+` und `+string(refPillHTML(l, "task", 124, "#"))+` und `+string(refPillHTML(l, "idea", 7, "#"))+`, kurz: `+string(refHTML(l, "request", 599, "#"))+` `+string(refHTML(l, "pain", 55, "#"))+` `+string(refHTML(l, "task", 124, "#"))+` `+string(refHTML(l, "idea", 7, "#"))+`</p>`)
	var av strings.Builder
	for _, s := range []avatarSpec{{Name: "Robin", Size: "lg"}, {Name: "Mara", Size: "lg"}, {Kind: avClaude, Name: "claude", Owner: "Robin", Size: "lg"}, {Kind: avCodex, Name: "codex", Owner: "Robin", Size: "lg"}, {Kind: avGardener, Size: "lg"}, {Kind: avSystem, Size: "lg"}, {Name: "Jonas"}, {Kind: avClaude, Name: "claude"}} {
		av.WriteString(string(avatarMarkup(l, s)) + " ")
	}
	part("Avatare", `<div class="gal-row">`+av.String()+`<span class="person">`+string(avatarMarkup(l, avatarSpec{Name: "Robin"}))+`Robin</span></div>`)
	part("Wartende", `<div class="gal-row">`+string(waitStack(l, []string{"Robin"}))+string(waitStack(l, []string{"Robin", "Mara"}))+string(waitStack(l, []string{"Robin", "Mara", "Jonas"}))+`</div>`)
	rows := `<div class="box">` +
		renderPart(t, l, "uirow", uiRow{Icon: "issue", Tone: "open", StateLabel: "Issue", Num: "#602", Title: "Git-natives ghosttree (Dach)", Href: "#", Kind: "Feature", Age: "4 T.", Person: "Robin", Progress: &rowProgress{Done: 3, Total: 5, Title: "3 von 5"}}) +
		renderPart(t, l, "uirow", uiRow{Icon: "issue", Tone: "open", StateLabel: "Issue", Num: "#601", Title: "Arbeitspakete: Requests bündeln", Href: "#", Kind: "Feature", Age: "3 T.", Person: "claude", Waiting: []string{"Robin", "Mara"}, Progress: &rowProgress{Done: 2, Total: 9, Title: "2 von 9"}}) +
		renderPart(t, l, "uirow", uiRow{Icon: "pain", Tone: "pain", StateLabel: "Pain", Num: "#55", Title: "Reproduzierbar: Neustart bei 240 Sessions", Href: "#", Kind: "Pain", Age: "2 T.", Person: "Mara", Waiting: []string{"Robin"}, Progress: &rowProgress{Done: 7, Total: 7, Title: "7 von 7"}}) + `</div>`
	part("Listenzeilen", rows)
	part("Zeitleiste", renderPart(t, l, "uitimeline", []tlItem{
		{Who: "Robin", Icon: "link", Head: template.HTML(`hat ` + string(refPillHTML(l, "pain", 52, "#")) + ` als Ursprung verknüpft`), Age: "vor 3 T."},
		{Who: "claude", Kind: avClaude, Owner: "Robin", Icon: "check", Tone: "ok", Head: `hat <a href="#">Kriterium 1</a> erfüllt <a class="chip sha" href="#">a41c7e2</a>`, Age: "vor 2 T.", Body: "Hell und Dunkel folgen dem System."},
		{Icon: "tri", Tone: "warn", Head: "Systemereignis ohne Autor", Age: "gestern"},
	}))
	part("Eingabe", renderPart(t, l, "uicomposer", composerView{Avatar: "Robin", Placeholder: "Kommentar schreiben", ModeLabel: "Status",
		Modes:  []composerMode{{Value: "open", Label: "Offen", Submit: "Kommentieren", Class: "pri", Selected: true}, {Value: "closed", Label: "Geschlossen", Submit: "Schließen", SubmitText: "Schließen und kommentieren", Class: "dangerf", Placeholder: "Warum wird geschlossen?"}},
		Fields: []composerField{{Name: "evidence", Label: "Nachweis", Kind: "text", Show: "closed", Wide: true}}}))
	part("Reaktionen", renderPart(t, l, "uireactions", reactionsView{Items: []reaction{{Emoji: "👍", Value: "up", Count: 3, Mine: true}, {Emoji: "🎉", Value: "tada", Count: 1}}, Choices: []reactionChoice{{Value: "up", Emoji: "👍", Label: "Daumen hoch", Mine: true}, {Value: "tada", Emoji: "🎉", Label: "Hurra"}}, Action: "#", Token: "x", Kind: "request", ID: "1"}))
	part("Hover-Karte", `<div class="gal-row gal-hc">`+renderPart(t, l, "uihc", hoverChip{Num: "#172", Title: "Git-natives ghosttree: Anker, Arbeitspakete, Oberfläche", Brief: "Offen · feat/git-nativ nach dev · 23 Commits", Href: "#", Show: true})+`</div>`)
	part("Befehl", renderPart(t, l, "uicmd", cmdBlock{ID: "gal", Mac: "curl -fsSL https://gt.example/install.sh | sh -s -- --pair k7q2m4xd", Linux: "curl -fsSL https://gt.example/install.sh | sh -s -- --pair k7q2m4xd"}))
	if b.Len() == 0 {
		t.Fatal("empty gallery")
	}
	if dir := os.Getenv("GT_GALLERY"); dir != "" {
		doc := `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="` + os.Getenv("GT_STATIC") + `/ui-tokens.css"><link rel="stylesheet" href="` + os.Getenv("GT_STATIC") + `/ui-base.css"><link rel="stylesheet" href="` + os.Getenv("GT_STATIC") + `/ui-shell.css"><link rel="stylesheet" href="` + os.Getenv("GT_STATIC") + `/ui-parts.css"><link rel="stylesheet" href="` + os.Getenv("GT_STATIC") + `/ui-pages.css"><style>.gal{margin:0 0 32px}.gal-h{margin:0 0 8px;font-size:12px;font-weight:600;color:var(--fg-muted);text-transform:uppercase;letter-spacing:.04em}.gal-row{display:flex;align-items:center;flex-wrap:wrap;gap:16px}.gal-p{font-size:14px}.gal-hc{padding-bottom:110px;justify-content:flex-end}.gal .rband{margin:0 -20px}</style></head><body class="ui gt"><main class="g2"><div class="wrap">` + b.String() + `</div></main><script src="` + os.Getenv("GT_STATIC") + `/ui-tabs.js"></script><script src="` + os.Getenv("GT_STATIC") + `/ui-parts.js"></script></body></html>`
		if err := os.WriteFile(filepath.Join(dir, "gallery.html"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommandBlockWithOneCommandHasNoTabs(t *testing.T) {
	page := renderPart(t, langDE, "uicmd", cmdBlock{ID: "rb", Mac: "ctx run x", Linux: "ctx run x"})
	if strings.Contains(page, `role="tab"`) || strings.Count(page, `<pre class="cmd"`) != 1 || !strings.Contains(page, `data-copy-from="#rb-lin"`) {
		t.Errorf("one command is one block with one copy button: %s", page)
	}
}

func TestCommandTextMarksTheAddressAndEscapesTheRest(t *testing.T) {
	got := string(cmdText(`curl -fsSL https://x.test/install.sh?a=1&b=2 | sh -s -- --note "<b>"`))
	for _, want := range []string{`<span class="u">https://x.test/install.sh?a=1&amp;b=2</span>`, ` | sh -s -- --note &#34;&lt;b&gt;&#34;`} {
		if !strings.Contains(got, want) {
			t.Errorf("cmdText lacks %q: %s", want, got)
		}
	}
	if plain := stdhtml.UnescapeString(partText(got)); !strings.Contains(plain, `https://x.test/install.sh?a=1&b=2 | sh -s -- --note "<b>"`) {
		t.Errorf("the copied text stays the plain command: %q", plain)
	}
}

func TestCopyButtonVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		btn  copyBtn
		want []string
		not  []string
	}{
		"from":  {copyBtn{From: "#a"}, []string{`data-copy-from="#a"`, `class="clb"`, `>Kopieren<`, `>Kopiert<`}, []string{`icon`}},
		"text":  {copyBtn{Text: "abc", Tip: "SHA kopieren"}, []string{`data-copy-text="abc"`, `aria-label="SHA kopieren"`, `icon quiet`}, []string{`class="clb"`}},
		"here":  {copyBtn{}, []string{`data-copy-here`}, nil},
		"page":  {copyBtn{Page: true, Label: "Link kopieren"}, []string{`data-copy-page`, `>Link kopieren<`}, nil},
		"label": {copyBtn{Text: "a/b", Label: "Pfad kopieren"}, []string{`data-copy-text="a/b"`, `>Pfad kopieren<`}, nil},
	} {
		page := renderPart(t, langDE, "uicopy", tc.btn)
		for _, want := range tc.want {
			if !strings.Contains(page, want) {
				t.Errorf("%s: copy button lacks %q: %s", name, want, page)
			}
		}
		for _, not := range tc.not {
			if strings.Contains(page, not) {
				t.Errorf("%s: copy button has %q: %s", name, not, page)
			}
		}
		if !strings.Contains(page, "copy js-only") {
			t.Errorf("%s: every copy button carries the class copy: %s", name, page)
		}
	}
}

func TestLabelPickerStartsEmptyAndTheButtonWaitsForAChoice(t *testing.T) {
	page := renderPart(t, langDE, "uilabelbox", &labelBoxView{CanEdit: true, Kind: "request", ID: 3, Options: []string{"git", "ui"}})
	for _, want := range []string{`class="sblk lbbox stk"`, `<option value="" selected disabled hidden>Label wählen …</option>`, `data-needs-pick`} {
		if !strings.Contains(page, want) {
			t.Errorf("label picker lacks %q: %s", want, page)
		}
	}
}
