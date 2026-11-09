package web

import (
	"fmt"
	"html"
	"html/template"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The history of an issue as sentences (REQ-599, mock git-native-v2): "claude
// hat Anforderung 5 erfüllt", "Robin hat #602 als Eltern-Issue gesetzt". The
// activity of the request is the source; every line says who did what to which
// object, with the proof under it. The old request page keeps its own list
// (activityLines); this one is only for the issue page.

// issueEvent is one entry of the history. Act is the kind of the activity
// ("criterion.met"), which stays in the markup (data-act) so a test or a script
// can count entries without reading sentences.
type issueEvent struct {
	// ID is the id of the activity behind the entry ("event-<ID>" in the markup);
	// Criterion is the requirement number a "met" or "waived" entry belongs to.
	ID              int64
	At              string
	Criterion       int
	Act, Tone, Icon string
	By, Age         string
	// Sentence is what follows the name; it holds the links to the objects.
	Sentence template.HTML
	// Quote is the text under the line (criterion, summary, reason).
	Quote string
	// Note is the text a close or drop was written with, as stored; Quote is its
	// shortened form. Kept to compare with the comment the same form wrote.
	Note string
	// Commits go behind the sentence, the other proofs under the line.
	Head, Body []evidenceLine
	// Git is the anchor the entry was written at.
	Git *anchorView
}

// sessionHandle is "claude@mainex": the harness and the machine it runs on. A
// guest of the project does not see the machine (decision #4349: an entry with
// project and machine is project knowledge, but its machine name stays inside).
func sessionHandle(pa *store.ProjectAccess, sess store.Session) string {
	name := "session"
	switch harnessKind(sess.Harness) {
	case avClaude:
		name = "claude"
	case avCodex:
		name = "codex"
	}
	if m := strings.TrimSpace(sess.Scope.Machine); m != "" && !pa.HidesMachines(sess.Scope.Project) {
		return name + "@" + m
	}
	return name
}

var (
	relationTarget = regexp.MustCompile(`^(\S+) (?:REQ-(\d+)|IDEA-(\d+)|knowledge #(\d+)|ext (.+))$`)
	taskData       = regexp.MustCompile(`^TASK-(\d+)(?: (\S+) (.*))?$`)
	painData       = regexp.MustCompile(`PAIN-(\d+)`)
	pkgData        = regexp.MustCompile(`^PKG-(\d+) branch:(\S*) role:(\S*)`)
	reopenedData   = regexp.MustCompile(`^AC-\d+\.(\d+)(?: \(was \w+\))?(?: — (.*))?$`)
	mergedData     = regexp.MustCompile(`^PR #(\d+)`)
)

// issueEvents turns the visible activity of a request into history entries,
// newest first. criteria are the requirement lines (for numbers, texts and
// proofs), tasks the open tasks (for who a task waits for), pkg the name of
// the work package of the issue's branch ("" when there is none).
func issueEvents(l lang, acts []requestdomain.Activity, criteria []criterionLine, assignees map[int64]string, pkg string, now func(string) string) []issueEvent {
	byID := map[int64]criterionLine{}
	byText := map[string]criterionLine{}
	for _, c := range criteria {
		byID[c.ID] = c
		byText[strings.TrimSpace(c.Text)] = c
	}
	crit := func(n int) template.HTML {
		return template.HTML(fmt.Sprintf(`<a href="#criterion-%d">%s</a>`, n, html.EscapeString(l.msg("issue.requirement_n", n))))
	}
	// sentence fills the catalog text, whose %s take already escaped HTML.
	sentence := func(key string, args ...any) template.HTML {
		return template.HTML(fmt.Sprintf(html.EscapeString(l.msg(key)), args...))
	}
	reqLink := func(n int64) string { return string(refPillHTML(l, "request", n, requestURL(n))) }
	var out []issueEvent
	for i := len(acts) - 1; i >= 0 && len(out) < activityShown; i-- {
		act := acts[i]
		ev := issueEvent{ID: act.ID, At: act.CreatedAt, Act: act.Kind, Tone: "pl", Icon: "link", By: act.Person, Age: now(act.CreatedAt)}
		switch act.Kind {
		case "request.created":
			ev.Icon, ev.Sentence = "issue", sentence("issue.ev.created")
		case "request.corrected":
			ev.Icon, ev.Sentence, ev.Quote = "pencil", sentence("issue.ev.corrected"), ellipsize(sessionScrub.ReplaceAllString(act.Data, l.msg("requests.session")), 200)
		case "request.moved":
			ev.Icon, ev.Sentence, ev.Quote = "folder", sentence("issue.ev.moved"), ellipsize(act.Data, 200)
		case "request.migrated":
			ev.Icon, ev.Sentence = "folder", sentence("issue.ev.migrated")
		case "request.done":
			ev.Tone, ev.Icon, ev.Sentence, ev.Quote, ev.Note = "don", "done", sentence("issue.ev.done"), ellipsize(sessionScrub.ReplaceAllString(act.Data, l.msg("requests.session")), 200), act.Data
		case "request.dropped":
			ev.Icon, ev.Sentence, ev.Quote, ev.Note = "drop", sentence("issue.ev.dropped"), ellipsize(sessionScrub.ReplaceAllString(act.Data, l.msg("requests.session")), 200), act.Data
		case "criterion.added":
			ev.Icon, ev.Quote = "plus", ellipsize(act.Data, 200)
			if c, ok := byText[strings.TrimSpace(act.Data)]; ok {
				ev.Sentence = sentence("issue.ev.criterion_added", crit(c.Number))
			} else {
				ev.Sentence = sentence("issue.ev.criterion_added_any")
			}
		case "criterion.met", "criterion.waived":
			id, err := strconv.ParseInt(strings.TrimPrefix(act.Data, "AC-"), 10, 64)
			c, ok := byID[id]
			if err != nil || !ok {
				ev.Sentence = sentence(map[string]string{"criterion.met": "issue.ev.criterion_met_any", "criterion.waived": "issue.ev.criterion_waived_any"}[act.Kind])
				break
			}
			ev.Quote = c.Text
			ev.Criterion = c.Number
			if act.Kind == "criterion.met" {
				ev.Tone, ev.Icon = "ok", "check"
				for _, e := range c.Evidence {
					if e.Kind == "commit" {
						ev.Head = append(ev.Head, e)
					} else {
						ev.Body = append(ev.Body, e)
					}
				}
				ev.Sentence = sentence("issue.ev.criterion_met", crit(c.Number))
			} else {
				ev.Icon = "drop"
				ev.Sentence = sentence("issue.ev.criterion_waived", crit(c.Number))
			}
		case "relation.added", "relation.removed":
			ev.Sentence = relationSentence(l, act, reqLink, sentence)
		case "work.started", "work.resumed":
			ev.Tone, ev.Icon = "acc", "play"
			role := ""
			if _, r, ok := strings.Cut(act.Data, "role:"); ok {
				role, _, _ = strings.Cut(strings.TrimSpace(r), " ")
			} else if _, ok := roleKeys[act.Data]; ok {
				role = act.Data
			}
			key := "issue.ev.work_started"
			if act.Kind == "work.resumed" {
				key = "issue.ev.work_resumed"
			}
			ev.Sentence = sentence(key)
			if _, ok := roleKeys[role]; ok && role != "primary" {
				ev.Sentence += template.HTML(" (" + html.EscapeString(label(l, roleKeys, role)) + ")")
			}
		case "work.finished":
			ev.Icon, ev.Sentence, ev.Quote = "pause", sentence("issue.ev.work_finished"), ellipsize(sessionScrub.ReplaceAllString(act.Data, l.msg("requests.session")), 200)
		case "evidence.migrated":
			ev.Icon, ev.Sentence = "check", sentence("issue.ev.evidence_migrated")
		case "request.reopened":
			ev.Icon, ev.Sentence = "issue", sentence("issue.ev.reopened")
		case "criterion.reopened":
			// "AC-<request>.<number> (was met) — <reason>": the number names the
			// requirement, the reason (the person's words) is the quote.
			ev.Icon = "issue"
			ev.Sentence = sentence("issue.ev.criterion_reopened_any")
			if m := reopenedData.FindStringSubmatch(act.Data); m != nil {
				n, _ := strconv.Atoi(m[1])
				ev.Sentence = sentence("issue.ev.criterion_reopened", crit(n))
				ev.Criterion, ev.Quote = n, ellipsize(strings.TrimSpace(m[2]), 200)
			}
		case "relation.taken_over":
			ev.Icon, ev.Sentence = "book", sentence("issue.ev.relation_taken_over")
		case "pr.merged":
			// The note is the store's English line ("PR #12 … merged into main …");
			// the page names the pull request and says it in its own language.
			ev.Icon, ev.Tone, ev.Sentence = "pr", "don", sentence("issue.ev.pr_merged_any")
			if m := mergedData.FindStringSubmatch(act.Data); m != nil {
				ev.Sentence = sentence("issue.ev.pr_merged", html.EscapeString("#"+m[1]))
			}
		case "package.added", "package.removed", "package.role":
			ev.Icon = "book"
			named := map[string][2]string{"package.added": {"issue.ev.package_added", "issue.ev.package_added_any"}, "package.removed": {"issue.ev.package_removed", "issue.ev.package_removed_any"},
				"package.role": {"issue.ev.package_role", "issue.ev.package_role_any"}}[act.Kind]
			if pkg != "" && pkgData.MatchString(act.Data) {
				ev.Sentence = sentence(named[0], html.EscapeString(pkg))
			} else {
				ev.Sentence = sentence(named[1])
			}
		case "pain.linked", "pain.unlinked":
			ev.Icon = "pain"
			link := html.EscapeString("#")
			if m := painData.FindStringSubmatch(act.Data); m != nil {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				link = string(refPillHTML(l, "pain", n, painURL(n)))
			}
			ev.Sentence = sentence(map[string]string{"pain.linked": "issue.ev.pain_linked", "pain.unlinked": "issue.ev.pain_unlinked"}[act.Kind], link)
		case "task.created", "task.done", "task.confirmed", "task.reopened", "task.dropped":
			ev.Icon, ev.Tone = "pin", "acc"
			m := taskData.FindStringSubmatch(strings.TrimSpace(act.Data))
			var n int64
			link := ""
			if m != nil {
				n, _ = strconv.ParseInt(m[1], 10, 64)
				link = string(refPillHTML(l, "task", n, "#task-"+m[1]))
			}
			switch act.Kind {
			case "task.created":
				if who := assignees[n]; who != "" {
					ev.Sentence = sentence("issue.ev.task_created_for", html.EscapeString(who), link)
				} else {
					ev.Sentence = sentence("issue.ev.task_created", link)
				}
				if m != nil {
					ev.Quote = ellipsize(m[3], 200)
				}
			case "task.done", "task.confirmed":
				ev.Tone, ev.Icon = "ok", "check"
				ev.Sentence = sentence(map[string]string{"task.done": "issue.ev.task_done", "task.confirmed": "issue.ev.task_confirmed"}[act.Kind], link)
			default:
				ev.Sentence = sentence(map[string]string{"task.reopened": "issue.ev.task_reopened", "task.dropped": "issue.ev.task_dropped"}[act.Kind], link)
			}
		default:
			text := act.Kind
			if key, ok := activityKinds[act.Kind]; ok {
				text = l.msg(key)
			}
			ev.Sentence = template.HTML(html.EscapeString(strings.ToLower(text)))
		}
		out = append(out, ev)
	}
	// The oldest entry comes first, like on GitHub; the limit above kept the newest.
	slices.Reverse(out)
	return out
}

// sameMomentWithin is how far apart a close and the comment its form wrote may
// be: both are written by one request, so seconds; two minutes leave room for a
// slow clock between the two writes.
const sameMomentWithin = 2 * time.Minute

// withoutRepeatedNotes leaves the note out of a close or drop entry when a
// comment of the same person with the same text stands beside it. The form of the
// status select writes the words twice, as the note of the change and as a
// comment; the timeline shows them once, in the comment. A note that differs, a
// comment by somebody else, from another time or deleted does not count.
func withoutRepeatedNotes(events []issueEvent, comments []tlItem) []issueEvent {
	out := slices.Clone(events)
	for i, ev := range out {
		note := strings.TrimSpace(ev.Note)
		if note == "" {
			continue
		}
		at := parseTime(ev.At)
		for _, c := range comments {
			v := c.Comment
			if v == nil || v.Deleted || v.Who != ev.By || strings.TrimSpace(v.Raw) != note || at.IsZero() || c.at.IsZero() {
				continue
			}
			if d := c.at.Sub(at); d < sameMomentWithin && d > -sameMomentWithin {
				out[i].Quote = ""
				break
			}
		}
	}
	return out
}

// relationSentence says what happened to a link, naming the other side.
func relationSentence(l lang, act requestdomain.Activity, reqLink func(int64) string, sentence func(string, ...any) template.HTML) template.HTML {
	removed := act.Kind == "relation.removed"
	m := relationTarget.FindStringSubmatch(act.Data)
	if m == nil {
		if removed {
			return sentence("issue.ev.rel_removed_any")
		}
		return sentence("issue.ev.rel_added_any")
	}
	kind, target := m[1], ""
	switch {
	case m[2] != "":
		n, _ := strconv.ParseInt(m[2], 10, 64)
		target = reqLink(n)
	case m[3] != "":
		n, _ := strconv.ParseInt(m[3], 10, 64)
		target = string(refPillHTML(l, "idea", n, ""))
	case m[4] != "":
		target = `<a href="/ui/knowledge/` + m[4] + `">` + html.EscapeString(l.msg("issue.knowledge_n", m[4])) + `</a>`
	default:
		target = html.EscapeString(ellipsize(m[5], 120))
		if strings.HasPrefix(m[5], "https://") || strings.HasPrefix(m[5], "http://") {
			target = `<a href="` + html.EscapeString(m[5]) + `" rel="noopener noreferrer">` + target + `</a>`
		}
	}
	if removed {
		return sentence("issue.ev.rel_removed", target)
	}
	if key, ok := relationEventKeys[kind]; ok {
		return sentence(key, target)
	}
	return sentence("issue.ev.rel.related", target)
}

// proofChips writes the proofs of a requirement as the chips of the mock: a
// commit as its short hash (a link when the address is known), a test or file
// by name, a decision, a session or a web address as a link.
func proofChips(l lang, es []evidenceLine) template.HTML {
	var b strings.Builder
	for _, e := range es {
		ref := e.Ref
		if ref == "" && e.Href == "" {
			continue
		}
		text := html.EscapeString(ref)
		switch e.Kind {
		case "commit":
			short := ref
			if len(short) > 7 {
				short = short[:7]
			}
			text = html.EscapeString(short)
			if e.Href != "" {
				b.WriteString(`<a class="chip sha" href="` + html.EscapeString(e.Href) + `" title="` + html.EscapeString(e.Label+" "+ref) + `" rel="noopener noreferrer">` + text + `</a>`)
			} else {
				b.WriteString(`<span class="chip sha" title="` + html.EscapeString(e.Label+" "+ref) + `">` + text + `</span>`)
			}
		case "test":
			b.WriteString(`<span class="chip test" title="` + html.EscapeString(e.Label) + `">` + text + `</span>`)
		default:
			inner := string(mockIcon(proofIcon[e.Kind])) + text
			if e.Href != "" {
				rel := ""
				if e.Kind == "url" {
					rel = ` rel="noopener noreferrer"`
				}
				b.WriteString(`<a class="chip ar" href="` + html.EscapeString(e.Href) + `" title="` + html.EscapeString(e.Label) + `"` + rel + `>` + inner + `</a>`)
			} else {
				b.WriteString(`<span class="chip ar" title="` + html.EscapeString(e.Label) + `">` + inner + `</span>`)
			}
		}
	}
	return template.HTML(b.String())
}

// relationEventKeys are the sentences of a new link by its kind; any other kind
// reads like "related".
var relationEventKeys = map[string]string{"parent": "issue.ev.rel.parent", "blocks": "issue.ev.rel.blocks", "duplicates": "issue.ev.rel.duplicates",
	"supersedes": "issue.ev.rel.supersedes", "derived": "issue.ev.rel.derived", "knowledge": "issue.ev.rel.knowledge", "related": "issue.ev.rel.related"}

var proofIcon = map[string]string{"file": "file", "decision": "book", "url": "ext", "session": "term", "request": "issue"}

// timelineItems turns the history into entries of the timeline component: the
// person's avatar on the line, the action icon beside the name, the proofs of
// the head behind the sentence and the text with the other proofs in the box.
func timelineItems(l lang, events []issueEvent) []tlItem {
	items := make([]tlItem, 0, len(events))
	for _, e := range events {
		tone := e.Tone
		if tone == "pl" {
			tone = ""
		}
		head := template.HTML(string(e.Sentence) + string(proofChips(l, e.Head)))
		var body template.HTML
		if e.Quote != "" || len(e.Body) > 0 {
			body = template.HTML(html.EscapeString(e.Quote))
			if len(e.Body) > 0 {
				body += " " + proofChips(l, e.Body)
			}
		}
		id := ""
		if e.ID != 0 {
			id = "event-" + strconv.FormatInt(e.ID, 10)
		}
		items = append(items, tlItem{ID: id, Act: e.Act, Who: e.By, Icon: e.Icon, Tone: tone, Head: head, Age: e.Age, Body: body, Git: e.Git, at: parseTime(e.At)})
	}
	return items
}
