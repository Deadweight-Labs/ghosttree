package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The read tools of an own prompt (stage 3, REQ-698). A workflow names the
// ones it needs (tools: [knowledge.search, issue.read]); the loop offers the
// model exactly those. Every tool reads with the rights of the account the run
// acts for, and only what every member of the project may read on the run
// page (audience.go, the same filter as the assembled inputs): knowledge of
// the project, issues, ideas, pains and file descriptions of the project,
// pull requests the server knows, sessions their owners shared with it
// (KN-4817). None of them
// writes anything; a tool result is data for the model and, for the gate, a
// source a quote may come from.

// ToolSpec is one read tool of the catalogue.
type ToolSpec struct {
	Name    string
	Summary string
	// Schema is the JSON schema of its input.
	Schema string
	// Research: the tool reaches the net; it comes with research: true, never
	// from the tools list.
	Research bool
	run      func(ctx context.Context, tb *Toolbox, in toolArgs) (string, error)
}

// toolArgs are the inputs of every tool; each reads the fields it needs.
type toolArgs struct {
	Query   string    `json:"query"`
	ID      refNumber `json:"id"`
	Path    string    `json:"path"`
	Number  refNumber `json:"number"`
	Session string    `json:"session"`
	URL     string    `json:"url"`
}

// Names of the tools that reach the net.
const (
	ToolWebSearch = "web.search"
	ToolWebFetch  = "web.fetch"
)

const (
	schemaQuery   = `{"type":"object","properties":{"query":{"type":"string","description":"words to search for"}},"required":["query"]}`
	schemaID      = `{"type":"object","properties":{"id":{"type":"integer","description":"the number"}},"required":["id"]}`
	schemaPath    = `{"type":"object","properties":{"path":{"type":"string","description":"path relative to the root of the repository; empty for the root"}},"required":["path"]}`
	schemaNumber  = `{"type":"object","properties":{"number":{"type":"integer","description":"number of the pull request"}},"required":["number"]}`
	schemaSession = `{"type":"object","properties":{"session":{"type":"string","description":"the address of the session"}},"required":["session"]}`
	schemaURL     = `{"type":"object","properties":{"url":{"type":"string","description":"an address a web.search of this run returned"}},"required":["url"]}`
)

// Tools is the catalogue of read tools.
var Tools = []ToolSpec{
	{Name: "knowledge.search", Summary: "search the knowledge entries of the project (pitfalls, decisions, notes, plans)", Schema: schemaQuery, run: toolKnowledgeSearch},
	{Name: "knowledge.read", Summary: "read one knowledge entry of the project in full (KN-n)", Schema: schemaID, run: toolKnowledgeRead},
	{Name: "issue.read", Summary: "read one issue of the project with its criteria (REQ-n)", Schema: schemaID, run: toolIssueRead},
	{Name: "idea.read", Summary: "read one idea of the project with its concept and decisions (IDEA-n)", Schema: schemaID, run: toolIdeaRead},
	{Name: "pain.read", Summary: "read one problem of the project (PAIN-n)", Schema: schemaID, run: toolPainRead},
	{Name: "file.describe", Summary: "read the description of a file or directory of the repository", Schema: schemaPath, run: toolFileDescribe},
	{Name: "pr.diff", Summary: "read the diff of a pull request of the project, or its changed files", Schema: schemaNumber, run: toolPRDiff},
	{Name: "session.messages", Summary: "read what a person typed in a session shared with the project", Schema: schemaSession, run: toolSessionMessages},
	{Name: ToolWebSearch, Summary: "search the web; the query leaves the server", Schema: schemaQuery, Research: true, run: toolWebSearch},
	{Name: ToolWebFetch, Summary: "read a page a web.search of this run found, as text", Schema: schemaURL, Research: true, run: toolWebFetch},
}

// ToolByName finds a tool of the catalogue.
func ToolByName(name string) (ToolSpec, bool) {
	i := slices.IndexFunc(Tools, func(t ToolSpec) bool { return t.Name == name })
	if i < 0 {
		return ToolSpec{}, false
	}
	return Tools[i], true
}

// ToolNames lists the tools a workflow may name in tools (not the research ones).
func ToolNames() []string {
	var out []string
	for _, t := range Tools {
		if !t.Research {
			out = append(out, t.Name)
		}
	}
	return out
}

// wireName is the name of a tool on the wire: both formats allow letters,
// digits, _ and - only.
func wireName(name string) string { return strings.ReplaceAll(name, ".", "_") }

// Limits of what tools hand the model.
const (
	// maxToolChars bounds one tool result for the model.
	maxToolChars = 12_000
	// maxToolTotal bounds what all tool results of a run hand out together.
	maxToolTotal = 120_000
	// maxRecordChars bounds a tool result in the run's full text.
	maxRecordChars = 2_000
)

// Toolbox is what the tools of one run share: the assembler with the run's
// rights, the research of the server, the tools the definition declared, and
// what was read, for the gate.
type Toolbox struct {
	asm      *assembler
	web      *WebResearch
	declared []ToolSpec
	// found are the addresses web.search returned in this run: web.fetch
	// reads only those, so a model cannot send data out in an address of its own.
	found map[string]bool
	// searches and fetches count what left the server; each has a ceiling
	// per run (maxSearchesPerRun, maxFetchesPerRun).
	searches, fetches int
	// texts are the tool results as the model got them; quotes may come from them.
	texts []string
	total int
	// searchable are the words a web.search may use: those of the declared
	// inputs, of the team's prompt and of earlier search hits, never those a
	// read tool returned, so that nothing read from the project leaves the
	// server in a query.
	searchable map[string]bool
}

// newToolbox prepares the tools of a definition. The research tools come
// with research: true and only when the server has a search service.
func newToolbox(asm *assembler, d *Def, web *WebResearch) *Toolbox {
	tb := &Toolbox{asm: asm, web: web, found: map[string]bool{}, searchable: map[string]bool{}}
	tb.allowSearchWords(d.TaskPrompt())
	for _, n := range d.Tools {
		if t, ok := ToolByName(n); ok && !t.Research {
			tb.declared = append(tb.declared, t)
		}
	}
	if d.Research && web != nil {
		for _, n := range []string{ToolWebSearch, ToolWebFetch} {
			t, _ := ToolByName(n)
			tb.declared = append(tb.declared, t)
		}
	}
	return tb
}

// wire is the tool list for the model.
func (tb *Toolbox) wire() []llm.Tool {
	out := make([]llm.Tool, 0, len(tb.declared))
	for _, t := range tb.declared {
		out = append(out, llm.Tool{Name: wireName(t.Name), Description: t.Summary, Schema: json.RawMessage(t.Schema)})
	}
	return out
}

// lookup finds a declared tool by its wire name.
func (tb *Toolbox) lookup(wire string) (ToolSpec, bool) {
	for _, t := range tb.declared {
		if wireName(t.Name) == wire {
			return t, true
		}
	}
	return ToolSpec{}, false
}

// Names lists the declared tools.
func (tb *Toolbox) Names() []string {
	out := make([]string, 0, len(tb.declared))
	for _, t := range tb.declared {
		out = append(out, t.Name)
	}
	return out
}

// Evidence is what the tools of the run handed the model.
func (tb *Toolbox) Evidence() string {
	if tb == nil {
		return ""
	}
	return strings.Join(tb.texts, "\n")
}

// errNotReadable is the answer for what does not exist and what the run may
// not read alike, so that a tool does not tell one from the other.
var errNotReadable = errors.New("not found, or not readable for this run")

// errReadBudget: the run read as much as it may.
var errReadBudget = errors.New("this run has read as much as it may; answer with what you have")

// call runs a declared tool and bounds its result.
func (tb *Toolbox) call(ctx context.Context, t ToolSpec, raw json.RawMessage) (string, error) {
	var in toolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("the input is no JSON object of the form %s", t.Schema)
	}
	if tb.total >= maxToolTotal {
		return "", errReadBudget
	}
	out, err := t.run(ctx, tb, in)
	if err != nil {
		return "", err
	}
	out = cut(strings.TrimSpace(out), min(maxToolChars, maxToolTotal-tb.total))
	if out == "" {
		out = "(nothing)"
	}
	tb.total += len(out)
	tb.texts = append(tb.texts, out)
	return out, nil
}

func (tb *Toolbox) project() string { return tb.asm.run.Project }

// The rule for what a web.search may send (KN-4823). A query leaves the
// server, so it may carry only words the run may hand out: those of the
// declared inputs, of the team's prompt and of earlier hits, never what a read
// tool returned. Before the check a query is normalized (NFKC, invisible
// format characters and combining marks removed, lower case, every other
// character a space), and what is sent is exactly the checked words: a
// zero-width space or a dot cannot hide a word inside an allowed one. A word
// of any length has to come from the searchable words; a short one
// (shorter than minQueryWord) only from a fixed list of stop words, at most
// maxShortWords of them, so that pairs of letters or digits cannot spell out
// what the run read. What stays open is the choice and order of allowed
// words, bounded by maxSearchesPerRun and maxResearchQuery.
const (
	minQueryWord      = 3
	maxShortWords     = 3
	maxSearchesPerRun = 10
	// maxFetchesPerRun bounds web.fetch: which found page a model reads is a
	// signal to whoever runs those pages (a few bits per fetch), a channel
	// that cannot be closed, only kept small.
	maxFetchesPerRun = 5
)

// queryStopWords are the short words a query may use without a source: German
// and English function words.
var queryStopWords = map[string]bool{
	"a": true, "an": true, "as": true, "at": true, "be": true, "by": true, "do": true, "if": true, "in": true, "is": true, "it": true,
	"no": true, "of": true, "on": true, "or": true, "to": true, "vs": true, "we": true,
	"ab": true, "am": true, "da": true, "du": true, "er": true, "es": true, "im": true, "ob": true, "so": true, "um": true, "wo": true, "zu": true,
}

// normalizeQuery is the form a query and the searchable words are compared
// in, and the form a query is sent in.
func normalizeQuery(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKC.String(s) {
		switch {
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func queryWords(s string) []string { return strings.Fields(normalizeQuery(s)) }

// allowSearchWords adds the words of a text a web.search may use.
func (tb *Toolbox) allowSearchWords(text string) {
	for _, w := range queryWords(text) {
		tb.searchable[w] = true
	}
}

// checkQuery returns the query as it is sent, or why it may not be: the words
// that come from nowhere a search may take them from, or too many short ones.
func (tb *Toolbox) checkQuery(q string) (string, error) {
	words := queryWords(q)
	var foreign []string
	short := 0
	for _, w := range words {
		if utf8.RuneCountInString(w) < minQueryWord {
			if !queryStopWords[w] {
				foreign = appendNew(foreign, w)
			}
			short++
			continue
		}
		if !tb.searchable[w] {
			foreign = appendNew(foreign, w)
		}
	}
	if len(foreign) > 0 {
		return "", fmt.Errorf("a query may use only words of the inputs, of the task and of earlier search results, and of short words only common ones like in, of, im, zu; not: %s", strings.Join(foreign, ", "))
	}
	if short > maxShortWords {
		return "", fmt.Errorf("a query may have at most %d short words", maxShortWords)
	}
	return strings.Join(words, " "), nil
}

func appendNew(list []string, w string) []string {
	if slices.Contains(list, w) {
		return list
	}
	return append(list, w)
}

func toolKnowledgeSearch(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	q := cut(strings.TrimSpace(in.Query), 200)
	if q == "" {
		return "", errors.New("query is empty")
	}
	list, err := tb.asm.st.SearchKnowledge(q, scope.Axes{Project: tb.project()}, 20)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, k := range list {
		if !tb.asm.membersReadKnowledge(k) {
			continue
		}
		lines = append(lines, fmt.Sprintf("KN-%d [%s] %s — %s", k.ID, k.Type, k.Title, cut(oneLine(k.Body), 300)))
		if len(lines) == 10 {
			break
		}
	}
	if len(lines) == 0 {
		return "no entries found", nil
	}
	return strings.Join(lines, "\n"), nil
}

func toolKnowledgeRead(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	k, ok := tb.asm.membersReadKnowledgeID(int64(in.ID))
	if !ok {
		return "", errNotReadable
	}
	return fmt.Sprintf("KN-%d [%s] %s\n\n%s", k.ID, k.Type, k.Title, cut(k.Body, 8000)), nil
}

func toolIssueRead(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	pa := tb.asm.pa
	if !tb.asm.membersReadRequest(int64(in.ID)) {
		return "", errNotReadable
	}
	// RequestRead checks the issue itself, as GET /api/requests/{id} does.
	d, err := pa.RequestRead(int64(in.ID))
	if err != nil || d.Request.Scope.Project != tb.project() {
		return "", errNotReadable
	}
	var b strings.Builder
	fmt.Fprintf(&b, "REQ-%d [%s, %s] %s\n\n%s\n", d.Request.ID, d.Request.Type, d.Request.State, d.Request.Title, cut(d.Request.Description, 6000))
	if len(d.Criteria) > 0 {
		b.WriteString("\nCriteria:\n")
		for _, c := range d.Criteria {
			fmt.Fprintf(&b, "- AC-%d.%d [%s] %s\n", d.Request.ID, c.Number, c.State, cut(oneLine(c.Description), 600))
		}
	}
	return b.String(), nil
}

func toolIdeaRead(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	pa := tb.asm.pa
	if pa == nil {
		return "", errNotReadable
	}
	ws, err := pa.IdeaWorkshop(int64(in.ID))
	if err != nil || !tb.asm.membersReadIdea(ws.Idea) {
		return "", errNotReadable
	}
	t := fmt.Sprintf("IDEA-%d %s\n\n%s", ws.Idea.ID, ws.Idea.Title, cut(ws.Idea.Body, 4000))
	if ws.Summary != nil && strings.TrimSpace(ws.Summary.Body) != "" {
		t += "\n\nSummary:\n" + cut(ws.Summary.Body, 2000)
	}
	if ws.Concept != nil {
		t += "\n\n" + conceptText(&ws)
	}
	if len(ws.Decisions) > 0 {
		t += "\n\nDecisions:\n" + decisionsText(ws.Decisions)
	}
	return t, nil
}

func toolPainRead(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	p, err := tb.asm.st.PainByID(int64(in.ID))
	if err != nil || !tb.asm.membersReadPain(p) {
		return "", errNotReadable
	}
	t := fmt.Sprintf("PAIN-%d [%s] %s\n\n%s", p.ID, p.State, p.Title, cut(p.Body, 6000))
	if strings.TrimSpace(p.Remaining) != "" {
		t += "\n\nRemaining: " + cut(p.Remaining, 1000)
	}
	return t, nil
}

func toolFileDescribe(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	pa := tb.asm.pa
	p := strings.Trim(path.Clean("/"+strings.TrimSpace(in.Path)), "/")
	if pa == nil || !pa.CanSeeProject(tb.project(), store.ResGhost) {
		return "", errNotReadable
	}
	g, err := tb.asm.st.GhostFileByPath(tb.project(), p)
	if err != nil {
		return "no description of " + displayPath(p), nil
	}
	return fmt.Sprintf("%s (%s)\n\n%s", displayPath(p), g.Kind, cut(g.Description, 8000)), nil
}

func displayPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func toolPRDiff(ctx context.Context, tb *Toolbox, in toolArgs) (string, error) {
	n := int64(in.Number)
	if n <= 0 {
		n = int64(in.ID)
	}
	// Only a pull request the server recorded: not one from a fork, which the
	// import leaves out on purpose, and no number GitHub would answer for
	// although ghosttree never saw it.
	pr, err := tb.asm.st.WorkflowPRInput(tb.project(), n)
	if err != nil || !pr.Known || tb.asm.pa == nil || !tb.asm.pa.CanSeeProject(tb.project(), store.ResProject) {
		return "", errNotReadable
	}
	head := fmt.Sprintf("pull request #%d %s\n\n", pr.Number, pr.Title)
	if tb.asm.prDiff != nil {
		if d, err := tb.asm.prDiff(ctx, tb.project(), pr.Number); err == nil && strings.TrimSpace(d) != "" {
			return head + d, nil
		}
	}
	files := prFiles(&pr)
	if len(files) == 0 {
		return head + "(no diff and no changed files known)", nil
	}
	return head + "(no diff available; changed files)\n" + strings.Join(files, "\n"), nil
}

// toolSessionMessages reads what a person typed in one session: only a session
// of the project its owner shared with the members (KN-4817), whatever the
// run's account may read beyond that.
func toolSessionMessages(_ context.Context, tb *Toolbox, in toolArgs) (string, error) {
	a := tb.asm
	ref := strings.TrimSpace(in.Session)
	if a.pa == nil || ref == "" {
		return "", errNotReadable
	}
	sess, err := a.st.SessionByPublicID(ref)
	if err != nil && strings.HasPrefix(ref, "s") {
		if id, perr := strconv.ParseInt(ref[1:], 10, 64); perr == nil {
			sess, err = a.st.SessionByID(id)
		}
	}
	if err != nil {
		return "", errNotReadable
	}
	owners, err := a.st.SessionsWithOwners([]int64{sess.ID})
	if err != nil {
		return "", errNotReadable
	}
	if s, ok := owners[sess.ID]; ok {
		sess = s
	}
	if sess.Scope.Project != tb.project() || !sharedWithProject(sess) || !a.pa.CanSeeTranscript(sess) {
		return "", errNotReadable
	}
	chunks, err := a.st.ReadSession(sess.ID, 0, 5000)
	if err != nil {
		return "", errNotReadable
	}
	st := SessionText{Ref: sess.PublicID, Person: sess.Owner}
	if st.Ref == "" {
		st.Ref = "s" + strconv.FormatInt(sess.ID, 10)
	}
	if len(sess.StartedAt) >= 10 {
		st.Date = sess.StartedAt[:10]
	}
	total := 0
	for _, c := range sessiondistill.UserChunks(chunks) {
		text := cut(strings.TrimSpace(c.Text), maxMessageChars)
		if total+len(text) > maxToolChars {
			break
		}
		total += len(text)
		st.Messages = append(st.Messages, text)
	}
	// A quote of a result that names this session is checked against it.
	if !slices.ContainsFunc(a.data.Sessions, func(s SessionText) bool { return s.Ref == st.Ref }) {
		a.data.Sessions = append(a.data.Sessions, st)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<session ref=%q person=%q date=%q>\n", st.Ref, st.Person, st.Date)
	for n, m := range st.Messages {
		fmt.Fprintf(&b, "[%d] %s\n", n+1, m)
	}
	b.WriteString("</session>")
	return b.String(), nil
}

func toolWebSearch(ctx context.Context, tb *Toolbox, in toolArgs) (string, error) {
	if tb.web == nil {
		return "", errResearchOff
	}
	q := strings.TrimSpace(in.Query)
	if utf8.RuneCountInString(q) > maxResearchQuery {
		q = string([]rune(q)[:maxResearchQuery])
	}
	q, err := tb.checkQuery(q)
	if err != nil {
		return "", err
	}
	if q == "" {
		return "", errors.New("query is empty")
	}
	if tb.searches >= maxSearchesPerRun {
		return "", fmt.Errorf("this run has searched %d times, as often as it may; answer with what you have", maxSearchesPerRun)
	}
	tb.searches++
	hits, err := tb.web.Search(ctx, q, 8)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "no results", nil
	}
	var b strings.Builder
	for _, h := range hits {
		tb.found[h.URL] = true
		tb.allowSearchWords(h.Title + " " + h.Content)
		fmt.Fprintf(&b, "%s\n%s\n%s\n\n", oneLine(h.Title), h.URL, cut(oneLine(h.Content), 400))
	}
	return b.String(), nil
}

func toolWebFetch(ctx context.Context, tb *Toolbox, in toolArgs) (string, error) {
	if tb.web == nil {
		return "", errResearchOff
	}
	u := strings.TrimSpace(in.URL)
	if !tb.found[u] {
		return "", errors.New("only an address a web.search of this run returned can be read")
	}
	if tb.fetches >= maxFetchesPerRun {
		return "", fmt.Errorf("this run has read %d pages, as many as it may; answer with what you have", maxFetchesPerRun)
	}
	tb.fetches++
	title, text, err := tb.web.Fetch(ctx, u)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(title + "\n" + u + "\n\n" + text), nil
}
