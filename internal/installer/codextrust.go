package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
				key := fmt.Sprintf("%s:%s:%d:%d", hooksPath, snakeEvent(event), gi, hi)
				if !strings.Contains(string(cfg), key) {
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
