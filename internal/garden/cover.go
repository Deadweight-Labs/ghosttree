package garden

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Kinds of answer to the regression question (job 5): a pitfall nobody has
// judged yet gets one of these, or none.
const (
	// CoverExisting names a test in the checkout that would catch the defect's return.
	CoverExisting = "existing"
	// CoverSketch says no such test exists and sketches one.
	CoverSketch = "sketch"
	// CoverUntestable says there is nothing to test here, and why.
	CoverUntestable = "untestable"
)

const (
	// maxCoverAsks is how many open pitfalls of one session get the question. The
	// rest wait for the next run: every answer costs a lookup, and the nightly
	// budget stays what it is.
	maxCoverAsks = 8

	coverRefPrefix      = "gardener:regression:"
	minUntestableReason = 12
	minSketchLines      = 2
	maxSketchLines      = 10
	maxSketchChars      = 1500
	maxTestFile         = 4 << 20
)

var testNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{1,119}$`)

// Cover is one validated answer to the regression question. It only exists once
// the checkout bears it out: a named test is defined in the file it is named in,
// and the session looked at that file.
type Cover struct {
	Entry Entry
	Kind  string
	// File and Test name the test: the one that exists, or where the sketched one
	// belongs. Empty for an untestable pitfall.
	File, Test string
	// Steps is the sketch: what the test sets up and checks.
	Steps   string
	Reason  string
	Session string
}

// CoverLabel is the question a regression proposal asks, as the title and the card say it.
func CoverLabel(kind string) string {
	switch kind {
	case CoverExisting:
		return "Test vorhanden?"
	case CoverSketch:
		return "Test fehlt?"
	}
	return "Nicht testbar?"
}

// TestID is how the test is recorded as the cover of a pitfall: file and name,
// the form the regression tool uses.
func (c Cover) TestID() string {
	if c.File == "" || c.Test == "" {
		return ""
	}
	return c.File + ":" + c.Test
}

// coverAsked is the pitfalls of a package the regression question is put to: the
// first maxCoverAsks of those that asked for it. The prompt and the validation
// both go by it, so an answer is only taken for a question that was asked.
func coverAsked(p Package) []Entry {
	var out []Entry
	for _, e := range p.Entries {
		if e.AskCover && e.Type == "pitfall" && len(out) < maxCoverAsks {
			out = append(out, e)
		}
	}
	return out
}

type agentCover struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	File   string `json:"file"`
	Test   string `json:"test"`
	Steps  string `json:"steps"`
	Reason string `json:"reason"`
}

// testDefined says whether the file, a tracked regular file of the checkout,
// defines the named test: for Go a `func Name(` in a _test.go file, for any other
// language the name as a word.
func (r *Repo) testDefined(file, name string) bool {
	if !r.tracked(file) {
		return false
	}
	p := filepath.Join(r.Root, file)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxTestFile))
	if err != nil {
		return false
	}
	if strings.HasSuffix(file, ".go") {
		if !strings.HasSuffix(file, "_test.go") {
			return false
		}
		return regexp.MustCompile(`(?m)^func\s+` + regexp.QuoteMeta(name) + `\s*\(`).Match(raw)
	}
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).Match(raw)
}

// cleanTestFile is a relative path inside the checkout, or "".
func cleanTestFile(f string) string {
	f = strings.TrimSpace(f)
	if f == "" || strings.ContainsAny(f, ": \t\r\n\x00\\") || strings.HasPrefix(f, "/") {
		return ""
	}
	f = path.Clean(strings.TrimPrefix(f, "./"))
	if f == "." || f == ".." || strings.HasPrefix(f, "../") {
		return ""
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == ".git" {
			return ""
		}
	}
	return f
}

// stepsOf cleans a sketch to its non-empty lines.
func stepsOf(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// validateCover holds one answer to the regression question to the checkout. It
// returns the cover, or why it was dropped.
func validateCover(repo *Repo, a agentCover, e Entry, looked *Looked) (Cover, string) {
	c := Cover{Entry: e, Kind: a.Kind, Reason: strings.TrimSpace(a.Reason)}
	switch a.Kind {
	case CoverExisting:
		file := cleanTestFile(a.File)
		if file == "" || !testNameRe.MatchString(strings.TrimSpace(a.Test)) {
			return Cover{}, "Datei oder Testname unbrauchbar"
		}
		name := strings.TrimSpace(a.Test)
		if !repo.testDefined(file, name) {
			return Cover{}, fmt.Sprintf("%s definiert keinen Test %s im Checkout", file, name)
		}
		if !looked.Has(file + ":1") {
			return Cover{}, fmt.Sprintf("die Session hat %s nicht angesehen", file)
		}
		c.File, c.Test = file, name
	case CoverSketch:
		file := cleanTestFile(a.File)
		name := strings.TrimSpace(a.Test)
		if file == "" || !testNameRe.MatchString(name) {
			return Cover{}, "Datei oder Testname der Skizze unbrauchbar"
		}
		if info, err := os.Stat(filepath.Join(repo.Root, filepath.Dir(file))); err != nil || !info.IsDir() {
			return Cover{}, fmt.Sprintf("das Verzeichnis von %s gibt es im Checkout nicht", file)
		}
		if repo.testDefined(file, name) {
			return Cover{}, fmt.Sprintf("%s gibt es schon in %s, als vorhandenen Test melden", name, file)
		}
		lines := stepsOf(a.Steps)
		if n := len(lines); n < minSketchLines || n > maxSketchLines || len(strings.Join(lines, "\n")) > maxSketchChars {
			return Cover{}, fmt.Sprintf("Skizze hat %d Zeilen (verlangt sind 3 bis 6, angenommen werden %d bis %d)", len(lines), minSketchLines, maxSketchLines)
		}
		c.File, c.Test, c.Steps = file, name, strings.Join(lines, "\n")
	case CoverUntestable:
		if len([]rune(c.Reason)) < minUntestableReason {
			return Cover{}, "nicht testbar ohne Grund"
		}
	default:
		return Cover{}, fmt.Sprintf("unbekannte Art %q", a.Kind)
	}
	return c, ""
}

// CoverRef is the session reference of a regression proposal: the pitfall and
// what is proposed, so the review page knows the action without reading prose.
func CoverRef(id int64, kind string) string { return fmt.Sprintf("%s%d:%s", coverRefPrefix, id, kind) }

// CoverProposed says whether the gardener has made a regression proposal for this
// pitfall that still stands for its current wording, in whatever state it is
// now. A person who said no is not asked again every night; only a change to the
// pitfall after the proposal reopens the question.
func CoverProposed(existing []store.Knowledge, e Entry) bool {
	changed := parseTime(e.UpdatedAt)
	prefix := fmt.Sprintf("%s%d:", coverRefPrefix, e.ID)
	for _, k := range existing {
		if k.Harness != Harness || !strings.HasPrefix(k.SessionRef, prefix) {
			continue
		}
		if changed.IsZero() || !changed.After(parseTime(k.CreatedAt)) {
			return true
		}
	}
	return false
}

// CoverProposal builds the staged note for a regression answer. Like every note
// of the gardener it changes nothing by itself: the pitfall stays as it is until
// a person confirms the note on the review page.
func CoverProposal(project string, c Cover) store.Knowledge {
	var b strings.Builder
	fmt.Fprintf(&b, "Vorschlag des Gärtners (Herkunft gardener, nicht bestätigt) zu Eintrag #%d: %s\n\n", c.Entry.ID, c.Entry.Title)
	fmt.Fprintf(&b, "Urteil: %s\n", CoverLabel(c.Kind))
	switch c.Kind {
	case CoverExisting:
		fmt.Fprintf(&b, "\nTest: %s\n", c.TestID())
	case CoverSketch:
		fmt.Fprintf(&b, "\nTest: %s (Skizze, noch nicht geschrieben)\n\nSchritte:\n", c.TestID())
		for _, l := range strings.Split(c.Steps, "\n") {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	if c.Reason != "" {
		fmt.Fprintf(&b, "\nBegründung: %s\n", c.Reason)
	}
	return store.Knowledge{
		Type:       "note",
		Title:      fmt.Sprintf("%s%s #%d: %s", titlePrefix, CoverLabel(c.Kind), c.Entry.ID, c.Entry.Title),
		Body:       b.String(),
		Scope:      scope.Axes{Project: project},
		Confidence: proposalState,
		Status:     "active",
		Origin:     "distilled",
		Harness:    Harness,
		SessionRef: CoverRef(c.Entry.ID, c.Kind),
	}
}

// ProposeCovers files one staged note per answer to the regression question that
// has no proposal yet, and returns how many.
func ProposeCovers(sink Sink, project string, existing []store.Knowledge, covers []Cover) (int, error) {
	written := 0
	for _, c := range covers {
		if CoverProposed(existing, c.Entry) {
			continue
		}
		if _, err := sink.Remember(CoverProposal(project, c), scope.Axes{Project: project}); err != nil {
			return written, fmt.Errorf("propose regression cover for #%d: %w", c.Entry.ID, err)
		}
		written++
	}
	return written, nil
}
