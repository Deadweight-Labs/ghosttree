package actor

import (
	"encoding/json"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
)

type callLine struct {
	Type    string `json:"type"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Payload *struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
	} `json:"payload"`
}

type toolUse struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// CallsTool says whether a raw transcript line is a call of one of the tools
// or a shell call of one of the CLI subcommands ("pain add"): in Claude Code
// an assistant line with a tool_use block (the tool by name, or Bash whose
// command runs ctx or ghosttree with the subcommand, also by path, after cd
// … && or variable assignments), in Codex a function_call or
// custom_tool_call. A tool counts by
// its last name part (mcp__ghosttree__pain_record is pain_record). Results,
// prompts and text that only mention the name do not count.
func CallsTool(raw string, tools, commands []string) bool {
	assistant := strings.Contains(raw, `"type":"assistant"`)
	codex := strings.Contains(raw, `"type":"response_item"`)
	if !assistant && !codex {
		return false
	}
	var l callLine
	if json.Unmarshal([]byte(raw), &l) != nil {
		return false
	}
	switch {
	case l.Type == "assistant" && l.Message != nil:
		var blocks []toolUse
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return false
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && callMatches(b.Name, string(b.Input), tools, commands) {
				return true
			}
		}
	case l.Type == "response_item" && l.Payload != nil:
		p := l.Payload
		if p.Type != "function_call" && p.Type != "custom_tool_call" {
			return false
		}
		args := p.Arguments
		if args == "" {
			args = string(p.Input)
		}
		return callMatches(p.Name, args, tools, commands)
	}
	return false
}

// IsToolResult says whether a raw line is the answer of a tool: a Claude
// tool_result block or a Codex function_call_output.
func IsToolResult(raw string) bool {
	return strings.Contains(raw, `"tool_result"`) || strings.Contains(raw, `"function_call_output"`) || strings.Contains(raw, `"custom_tool_call_output"`)
}

func callMatches(name, input string, tools, commands []string) bool {
	for _, t := range tools {
		if name == t || strings.HasSuffix(name, "__"+t) || strings.HasSuffix(name, "."+t) || strings.HasSuffix(name, "/"+t) {
			return true
		}
	}
	// A core tool names the older tool through its action: pain with action
	// record is pain_record.
	if core, action, ok := coretools.Classify(name, inputAction(input)); ok {
		if route, found := coretools.Route(core, action); found {
			for _, t := range tools {
				if route.Tool == t {
					return true
				}
			}
		}
	}
	if len(commands) == 0 || input == "" {
		return false
	}
	for _, cmd := range shellCommands(input) {
		for _, seg := range shellSegments(cmd) {
			for _, c := range commands {
				if invokesCLI(seg, c) {
					return true
				}
			}
		}
	}
	return false
}

// cliNames are the names of the ghosttree binary: ctx and ghosttree.
var cliNames = map[string]bool{"ctx": true, "ghosttree": true}

// shellSegments splits a shell line into the commands it runs one after the
// other (&&, ||, ;, |, line breaks): "cd repo && ctx pain add x" has two.
func shellSegments(line string) []string {
	f := func(r rune) bool { return r == ';' || r == '|' || r == '&' || r == '\n' }
	return strings.FieldsFunc(line, f)
}

// invokesCLI says whether one shell command runs the CLI with this subcommand
// ("pain add"): the binary by name or path (ctx, ghosttree, /usr/local/bin/ctx),
// optionally after variable assignments (FOO=1 ctx pain add).
func invokesCLI(seg, sub string) bool {
	words := strings.Fields(seg)
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.ContainsAny(words[0][:strings.Index(words[0], "=")], "/ ") {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	bin := words[0]
	if i := strings.LastIndex(bin, "/"); i >= 0 {
		bin = bin[i+1:]
	}
	if !cliNames[strings.Trim(bin, `"'`)] {
		return false
	}
	want := strings.Fields(sub)
	if len(words)-1 < len(want) {
		return false
	}
	for i, w := range want {
		if words[1+i] != w {
			return false
		}
	}
	return true
}

// shellCommands reads the command of a shell call: Claude Bash
// {"command":"..."}, Codex shell {"command":["bash","-lc","..."]} or
// exec_command {"cmd":"..."}. Input that is not JSON is the command itself.
func shellCommands(input string) []string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &m) != nil {
		return []string{strings.TrimSpace(input)}
	}
	var out []string
	for _, key := range []string{"command", "cmd"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, strings.TrimSpace(s))
			continue
		}
		var parts []string
		if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
			out = append(out, strings.TrimSpace(parts[len(parts)-1]))
		}
	}
	return out
}

func inputAction(input string) string {
	var in struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(input), &in)
	return in.Action
}
