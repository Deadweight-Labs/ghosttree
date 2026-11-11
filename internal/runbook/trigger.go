package runbook

import (
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

// validatePaths nutzt die Regeln der Instruktions-Aktivierung: dieselbe
// Pfadsyntax, dieselbe Prüfung, kein zweiter Mechanismus.
func validatePaths(paths []string) error {
	return activation.ValidateRule(activation.Rule{Paths: paths})
}

// Input ist, woran ein Auslöser geprüft wird. Jede Quelle ist optional: der
// Prompt-Hook füllt Prompt, der Werkzeug-Hook Command oder Paths.
type Input struct {
	Prompt   string
	Command  string
	RepoPath string
	Paths    []string
}

// Match sagt, ob ein Auslöser des Ablaufs auf die Eingabe passt, und nennt den
// Grund (das Stichwort, das Muster, den Pfad), damit der Leser sieht, warum der
// Ablauf vor ihm liegt.
func (s Spec) Match(in Input) (string, bool) {
	if in.Prompt != "" {
		low := strings.ToLower(in.Prompt)
		for _, k := range s.Triggers.Keywords {
			if containsWord(low, k) {
				return "keyword " + k, true
			}
		}
	}
	if in.Command != "" {
		for _, seg := range commandSegments(in.Command) {
			for _, p := range s.Triggers.Commands {
				if commandMatches(p, seg) {
					return "command " + p, true
				}
			}
		}
	}
	if len(s.Triggers.Paths) > 0 {
		ctx := activation.Context{RepoPath: in.RepoPath, Paths: in.Paths}
		if norm, err := activation.NormalizeContext(ctx); err == nil &&
			activation.Matches(activation.Rule{Paths: s.Triggers.Paths}, norm) {
			return "path", true
		}
	}
	return "", false
}

// HasTriggers sagt, ob der Ablauf von selbst ausgeliefert werden kann.
func (s Spec) HasTriggers() bool {
	return len(s.Triggers.Keywords)+len(s.Triggers.Commands)+len(s.Triggers.Paths) > 0
}

// containsWord sucht k in text mit Wortgrenze links: "deploy" trifft "deployen",
// "redeploy" nicht. Rechts darf es weitergehen, damit Beugungen treffen.
func containsWord(text, k string) bool {
	from := 0
	for {
		i := strings.Index(text[from:], k)
		if i < 0 {
			return false
		}
		i += from
		if i == 0 {
			return true
		}
		prev := []rune(text[:i])
		if r := prev[len(prev)-1]; !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
		from = i + len(k)
		if from >= len(text) {
			return false
		}
	}
}

// commandSegments zerlegt eine Befehlszeile grob an Zeilenumbruch, ;, &&, || und
// |, entfernt vorangestellte Zuweisungen (VAR=x) und "sudo". Ohne Rücksicht auf
// Anführungszeichen: für einen Auslöser genügt es, in beide Richtungen zu irren,
// ein Treffer zu viel kostet eine Zeile Kontext, einer zu wenig den Hinweis.
func commandSegments(cmd string) []string {
	cmd = strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "&", "\n").Replace(cmd)
	var out []string
	for _, line := range strings.Split(cmd, "\n") {
		fields := strings.Fields(line)
		for len(fields) > 0 && (strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "=") || fields[0] == "sudo") {
			fields = fields[1:]
		}
		if len(fields) > 0 {
			out = append(out, strings.Join(fields, " "))
		}
	}
	return out
}

// commandMatches: ohne Stern passt das Muster auf den Anfang des Befehls, mit
// Wortgrenze rechts ("ssh prod" trifft "ssh prod.example.com", nicht "ssh
// prodx"); mit Stern ist es ein Glob über den ganzen Befehl.
func commandMatches(pattern, seg string) bool {
	if strings.Contains(pattern, "*") {
		return glob(pattern, seg)
	}
	if !strings.HasPrefix(seg, pattern) {
		return false
	}
	rest := seg[len(pattern):]
	if rest == "" {
		return true
	}
	r := []rune(rest)[0]
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
}

// glob kennt nur '*' (beliebig viele Zeichen, auch '/').
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}
