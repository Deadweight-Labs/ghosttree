package web

import (
	"fmt"
	"net/http"
	"path"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// WithExplainer schaltet den Block "Kurz erklärt" ein. Ohne Dienst bleibt er
// aus; mit einem Dienst ohne Modell zeigt er nur, was Agenten mitgeschrieben
// haben. Der Dienst erzeugt nie im Request: die Seite zeigt den Block, sobald
// er da ist.
func WithExplainer(svc *explain.Service) Option {
	return func(a *app) { a.explain = svc }
}

// explainView ist der Block "Kurz erklärt" einer Seite. Pending ist ein
// unsichtbarer Platzhalter, den explain.js ersetzt, sobald der Text erzeugt ist.
type explainView struct {
	DOMID     string
	Title     string
	Text      string
	Mark      string
	MarkTitle string
	Generated bool
	Pending   bool
}

// explainFor liefert den Block eines Objekts oder nil. Die Berechtigung für das
// Objekt prüft die Seite, die den Block zeigt: er steht nur dort, wo auch das
// Objekt steht.
func (a *app) explainFor(l lang, kind string, id int64) *explainView {
	if a.explain == nil || id <= 0 {
		return nil
	}
	v := a.explain.View(kind, id)
	if v.Empty() {
		return nil
	}
	out := &explainView{DOMID: "ex-" + kind + "-" + strconv.FormatInt(id, 10), Title: l.msg("explain.title"), Text: v.Text, Generated: v.Generated, Pending: v.Text == ""}
	if v.Text != "" {
		out.Mark, out.MarkTitle = l.msg("explain.agent"), l.msg("explain.agent_title")
		if v.Generated {
			out.Mark, out.MarkTitle = l.msg("explain.generated"), l.msg("explain.generated_title")
		}
	}
	return out
}

// explainStoredFor is explainFor without generation, for objects that are
// explained only when an agent wrote the text.
func (a *app) explainStoredFor(l lang, kind string, id int64) *explainView {
	if a.explain == nil || id <= 0 {
		return nil
	}
	v := a.explain.Stored(kind, id)
	if v.Empty() {
		return nil
	}
	out := &explainView{DOMID: "ex-" + kind + "-" + strconv.FormatInt(id, 10), Title: l.msg("explain.title"), Text: v.Text, Generated: v.Generated,
		Mark: l.msg("explain.agent"), MarkTitle: l.msg("explain.agent_title")}
	if v.Generated {
		out.Mark, out.MarkTitle = l.msg("explain.generated"), l.msg("explain.generated_title")
	}
	return out
}

// explainAttention sets the block on the cards of questions, approvals,
// blockers and handoffs. Only a question is explained by the model; the other
// reasons show what the agent wrote.
func (a *app) explainAttention(l lang, views []coordAttentionView, items []store.AttentionItem) {
	if a.explain == nil {
		return
	}
	byID := make(map[int64]store.AttentionItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for i := range views {
		item := byID[views[i].ID]
		if item.Reason == store.AttentionQuestion {
			views[i].Explain = a.explainFor(l, explain.KindQuestion, item.MessageID)
		} else {
			views[i].Explain = a.explainStoredFor(l, explain.KindQuestion, item.MessageID)
		}
	}
}

// explainMessages sets the block on the question cards of a room page.
func (a *app) explainMessages(l lang, views []coordMessageView) {
	if a.explain == nil {
		return
	}
	for i := range views {
		if views[i].Request == nil {
			continue
		}
		if views[i].Intent == store.IntentQuestion {
			views[i].Request.Explain = a.explainFor(l, explain.KindQuestion, views[i].ID)
		} else {
			views[i].Request.Explain = a.explainStoredFor(l, explain.KindQuestion, views[i].ID)
		}
	}
}

// originItem is one pointer to where an entry comes from.
type originItem struct {
	Label, Text, Href string
	Code, External    bool
}

// originView is the line above the explanation: project and where the entry
// comes from (session, request, pull request, code).
type originView struct {
	Project string
	Items   []originItem
}

// originFor names the origin of an entry from what is on it: the project, the
// session it was written in (link given only when the viewer may read it) and
// the requests, pull requests and code paths its text names. A request is
// named only when the viewer may read it.
func (a *app) originFor(l lang, pa *store.ProjectAccess, project, sessionHref string, texts ...string) *originView {
	out := &originView{}
	if project != "" {
		out.Project = path.Base(project)
	}
	if sessionHref != "" {
		out.Items = append(out.Items, originItem{Text: l.msg("knowledge.in_session"), Href: sessionHref})
	}
	var refs explain.Refs
	for _, t := range texts {
		r := explain.References(t)
		refs.Requests = append(refs.Requests, r.Requests...)
		refs.Paths = append(refs.Paths, r.Paths...)
		refs.PRs = append(refs.PRs, r.PRs...)
	}
	seen := map[int64]bool{}
	for _, n := range refs.Requests {
		if seen[n] || len(seen) >= 3 || !a.requestReadable(pa, n) {
			continue
		}
		seen[n] = true
		text := fmt.Sprintf("REQ-%d", n)
		if d, err := a.store.RequestByID(n); err == nil && d.Request.Title != "" {
			text += " " + ellipsize(oneLineText(d.Request.Title), 70)
		}
		out.Items = append(out.Items, originItem{Label: l.msg("explain.origin.request"), Text: text, Href: requestURL(n)})
	}
	seenPR := map[int]bool{}
	for _, pr := range refs.PRs {
		if seenPR[pr.Number] || len(seenPR) >= 2 {
			continue
		}
		seenPR[pr.Number] = true
		out.Items = append(out.Items, originItem{Label: l.msg("explain.origin.pr"), Text: "#" + strconv.Itoa(pr.Number), Href: pr.URL, External: pr.URL != ""})
	}
	seenPath := map[string]bool{}
	for _, p := range refs.Paths {
		if seenPath[p] || len(seenPath) >= 3 {
			continue
		}
		seenPath[p] = true
		out.Items = append(out.Items, originItem{Label: l.msg("explain.origin.code"), Text: p, Code: true})
	}
	if out.Project == "" && len(out.Items) == 0 {
		return nil
	}
	return out
}

// explainAdminView is the administrator's view of "Kurz erklärt".
type explainAdminView struct {
	State     string
	ModelLine string
	// Usage is one sentence on what the job used today and this month.
	Usage string
	// Limit names the cap that is used up, empty while there is room.
	Limit  string
	Counts string
	Days   []explainDayRow
}

type explainDayRow struct{ Day, Calls, Tokens string }

func (a *app) explainAdmin(l lang) *explainAdminView {
	if a.explain == nil {
		return nil
	}
	v := &explainAdminView{}
	language := l.msg("explain.admin.lang_en")
	if a.explain.Language() == "de" {
		language = l.msg("explain.admin.lang_de")
	}
	if !a.explain.Enabled() {
		v.State = l.msg("explain.admin.off")
	} else {
		v.State = l.msg("explain.admin.on")
		v.ModelLine = l.msg("explain.admin.model_line", a.explain.Model(), language)
	}
	day, errD := a.explain.Today()
	month, errM := a.explain.Month()
	if errD == nil && errM == nil {
		v.Usage, v.Limit = usageSentence(l, llmusage.JobExplain, day, month, a.explain.Limits())
	}
	if agent, generated, err := a.store.ExplanationCounts(); err == nil {
		v.Counts = l.msg("explain.admin.counts", agent, generated)
	}
	if days, err := a.store.LLMUsageRecent(llmusage.JobExplain, 7); err == nil {
		for _, d := range days {
			v.Days = append(v.Days, explainDayRow{Day: d.Day, Calls: strconv.Itoa(d.Calls), Tokens: strconv.Itoa(d.Tokens())})
		}
	}
	return v
}

// explainAdminFor is the admin block for a viewer, or nil for anyone else.
func (a *app) explainAdminFor(r *http.Request) *explainAdminView {
	if !a.isAdmin(browserPrincipal(r)) {
		return nil
	}
	return a.explainAdmin(a.langFor(r))
}
