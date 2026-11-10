package main

import "encoding/json"

// promptRaw is a transcript line of the harness with one prompt. The session
// search indexes what a line says, not the text column, so a test chunk needs
// a line its harness writes.
func promptRaw(harness, text string) string {
	var v any = map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
	if harness == "codex" {
		v = map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": text}}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
