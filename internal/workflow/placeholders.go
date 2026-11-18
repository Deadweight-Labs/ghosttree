package workflow

import (
	"fmt"
	"slices"
	"strings"
)

// Placeholders in the text of a workflow (REQ-708). The engine does not
// substitute them: the model gets the text as written and the declared inputs
// as <input name="..."> blocks (renderInputs). A {{name}} in an instruction or
// a prompt is therefore a reference by name to one of those blocks, and the
// check makes sure there is one: a misspelt or undeclared name would send the
// model looking for an input it never receives.

// placeholder is one {{name}} in the text, with the line of the file it stands
// on and the key it belongs to: KeyPrompt for the prompt: key, "body" for the
// text below the second ---.
type placeholder struct {
	Line int
	Key  string
	Name string
}

// BodyKey is the Problem.Key of a problem in the text below the front matter.
const BodyKey = "body"

// placeholdersIn finds the {{name}} of the prompt: key and of the text below
// the second ---. Lines are numbered as in the file.
func placeholdersIn(text string) []placeholder {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		return nil
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	var out []placeholder
	scan := func(i int, key string) {
		rest := lines[i]
		for {
			from := strings.Index(rest, "{{")
			if from < 0 {
				return
			}
			to := strings.Index(rest[from+2:], "}}")
			if to < 0 {
				return
			}
			out = append(out, placeholder{Line: i + 1, Key: key, Name: strings.TrimSpace(rest[from+2 : from+2+to])})
			rest = rest[from+2+to+2:]
		}
	}
	inPrompt := false
	for i := start + 1; i < end; i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if l[0] != ' ' {
			inPrompt = strings.HasPrefix(l, KeyPrompt+":")
		}
		if inPrompt {
			scan(i, KeyPrompt)
		}
	}
	for i := end + 1; i < len(lines); i++ {
		scan(i, BodyKey)
	}
	return out
}

// checkPlaceholders reports every {{name}} of the text that no input answers:
// unknown names with the nearest known one and inputs the event does not carry
// are errors; an input the workflow does not read is a warning, since the name
// is right and only the model would miss the text.
func checkPlaceholders(text string, d Def, read []string, addErr, addWarn func(Problem)) {
	seen := map[string]bool{}
	for _, p := range placeholdersIn(text) {
		id := fmt.Sprintf("%d:%s", p.Line, p.Name)
		if seen[id] {
			continue
		}
		seen[id] = true
		prob := func(format string, args ...any) Problem {
			return Problem{Line: p.Line, Key: p.Key, Item: p.Name, Msg: fmt.Sprintf(format, args...)}
		}
		spec, ok := InputByName(p.Name)
		switch {
		case p.Name == "":
			addErr(prob("empty placeholder {{}}: name an input, for example {{%s}}", Inputs[0].Name))
		case !ok && p.Name == "research" && d.Research:
		case !ok:
			if near := nearest(p.Name, InputNames()); near != "" {
				addErr(prob("unknown placeholder {{%s}}: did you mean {{%s}}?", p.Name, near))
			} else {
				addErr(prob("unknown placeholder {{%s}} (inputs: %s)", p.Name, strings.Join(InputNames(), ", ")))
			}
		case len(spec.Events) > 0 && d.On != "" && !slices.Contains(spec.Events, d.On):
			addErr(prob("placeholder {{%s}}: the input is not there on %s", p.Name, d.On))
		case !slices.Contains(read, p.Name):
			addWarn(prob("placeholder {{%s}}: the workflow does not read it, the model will not get it; add %s under inputs:", p.Name, p.Name))
		}
	}
}

// InputNames lists the inputs of the catalogue.
func InputNames() []string {
	names := make([]string, 0, len(Inputs))
	for _, i := range Inputs {
		names = append(names, i.Name)
	}
	return names
}

// nearest is the candidate closest to name by edit distance, ignoring case,
// or "" when none is close enough to be a misspelling (at most a third of the
// name, at least two edits). A name that differs only in case or that matches
// a part of a longer one counts as close.
func nearest(name string, candidates []string) string {
	lower := strings.ToLower(name)
	best, bestD := "", 1<<30
	for _, c := range candidates {
		if c == name {
			continue
		}
		d := editDistance(lower, strings.ToLower(c))
		if d < bestD {
			best, bestD = c, d
		}
	}
	limit := len([]rune(name)) / 3
	if limit < 2 {
		limit = 2
	}
	if bestD > limit {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance of two strings, in runes.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur := make([]int, len(y)+1)
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(y)]
}

// didYouMean is the sentence part " (did you mean X?)" for a name that is not
// in the list, or "".
func didYouMean(name string, candidates []string) string {
	if near := nearest(name, candidates); near != "" {
		return fmt.Sprintf(" — did you mean %s?", near)
	}
	return ""
}
