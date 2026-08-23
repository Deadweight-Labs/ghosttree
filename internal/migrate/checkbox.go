package migrate

import (
	"regexp"
	"strings"
)

type Step struct {
	Text string
	Done bool
	Line int
}

var checkboxRE = regexp.MustCompile(`^\s*-\s*\[([ xX])\]\s*(.+)$`)

func ParseSteps(markdown string) []Step {
	var out []Step
	for i, line := range strings.Split(markdown, "\n") {
		m := checkboxRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Step{Text: strings.ReplaceAll(m[2], "**", ""), Done: m[1] == "x" || m[1] == "X", Line: i + 1})
	}
	return out
}
