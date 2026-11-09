package web

import (
	"html"
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

// Identifiers and avatars of the pages in the GitHub look (REQ-599, decisions
// #4310 and #4315). Both are template functions, so a page never assembles
// them by hand:
//
//	{{ref "request" .ID .Href}}   colored icon of the kind, quiet "#599"
//	{{avatar "claude" ""}}        harness avatar; "human" takes the name
//	{{avatarOf .Person}}          kind guessed from the name of an actor
//	{{avatarWait .Person}}        profile picture with the red ring
//
// "REQ-599" appears only in the title attribute of the link; the text a person
// reads is the number alone. The short forms stay in agent text, MCP and CLI.

// refKind is what one kind of record looks like: the prefix of its short form,
// the class that colors it, the catalog key of its name and the icon.
type refKind struct {
	Prefix, Class, Key, Icon string
}

var refKinds = map[string]refKind{
	"request": {"REQ", "k-req", "ref.kind.issue", `<circle cx="8" cy="8" r="6.25"/><circle cx="8" cy="8" r="1.4" fill="currentColor" stroke="none"/>`},
	"pain":    {"PAIN", "k-pain", "ref.kind.pain", `<circle cx="8" cy="8" r="6.25"/><path d="M8 4.7v3.9"/><circle cx="8" cy="11.1" r=".95" fill="currentColor" stroke="none"/>`},
	"idea":    {"IDEA", "k-idea", "ref.kind.idea", `<path d="M6.2 12.4h3.6M6.6 14.2h2.8"/><path d="M8 1.8a4.2 4.2 0 0 0-2.5 7.6c.5.4.8.9.8 1.5v.5h3.4v-.5c0-.6.3-1.1.8-1.5A4.2 4.2 0 0 0 8 1.8z"/>`},
	"task":    {"TASK", "k-task", "ref.kind.task", `<path d="M9.8 2.2 13.8 6.2 12 7.2l-1.2 3.1-1.2-1.2-3.5 3.5-.9-.9 3.5-3.5-1.2-1.2L9 5z"/>`},
}

// safeRefHref lets only relative paths, http and https through. Anything else
// (javascript:, data:, a scheme-relative //host, a backslash trick) becomes "#",
// because the href of an identifier may come from data a person wrote.
func safeRefHref(href string) string {
	if href == "" {
		return ""
	}
	if strings.ContainsAny(href, "\\") || strings.HasPrefix(href, "//") {
		return "#"
	}
	u, err := url.Parse(href)
	if err != nil {
		return "#"
	}
	switch strings.ToLower(u.Scheme) {
	case "":
		if u.Host != "" {
			return "#"
		}
	case "http", "https":
	default:
		return "#"
	}
	return href
}

// refHTML writes one identifier. href may be empty: then it is no link.
func refHTML(l lang, kind string, n int64, href string) template.HTML {
	href = safeRefHref(href)
	k, ok := refKinds[kind]
	if !ok {
		k = refKinds["request"]
	}
	short := k.Prefix + "-" + strconv.FormatInt(n, 10)
	title := html.EscapeString(l.msg(k.Key) + " " + short)
	inner := `<svg class="i" viewBox="0 0 16 16" aria-hidden="true">` + k.Icon + `</svg><span class="num">#` + strconv.FormatInt(n, 10) + `</span>`
	if href == "" {
		return template.HTML(`<span class="ref ` + k.Class + `" data-ref="` + short + `" title="` + title + `">` + inner + `</span>`)
	}
	return template.HTML(`<a class="ref ` + k.Class + `" href="` + html.EscapeString(href) + `" data-ref="` + short + `" title="` + title + `">` + inner + `</a>`)
}

// refPillHTML writes the long form of an identifier as a pill: icon and
// "PAIN-55", for running text and meta lines. The short form (refHTML) stays in
// columns and lists, where the row already says what it is.
func refPillHTML(l lang, kind string, n int64, href string) template.HTML {
	href = safeRefHref(href)
	k, ok := refKinds[kind]
	if !ok {
		k = refKinds["request"]
	}
	short := k.Prefix + "-" + strconv.FormatInt(n, 10)
	title := html.EscapeString(l.msg(k.Key) + " " + short)
	inner := `<svg class="i" viewBox="0 0 16 16" aria-hidden="true">` + k.Icon + `</svg><span class="num">` + short + `</span>`
	if href == "" {
		return template.HTML(`<span class="ref pill ` + k.Class + `" data-ref="` + short + `" title="` + title + `">` + inner + `</span>`)
	}
	return template.HTML(`<a class="ref pill ` + k.Class + `" href="` + html.EscapeString(href) + `" data-ref="` + short + `" title="` + title + `">` + inner + `</a>`)
}

// Avatar kinds. A person gets initials (or a picture, see avatarImage), every
// agent the mark of its harness, the gardener and the system a sign of their own.
const (
	avHuman    = "human"
	avClaude   = "claude"
	avCodex    = "codex"
	avGardener = "gardener"
	avSystem   = "system"
)

// avatarKeys name the avatars that stand for no person.
var avatarKeys = map[string]string{avClaude: "avatar.claude", avCodex: "avatar.codex", avGardener: "avatar.gardener", avSystem: "avatar.system"}

// harnessKind maps the harness of a session ("claude-code", "codex") to its
// avatar kind. An unknown harness is shown as the system.
func harnessKind(harness string) string {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "claude", "claude-code":
		return avClaude
	case "codex":
		return avCodex
	}
	return avSystem
}

// splitAgentLabel takes the readable name of an agent apart: "Claude · Lena
// Berger · lena-laptop" is the agent "Claude" of the owner "Lena Berger" (tool
// or own name, owner, then machine and branch). A name without the separator is
// the whole name and has no owner.
func splitAgentLabel(label string) (name, owner string) {
	parts := strings.Split(label, agentLabelSep)
	name = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		owner = strings.TrimSpace(parts[1])
	}
	return name, owner
}

// actorKind guesses what stands behind the name of an actor in a history. For
// the label of an agent ("Claude · Lena Berger · lena-laptop") the first part
// decides.
func actorKind(name string) string {
	first, _ := splitAgentLabel(name)
	n := strings.ToLower(first)
	switch {
	case n == "gardener" || n == "gärtner" || strings.HasPrefix(n, "gardener-"):
		return avGardener
	case n == "ghosttree" || n == "system" || n == "agent" || n == "":
		return avSystem
	case n == "claude" || strings.HasPrefix(n, "claude-") || strings.HasPrefix(n, "claude@") || strings.HasPrefix(n, "claude:"):
		return avClaude
	case n == "codex" || strings.HasPrefix(n, "codex-") || strings.HasPrefix(n, "codex@") || strings.HasPrefix(n, "codex:"):
		return avCodex
	}
	return avHuman
}

// avatarSpec is everything one avatar needs. Size is "", "lg" (32 px) or "xl"
// (40 px). Owner is the person an agent belongs to; their picture sits in the
// corner. Live puts the pulse dot on an agent that works right now. Waiting
// draws the red ring and dot around a person (the avatar of "waits for").
type avatarSpec struct {
	Kind, Name, Size, Owner string
	// Title replaces the tooltip of an agent.
	Title         string
	Waiting, Live bool
}

// humanTint is the number 0..5 of the colour a person without a picture gets,
// the same for the same name everywhere.
func humanTint(name string) int {
	h := 0
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		h = (h*31 + int(r)) % 9973
	}
	return h % 6
}

// avatarPhoto is the mark the picture pass looks for (avatars.go): a person's
// avatar is written with their name in data-av and replaced by <img> of the
// picture the server itself serves, when there is one.
func humanBadge(cls, name string) string {
	ini := initialsOf(name)
	if ini == "" {
		ini = "?"
	}
	if cls == "own" {
		// The owner's corner badge carries the tint of its person, like the
		// initials avatar does: an opaque disc that reads on any logo.
		cls += " hc-" + strconv.Itoa(humanTint(name))
	}
	return `<span class="` + cls + `" data-av="` + html.EscapeString(name) + `" title="` + html.EscapeString(name) + `">` + html.EscapeString(ini) + `</span>`
}

func avatarMarkup(l lang, o avatarSpec) template.HTML {
	if o.Kind == "" {
		o.Kind = actorKind(o.Name)
	}
	if o.Kind == avClaude || o.Kind == avCodex {
		// An agent called by its label brings its owner along.
		name, owner := splitAgentLabel(o.Name)
		o.Name = name
		if o.Owner == "" {
			o.Owner = owner
		}
	}
	size := ""
	if o.Size != "" {
		size = " " + o.Size
	}
	var out string
	switch o.Kind {
	case avClaude, avCodex, avGardener, avSystem:
		title := o.Name
		if title == "" || o.Kind != avClaude && o.Kind != avCodex {
			title = l.msg(avatarKeys[o.Kind])
		}
		if o.Owner != "" {
			title = l.msg("avatar.agent_of", title, o.Owner)
		}
		if o.Title != "" {
			title = o.Title
		}
		glyph := string(brandMark(o.Kind))
		switch o.Kind {
		case avGardener:
			glyph = string(mockIcon("sprout"))
		case avSystem:
			glyph = string(mockIcon("ghost"))
		}
		badge := ""
		if o.Owner != "" && o.Size != "" {
			badge = humanBadge("own", o.Owner)
		}
		live := ""
		if o.Live {
			live = `<i class="live" aria-hidden="true"></i>`
		}
		out = `<span class="gav agent a-` + o.Kind + size + `" title="` + html.EscapeString(title) + `">` + glyph + badge + live + `</span>`
	default:
		out = humanBadge("gav hc-"+strconv.Itoa(humanTint(o.Name))+size, o.Name)
	}
	if o.Waiting {
		return wrapWaiting(l, []string{o.Name}, []string{out})
	}
	return template.HTML(out)
}

// wrapWaiting draws the stack of the waiting: every avatar in a ring, the dot at
// the last one, the names for small screens and the tooltip.
func wrapWaiting(l lang, names, avatars []string) template.HTML {
	tip := html.EscapeString(l.msg("avatar.waiting", strings.Join(names, ", ")))
	cls := "waits"
	if len(names) > 1 {
		cls += " multi"
	}
	var rings strings.Builder
	for _, a := range avatars {
		rings.WriteString(`<span class="ring">` + a + `</span>`)
	}
	return template.HTML(`<span class="` + cls + `" title="` + tip + `"><span class="wstack">` + rings.String() + `</span><span class="wn">` + html.EscapeString(strings.Join(names, ", ")) + `</span></span>`)
}

// avatarsWaiting is the stack for several people waited for.
func avatarsWaiting(l lang, names []string) template.HTML {
	avatars := make([]string, len(names))
	for i, n := range names {
		avatars[i] = string(avatarMarkup(l, avatarSpec{Kind: avHuman, Name: n}))
	}
	return wrapWaiting(l, names, avatars)
}

func avatarHTML(l lang, kind, name string, waiting bool) template.HTML {
	return avatarMarkup(l, avatarSpec{Kind: kind, Name: name, Waiting: waiting})
}

// identityFuncs are the template functions of this file, bound to a language.
func identityFuncs(l lang) template.FuncMap {
	return template.FuncMap{
		"refPill":   func(kind string, n int64, href string) template.HTML { return refPillHTML(l, kind, n, href) },
		"waitStack": func(names []string) template.HTML { return waitStack(l, names) },
		"join":      strings.Join,
		"ref":       func(kind string, n int64, href string) template.HTML { return refHTML(l, kind, n, href) },
		"avatar":    func(kind, name string) template.HTML { return avatarHTML(l, kind, name, false) },
		// actor is who wrote an entry (REQ-620): a person, or "Robin’s Claude".
		// Without a stamp built for the page, person stands as before.
		"actor": func(v actorView, person string) template.HTML {
			if v.Label == "" {
				v = actorView{Person: person, Label: person, Kind: avHuman}
			}
			return actorHTML(l, v)
		},
		"avatarOf":   func(name string) template.HTML { return avatarHTML(l, "", name, false) },
		"avatarWait": func(name string) template.HTML { return avatarHTML(l, avHuman, name, true) },
		// avatarX is the full form: size, kind ("" guesses it), name, owner and live.
		"avatarX": func(size, kind, name, owner string, live bool) template.HTML {
			return avatarMarkup(l, avatarSpec{Kind: kind, Name: name, Size: size, Owner: owner, Live: live})
		},
		"issueRow": func(r issueRow) uiRow { return issueUIRow(l, r) },
		"inc":      func(i int) int { return i + 1 },
		"hc": func(num, title, brief, href string) hoverChip {
			return hoverChip{Num: num, Title: title, Brief: brief, Href: href}
		},
		"liveBox":     func(la liveAgent) template.HTML { return liveBoxHTML(l, la) },
		"liveDot":     func(la liveAgent) template.HTML { return liveDotHTML(l, la) },
		"markIcon":    func(mark string) template.HTML { return mockIcon(markIcons(mark)) },
		"proofs":      func(es []evidenceLine) template.HTML { return proofChips(l, es) },
		"mi":          func(name string) template.HTML { return mockIcon(name) },
		"mibig":       func(name string) template.HTML { return mockIcon(name, "l") },
		"refKeyOf":    refKeyOf,
		"cmdBlock":    func(id, mac, linux string) cmdBlock { return cmdBlock{ID: id, Mac: mac, Linux: linux} },
		"srvVersion":  srvVersion,
		"agentKindOf": func(provider string) string { return harnessKind(provider) },
		"agentHref":   agentURL,
		"msgAvatar":   func(m coordMessageView, size string) template.HTML { return msgAvatar(l, m, size) },
		// composerOf is the composer of a room or a thread (coord.html).
		"composerOf": func(d *coordRoomDetailView, id string, thread bool) coordComposer {
			return coordComposerFor(l, d, id, thread)
		},
	}
}

// markIcons names the mock icon of a row mark of the inbox.
func markIcons(mark string) string {
	switch mark {
	case "ask", "rooms":
		return "chat"
	case "approve":
		return "check"
	case "warn":
		return "tri"
	case "garden":
		return "sprout"
	case "knowledge":
		return "book"
	case "decision":
		return "decide"
	}
	return "pin"
}
