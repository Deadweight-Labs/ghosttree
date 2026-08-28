package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// codexTrustCheck beantwortet die Frage, die den Doctor 482 Sitzungen lang
// entgangen ist: läuft der Hook, den wir eingetragen haben, überhaupt?
//
// Codex verlangt für jeden nicht verwalteten Command-Hook eine einmalige
// Freigabe über `/hooks` und merkt sie als trusted_hash in config.toml, unter
// dem Schlüssel <hooks-datei>:<ereignis_snake_case>:<gruppenindex>:<handlerindex>.
// Ein nicht freigegebener Hook wird übersprungen — und von aussen sieht
// "übersprungen" genauso aus wie "gelaufen und hat nichts geliefert". Der
// Installer schrieb den Eintrag, der Doctor fand ihn, beides war wahr, und
// nichts passierte. Die Freigabe liegt eben nicht in der Datei, die der
// Installer schreibt.
//
// Geprüft wird nur die ANWESENHEIT eines Eintrags, nicht der Hash. Der Hash
// bindet an die Definition, und die zu berechnen hiesse Codex' interne
// Serialisierung nachzubauen; sie ändert sich, wenn sie sich ändert. Die
// Abwesenheit ist der Fall, der schadet.
func codexTrustCheck(home string) Check {
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	c := Check{
		Name:   "codex hook trust confirmation",
		Detail: cfgPath,
		Fix:    "start codex, run /hooks and trust the ghosttree entries — an untrusted hook is skipped, and nothing else shows it",
	}
	missing := untrustedCodexHooks(hooksPath, cfgPath)
	switch {
	case missing == nil:
		c.OK = true
	default:
		c.Detail = fmt.Sprintf("%s (never trusted: %s)", cfgPath, strings.Join(missing, ", "))
	}
	return c
}

// untrustedCodexHooks nennt die Ereignisse, deren ghosttree-Hook keine Freigabe
// hat. Der Gruppenindex wird aus hooks.json ermittelt statt angenommen: hängt
// der Installer hinter fremde Hooks an, verschiebt er sich, und wer 0 fest
// verdrahtet, prüft die Freigabe eines fremden Eintrags.
func untrustedCodexHooks(hooksPath, cfgPath string) []string {
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		return nil // kein Hook eingetragen: das meldet bereits der Kanalcheck
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		cfg = nil
	}
	var missing []string
	for event, groups := range file.Hooks {
		for gi, group := range groups {
			for hi, h := range group.Hooks {
				if !strings.HasPrefix(h.Command, "ctx hook ") {
					continue
				}
				header := fmt.Sprintf("[hooks.state.%q]", fmt.Sprintf("%s:%s:%d:%d", hooksPath, snakeEvent(event), gi, hi))
				if !validCodexTrustSection(string(cfg), header) {
					missing = append(missing, event)
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

func validCodexTrustSection(content, header string) bool {
	bodies := tomlSectionBodies(content, header)
	if len(bodies) != 1 {
		return false
	}
	values := map[string]string{}
	for _, line := range strings.Split(bodies[0], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return false
		}
		key := strings.TrimSpace(parts[0])
		if key != "trusted_hash" && key != "enabled" {
			return false
		}
		if _, duplicate := values[key]; duplicate {
			return false
		}
		values[key] = strings.TrimSpace(strings.SplitN(parts[1], "#", 2)[0])
	}
	hash, ok := quotedTOMLString(values["trusted_hash"])
	if !ok || hash == "" {
		return false
	}
	enabled := values["enabled"]
	return enabled == "" || enabled == "true"
}

func quotedTOMLString(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], true
	}
	if raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	value, err := strconv.Unquote(raw)
	return value, err == nil
}

func tomlSectionBodies(content, header string) []string {
	lines := strings.SplitAfter(content, "\n")
	var bodies []string
	var current strings.Builder
	inSection := false
	flush := func() {
		if inSection {
			bodies = append(bodies, current.String())
			current.Reset()
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isTable := strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
		if isTable {
			flush()
			inSection = trimmed == header
			continue
		}
		if inSection {
			current.WriteString(line)
		}
	}
	flush()
	return bodies
}

// snakeEvent übersetzt SessionStart in session_start — die Schreibweise, in der
// Codex den Zustandsschlüssel führt.
func snakeEvent(event string) string {
	var b strings.Builder
	for i, r := range event {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
