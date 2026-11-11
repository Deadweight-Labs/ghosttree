package web

import (
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Wissen auf der neuen Oberfläche (REQ-599, Phase 2 B): die Zeilen der Prüfen-
// Seite, die Seitenleiste des Eintrags und der Agentenkontext. Die Daten stammen
// aus knowledge.go und review_garden.go; hier steht nur, wie sie sich in die
// Seiten der neuen Oberfläche fügen.

// rvRow is one row of the review list: a note of the gardener or the proposal of
// an agent, with what its expansion shows (reason, before and after, proofs,
// source) and the actions the viewer may take.
type rvRow struct {
	Gardener bool
	ID, Href string
	Label    string
	Tone     string
	Title    string
	// Num is the number of the entry (proposals of agents), 0 for a note.
	Num      int64
	Who, Age string
	Old      bool
	// Level is the trust status of a proposal, with its label and tooltip.
	Level, LevelLabel, LevelTitle string
	Why                           string
	Reason                        []string
	// Before and After are the old and the proposed text of a note that has both.
	Before, After string
	// BeforeLabel and AfterLabel name the two texts for screen readers.
	BeforeLabel, AfterLabel string
	Texts                   []gardenText
	Lines                   []string
	Refs                    []gardenRef
	Blocks                  []bodyBlock
	Evidence                []evidenceRow
	Proof                   *proofView
	From                    *originView
	Source                  string
	Explain                 *explainView
	Warn                    string
	CanApprove              bool
	CanReject               bool
	Retire                  bool
	CanEdit                 bool
	Act                     *gardenAct
	Token, Next             string
	Filter                  string
}

// reviewRows turns the notes of the gardener and the cards of the agents into
// one list: the gardener first, each oldest first, as the page always sorted.
func reviewRows(l lang, garden *gardenView, cards []knowledgeCard) []rvRow {
	var rows []rvRow
	if garden != nil {
		for _, g := range garden.Rows {
			row := rvRow{Gardener: true, ID: g.ID, Href: g.Href, Label: g.Verdict, Tone: g.Tone, Title: g.Subject, Who: l.msg("avatar.gardener"), Age: g.Age, Old: g.Old,
				Why: g.Reason, Lines: g.Lines, Refs: g.Refs, Texts: g.Texts, Explain: g.Explain, Warn: g.Warn, Source: garden.Source,
				CanApprove: g.CanApprove, CanReject: g.CanReject, Act: g.Act, Token: g.Token, Next: g.Next, Filter: g.Filter}
			if g.Reason != "" {
				row.Reason = []string{g.Reason}
			}
			// Two texts are the old and the new one: shown as a diff.
			if len(g.Texts) == 2 && len(g.Texts[0].Lines) > 0 && len(g.Texts[1].Lines) > 0 {
				row.Before, row.After = strings.Join(g.Texts[0].Lines, " "), strings.Join(g.Texts[1].Lines, " ")
				row.BeforeLabel, row.AfterLabel = g.Texts[0].Label, g.Texts[1].Label
				row.Texts = nil
			}
			rows = append(rows, row)
		}
	}
	for _, c := range cards {
		num, _ := strconv.ParseInt(c.ID, 10, 64)
		row := rvRow{ID: c.ID, Href: c.Href, Label: c.TypeLabel, Tone: c.Tone, Title: c.Title, Num: num, Who: c.Person, Age: c.Age, Old: c.Old, Why: c.Excerpt,
			Level: c.Level, LevelLabel: c.LevelLabel, LevelTitle: c.LevelTitle,
			Blocks: c.Blocks, Evidence: c.Evidence, Proof: c.Proof, From: c.From, Explain: c.Explain,
			CanApprove: c.CanApprove, CanReject: c.CanReject, Retire: c.Retire, CanEdit: c.CanEdit, Token: c.Token, Next: c.Next, Filter: c.Filter}
		if c.Stale {
			row.Label, row.Tone = l.msg("knowledge.stale"), "warn"
		}
		// A ready explanation leads the row: it speaks before the original text.
		if c.Explain != nil && !c.Explain.Pending && c.Explain.Text != "" {
			row.Why = leadSentence(c.Explain.Text, 160)
		}
		rows = append(rows, row)
	}
	return rows
}

// reviewTabs are the three tabs of the list head: all, from the gardener, from
// agents. The current one follows ?source=.
func reviewTabs(r *http.Request, l lang, gardener, agents int, source string) []knTab {
	mk := func(label, src string, n int) knTab {
		return knTab{Label: l.msg(label), Href: withQuery(r, map[string]string{"source": src}), Count: countText(n), Current: source == src}
	}
	return []knTab{mk("review.tab.all", "", gardener+agents), mk("review.tab.gardener", "gardener", gardener), mk("review.tab.agents", "agents", agents)}
}

// contextLines splits the text an agent is given into numbered lines. Lines that
// only the preview has (they wait for review) are marked Added; Head marks the
// headings.
type contextLine struct {
	N     int
	Text  string
	Head  bool
	Added bool
}

func contextLines(text, delivered string) []contextLine {
	have := map[string]int{}
	if delivered != "" {
		for _, ln := range strings.Split(delivered, "\n") {
			have[ln]++
		}
	}
	var out []contextLine
	for i, ln := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		cl := contextLine{N: i + 1, Text: ln, Head: strings.HasPrefix(ln, "#")}
		if delivered != "" && strings.TrimSpace(ln) != "" {
			if have[ln] > 0 {
				have[ln]--
			} else {
				cl.Added = true
			}
		}
		out = append(out, cl)
	}
	return out
}

// pendingFor counts the entries waiting for review that the viewer may see.
func (a *app) pendingFor(pa *store.ProjectAccess, project string) int {
	pend, err := a.store.PendingKnowledge(project, a.overfetch(reviewShown), pa.Viewer())
	if err != nil {
		return 0
	}
	return len(keep(pend, reviewShown, pa.CanSeeKnowledge))
}

// knowledgeCountText is the number of live entries the viewer sees in the
// project (all projects when empty), as the tab shows it.
func (a *app) knowledgeCountText(pa *store.ProjectAccess, project string) string {
	found, err := a.store.BrowseKnowledge(project, "", "", a.overfetch(repoCountAt), pa.Viewer())
	if err != nil {
		return ""
	}
	return countText(len(keep(found, repoCountAt, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale")
	})))
}

// knRevision is one line of "Revisions": the current text and the earlier ones.
type knRevision struct {
	N          int
	By, Title  string
	Age        string
	Current    bool
	Blocks     []bodyBlock
	ReplacedBy string
	// Sha is the git anchor of an earlier version, empty for the current one
	// (the store does not give the anchor of the current text back).
	Sha string
}

// knSide is the side column of an entry: the area it belongs to, the git state
// it was written at, the session it came from.
type knSide struct {
	Areas       []string
	Sha, Branch string
	Session     string
}

// fillKnowledgeItem adds what the page of an entry shows beyond the card: the
// revisions (newest first, the current text on top), the timeline of what
// happened to it and the side column. All of it comes from data the page already
// read; nothing is guessed.
func (a *app) fillKnowledgeItem(l lang, k store.Knowledge, v *knowledgeItemView) {
	c := v.Card
	// Side column.
	if k.OriginContext != nil {
		for _, ar := range k.OriginContext.Areas {
			if ar.Path != "" && len(v.Side.Areas) < 4 {
				v.Side.Areas = append(v.Side.Areas, ar.Path)
			}
		}
	}
	if k.GitAnchor != nil && !k.GitAnchor.IsZero() {
		v.Side.Sha, v.Side.Branch = shortSha(k.GitAnchor.Commit), k.GitAnchor.Branch
	}
	v.Side.Session = c.Source
	// Revisions: the history runs from new to old and holds the earlier texts.
	author := firstNonEmpty(k.LastModifiedBy, k.Person)
	n := len(v.History) + 1
	v.Revs = append(v.Revs, knRevision{N: n, By: author, Title: k.Title, Age: c.Age, Current: true})
	for i, h := range v.History {
		v.Revs = append(v.Revs, knRevision{N: n - 1 - i, By: h.By, Title: h.Title, Age: h.Age, Blocks: h.Blocks, ReplacedBy: h.ReplacedBy, Sha: h.Sha})
	}
	if len(v.Revs) == 1 {
		v.Revs = nil
	}
	// Timeline, newest first.
	chip := func(s string) string { return " " + string(shaPill(l, s, "")) }
	sha := ""
	if v.Side.Sha != "" {
		sha = chip(v.Side.Sha)
	}
	if author != k.Person && author != "" {
		v.Timeline = append(v.Timeline, tlItem{Who: author, Icon: "pencil", Tone: "warn", Head: template.HTML(html.EscapeString(l.msg("knowledge.tl.edited")) + sha)})
	}
	for _, m := range v.Moves {
		v.Timeline = append(v.Timeline, tlItem{Who: m.By, Icon: "folder", Tone: "acc", Head: template.HTML(html.EscapeString(m.Text)), Age: m.Age})
	}
	if c.ConfirmedBy != "" {
		v.Timeline = append(v.Timeline, tlItem{Who: c.ConfirmedBy, Icon: "check", Tone: "ok", Head: template.HTML(html.EscapeString(l.msg("knowledge.tl.confirmed")))})
	}
	if v.Closure != nil {
		head := html.EscapeString(v.Closure.Label)
		if v.Closure.Title != "" {
			head += ` <a href="` + html.EscapeString(v.Closure.Href) + `">` + html.EscapeString(v.Closure.Title) + `</a>`
		}
		v.Timeline = append(v.Timeline, tlItem{Who: v.Closure.By, Icon: "drop", Tone: "don", Head: template.HTML(head), Age: v.Closure.Age, Body: mdText(v.Closure.Reason)})
	}
	if c.Person != "" {
		v.Timeline = append(v.Timeline, tlItem{Who: c.Person, Icon: "book", Tone: "warn", Head: template.HTML(html.EscapeString(l.msg("knowledge.tl.created")) + sha), Age: c.Age})
	}
}

// contextView is the Agent context page: what a project's agents are given,
// optionally with what still waits for review (marked line by line).
type contextView struct {
	Project string
	Preview bool
	Output  string
	// Choices are the projects to pick from while none is chosen.
	Choices []projectOption
	Lines   []contextLine
	// Size is "26 lines · 1,912 characters"; Waiting the entries that wait for review.
	Size    string
	Waiting int
	Segs    []knSeg
	// DeliveredHref and PreviewHref are the two views, Review the page behind "n to review".
	DeliveredHref, PreviewHref, ReviewHref string
}

func (a *app) contextPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	project := a.projectParam(r)
	r = a.repoScoped(r, project, "knowledge")
	preview := r.URL.Query().Get("preview") == "1"
	view := &contextView{Project: project, Preview: preview}
	if project == "" {
		view.Choices = a.shellFor(r, "context").Projects
		// Wer nur ein Projekt sieht, braucht keine Auswahl.
		if len(view.Choices) == 1 {
			q := r.URL.Query()
			q.Set("project", view.Choices[0].Remote)
			http.Redirect(w, r, "/ui/context?"+q.Encode(), http.StatusSeeOther)
			return
		}
		a.renderBrowser(w, r, "context", pageData{Title: l.msg("context.title"), ContextV: view})
		return
	}
	pa := a.access(r)
	render := func(withStaged bool) (string, error) {
		var entries []store.Knowledge
		var err error
		if withStaged {
			entries, err = a.store.KnowledgeForActivatedPreview(scope.Axes{Project: project}, activation.Context{}, pa.Viewer())
		} else {
			entries, err = a.store.KnowledgeForContext(scope.Axes{Project: project}, pa.Viewer())
		}
		if err != nil {
			return "", err
		}
		entries = keep(entries, 0, func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) })
		if len(entries) == 0 {
			return "", nil
		}
		if withStaged {
			return server.RenderBootstrapPreview(entries, 12000), nil
		}
		return server.RenderBootstrap(entries, 12000), nil
	}
	delivered, err := render(false)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	view.Output = delivered
	if preview {
		if view.Output, err = render(true); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if view.Output != "" {
		if preview {
			view.Lines = contextLines(view.Output, delivered)
		} else {
			view.Lines = contextLines(view.Output, "")
		}
		view.Size = l.msg("context.size", len(view.Lines), thousands(l, utf8.RuneCountInString(view.Output)))
	}
	view.Waiting = a.pendingFor(pa, project)
	view.DeliveredHref = withQuery(r, map[string]string{"preview": ""})
	view.PreviewHref = withQuery(r, map[string]string{"preview": "1"})
	view.ReviewHref = knowledgeSub(r, project, "/ui/review", "/review")
	view.Segs = a.knowledgeSegs(r, pa, l, project, "context", a.knowledgeCountText(pa, project))
	a.renderBrowser(w, r, "context", pageData{Title: l.msg("context.title"), ContextV: view})
}

// thousands writes a number with the separator of the language.
func thousands(l lang, n int) string {
	s := strconv.Itoa(n)
	sep := ","
	if l == langDE {
		sep = "."
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + sep + s[i:]
	}
	return s
}

// shortSha is the seven characters a commit is shown with.
func shortSha(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
