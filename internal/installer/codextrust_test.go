package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Der teuerste blinde Fleck des Doctors, 482 Sitzungen lang: der Hook stand in
// der Datei, der Check fand ihn, und er lief trotzdem nie. Codex verlangt für
// jeden nicht verwalteten Command-Hook eine einmalige Freigabe über /hooks,
// gemerkt als trusted_hash in config.toml unter dem Schlüssel
// <hooks-datei>:<ereignis>:<gruppe>:<handler>. Diese Freigabe liegt AUSSERHALB
// der Datei, die der Installer schreibt — deshalb sah alles richtig aus.
func TestDoctorReportsACodexHookThatWasNeverTrusted(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	checks := VerifyCodex(home)
	trust := findCheck(checks, "trust")
	if trust == nil {
		t.Fatal("the doctor says nothing about the trust confirmation a codex hook needs")
	}
	if trust.OK {
		t.Errorf("an untrusted hook must be a named gap, not a green tick: %+v", trust)
	}
	if !strings.Contains(strings.ToLower(trust.Fix), "/hooks") {
		t.Errorf("the fix must name the way to grant it: %q", trust.Fix)
	}

	// Freigabe eintragen, wie Codex es nach /hooks täte: unsere Hooks stehen in
	// Gruppe 0, weil dieses Zuhause sonst keine hat.
	cfg := filepath.Join(home, ".codex", "config.toml")
	body, _ := os.ReadFile(cfg)
	hooks := filepath.Join(home, ".codex", "hooks.json")
	extra := "\n[hooks.state.\"" + hooks + ":session_start:0:0\"]\ntrusted_hash = \"sha256:aaa\"\n" +
		"[hooks.state.\"" + hooks + ":user_prompt_submit:0:0\"]\ntrusted_hash = \"sha256:bbb\"\n" +
		"[hooks.state.\"" + hooks + ":pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:ccc\"\n"
	os.WriteFile(cfg, append(body, []byte(extra)...), 0o644)

	if trust := findCheck(VerifyCodex(home), "trust"); trust == nil || !trust.OK {
		t.Errorf("after the confirmation the check must go quiet: %+v", trust)
	}
}

// Der Fehler im Prüfer selbst, gefunden am 2026-08-25 beim Ausrollen des
// geänderten PreToolUse-Matchers: Codex bindet die Freigabe an einen Hash der
// Hook-Definition. Ändert der Installer den Hook, ist die Freigabe erloschen —
// Codex fragte prompt wieder "1 hook is new or changed" —, aber der alte
// trusted_hash-Eintrag bleibt in der Datei stehen. Ein Prüfer, der nur nach
// Anwesenheit sieht, meldet dann grün für einen Hook, der nicht läuft. Genau
// die Lücke, gegen die dieser Check angetreten ist.
// setMatcherOnPreToolUseHook setzt den Matcher der Gruppe, in der unser
// PreToolUse-Hook steht, und meldet, ob sie gefunden wurde.
func setMatcherOnPreToolUseHook(settings map[string]any, matcher string) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PreToolUse"].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if hm["command"] == preToolHookCommand+" --harness codex" {
				group["matcher"] = matcher
				return true
			}
		}
	}
	return false
}

func TestChangingAHookDropsItsStaleTrustEntry(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(home, ".codex", "hooks.json")
	cfg := filepath.Join(home, ".codex", "config.toml")
	body, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(body, []byte(
		"\n[hooks.state.\""+hooks+":pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:alt\"\n")...), 0o644)
	if trust := findCheck(VerifyCodex(home), "trust"); trust == nil || trust.OK {
		t.Fatalf("precondition: the check should be quiet once trusted: %+v", trust)
	}

	// Den realen Fall nachstellen: unser Hook steht mit einem Matcher in der
	// Datei, und der Installer räumt ihn weg. Genau das ist am 2026-08-25
	// zweimal passiert — erst von "Read|Edit|Write" auf "exec", dann auf gar
	// keinen, weil jeder Matcher Codex den Hook überspringen liess (#1449).
	settings, err := readJSONFile(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if !setMatcherOnPreToolUseHook(settings, "Read|Edit|Write|NotebookEdit") {
		t.Fatalf("precondition: no codex pre-tool-use hook in hooks.json: %#v", settings)
	}
	if err := writeJSONFile(hooks, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}

	after, _ := os.ReadFile(cfg)
	if strings.Contains(string(after), "sha256:alt") {
		t.Errorf("the stale confirmation survived the change and now vouches for a hook that no longer runs:\n%s", after)
	}
	if trust := findCheck(VerifyCodex(home), "trust"); trust == nil || trust.OK {
		t.Errorf("after changing the hook the check must ask for a new confirmation: %+v", trust)
	}
}

func TestCodexTrustRequiresNonEmptyHashAndEnabledState(t *testing.T) {
	for _, body := range []string{
		`trusted_hash = ""`,
		"trusted_hash = \"sha256:abc\"\nenabled = false",
		`trusted_hash = 123`,
		"trusted_hash = \"sha256:abc\"\nenabled = \"false\"",
		"trusted_hash = \"sha256:abc\"\nenabled = false\nenabled = true",
		"trusted_hash = \"sha256:abc\"\nthis is not toml",
	} {
		home := t.TempDir()
		if _, err := InstallCodex(home); err != nil {
			t.Fatal(err)
		}
		hooks := filepath.Join(home, ".codex", "hooks.json")
		cfg := filepath.Join(home, ".codex", "config.toml")
		raw, _ := os.ReadFile(cfg)
		for header := range ghosttreeTrustSections(hooks) {
			raw = append(raw, []byte("\n"+header+"\n"+body+"\n")...)
		}
		if err := os.WriteFile(cfg, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if trust := codexTrustCheck(home); trust.OK {
			t.Fatalf("invalid trust state passed for %q: %+v", body, trust)
		}
	}
}

// Fremde Hooks vor unserem verschieben den Gruppenindex — auf workstation-a steht
// session-lease in Gruppe 0 und ghosttree in Gruppe 1. Wer den Index festverdrahtet,
// prüft die Freigabe eines fremden Hooks.
func TestTheTrustCheckFindsOurOwnGroupNotTheFirstOne(t *testing.T) {
	home := t.TempDir()
	hooks := filepath.Join(home, ".codex", "hooks.json")
	os.MkdirAll(filepath.Dir(hooks), 0o755)
	os.WriteFile(hooks, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bash /fremd/lease.sh"}]}]}}`), 0o644)
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".codex", "config.toml")
	body, _ := os.ReadFile(cfg)
	// Nur der FREMDE Hook ist freigegeben.
	os.WriteFile(cfg, append(body, []byte("\n[hooks.state.\""+hooks+":session_start:0:0\"]\ntrusted_hash = \"sha256:fremd\"\n")...), 0o644)

	trust := findCheck(VerifyCodex(home), "trust")
	if trust == nil || trust.OK {
		t.Errorf("a foreign hook's confirmation must not count as ours: %+v", trust)
	}
}

func findCheck(checks []Check, needle string) *Check {
	for i := range checks {
		if strings.Contains(strings.ToLower(checks[i].Name), needle) {
			return &checks[i]
		}
	}
	return nil
}
