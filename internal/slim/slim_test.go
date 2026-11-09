package slim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Die Zeilen hier sind selbst gebaut und nachgeformt: Schlüssel, Verschachtelung
// und Reihenfolge wie bei Claude Code 2.1.30x und Codex, Inhalte erfunden.

func long(prefix string, n int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; b.Len() < n; i++ {
		b.WriteString(" line ")
		b.WriteString(strings.Repeat("x", i%40))
		b.WriteString("\n")
	}
	return b.String()
}

func q(s string) string { return string(encodeString(s)) }

const (
	claudeAssistant     = `{"parentUuid":"p-1","isSidechain":false,"userType":"external","cwd":"/work/repo","sessionId":"s-1","version":"2.1.305","gitBranch":"main","message":{"model":"claude-opus-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"Check the <file> first & then edit.","signature":"c2lnbmF0dXJlLWJ5dGVz"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la","description":"List files"}}],"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":120,"cache_read_input_tokens":4000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":120},"output_tokens":42,"service_tier":"standard","server_tool_use":{"web_search_requests":0}},"diagnostics":null,"context_management":null,"container":null,"input_transformations":[{"type":"cache_edit","blocks":[1,2,3]}]},"requestId":"req_1","requestRef":"rr_1","wireToolInputs":{"toolu_1":{"command":"ls -la","description":"List files"}},"type":"assistant","uuid":"a-1","timestamp":"2026-11-01T10:00:01.000Z"}`
	claudeAssistantSlim = `{"parentUuid":"p-1","isSidechain":false,"userType":"external","cwd":"/work/repo","sessionId":"s-1","version":"2.1.305","gitBranch":"main","message":{"model":"claude-opus-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"Check the <file> first & then edit."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la","description":"List files"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"cache_creation_input_tokens":120,"cache_read_input_tokens":4000,"output_tokens":42}},"requestId":"req_1","type":"assistant","uuid":"a-1","timestamp":"2026-11-01T10:00:01.000Z"}`
)

func claudeToolResult(content, toolUseResult string) string {
	return `{"parentUuid":"a-1","isSidechain":false,"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":` + content + `,"is_error":false}]},"uuid":"u-2","timestamp":"2026-11-01T10:00:02.000Z","toolUseResult":` + toolUseResult + `,"sourceToolAssistantUUID":"a-1"}`
}

func TestLineClaudeAssistantKeepsContentAndOrder(t *testing.T) {
	got, keep := Line(claudeAssistant)
	if !keep {
		t.Fatal("assistant line dropped")
	}
	if got != claudeAssistantSlim {
		t.Errorf("slimmed line\n got %s\nwant %s", got, claudeAssistantSlim)
	}
}

func TestLineDropsCopiesAndBallast(t *testing.T) {
	for name, line := range map[string]string{
		"api-request-blob":  `{"type":"api-request-blob","hash":"h1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"copy"}]},"sessionId":"s-1","timestamp":"2026-11-01T10:00:03.000Z"}`,
		"api-request":       `{"type":"api-request","requestDigest":"d1","params":{"model":"claude-opus-5-5","max_tokens":32000},"cacheControl":{"type":"ephemeral"},"base":"b","tail":["h1"],"sessionId":"s-1","timestamp":"2026-11-01T10:00:03.000Z"}`,
		"api-request-shape": `{"type":"api-request-shape","shapeHash":"sh1","shape":{"system":[{"type":"text","text":"You are ..."}],"tools":[{"name":"Bash","input_schema":{}}]},"timestamp":"2026-11-01T10:00:03.000Z"}`,
		"prompt_snapshot":   `{"parentUuid":"a-1","type":"attachment","attachment":{"type":"prompt_snapshot","prompt":{"system":"You are ...","tools":[]}},"uuid":"t-1","timestamp":"2026-11-01T10:00:03.000Z"}`,
		"hook_success":      `{"parentUuid":"a-1","type":"attachment","attachment":{"type":"hook_success","hookName":"SessionStart:startup","hookEvent":"SessionStart","content":"{\"hookSpecificOutput\":{}}","stdout":"{}","exitCode":0},"uuid":"t-2","timestamp":"2026-11-01T10:00:03.000Z"}`,
		"codex user copy":   `{"timestamp":"2026-11-01T10:00:03.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"UserMessage","id":"i1","content":[{"type":"text","text":"fix the bug"}]}}}`,
		"codex agent copy":  `{"timestamp":"2026-11-01T10:00:03.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"AgentMessage","id":"i2","content":[{"type":"Text","text":"done"}],"phase":"final_answer"}}}`,
		"codex reasoning":   `{"timestamp":"2026-11-01T10:00:03.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"Reasoning","id":"i3","summary_text":[],"raw_content":[]}}}`,
	} {
		if got, keep := Line(line); keep {
			t.Errorf("%s kept: %s", name, got)
		}
	}
}

func TestLineClaudeFieldStrips(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"queue-operation content",
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-01T10:00:00.000Z","sessionId":"s-1","content":"also check the tests"}`,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-01T10:00:00.000Z","sessionId":"s-1"}`},
		{"attachment rendered",
			`{"parentUuid":"a-1","type":"attachment","attachment":{"type":"hook_additional_context","content":["known context"],"hookName":"SessionStart"},"rendered":"<system-reminder>known context</system-reminder>","renderedInHumanTurn":false,"uuid":"t-3","timestamp":"2026-11-01T10:00:00.000Z"}`,
			`{"parentUuid":"a-1","type":"attachment","attachment":{"type":"hook_additional_context","content":["known context"],"hookName":"SessionStart"},"renderedInHumanTurn":false,"uuid":"t-3","timestamp":"2026-11-01T10:00:00.000Z"}`},
		{"toolUseResult allow list",
			claudeToolResult(`"total 8\ndrwxr-xr-x a\n"`, `{"stdout":"total 8\ndrwxr-xr-x a\n","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}`),
			claudeToolResult(`"total 8\ndrwxr-xr-x a\n"`, `{"interrupted":false,"isImage":false,"noOutputExpected":false}`)},
		{"toolUseResult command launch",
			claudeToolResult(`"Launching skill: review"`, `{"success":true,"commandName":"review","allowedTools":["Read"]}`),
			claudeToolResult(`"Launching skill: review"`, `{"success":true,"commandName":"review"}`)},
		{"toolUseResult edit copies",
			claudeToolResult(`"The file was updated"`, `{"filePath":"/work/repo/a.go","oldString":"a","newString":"b","originalFile":"package a\n","structuredPatch":[{"oldStart":1,"lines":["-a","+b"]}],"userModified":false,"replaceAll":false}`),
			claudeToolResult(`"The file was updated"`, `{"filePath":"/work/repo/a.go","userModified":false,"replaceAll":false}`)},
		{"toolUseResult long scalar",
			claudeToolResult(`"ok"`, `{"type":"text","filePath":`+q("/"+strings.Repeat("d/", 200))+`}`),
			claudeToolResult(`"ok"`, `{"type":"text"}`)},
	}
	for _, c := range cases {
		got, keep := Line(c.in)
		if !keep || got != c.want {
			t.Errorf("%s:\n got %v %s\nwant %s", c.name, keep, got, c.want)
		}
	}
	// A bare string toolUseResult (an error text) goes; the line stays.
	in := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Error: no such file","is_error":true}]},"uuid":"u-3","timestamp":"2026-11-01T10:00:02.000Z","toolUseResult":"Error: no such file"}`
	got, _ := Line(in)
	if strings.Contains(got, "toolUseResult") || !strings.Contains(got, `"is_error":true`) {
		t.Errorf("string toolUseResult: %s", got)
	}
}

func TestLineCapsLongToolOutputs(t *testing.T) {
	out := long("BEGIN", 40_000) + "END"
	in := claudeToolResult(q(out), `{"stdout":`+q(out)+`,"stderr":"","interrupted":false,"isImage":false}`)
	got, keep := Line(in)
	if !keep {
		t.Fatal("dropped")
	}
	if len(got) > CapOver+1000 {
		t.Errorf("slimmed line is %d bytes", len(got))
	}
	sum := sha256.Sum256([]byte(out))
	marker := Marker(len(out), hex.EncodeToString(sum[:8]))
	if !strings.Contains(got, q(marker)[1:len(q(marker))-1]) {
		t.Errorf("marker %q missing in %s", marker, got[:200])
	}
	p := transcript.Parse("claude-code", 1, got)
	if len(p.Blocks) != 1 || !strings.HasPrefix(p.Blocks[0].Output, "BEGIN") || !strings.HasSuffix(p.Blocks[0].Output, "END") {
		t.Fatalf("parsed result %+v", p.Blocks)
	}
	// A list of text parts is capped part by part.
	list := `[{"type":"text","text":` + q(out) + `},{"type":"text","text":"short"}]`
	got, _ = Line(claudeToolResult(list, `{"isImage":false}`))
	if !strings.Contains(got, "[ghosttree: gekürzt") || !strings.Contains(got, `"short"`) {
		t.Errorf("list content not capped: %d bytes", len(got))
	}
}

func TestCapCutsAtRuneBoundaries(t *testing.T) {
	s := strings.Repeat("ä", CapOver)
	c, cut := Cap(s)
	if !cut {
		t.Fatal("not cut")
	}
	if !json.Valid(encodeString(c)) || strings.ContainsRune(c, '\uFFFD') {
		t.Fatal("cut inside a character")
	}
	if len(c) > CapOver {
		t.Errorf("capped string is %d bytes, above CapOver", len(c))
	}
	if again, cut := Cap(c); cut || again != c {
		t.Error("capping twice changes the string")
	}
	if short, cut := Cap("short"); cut || short != "short" {
		t.Error("short string cut")
	}
}

func TestLineCodex(t *testing.T) {
	out := long("OUT", 30_000) + "TAIL"
	cases := []struct{ name, in, want string }{
		{"reasoning",
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Looking at the parser"}],"encrypted_content":"Z0FBQUFBQm8=","internal_chat_message_metadata_passthrough":{"turn_id":"tu"}}}`,
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Looking at the parser"}],"internal_chat_message_metadata_passthrough":{"turn_id":"tu"}}}`},
		{"compacted",
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"compacted","payload":{"message":"Summary of the work so far","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]}],"guardian_history":[{"type":"message","role":"user","content":[]}],"window_number":2}}`,
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"compacted","payload":{"message":"Summary of the work so far","window_number":2}}`},
		{"token_count",
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":5}},"rate_limits":{"limit_id":"codex","primary":{"used_percent":12.5}}}}`,
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":5}}}}`},
		{"command execution",
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"CommandExecution","id":"ce_1","command":"go test ./...","cwd":"/work/repo","process_id":"7","source":"unified_exec_startup","status":"completed","parsed_cmd":[{"type":"unknown","cmd":"go test ./..."}],"stdout":"ok\n","stderr":"","aggregated_output":"ok\n","exit_code":0,"duration":{"secs":1,"nanos":0},"formatted_output":"ok\n"},"started_at_ms":1,"completed_at_ms":2}}`,
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"CommandExecution","id":"ce_1","command":"go test ./...","cwd":"/work/repo","process_id":"7","source":"unified_exec_startup","status":"completed","aggregated_output":"ok\n","exit_code":0,"duration":{"secs":1,"nanos":0}},"started_at_ms":1,"completed_at_ms":2}}`},
	}
	for _, c := range cases {
		got, keep := Line(c.in)
		if !keep || got != c.want {
			t.Errorf("%s:\n got %v %s\nwant %s", c.name, keep, got, c.want)
		}
	}
	// A long command output is kept once, capped.
	in := `{"timestamp":"2026-11-01T10:00:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"ce_2","command":"cat big.log","stdout":` + q(out) + `,"stderr":"","aggregated_output":` + q(out) + `,"exit_code":1,"formatted_output":` + q(out) + `}}}`
	got, _ := Line(in)
	if len(got) > CapOver+600 || strings.Count(got, "[ghosttree: gekürzt") != 1 {
		t.Errorf("command output: %d bytes, %d markers", len(got), strings.Count(got, "[ghosttree: gekürzt"))
	}
	p := transcript.Parse("codex", 3, got)
	if len(p.Blocks) != 1 || p.Blocks[0].Input != "cat big.log" || !strings.HasPrefix(p.Blocks[0].Output, "OUT") || !strings.HasSuffix(p.Blocks[0].Output, "TAIL") || p.Blocks[0].Status != "exit 1" {
		t.Errorf("command block %+v", p.Blocks)
	}
	// The exec tool's own output, as a list of parts.
	in = `{"timestamp":"2026-11-01T10:00:00.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Script completed"},{"type":"input_text","text":` + q(out) + `}]}}`
	got, _ = Line(in)
	if p := transcript.Parse("codex", 4, got); len(p.Blocks) != 1 || !strings.Contains(p.Blocks[0].Output, "Script completed") || !strings.Contains(p.Blocks[0].Output, "[ghosttree: gekürzt") {
		t.Errorf("exec output %+v", p.Blocks)
	}
	// Older rollouts wrap the output as JSON text; the wrapper stays readable.
	wrapped, _ := json.Marshal(map[string]any{"output": out, "metadata": map[string]any{"exit_code": 2}})
	in = `{"timestamp":"2026-11-01T10:00:00.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_2","output":` + q(string(wrapped)) + `}}`
	got, _ = Line(in)
	if p := transcript.Parse("codex", 5, got); len(p.Blocks) != 1 || p.Blocks[0].Status != "exit 2" || !strings.HasPrefix(p.Blocks[0].Output, "OUT") || len(got) > CapOver+800 {
		t.Errorf("wrapped output: %d bytes, %+v", len(got), p.Blocks)
	}
}

func TestLineLeavesUnknownAndInvalidLinesAlone(t *testing.T) {
	big := q(long("x", 50_000))
	for _, line := range []string{
		`{"type":"some-future-line","payload_like":{"text":` + big + `}}`,
		`{"type":"ai-title","aiTitle":"Fix the parser","sessionId":"s-1"}`,
		`{"timestamp":"2026-11-01T10:00:00.000Z","type":"turn_context","payload":{"turn_id":"tu","cwd":"/work/repo","model":"gpt-5.5-codex","effort":"high"}}`,
		`{"timestamp":"2026-11-01T10:00:00.000Z","type":"session_meta","payload":{"id":"s-2","cwd":"/work/repo","base_instructions":{"text":` + big + `}}}`,
		`{"type":"user","message":{"role":"user","content":` + big + `},"uuid":"u-9"}`,
		`{"type":"user", "message": {"role":"user","content":"spaced"}}`,
		`not json`,
		`{"type":"assistant","message":`,
		`[1,2,3]`,
	} {
		got, keep := Line(line)
		if !keep || got != line {
			t.Errorf("changed: %.120s", line)
		}
	}
}

// Was ein Leser aus einer Zeile zieht, ist vor und nach dem Schlanken gleich;
// eine weggelassene Zeile gab keinem Leser etwas.
func TestReadersSeeTheSame(t *testing.T) {
	out := long("R", 20_000)
	lines := map[string][]string{
		"claude-code": {
			claudeAssistant,
			claudeToolResult(`"short result"`, `{"stdout":"short result","stderr":"","interrupted":false,"isImage":false}`),
			claudeToolResult(q(out), `{"stdout":`+q(out)+`}`),
			claudeToolResult(`"Launching skill: init"`, `{"success":true,"commandName":"init"}`),
			`{"parentUuid":"u-2","isSidechain":false,"type":"user","message":{"role":"user","content":"<command-name>/init</command-name>"},"uuid":"u-4","timestamp":"2026-11-01T10:00:05.000Z"}`,
			`{"parentUuid":"u-4","isSidechain":true,"type":"assistant","message":{"model":"claude-haiku-5-5","role":"assistant","content":[{"type":"text","text":"sub agent"}],"usage":{"input_tokens":1,"output_tokens":1,"service_tier":"standard"}},"uuid":"a-5","timestamp":"2026-11-01T10:00:06.000Z"}`,
			`{"type":"api-request-blob","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"copy"}]},"timestamp":"2026-11-01T10:00:07.000Z"}`,
			`{"type":"attachment","attachment":{"type":"hook_stopped_continuation","message":"paused"},"uuid":"t-9","timestamp":"2026-11-01T10:00:08.000Z"}`,
		},
		"codex": {
			`{"timestamp":"2026-11-01T10:00:00.000Z","type":"turn_context","payload":{"turn_id":"tu","model":"gpt-5.5-codex"}}`,
			`{"timestamp":"2026-11-01T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"fix the parser"}}`,
			`{"timestamp":"2026-11-01T10:00:02.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"Zm9v"}}`,
			`{"timestamp":"2026-11-01T10:00:03.000Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"run('go test')"}}`,
			`{"timestamp":"2026-11-01T10:00:04.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Process exited with code 0\nOutput:\nok"}}`,
			`{"timestamp":"2026-11-01T10:00:05.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"done"}]}}}`,
			`{"timestamp":"2026-11-01T10:00:06.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		},
	}
	for harness, ls := range lines {
		for i, l := range ls {
			slimmed, keep := Line(l)
			p0 := transcript.Parse(harness, i, l)
			m0, ok0 := actor.ObserveModel(l)
			if !keep {
				if len(p0.Blocks) > 0 || p0.Title != "" || ok0 {
					t.Errorf("%s line %d dropped although readers use it", harness, i)
				}
				continue
			}
			p1 := transcript.Parse(harness, i, slimmed)
			if len(p0.Blocks) != len(p1.Blocks) {
				t.Fatalf("%s line %d: %d blocks became %d", harness, i, len(p0.Blocks), len(p1.Blocks))
			}
			for j := range p0.Blocks {
				a, b := p0.Blocks[j], p1.Blocks[j]
				if a.Output != b.Output && !(strings.Contains(b.Output, "[ghosttree: gekürzt") && a.Output[:CapHead-100] == b.Output[:CapHead-100]) {
					t.Errorf("%s line %d: output changed", harness, i)
				}
				a.Output, b.Output = "", ""
				if !reflect.DeepEqual(a, b) {
					t.Errorf("%s line %d: block\n%+v\n%+v", harness, i, a, b)
				}
			}
			if m1, ok1 := actor.ObserveModel(slimmed); ok0 != ok1 || m0 != m1 {
				t.Errorf("%s line %d: model %v %v -> %v %v", harness, i, m0, ok0, m1, ok1)
			}
			var d0, d1 struct {
				UUID, ParentUUID, Timestamp string
				ToolUseResult               struct{ CommandName string }
			}
			if json.Unmarshal([]byte(l), &d0) == nil {
				if err := json.Unmarshal([]byte(slimmed), &d1); err != nil || d0 != d1 {
					t.Errorf("%s line %d: distiller fields %+v -> %+v", harness, i, d0, d1)
				}
			}
		}
	}
}

func TestLineIsIdempotent(t *testing.T) {
	out := long("I", 25_000)
	for _, l := range []string{
		claudeAssistant,
		claudeToolResult(q(out), `{"stdout":`+q(out)+`,"interrupted":false}`),
		`{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":"x","stdout":` + q(out) + `,"aggregated_output":` + q(out) + `}}}`,
		`{"timestamp":"t","type":"response_item","payload":{"type":"custom_tool_call_output","output":[{"type":"input_text","text":` + q(out) + `}]}}`,
	} {
		once, _ := Line(l)
		twice, keep := Line(once)
		if !keep || twice != once {
			t.Errorf("second pass changed %.80s", l)
		}
	}
}

func TestKind(t *testing.T) {
	for line, want := range map[string]string{
		claudeAssistant: "claude:assistant",
		`{"type":"attachment","attachment":{"type":"hook_success"}}`:                                  "claude:attachment:hook_success",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution"}}}`: "codex:event_msg:item_completed:CommandExecution",
		`{"type":"compacted","payload":{"message":"m"}}`:                                              "codex:compacted",
		`{"cwd":"/x"}`: "claude:?",
		`nope`:         "invalid",
	} {
		if got := Kind(line); got != want {
			t.Errorf("Kind(%.60s) = %q, want %q", line, got, want)
		}
	}
}

func FuzzLine(f *testing.F) {
	f.Add(claudeAssistant)
	f.Add(claudeToolResult(q(long("F", 9000)), `{"stdout":"x"}`))
	f.Add(`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","stdout":"a","aggregated_output":"a"}}}`)
	f.Add(`{"type":"queue-operation","content":"a","content":"b"}`)
	f.Fuzz(func(t *testing.T, line string) {
		out, keep := Line(line)
		if !keep {
			return
		}
		if json.Valid([]byte(line)) && !json.Valid([]byte(out)) {
			t.Fatalf("valid line became invalid: %q -> %q", line, out)
		}
		if again, k := Line(out); !k || again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", line, out, again)
		}
	})
}
