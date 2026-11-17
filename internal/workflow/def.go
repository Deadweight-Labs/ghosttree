package workflow

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A workflow definition (REQ-695, stage E2) is a text file kept on the server:
// a front matter between two lines of "---" and, below it, an optional
// instruction in Markdown. The front matter is a strict subset of YAML: one
// key per line, values as plain or quoted scalars, inline lists [a, b], or
// indented blocks of "- item" lines and "key: value" lines. Nothing else
// (anchors, multi-line scalars, flow maps) is accepted, so that every file
// reads the same to people, to the server and to the block view of stage E4:
// each top-level key is exactly one block.
//
//	---
//	description: Wissen nach dem Merge prüfen
//	on: pr.merged
//	branches: [main, release/*]
//	inputs: [pr.title, pr.diff]
//	uses: ghosttree/ask
//	results:
//	  knowledge: 2
//	model: klein
//	budget:
//	  run_usd: 0.20
//	  month_usd: 5
//	---
//	Achte besonders auf Migrationen.
//
// Without uses the Markdown is an own prompt (stage 3, REQ-698). It runs in
// ghosttree's loop with the read tools it names and proposes the results it
// declares:
//
//	---
//	on: idea.created
//	inputs: [idea.text]
//	tools: [knowledge.search, issue.read]
//	research: true
//	results:
//	  comment: 1
//	model: mittel
//	---
//	Prüfe, ob es schon Wissen oder Issues zu dieser Idee gibt.

// Keys of the front matter, in the order the block view shows them.
const (
	KeyDescription = "description"
	KeyOn          = "on"
	KeyBranches    = "branches"
	KeyEvery       = "every"
	KeyChain       = "chain"
	KeyInputs      = "inputs"
	KeyUses        = "uses"
	KeyResults     = "results"
	KeyModel       = "model"
	KeyBudget      = "budget"
	KeyResearch    = "research"
	KeyPrompt      = "prompt"
	// KeyTools and KeyRounds belong to an own prompt (stage 3, REQ-698): the
	// read tools the model may call and how many rounds the loop may take.
	KeyTools  = "tools"
	KeyRounds = "rounds"
)

// Keys lists every key a definition may have.
var Keys = []string{KeyDescription, KeyOn, KeyBranches, KeyEvery, KeyChain, KeyInputs, KeyUses, KeyTools, KeyRounds, KeyResults, KeyModel, KeyBudget, KeyResearch, KeyPrompt}

// Budget keys.
const (
	BudgetRun   = "run_usd"
	BudgetMonth = "month_usd"
)

// Stages of freedom (DEC-42).
const (
	// StageBlock: a block from the catalogue, nothing else.
	StageBlock = 1
	// StageInstruction: a block plus an instruction of the team.
	StageInstruction = 2
	// StagePrompt: an own prompt with declared read tools, run in the loop
	// of stage E5 (REQ-698).
	StagePrompt = 3
)

// ResultLimit is one kind of result a workflow may create, and how many per run.
type ResultLimit struct {
	Kind string `json:"kind"`
	Max  int    `json:"max"`
}

// Def is a parsed workflow definition.
type Def struct {
	Description string        `json:"description,omitempty"`
	On          string        `json:"on"`
	Branches    []string      `json:"branches,omitempty"`
	Every       int           `json:"every,omitempty"`
	Chain       bool          `json:"chain,omitempty"`
	Inputs      []string      `json:"inputs,omitempty"`
	Uses        string        `json:"uses,omitempty"`
	Results     []ResultLimit `json:"results,omitempty"`
	Model       string        `json:"model,omitempty"`
	RunUSD      float64       `json:"run_usd,omitempty"`
	MonthUSD    float64       `json:"month_usd,omitempty"`
	Research    bool          `json:"research,omitempty"`
	// Prompt is an own prompt in one line (stage 3); the Markdown below the
	// front matter is the longer way to write it.
	Prompt string `json:"prompt,omitempty"`
	// Tools are the read tools of an own prompt (stage 3); Rounds bounds its
	// loop, 0 means DefaultRounds.
	Tools  []string `json:"tools,omitempty"`
	Rounds int      `json:"rounds,omitempty"`
	// Instruction is the Markdown below the front matter: the instruction of
	// the team to a block (stage 2), or the own prompt (stage 3).
	Instruction string `json:"instruction,omitempty"`
}

// Bounds of the loop of an own prompt.
const (
	DefaultRounds = 12
	MaxRounds     = 24
)

// TaskPrompt is the own prompt of a stage 3 definition: the prompt key, else
// the Markdown below the front matter.
func (d Def) TaskPrompt() string {
	if p := strings.TrimSpace(d.Prompt); p != "" {
		return p
	}
	return strings.TrimSpace(d.Instruction)
}

// RoundLimit is how many model calls the loop of the definition may make.
func (d Def) RoundLimit() int {
	if d.Rounds <= 0 {
		return DefaultRounds
	}
	return min(d.Rounds, MaxRounds)
}

// Stage is the stage of freedom the definition uses.
func (d Def) Stage() int {
	switch {
	case d.Uses == "" || d.Prompt != "":
		return StagePrompt
	case strings.TrimSpace(d.Instruction) != "":
		return StageInstruction
	}
	return StageBlock
}

// MaxOf is how many results of a kind the definition allows; 0 when it does
// not declare the kind.
func (d Def) MaxOf(kind string) int {
	for _, r := range d.Results {
		if r.Kind == kind {
			return r.Max
		}
	}
	return 0
}

// Problem is an error or a warning about a definition, with the line it is on
// (0: the file as a whole).
type Problem struct {
	Line int    `json:"line,omitempty"`
	Key  string `json:"key,omitempty"`
	Msg  string `json:"message"`
}

func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s", p.Line, p.Msg)
	}
	return p.Msg
}

// value is one value of the front matter as the parser read it.
type value struct {
	line   int
	scalar string
	list   []string
	pairs  []pair
	isList bool
	isMap  bool
	// inline: the value stands on the key's own line.
	inline bool
}

type pair struct {
	line       int
	key, value string
}

// MaxDefBytes bounds the text of a definition.
const MaxDefBytes = 64 << 10

// Parse reads a definition. It reports every problem it finds rather than the
// first; the Def holds what could be read.
func Parse(text string) (Def, []Problem) {
	var d Def
	var probs []Problem
	add := func(line int, key, format string, args ...any) {
		probs = append(probs, Problem{Line: line, Key: key, Msg: fmt.Sprintf(format, args...)})
	}
	if len(text) > MaxDefBytes {
		add(0, "", "the file is larger than %d KiB", MaxDefBytes>>10)
		return d, probs
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		add(1, "", "the file has to begin with a line ---, then the settings, then another line ---")
		return d, probs
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		add(start+1, "", "the settings are not closed by a line ---")
		return d, probs
	}
	d.Instruction = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	values := map[string]value{}
	var order []string
	var cur string
	for i := start + 1; i < end; i++ {
		n := i + 1
		raw := stripComment(lines[i])
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if strings.Contains(raw, "\t") {
			add(n, "", "use spaces, not tabs")
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		body := strings.TrimSpace(raw)
		if indent == 0 {
			key, rest, ok := strings.Cut(body, ":")
			key = strings.TrimSpace(key)
			if !ok || key == "" || strings.ContainsAny(key, " \"'[]{}") {
				add(n, "", "expected \"key: value\"")
				cur = ""
				continue
			}
			if _, dup := values[key]; dup {
				add(n, key, "%s is set twice", key)
				cur = ""
				continue
			}
			v := value{line: n}
			rest = strings.TrimSpace(rest)
			switch {
			case strings.HasPrefix(rest, "["):
				items, err := inlineList(rest)
				if err != nil {
					add(n, key, "%s: %v", key, err)
				}
				v.list, v.isList, v.inline = items, true, true
			case rest != "":
				v.inline = true
				s, err := unquote(rest)
				if err != nil {
					add(n, key, "%s: %v", key, err)
				}
				v.scalar = s
			}
			values[key] = v
			order = append(order, key)
			cur = key
			continue
		}
		if cur == "" {
			add(n, "", "indented line without a key above it")
			continue
		}
		v := values[cur]
		if v.inline {
			add(n, cur, "%s already has a value on line %d", cur, v.line)
			continue
		}
		if item, ok := strings.CutPrefix(body, "- "); ok || body == "-" {
			if v.isMap {
				add(n, cur, "%s mixes a list with key: value lines", cur)
				continue
			}
			s, err := unquote(strings.TrimSpace(item))
			if err != nil {
				add(n, cur, "%s: %v", cur, err)
			}
			v.list, v.isList = append(v.list, s), true
			values[cur] = v
			continue
		}
		k, val, ok := strings.Cut(body, ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			add(n, cur, "expected \"- item\" or \"key: value\" below %s", cur)
			continue
		}
		if v.isList {
			add(n, cur, "%s mixes a list with key: value lines", cur)
			continue
		}
		s, err := unquote(strings.TrimSpace(val))
		if err != nil {
			add(n, cur, "%s.%s: %v", cur, k, err)
		}
		v.pairs, v.isMap = append(v.pairs, pair{line: n, key: k, value: s}), true
		values[cur] = v
	}
	for _, key := range order {
		v := values[key]
		switch key {
		case KeyDescription:
			d.Description = scalarOf(v, key, add)
		case KeyOn:
			d.On = scalarOf(v, key, add)
		case KeyBranches:
			d.Branches = listOf(v, key, add)
		case KeyEvery:
			if s := scalarOf(v, key, add); s != "" {
				n, err := strconv.Atoi(s)
				if err != nil {
					add(v.line, key, "every: a number of minutes")
				}
				d.Every = n
			}
		case KeyChain:
			d.Chain = boolOf(v, key, add)
		case KeyInputs:
			d.Inputs = listOf(v, key, add)
		case KeyUses:
			d.Uses = scalarOf(v, key, add)
		case KeyResults:
			for _, p := range pairsOf(v, key, add) {
				n, err := strconv.Atoi(p.value)
				if err != nil || n < 1 {
					add(p.line, key, "results.%s: a number of at least 1", p.key)
					continue
				}
				if slices.ContainsFunc(d.Results, func(r ResultLimit) bool { return r.Kind == p.key }) {
					add(p.line, key, "results.%s is set twice", p.key)
					continue
				}
				d.Results = append(d.Results, ResultLimit{Kind: p.key, Max: n})
			}
		case KeyModel:
			d.Model = scalarOf(v, key, add)
		case KeyBudget:
			for _, p := range pairsOf(v, key, add) {
				f, err := strconv.ParseFloat(strings.Replace(p.value, ",", ".", 1), 64)
				if err != nil || f <= 0 {
					add(p.line, key, "budget.%s: an amount in US dollars above 0", p.key)
					continue
				}
				switch p.key {
				case BudgetRun:
					d.RunUSD = f
				case BudgetMonth:
					d.MonthUSD = f
				default:
					add(p.line, key, "unknown key budget.%s (known: %s, %s)", p.key, BudgetRun, BudgetMonth)
				}
			}
		case KeyResearch:
			d.Research = boolOf(v, key, add)
		case KeyPrompt:
			d.Prompt = scalarOf(v, key, add)
		case KeyTools:
			d.Tools = listOf(v, key, add)
		case KeyRounds:
			if s := scalarOf(v, key, add); s != "" {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 || n > MaxRounds {
					add(v.line, key, "rounds: a number from 1 to %d", MaxRounds)
				}
				d.Rounds = n
			}
		default:
			add(v.line, key, "unknown key %s (known: %s)", key, strings.Join(Keys, ", "))
		}
	}
	return d, probs
}

func scalarOf(v value, key string, add func(int, string, string, ...any)) string {
	if v.isList || v.isMap {
		add(v.line, key, "%s takes one value", key)
		return ""
	}
	return v.scalar
}

func listOf(v value, key string, add func(int, string, string, ...any)) []string {
	if v.isMap {
		add(v.line, key, "%s takes a list", key)
		return nil
	}
	if v.isList {
		return v.list
	}
	if v.scalar != "" {
		return []string{v.scalar}
	}
	return nil
}

func pairsOf(v value, key string, add func(int, string, string, ...any)) []pair {
	if !v.isMap {
		if v.scalar != "" || v.isList {
			add(v.line, key, "%s takes indented \"key: value\" lines", key)
		}
		return nil
	}
	return v.pairs
}

func boolOf(v value, key string, add func(int, string, string, ...any)) bool {
	switch strings.ToLower(scalarOf(v, key, add)) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off", "":
		return false
	}
	add(v.line, key, "%s: true or false", key)
	return false
}

// stripComment cuts a comment: a # at the start or after a space, outside quotes.
func stripComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && (i == 0 || line[i-1] == ' '):
			return strings.TrimRight(line[:i], " ")
		}
	}
	return strings.TrimRight(line, " ")
}

func unquote(s string) (string, error) {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1], nil
	}
	if strings.ContainsAny(s[:min(len(s), 1)], "\"'&*!|>{%@`") {
		return s, fmt.Errorf("a value starting with %q has to be in quotes", s[:1])
	}
	return s, nil
}

func inlineList(s string) ([]string, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("a list [a, b] has to end with ]")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		v, err := unquote(strings.TrimSpace(part))
		if err != nil {
			return out, err
		}
		if v == "" {
			return out, fmt.Errorf("empty item in the list")
		}
		out = append(out, v)
	}
	return out, nil
}
