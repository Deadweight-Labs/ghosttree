package installer

import (
	"fmt"
	"strings"
)

type Component string

const (
	ComponentMCP    Component = "mcp"
	ComponentHooks  Component = "hooks"
	ComponentRules  Component = "rules"
	ComponentSkills Component = "skills"
)

type ComponentSet map[Component]bool

var componentOrder = []Component{
	ComponentMCP,
	ComponentHooks,
	ComponentRules,
	ComponentSkills,
}

func SupportedComponents(harness string) []Component {
	h := harnessNamed(harness)
	return append([]Component(nil), h.Components...)
}

func ResolveComponents(harness string, only []string) (ComponentSet, error) {
	supported := SupportedComponents(harness)
	if len(supported) == 0 {
		return nil, fmt.Errorf("unknown harness %q", harness)
	}
	allowed := make(ComponentSet, len(supported))
	for _, component := range supported {
		allowed[component] = true
	}
	if len(only) == 0 {
		return allowed, nil
	}
	selected := make(ComponentSet, len(only))
	for _, raw := range only {
		component := Component(raw)
		known := false
		for _, candidate := range componentOrder {
			if component == candidate {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown component %q", raw)
		}
		if !allowed[component] {
			return nil, fmt.Errorf("%s does not support %s; supported components: %s", harness, raw, componentNames(supported))
		}
		selected[component] = true
	}
	return selected, nil
}

func componentNames(components []Component) string {
	names := make([]string, len(components))
	for i, component := range components {
		names[i] = string(component)
	}
	return strings.Join(names, ", ")
}

func InstallSelected(harness, home string, selected ComponentSet) ([]Change, error) {
	switch harness {
	case "claude":
		return installClaudeSelected(home, selected)
	case "codex":
		return installCodexSelected(home, selected)
	case "opencode":
		return installOpencodeSelected(home, selected)
	default:
		return nil, fmt.Errorf("unknown harness %q", harness)
	}
}

func VerifySelected(harness, home string, selected ComponentSet) []Check {
	h := harnessNamed(harness)
	var checks []Check
	switch harness {
	case "claude":
		checks = VerifyClaude(home)
	case "codex":
		checks = VerifyCodex(home)
	case "opencode":
		checks = VerifyOpencode(home)
	}
	filtered := checks[:0]
	for _, check := range checks {
		component := checkComponent(check)
		if selected[component] {
			filtered = append(filtered, check)
		}
	}
	if selected[ComponentHooks] && h.HooksPath != nil {
		filtered = append(filtered, hookRuntimeChecks(h, home)...)
	}
	if selected[ComponentMCP] {
		configOK := false
		for _, check := range filtered {
			if checkComponent(check) == ComponentMCP && strings.Contains(check.Name, "registration") {
				configOK = check.OK
				break
			}
		}
		filtered = append(filtered, mcpRuntimeCheck(harness, configOK))
		if harness == "codex" {
			filtered = append(filtered, codexEffectiveMCPCheck(home, configOK))
		}
	}
	return filtered
}

func checkComponent(check Check) Component {
	if check.Component != "" {
		return check.Component
	}
	name := check.Name
	switch {
	case strings.Contains(name, "mcp"):
		return ComponentMCP
	case strings.Contains(name, "hook") || strings.Contains(name, "trust"):
		return ComponentHooks
	case strings.Contains(name, "rule section"):
		return ComponentRules
	case strings.Contains(name, "skills"):
		return ComponentSkills
	default:
		return ""
	}
}
