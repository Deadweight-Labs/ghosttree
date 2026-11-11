package web

import (
	"bytes"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Hover cards of references (REQ-599, task 3). Every pill (REQ-5, PAIN-55, ...)
// and every number chip (#5) carries data-ref="REQ-5". After a page is
// rendered, refCards looks at the keys the page uses, reads each object as the
// viewer (ProjectAccess) and appends one <template data-ref-card="REQ-5"> per
// object the viewer may read. ui-refs.js shows the card of such a template on
// hover and focus. There is no request while the page is open.
//
// What the viewer may not read gets no template and so no card. A later
// endpoint (/api/refs) can replace this: ui-refs.js asks one function
// (GTRefs.card) for the card of a key, and the card markup is the "refcard"
// template of components.html either way.

// refCardsMax caps the cards of one page; a page with more references shows
// the first ones.
const refCardsMax = 80

var (
	refKeyRE = regexp.MustCompile(`data-ref="(REQ|PAIN|TASK|IDEA|DEC|FEAT|KN)-([0-9]{1,15})"`)
	// hrefRefRE reads the reference an address stands for: .../issues/12,
	// /ui/requests/12, .../pains/5, .../ideas/3.
	hrefRefRE = regexp.MustCompile(`(?:^|/)(issues|requests|pains|ideas|tasks)/([0-9]{1,15})(?:[/?#].*)?$`)
	hrefTask  = regexp.MustCompile(`#task-([0-9]{1,15})$`)
)

// refKeyOf is the reference key ("REQ-12") of an address a chip links to, or ""
// when the address is no issue, pain, idea or task.
func refKeyOf(href string) string {
	// An address outside ghosttree is nobody's issue: https://github.com/x/issues/12 is no REQ-12.
	if strings.Contains(href, "://") || strings.HasPrefix(href, "//") {
		return ""
	}
	if m := hrefTask.FindStringSubmatch(href); m != nil && !strings.Contains(href, "/") {
		return "TASK-" + m[1]
	}
	m := hrefRefRE.FindStringSubmatch(href)
	if m == nil {
		return ""
	}
	prefix := map[string]string{"issues": "REQ", "requests": "REQ", "pains": "PAIN", "ideas": "IDEA", "tasks": "TASK"}[m[1]]
	return prefix + "-" + m[2]
}

// refCardView is what the card of one object shows: kind and number, state,
// title, a short text, author and time, for a request its progress.
type refCardView struct {
	Key, Long, Class    string
	Icon                template.HTML
	State, Tone         string
	Title, Brief        string
	Who, When           string
	Done, Total         int
	Percent             int
	ProgTitle, ProgText string
	// Note is, on the card of a task, what it says about itself: "Kurz erklärt" and its first steps.
	Note *taskNote
}

// refCardTone is the colour of the state pill of a card.
var refCardTone = map[string]map[string]string{
	"request":  {"open": "open", "done": "done", "dropped": "gray"},
	"pain":     {"active": "open", "mitigated": "amber4", "accepted": "gray", "resolved": "done", "obsolete": "gray"},
	"task":     {"open": "open", "done": "done", "confirmed": "done", "dropped": "gray"},
	"idea":     {"open": "open", "accepted": "done", "dismissed": "gray"},
	"decision": {"open": "open", "decided": "done", "deferred": "amber4", "dropped": "gray"},
}

var refTaskStateKeys = map[string]string{"open": "requests.state.open", "done": "tasks.state.done", "confirmed": "tasks.state.confirmed", "dropped": "tasks.state.dropped"}

func refCardBase(l lang, kind string, id int64, title, state string, stateKeys map[string]string) refCardView {
	k := refKinds[kind]
	long := k.Prefix + "-" + strconv.FormatInt(id, 10)
	v := refCardView{Key: long, Long: long, Class: k.Class, Icon: template.HTML(k.Icon),
		Title: links.Clean(title, 140), Tone: refCardTone[kind][state]}
	if v.Tone == "" {
		v.Tone = "gray"
	}
	if state != "" {
		v.State = label(l, stateKeys, state)
	}
	return v
}

// resolveRefCard reads one object as the viewer. ok is false for an object that
// does not exist and for one the viewer may not read: both look the same.
func (a *app) resolveRefCard(l lang, pa *store.ProjectAccess, now time.Time, prefix string, id int64) (card refCardView, href string, ok bool) {
	when := func(at string) string {
		if t := parseTime(at); !t.IsZero() {
			return shortAgo(l, now, t)
		}
		return ""
	}
	switch prefix {
	case "REQ":
		if !a.requestReadable(pa, id) {
			return
		}
		d, err := a.store.RequestByID(id)
		if err != nil {
			return
		}
		c := links.Builder{}.RequestCard(d)
		card = refCardBase(l, "request", id, c.Title, d.Request.State, requestStateKeys)
		card.Brief, card.Who, card.When = links.Clean(d.Request.Description, 180), d.Request.Person, when(d.Request.CreatedAt)
		if p, err := a.store.CriteriaProgress([]int64{id}); err == nil && p[id].Total > 0 {
			card.Done, card.Total, card.Percent = p[id].Done, p[id].Total, p[id].Done*100/p[id].Total
			card.ProgTitle, card.ProgText = l.msg("requests.progress"), strconv.Itoa(card.Done)+"/"+strconv.Itoa(card.Total)
		}
		href = requestURL(id)
		if project := d.Request.Scope.Project; project != "" {
			href = repoHref(project) + "/issues/" + strconv.FormatInt(id, 10)
		}
	case "PAIN":
		p, err := a.store.PainByID(id)
		if err != nil || pa.CheckPain(p, store.ActRead) != nil {
			return
		}
		c := links.Builder{}.PainCard(p)
		card = refCardBase(l, "pain", id, c.Title, p.State, painStateKeys)
		card.Brief, card.Who, card.When = links.Clean(p.Body, 180), p.Person, when(p.CreatedAt)
		href = painURL(id)
		if p.Project != "" {
			href = repoHref(p.Project) + "/pains/" + strconv.FormatInt(id, 10)
		}
	case "TASK":
		t, err := pa.GetHumanTask(id)
		if err != nil {
			return
		}
		c := links.Builder{}.TaskCard(t)
		card = refCardBase(l, "task", id, c.Title, t.State, refTaskStateKeys)
		card.Brief, card.When = links.Clean(t.Description, 180), when(t.CreatedAt)
		card.Who = firstNonEmpty(t.Assignee, t.CreatedBy)
		card.Note = a.taskNoteFor(l, id, t.Steps).forCard()
		if t.Project != "" {
			href = taskHrefOf(t)
		}
	case "IDEA":
		i, err := pa.IdeaView(id)
		if err != nil {
			return
		}
		c := links.Builder{}.IdeaCard(i)
		card = refCardBase(l, "idea", id, c.Title, i.State, ideaStateKeys)
		card.Brief, card.Who, card.When = links.Clean(i.Body, 180), i.Person, when(i.CreatedAt)
		if i.Project != "" {
			href = repoHref(i.Project) + "/ideas/" + strconv.FormatInt(id, 10)
		}
	case "DEC":
		d, err := pa.DecisionView(id)
		if err != nil {
			return
		}
		card = refCardBase(l, "decision", id, d.Question, d.State, workshopDecisionKeys)
		card.Brief, card.Who, card.When = links.Clean(d.Detail, 180), d.Person, when(d.CreatedAt)
		href = decisionHref(d)
	case "KN":
		k, err := pa.KnowledgeRead(id)
		if err != nil {
			return
		}
		c := links.New("https://ghosttree.invalid", "").KnowledgeCard(k)
		kd := refKinds["knowledge"]
		// The card says what the entry is about, in words; a code block belongs on its page.
		body, _, _ := strings.Cut(k.Body, "```")
		card = refCardView{Key: "KN-" + strconv.FormatInt(id, 10), Long: knref.Format(id), Class: kd.Class, Icon: template.HTML(kd.Icon),
			Tone: "gray", Title: links.Clean(c.Title, 140), Brief: links.Clean(body, 180), Who: k.Person, When: when(k.CreatedAt)}
		if u, err := url.Parse(c.URL); err == nil && u.Path != "" {
			href = u.RequestURI()
		}
	case "FEAT":
		info, err := links.Peek(links.New("https://ghosttree.invalid", ""), links.AccessReader{PA: pa}, links.Ref{Kind: links.KindFeature, ID: id}, "")
		if err != nil || !info.Gefunden {
			return
		}
		kd := refKinds["feature"]
		card = refCardView{Key: "FEAT-" + strconv.FormatInt(id, 10), Long: "FEAT-" + strconv.FormatInt(id, 10), Class: kd.Class, Icon: template.HTML(kd.Icon),
			State: info.Zustand, Tone: "gray", Title: info.Titel, Brief: info.Kurzfassung, Who: info.Autor, When: when(info.Zeit)}
		if u, err := url.Parse(info.Link); err == nil && u.Path != "" {
			href = u.RequestURI()
		}
	default:
		return
	}
	return card, href, true
}

// refCards appends the cards of the references of a page, for the viewer of r.
func (a *app) refCards(r *http.Request, l lang, page []byte) []byte {
	matches := refKeyRE.FindAllSubmatch(page, -1)
	if len(matches) == 0 {
		return page
	}
	end := bytes.LastIndex(page, []byte("</body>"))
	if end < 0 {
		return page
	}
	pa := a.access(r)
	now := overviewNow().UTC()
	seen := map[string]bool{}
	var cards bytes.Buffer
	for _, m := range matches {
		key := string(m[1]) + "-" + string(m[2])
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > refCardsMax {
			break
		}
		id, err := strconv.ParseInt(string(m[2]), 10, 64)
		if err != nil {
			continue
		}
		card, href, ok := a.resolveRefCard(l, pa, now, string(m[1]), id)
		if !ok {
			continue
		}
		var body bytes.Buffer
		if err := l.page().ExecuteTemplate(&body, "refcard", card); err != nil {
			continue
		}
		cards.WriteString(`<template data-ref-card="` + key + `"`)
		if href != "" {
			cards.WriteString(` data-href="` + html.EscapeString(href) + `"`)
		}
		cards.WriteString(">")
		cards.Write(body.Bytes())
		cards.WriteString("</template>\n")
	}
	if cards.Len() == 0 {
		return page
	}
	out := make([]byte, 0, len(page)+cards.Len())
	out = append(out, page[:end]...)
	out = append(out, cards.Bytes()...)
	return append(out, page[end:]...)
}
