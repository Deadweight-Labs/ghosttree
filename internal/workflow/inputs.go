package workflow

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The inputs the machine assembles from the project (REQ-696, stage E3). A
// block never reads the database itself: the resolver assembles what the
// definition declares and what the block always reads (BlockSpec.Inputs), with
// the rights of the account the run acts for, and the gate checks evidence
// against exactly that text. Every input is bounded, so a large pull request
// costs at most a known amount.

// Limits of the assembled inputs, in characters.
const (
	maxDiffChars     = 60_000
	maxMessageChars  = 4_000
	maxMessagesChars = 40_000
	maxListLines     = 150
)

// Data is what the resolver assembled for a block, typed, beside the text of
// the inputs: the pull request, the idea with its workshop, the sessions a
// person typed in. Blocks decide on it (are decisions open, which idea is it);
// the model only ever sees the text.
type Data struct {
	PR       *store.WorkflowPR
	Workshop *store.IdeaWorkshop
	IdeaID   int64
	// Sessions are the sessions of the pull request the run may read, with
	// what a person typed in each, in the order of the pull request.
	Sessions []SessionText
	// Research is what a research step found; empty without one.
	Research string
}

// SessionText is what a person typed in one session.
type SessionText struct {
	Ref, Person, Date string
	Messages          []string
}

// Researcher searches the net for a short query and returns what it found as
// text. The server has none until stage E5; a test brings its own.
type Researcher func(ctx context.Context, query string) (string, error)

// assembler puts the inputs of one run together.
type assembler struct {
	st     *store.Store
	run    store.WorkflowRun
	ev     store.WorkflowEvent
	pa     *store.ProjectAccess
	prDiff func(ctx context.Context, project string, pr int64) (string, error)
	data   Data
	loaded map[string]bool
	errs   []string
}

func newAssembler(st *store.Store, run store.WorkflowRun, ev store.WorkflowEvent, account int64, prDiff func(context.Context, string, int64) (string, error)) *assembler {
	a := &assembler{st: st, run: run, ev: ev, prDiff: prDiff, loaded: map[string]bool{}}
	if account > 0 {
		label := ""
		if acc, err := st.AccountByPrincipalID(fmt.Sprintf("person:%d", account)); err == nil {
			label = acc.Name
		}
		a.pa = st.Access(store.Principal{ID: fmt.Sprintf("person:%d", account), Label: label})
	}
	if n, err := strconv.ParseInt(ev.SubjectID, 10, 64); err == nil && ev.SubjectKind == "idea" {
		a.data.IdeaID = n
	}
	return a
}

// sources are the texts a result may name as its source: what a person typed,
// by session.
func (a *assembler) sources() map[string]string {
	out := map[string]string{}
	for _, s := range a.data.Sessions {
		out[s.Ref] = strings.Join(s.Messages, "\n")
	}
	return out
}

func (a *assembler) pr() *store.WorkflowPR {
	if a.loaded["pr"] {
		return a.data.PR
	}
	a.loaded["pr"] = true
	n, err := strconv.ParseInt(a.ev.SubjectID, 10, 64)
	if err != nil || a.ev.SubjectKind != "pr" {
		return nil
	}
	pr, err := a.st.WorkflowPRInput(a.run.Project, n)
	if err != nil {
		a.errs = append(a.errs, "pull request: "+err.Error())
		return nil
	}
	a.data.PR = &pr
	return a.data.PR
}

func (a *assembler) workshop() *store.IdeaWorkshop {
	if a.loaded["idea"] {
		return a.data.Workshop
	}
	a.loaded["idea"] = true
	if a.pa == nil || a.data.IdeaID == 0 {
		return nil
	}
	ws, err := a.pa.IdeaWorkshop(a.data.IdeaID)
	if err != nil {
		a.errs = append(a.errs, "idea: "+err.Error())
		return nil
	}
	a.data.Workshop = &ws
	return a.data.Workshop
}

// sessions reads what a person typed in the sessions of the pull request that
// the run's account may read; the agent's turns, tool output and what a harness
// put in a person's turn are left out (sessiondistill.UserChunks).
func (a *assembler) sessions() []SessionText {
	if a.loaded["sessions"] {
		return a.data.Sessions
	}
	a.loaded["sessions"] = true
	pr := a.pr()
	if pr == nil || a.pa == nil || len(pr.Sessions) == 0 {
		return nil
	}
	owners, err := a.st.SessionsWithOwners(pr.Sessions)
	if err != nil {
		a.errs = append(a.errs, "sessions: "+err.Error())
		return nil
	}
	total := 0
	for _, id := range pr.Sessions {
		sess, ok := owners[id]
		// The run's text and its proposals are read by every member of the
		// project, so only a session shared with them goes in: an owner or a
		// lead reads private transcripts too, and their reading must not
		// carry a private one onto the run page.
		if !ok || sess.Scope.Project != a.run.Project || !sharedWithProject(sess) || !a.pa.CanSeeTranscript(sess) {
			continue
		}
		chunks, err := a.st.ReadSession(id, 0, 5000)
		if err != nil {
			continue
		}
		st := SessionText{Ref: sess.PublicID, Person: sess.Owner}
		if st.Ref == "" {
			st.Ref = "s" + strconv.FormatInt(id, 10)
		}
		if len(sess.StartedAt) >= 10 {
			st.Date = sess.StartedAt[:10]
		}
		for _, c := range sessiondistill.UserChunks(chunks) {
			text := cut(strings.TrimSpace(c.Text), maxMessageChars)
			if total+len(text) > maxMessagesChars {
				break
			}
			total += len(text)
			st.Messages = append(st.Messages, text)
		}
		if len(st.Messages) > 0 {
			a.data.Sessions = append(a.data.Sessions, st)
		}
	}
	return a.data.Sessions
}

// sharedWithProject says whether the owner of a session opened it to the
// members of its project (or to its guests as well).
func sharedWithProject(sess store.Session) bool {
	return sess.Visibility == store.VisProject || sess.Visibility == store.VisGuests
}

// value is the text of an assembled input; ok is false when the run has
// nothing for it (another event, no such pull request).
func (a *assembler) value(ctx context.Context, name string) (string, bool) {
	switch name {
	case "pr.text":
		if pr := a.pr(); pr != nil {
			return strings.TrimSpace(pr.Title + "\n\n" + pr.Body), true
		}
	case "pr.commits":
		if pr := a.pr(); pr != nil {
			var b strings.Builder
			for _, c := range pr.Commits {
				msg := strings.TrimSpace(c.Message)
				if msg == "" {
					msg = c.Title
				}
				fmt.Fprintf(&b, "commit %s\n%s\n\n", short(c.SHA), cut(msg, 2000))
			}
			return strings.TrimSpace(b.String()), true
		}
	case "pr.files":
		if pr := a.pr(); pr != nil {
			return strings.Join(prFiles(pr), "\n"), true
		}
	case "pr.diff":
		pr := a.pr()
		if pr == nil {
			return "", false
		}
		if a.prDiff != nil {
			if d, err := a.prDiff(ctx, a.run.Project, pr.Number); err == nil && strings.TrimSpace(d) != "" {
				return cut(d, maxDiffChars), true
			}
		}
		// Without a diff the changed files stand in; the commits are their own input.
		files := prFiles(pr)
		if len(files) == 0 {
			return "(no diff and no changed files known)", true
		}
		return "(no diff available; changed files)\n" + strings.Join(files, "\n"), true
	case "pr.knowledge":
		if pr := a.pr(); pr != nil {
			var lines []string
			for _, k := range pr.Knowledge {
				lines = append(lines, fmt.Sprintf("KN-%d [%s] %s", k.ID, k.Type, k.Title))
			}
			return strings.Join(lines, "\n"), true
		}
	case "pr.sessions.user_messages":
		if a.pr() == nil {
			return "", false
		}
		var b strings.Builder
		for _, s := range a.sessions() {
			fmt.Fprintf(&b, "<session ref=%q person=%q date=%q>\n", s.Ref, s.Person, s.Date)
			for n, m := range s.Messages {
				fmt.Fprintf(&b, "[%d] %s\n", n+1, m)
			}
			b.WriteString("</session>\n")
		}
		return strings.TrimSpace(b.String()), true
	case "knowledge.related":
		pr := a.pr()
		if pr == nil {
			return "", false
		}
		return a.relatedKnowledge(pr), true
	case "requests.open":
		lines, err := a.st.OpenRequestsForPrompt(a.run.Project, maxListLines)
		if err != nil {
			return "", false
		}
		return strings.Join(lines, "\n"), true
	case "idea.text":
		if ws := a.workshop(); ws != nil {
			t := fmt.Sprintf("IDEA-%d %s\n\n%s", ws.Idea.ID, ws.Idea.Title, ws.Idea.Body)
			if ws.Summary != nil && strings.TrimSpace(ws.Summary.Body) != "" {
				t += "\n\nSummary:\n" + ws.Summary.Body
			}
			return strings.TrimSpace(t), true
		}
	case "idea.concept":
		if ws := a.workshop(); ws != nil {
			return conceptText(ws), true
		}
	case "idea.decisions":
		if ws := a.workshop(); ws != nil {
			return decisionsText(ws.Decisions), true
		}
	case "idea.related":
		if ws := a.workshop(); ws != nil {
			return a.relatedIdeas(ws.Idea), true
		}
	case "features.related":
		if ws := a.workshop(); ws != nil {
			return a.relatedFeatures(ws.Idea), true
		}
	case "research":
		return a.data.Research, a.data.Research != ""
	}
	return "", false
}

func prFiles(pr *store.WorkflowPR) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range pr.Paths {
		add(p)
	}
	for _, c := range pr.Commits {
		for _, p := range c.Files {
			add(p)
		}
	}
	if len(out) > maxListLines {
		out = out[:maxListLines]
	}
	return out
}

// relatedKnowledge are the entries of the project the words of the pull
// request's title find, besides those written on it.
func (a *assembler) relatedKnowledge(pr *store.WorkflowPR) string {
	q := strings.Join(keywords(pr.Title, 6), " ")
	if q == "" {
		return ""
	}
	seen := map[int64]bool{}
	for _, k := range pr.Knowledge {
		seen[k.ID] = true
	}
	var lines []string
	for _, w := range keywords(pr.Title, 6) {
		list, err := a.st.SearchKnowledge(w, scope.Axes{Project: a.run.Project}, 5)
		if err != nil {
			continue
		}
		for _, k := range list {
			if seen[k.ID] || k.Scope.Project != a.run.Project || k.Status != "active" {
				continue
			}
			seen[k.ID] = true
			lines = append(lines, fmt.Sprintf("KN-%d [%s] %s", k.ID, k.Type, k.Title))
		}
	}
	if len(lines) > 15 {
		lines = lines[:15]
	}
	return strings.Join(lines, "\n")
}

// relatedIdeas are the other ideas of the project that share words of the
// title, the ones sharing most first.
func (a *assembler) relatedIdeas(i idea.Idea) string {
	if a.pa == nil {
		return ""
	}
	hits := map[int64]int{}
	byID := map[int64]idea.Idea{}
	for _, w := range keywords(i.Title, 6) {
		list, err := a.pa.SearchIdeas(idea.Filter{Project: i.Project, State: "all", Query: w, Limit: 10})
		if err != nil {
			continue
		}
		for _, o := range list {
			if o.ID == i.ID {
				continue
			}
			hits[o.ID]++
			byID[o.ID] = o
		}
	}
	var lines []string
	for _, id := range rankedIDs(hits, 8) {
		o := byID[id]
		lines = append(lines, fmt.Sprintf("IDEA-%d [%s] %s — %s", o.ID, idea.EffectivePhase(o.State, o.Phase), o.Title, cut(oneLine(o.Body), 200)))
	}
	return strings.Join(lines, "\n")
}

// relatedFeatures are the active features of the project that share words of
// the title, and those the idea already affects.
func (a *assembler) relatedFeatures(i idea.Idea) string {
	if a.pa == nil {
		return ""
	}
	hits := map[int64]int{}
	byID := map[int64]feature.Feature{}
	for _, w := range keywords(i.Title, 6) {
		list, err := a.pa.SearchFeatures(feature.Filter{Project: i.Project, Query: w, Limit: 10})
		if err != nil {
			continue
		}
		for _, f := range list {
			hits[f.ID]++
			byID[f.ID] = f
		}
	}
	linked := map[int64]bool{}
	for _, f := range i.Features {
		linked[f.ID] = true
	}
	var lines []string
	for _, id := range rankedIDs(hits, 8) {
		f := byID[id]
		line := fmt.Sprintf("FEAT-%d %s — %s", f.ID, f.Name, cut(oneLine(f.Summary), 200))
		if linked[id] {
			line += " (already linked: the idea affects it)"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func rankedIDs(hits map[int64]int, max int) []int64 {
	ids := make([]int64, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(x, y int) bool {
		if hits[ids[x]] != hits[ids[y]] {
			return hits[ids[x]] > hits[ids[y]]
		}
		return ids[x] > ids[y]
	})
	if len(ids) > max {
		ids = ids[:max]
	}
	return ids
}

func conceptText(ws *store.IdeaWorkshop) string {
	if ws.Concept == nil {
		return "(no concept)"
	}
	c := ws.Concept.Concept
	var b strings.Builder
	fmt.Fprintf(&b, "Concept, version %d\n", ws.Concept.Revision)
	for _, s := range []struct{ name, text string }{{"About", c.About}, {"What", c.What}, {"How", c.How}, {"Not", c.Not}, {"Open questions", c.Questions}} {
		if strings.TrimSpace(s.text) != "" {
			fmt.Fprintf(&b, "\n## %s\n%s\n", s.name, strings.TrimSpace(s.text))
		}
	}
	if len(c.Requirements) > 0 {
		b.WriteString("\n## Requirements\n")
		for n, r := range c.Requirements {
			fmt.Fprintf(&b, "%d. %s\n", n+1, r)
		}
	}
	return strings.TrimSpace(b.String())
}

func decisionsText(ds []idea.Decision) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%s [%s] %s\n", d.HumanID(), d.State, d.Question)
		for _, v := range d.Variants {
			if v.Label == d.ChosenVariant {
				fmt.Fprintf(&b, "  chosen: %s %s: %s\n", v.Label, v.Title, cut(oneLine(v.Text), 600))
			}
		}
		for _, s := range []struct{ k, v string }{{"answer", d.Answer}, {"reason", d.Reason}} {
			if strings.TrimSpace(s.v) != "" {
				fmt.Fprintf(&b, "  %s: %s\n", s.k, cut(oneLine(s.v), 600))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// OpenDecisions are the decisions of a workshop that are still open, by their
// number.
func OpenDecisions(ws *store.IdeaWorkshop) []string {
	if ws == nil {
		return nil
	}
	var out []string
	for _, d := range ws.Decisions {
		if d.State == idea.DecisionOpen {
			out = append(out, d.HumanID())
		}
	}
	return out
}

// keywords are the longer words of a title, lower case, without repeats.
func keywords(s string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) < 4 || seen[w] || stopwords[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == max {
			break
		}
	}
	return out
}

var stopwords = map[string]bool{"eine": true, "einen": true, "einer": true, "nicht": true, "oder": true, "auch": true, "wenn": true, "dass": true, "with": true,
	"that": true, "this": true, "from": true, "into": true, "feat": true, "beim": true, "nach": true, "über": true, "werden": true, "wird": true, "sind": true}

func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + " …"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
