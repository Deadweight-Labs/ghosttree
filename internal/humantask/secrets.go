package humantask

import (
	"fmt"
	"regexp"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

type secretPattern struct {
	name string
	re   *regexp.Regexp
}

var secretPatterns = []secretPattern{
	{"api key prefix", regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|sk-ant-[A-Za-z0-9_-]{8,}|gh[pos]_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|glpat-[A-Za-z0-9_-]{8,}|xox[abprs]-[A-Za-z0-9-]{8,}|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|SG\.[A-Za-z0-9_-]{8,}|[psr]k_live_[A-Za-z0-9]{8,})`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"bearer token", regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{"url with credentials", regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)},
	{"credential assignment", regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|authorization)\s*[:=]\s*['"]?\S{8,}`)},
}

var (
	entropyToken = regexp.MustCompile(`^[A-Za-z0-9_-]{32,}$`)
	hexOnly      = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	uuidRe       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hasLetter    = regexp.MustCompile(`[A-Za-z]`)
	hasDigit     = regexp.MustCompile(`[0-9]`)
)

// CheckText rejects text that looks like it carries a secret. It is a
// heuristic, not a guarantee: the duty stays with the writer. Neither the value
// nor the matching pattern text is returned.
func CheckText(field, text string) *requestdomain.RuleError {
	if kind := detect(text); kind != "" {
		return &requestdomain.RuleError{
			ErrorCode:  "secret_detected",
			Message:    fmt.Sprintf("%s looks like it contains a secret (%s)", field, kind),
			Resolution: "name only where it lives (path, vault entry, env file), never the value",
		}
	}
	return nil
}

func detect(text string) string {
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			return p.name
		}
	}
	for _, tok := range strings.Fields(text) {
		tok = strings.Trim(tok, `.,;:()[]{}<>"'`+"`")
		if !entropyToken.MatchString(tok) || !hasLetter.MatchString(tok) || !hasDigit.MatchString(tok) {
			continue
		}
		if uuidRe.MatchString(tok) || (hexOnly.MatchString(tok) && (len(tok) == 40 || len(tok) == 64)) {
			continue
		}
		return "long random token"
	}
	return ""
}

// CheckFields checks several fields in order and returns the first hit.
func CheckFields(fields ...string) *requestdomain.RuleError {
	for i := 0; i+1 < len(fields); i += 2 {
		if err := CheckText(fields[i], fields[i+1]); err != nil {
			return err
		}
	}
	return nil
}
