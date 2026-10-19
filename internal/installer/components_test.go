package installer

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveComponentsDefaultsToHarnessSupport(t *testing.T) {
	got, err := ResolveComponents("codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ComponentSet{
		ComponentMCP: true, ComponentHooks: true,
		ComponentRules: true, ComponentSkills: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("components = %#v, want %#v", got, want)
	}
}

func TestResolveComponentsDeduplicatesSelection(t *testing.T) {
	got, err := ResolveComponents("claude", []string{"hooks", "mcp", "hooks"})
	if err != nil {
		t.Fatal(err)
	}
	want := ComponentSet{ComponentHooks: true, ComponentMCP: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("components = %#v, want %#v", got, want)
	}
}

func TestResolveComponentsRejectsUnsupported(t *testing.T) {
	_, err := ResolveComponents("opencode", []string{"hooks"})
	if err == nil || !strings.Contains(err.Error(), "mcp, rules") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveComponentsRejectsUnknown(t *testing.T) {
	_, err := ResolveComponents("codex", []string{"widgets"})
	if err == nil || !strings.Contains(err.Error(), "unknown component") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupLabelsNameEachItemInFewWords(t *testing.T) {
	home := t.TempDir()
	want := map[string][]string{
		"claude":   {"Claude hooks", "Claude MCP server", "Claude skills", "CLAUDE.md section"},
		"codex":    {"Codex hooks", "Codex MCP server", "Codex skills", "AGENTS.md section"},
		"opencode": {"OpenCode MCP server"},
	}
	for harness, labels := range want {
		got := SetupLabels(harness, home)
		if len(got) < len(labels) || strings.Join(got[:len(labels)], "|") != strings.Join(labels, "|") {
			t.Fatalf("%s: %v", harness, got)
		}
		for _, l := range got {
			if n := len(strings.Fields(l)); n < 2 || n > 4 {
				t.Fatalf("%s: %q is not two or three words", harness, l)
			}
		}
	}
}
