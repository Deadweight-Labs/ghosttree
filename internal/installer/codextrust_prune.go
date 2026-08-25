package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// dropStaleCodexTrust entfernt die Freigabe-Einträge, die zu ghosttrees eigenen
// Hooks gehören — und nur diese.
//
// Der Grund ist eine Asymmetrie, die am 2026-08-25 einen falschen grünen Haken
// erzeugt hat: Codex bindet die Freigabe an einen Hash der Hook-Definition.
// Ändert der Installer den Hook — ein neuer Matcher, ein anderes Kommando —,
// ist die Freigabe erloschen, und Codex fragt beim nächsten Start prompt wieder
// danach ("1 hook is new or changed"). Der ALTE Eintrag bleibt dabei stehen,
// und ein Prüfer, der nur nach Anwesenheit sieht, meldet grün für einen Hook,
// der nicht mehr läuft. Genau die Lücke, gegen die codexTrustCheck angetreten
// ist.
//
// Wer den Hook ändert, entwertet also seine Freigabe selbst und schreibt das
// hin, statt es dem Prüfer zu überlassen. Den Hash nachzurechnen wäre der
// andere Weg und ist keiner: acht Kandidatenformate haben den bekannten Wert
// eines vorhandenen Eintrags nicht getroffen, und Codex' interne
// Serialisierung nachzubauen hiesse, sie mitpflegen zu müssen.
//
// Fremde Freigaben bleiben unangetastet. Another tool hält welche in derselben
// Datei; sie zu entfernen hiesse, fremdes Werkzeug still zu entwaffnen — und
// den Menschen für einen Hook um Freigabe zu bitten, an dem sich nichts
// geändert hat.
func dropStaleCodexTrust(cfgPath, hooksPath string) error {
	ours := ghosttreeTrustSections(hooksPath)
	if len(ours) == 0 {
		return nil
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil // keine Konfiguration, keine Freigabe
	}
	var out []string
	dropping := false
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "[") {
			dropping = ours[trimmed]
		}
		if dropping {
			continue
		}
		out = append(out, line)
	}
	updated := strings.Join(out, "\n")
	if updated == string(raw) {
		return nil
	}
	return writeAtomic(cfgPath, []byte(updated), 0o644)
}

// ghosttreeTrustSections nennt die Abschnittsüberschriften, unter denen Codex
// die Freigabe für unsere Hooks führt. Die Indizes kommen aus hooks.json, weil
// sie sich verschieben, sobald jemand anderes einen Hook vor unserem einträgt.
func ghosttreeTrustSections(hooksPath string) map[string]bool {
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		return nil
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	out := map[string]bool{}
	for event, groups := range file.Hooks {
		for gi, group := range groups {
			for hi, h := range group.Hooks {
				if !strings.HasPrefix(h.Command, "ctx hook ") {
					continue
				}
				out[fmt.Sprintf("[hooks.state.%q]", fmt.Sprintf("%s:%s:%d:%d",
					hooksPath, snakeEvent(event), gi, hi))] = true
			}
		}
	}
	return out
}
