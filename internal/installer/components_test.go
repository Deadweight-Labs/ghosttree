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
