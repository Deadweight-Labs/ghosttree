package web

import (
	"regexp"
	"strings"
)

// Die Regeln eines Ablaufs (internal/runbook) melden sich auf Englisch und ohne zu
// sagen, welches Feld gemeint ist. Das Formular übersetzt die, die ein Mensch beim
// Tippen trifft, und nennt das Feld; was hier fehlt, erscheint mit seinem Rohtext
// hinter „Der Ablauf lässt sich nicht speichern:“.

type runbookRule struct {
	re    *regexp.Regexp
	key   string
	field string
}

var runbookRules = []runbookRule{
	{regexp.MustCompile(`^runbook name `), "runbooks.form.err.name", "name"},
	{regexp.MustCompile(`^rollback \d+ title is required`), "runbooks.form.err.back_title", "back_title"},
	{regexp.MustCompile(`^step \d+ title is required`), "runbooks.form.err.step_title", "step_title"},
	{regexp.MustCompile(`^(?:rollback|step) \d+ title is longer`), "runbooks.form.err.long_title", ""},
	{regexp.MustCompile(`^parameter name `), "runbooks.form.err.param_name", "param_name"},
	{regexp.MustCompile(`^parameter \S+ is declared twice`), "runbooks.form.err.param_twice", "param_name"},
	{regexp.MustCompile(`^parameter \S+: an enum needs`), "runbooks.form.err.enum_values", "param_values"},
	{regexp.MustCompile(`^default: `), "runbooks.form.err.default", "param_default"},
	{regexp.MustCompile(`uses \{\{.*\}\}, which is not a declared parameter`), "runbooks.form.err.undeclared", ""},
	{regexp.MustCompile(`^the pitfalls are numbers`), "runbooks.form.err.pitfalls", "pitfalls"},
	{regexp.MustCompile(`^keyword `), "runbooks.form.err.keyword", "keywords"},
}

var placeholderRE = regexp.MustCompile(`\{\{([a-z0-9_]*)\}\}`)

// runbookFailText says what a refusal of the form means and which field it is about.
func runbookFailText(l lang, key, detail string) (text, field string) {
	if key != "runbooks.form.err.invalid" || detail == "" {
		return l.msg(key), ""
	}
	for _, rule := range runbookRules {
		if rule.re.MatchString(detail) {
			arg := ""
			if m := placeholderRE.FindStringSubmatch(detail); m != nil && rule.key == "runbooks.form.err.undeclared" {
				arg = m[1]
			}
			return l.msg(rule.key, arg), rule.field
		}
	}
	return strings.TrimSpace(l.msg(key) + " " + detail), ""
}
