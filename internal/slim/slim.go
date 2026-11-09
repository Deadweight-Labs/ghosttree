// Package slim lässt aus einer JSONL-Zeile eines Agenten-Transkripts weg, was
// kein Leser braucht: reine Kopien und Ballast der Harnesses. Lange
// Werkzeugausgaben werden auf Anfang und Ende gekürzt.
//
// Die Regel ist zustandslos (jede Zeile für sich) und idempotent: eine
// geschlankte Zeile kommt unverändert zurück. Unbekannte Zeilentypen und
// ungültiges JSON bleiben, wie sie sind. Ob eine Zeile von Claude Code oder
// Codex stammt, entscheidet ihr Typ; die beiden Typmengen überschneiden sich
// nicht.
package slim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version der Regel. Wer die Regel ändert, erhöht sie; ein Bestandslauf kann
// daran erkennen, welche Zeilen schon nach der aktuellen Regel geschlankt sind.
const Version = 1

// Grenzen für lange Werkzeugausgaben: ein String über CapOver Bytes wird auf
// CapHead Bytes Anfang, eine Markierung mit Länge und Hash und CapTail Bytes
// Ende gekürzt. CapOver liegt über der Länge einer gekürzten Ausgabe, damit ein
// zweiter Durchlauf nichts mehr ändert.
const (
	CapHead = 6 << 10
	CapTail = 2 << 10
	CapOver = CapHead + CapTail + 256
)

// Line schlankt eine Rohzeile. keep ist false, wenn die Zeile ganz wegfällt.
// Eine Zeile ohne Änderung kommt als derselbe String zurück.
func Line(line string) (out string, keep bool) {
	o, ok := parseObject([]byte(line))
	if !ok {
		return line, true
	}
	typ := o.str("type")
	var drop bool
	if codexTypes[typ] {
		drop = codexLine(o, typ)
	} else {
		drop = claudeLine(o, typ)
	}
	if drop {
		return "", false
	}
	if !o.changed {
		return line, true
	}
	return string(o.bytes()), true
}

// Kind benennt den Zeilentyp für Statistiken: "claude:assistant",
// "claude:attachment:hook_success", "codex:event_msg:item_completed:CommandExecution".
// Ungültiges JSON heißt "invalid".
func Kind(line string) string {
	o, ok := parseObject([]byte(line))
	if !ok {
		return "invalid"
	}
	typ := o.str("type")
	if codexTypes[typ] {
		k := "codex:" + typ
		if p, ok := parseObject(o.get("payload")); ok {
			if pt := p.str("type"); pt != "" {
				k += ":" + pt
				if pt == "item_completed" {
					if it, ok := parseObject(p.get("item")); ok {
						k += ":" + it.str("type")
					}
				}
			}
		}
		return k
	}
	if typ == "" {
		typ = "?"
	}
	k := "claude:" + typ
	if typ == "attachment" {
		if a, ok := parseObject(o.get("attachment")); ok {
			k += ":" + a.str("type")
		}
	}
	return k
}

// ------------------------------------------------------------- Claude Code

// claudeDropTypes sind Zeilen, die Claude Code seit 2.1.301 für die eigene
// Wiederholung einer API-Anfrage schreibt: eine Kopie von Nachrichten,
// Denkblöcken und Werkzeugaufrufen, die schon in user/assistant stehen, dazu
// Anfrageparameter und Hashes.
var claudeDropTypes = map[string]bool{
	"api-request-blob":  true,
	"api-request":       true,
	"api-request-shape": true,
}

// claudeDropAttachments sind Anhänge ohne Leser: prompt_snapshot ist der
// komplette Systemprompt samt Werkzeugdefinitionen einer Anfrage,
// hook_success die Rohausgabe eines Hooks, deren Wirkung als
// hook_additional_context erhalten bleibt.
var claudeDropAttachments = map[string]bool{
	"prompt_snapshot": true,
	"hook_success":    true,
}

// toolUseResultKeep sind die Felder von toolUseResult, die bleiben, und nur
// als kleiner Skalar. Alles andere dort (stdout, file, originalFile,
// structuredPatch, content …) ist eine Kopie des tool_result in message.content
// oder der Werkzeugeingabe.
var toolUseResultKeep = map[string]bool{
	"commandName": true, "success": true, "interrupted": true, "isImage": true,
	"noOutputExpected": true, "exitCode": true, "returnCodeInterpretation": true,
	"durationMs": true, "durationSeconds": true, "numFiles": true, "numLines": true,
	"totalLines": true, "startLine": true, "type": true, "filePath": true,
	"status": true, "agentId": true, "agentType": true, "resolvedModel": true,
	"totalDurationMs": true, "totalTokens": true, "totalToolUseCount": true,
	"userModified": true, "replaceAll": true, "mode": true, "truncated": true,
	"backgroundTaskId": true, "taskId": true, "taskType": true, "isAsync": true,
	"timedOutAfterMs": true, "url": true, "code": true, "codeText": true,
	"bytes": true, "query": true, "outputFile": true, "isAgent": true,
}

// smallScalar ist die Grenze für einen behaltenen toolUseResult-Wert.
const smallScalar = 256

// usageKeep sind die Token-Zahlen von message.usage.
var usageKeep = map[string]bool{
	"input_tokens": true, "output_tokens": true,
	"cache_read_input_tokens": true, "cache_creation_input_tokens": true,
}

func claudeLine(o *object, typ string) (drop bool) {
	switch {
	case claudeDropTypes[typ]:
		return true
	case typ == "queue-operation":
		// Der Text einer eingereihten Eingabe kommt später als Prompt.
		o.del("content")
	case typ == "attachment":
		var at string
		if a, ok := parseObject(o.get("attachment")); ok {
			at = a.str("type")
		}
		if claudeDropAttachments[at] {
			return true
		}
		o.del("rendered")
	case typ == "user" || typ == "assistant":
		o.del("wireToolInputs", "requestRef")
		o.edit("message", claudeMessage)
		claudeToolUseResult(o)
	}
	return false
}

func claudeMessage(m *object) {
	m.del("input_transformations")
	m.delIfEmpty("diagnostics", "stop_details", "context_management", "container", "stop_sequence")
	m.edit("usage", func(u *object) {
		kept := u.members[:0:0]
		for _, mb := range u.members {
			if usageKeep[mb.key] {
				kept = append(kept, mb)
			}
		}
		if len(kept) != len(u.members) {
			u.members, u.changed = kept, true
		}
	})
	items, ok := parseArray(m.get("content"))
	if !ok {
		return
	}
	changed := false
	for i, it := range items {
		b, ok := parseObject(it)
		if !ok {
			continue
		}
		switch b.str("type") {
		case "thinking":
			b.del("signature")
		case "redacted_thinking":
			b.del("data")
		case "tool_result":
			if v, ok := capValue(b.get("content"), 0); ok {
				b.set("content", v)
			}
		}
		if b.changed {
			items[i] = b.bytes()
			changed = true
		}
	}
	if changed {
		m.set("content", joinArray(items))
	}
}

func claudeToolUseResult(o *object) {
	v := o.get("toolUseResult")
	if v == nil {
		return
	}
	r, ok := parseObject(v)
	if !ok {
		// Ein String (Fehlertext) oder eine Liste steht ebenso im tool_result.
		o.del("toolUseResult")
		return
	}
	kept := r.members[:0:0]
	for _, mb := range r.members {
		if toolUseResultKeep[mb.key] && scalar(mb.val) && len(mb.val) <= smallScalar {
			kept = append(kept, mb)
		}
	}
	if len(kept) == len(r.members) {
		return
	}
	if len(kept) == 0 {
		o.del("toolUseResult")
		return
	}
	r.members = kept
	o.set("toolUseResult", r.bytes())
}

func scalar(v json.RawMessage) bool {
	if len(v) == 0 {
		return false
	}
	return v[0] != '{' && v[0] != '['
}

// ------------------------------------------------------------------- Codex

var codexTypes = map[string]bool{
	"session_meta": true, "response_item": true, "event_msg": true,
	"turn_context": true, "compacted": true, "world_state": true,
	"token_usage_record": true, "inter_agent_communication_metadata": true,
}

// codexCopyItems sind item_completed-Ereignisse, die eine Nachricht oder einen
// Denkblock wiederholen, der schon als response_item oder event_msg dasteht.
var codexCopyItems = map[string]bool{
	"UserMessage":  true,
	"AgentMessage": true,
	"Reasoning":    true,
}

func codexLine(o *object, typ string) (drop bool) {
	p, ok := parseObject(o.get("payload"))
	if !ok {
		return false
	}
	pt := p.str("type")
	switch typ {
	case "response_item":
		switch pt {
		case "reasoning":
			// Verschlüsselt, nur für die Fortsetzung beim Anbieter lesbar.
			p.del("encrypted_content")
		case "function_call_output", "custom_tool_call_output":
			if v, ok := capOutput(p.get("output")); ok {
				p.set("output", v)
			}
		}
	case "compacted":
		// Die ersetzte Geschichte ist eine Kopie der Zeilen davor; die
		// Zusammenfassung (message) bleibt.
		p.del("replacement_history", "guardian_history")
	case "event_msg":
		switch pt {
		case "token_count":
			p.del("rate_limits")
		case "item_completed":
			it, ok := parseObject(p.get("item"))
			if !ok {
				break
			}
			itt := it.str("type")
			if codexCopyItems[itt] {
				return true
			}
			switch itt {
			case "CommandExecution":
				commandOutput(it)
			case "McpToolCall":
				capField(it, "result")
			}
			if it.changed {
				p.set("item", it.bytes())
			}
		case "exec_command_end":
			commandOutput(p)
		case "mcp_tool_call_end":
			capField(p, "result")
		}
	}
	if p.changed {
		o.set("payload", p.bytes())
	}
	return false
}

// commandOutput behält die Ausgabe eines Befehls einmal: aggregated_output
// (stdout und stderr in Reihenfolge), gekürzt. stdout und stderr fallen weg,
// wenn sie darin schon stehen; formatted_output ist eine für das Modell
// aufbereitete Kopie, parsed_cmd eine Zerlegung von command.
func commandOutput(it *object) {
	agg, hasAgg := stringValue(it.get("aggregated_output"))
	if hasAgg {
		for _, k := range []string{"stdout", "stderr"} {
			if s, ok := stringValue(it.get(k)); ok && strings.Contains(agg, s) {
				it.del(k)
			}
		}
	}
	it.del("formatted_output", "parsed_cmd")
	for _, k := range []string{"aggregated_output", "stdout", "stderr"} {
		capField(it, k)
	}
}

func stringValue(v json.RawMessage) (string, bool) {
	if len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

func capField(o *object, k string) {
	if v, ok := capValue(o.get(k), 0); ok {
		o.set(k, v)
	}
}

// capOutput kürzt die Ausgabe eines Codex-Werkzeugs. Ältere Rollouts packen
// sie als JSON-Text {"output": "...", "metadata": {...}} in einen String;
// gekürzt wird dann nur output darin, damit die Hülle lesbar bleibt.
func capOutput(v json.RawMessage) (json.RawMessage, bool) {
	s, ok := stringValue(v)
	if !ok || len(s) <= capOver || s[0] != '{' {
		return capValue(v, 0)
	}
	inner, ok := parseObject([]byte(s))
	if !ok || inner.get("output") == nil {
		return capValue(v, 0)
	}
	capField(inner, "output")
	if !inner.changed {
		return nil, false
	}
	return encodeString(string(inner.bytes())), true
}

// maxDepth begrenzt den Abstieg in verschachtelte Ausgaben.
const maxDepth = 16

// capValue kürzt jeden langen String in v (auch in Listen und Objekten). ok ist
// false, wenn nichts zu kürzen war.
func capValue(v json.RawMessage, depth int) (json.RawMessage, bool) {
	if len(v) == 0 || depth > maxDepth {
		return nil, false
	}
	switch v[0] {
	case '"':
		if len(v) <= capOver {
			return nil, false
		}
		s, ok := stringValue(v)
		if !ok {
			return nil, false
		}
		c, cut := Cap(s)
		if !cut {
			return nil, false
		}
		return encodeString(c), true
	case '[':
		items, ok := parseArray(v)
		if !ok {
			return nil, false
		}
		changed := false
		for i, it := range items {
			if c, ok := capValue(it, depth+1); ok {
				items[i], changed = c, true
			}
		}
		if !changed {
			return nil, false
		}
		return joinArray(items), true
	case '{':
		o, ok := parseObject(v)
		if !ok {
			return nil, false
		}
		for i := range o.members {
			if c, ok := capValue(o.members[i].val, depth+1); ok {
				o.members[i].val, o.changed = c, true
			}
		}
		if !o.changed {
			return nil, false
		}
		return o.bytes(), true
	}
	return nil, false
}

// Cap kürzt s, wenn es länger als CapOver Bytes ist: CapHead Bytes Anfang, eine
// Markierung mit der ursprünglichen Länge und den ersten 16 Hexzeichen des
// sha256, CapTail Bytes Ende. Geschnitten wird an Zeichengrenzen.
func Cap(s string) (string, bool) {
	if len(s) <= capOver {
		return s, false
	}
	head := CapHead
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tail := len(s) - CapTail
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	sum := sha256.Sum256([]byte(s))
	return s[:head] + Marker(len(s), hex.EncodeToString(sum[:8])) + s[tail:], true
}

// Marker ist der Text zwischen Anfang und Ende einer gekürzten Ausgabe.
func Marker(bytes int, hash16 string) string {
	return "\n[ghosttree: gekürzt, " + strconv.Itoa(bytes) + " Bytes, sha256:" + hash16 + "]\n"
}

// capOver ist CapOver; Messungen ohne Kürzen setzen es hoch.
var capOver = CapOver
