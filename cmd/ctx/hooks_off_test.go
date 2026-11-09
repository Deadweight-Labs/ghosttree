package main

import (
	"bytes"
	"strings"
	"testing"
)

// REQ-623: GHOSTTREE_HOOKS=off silences every hook and the collector, so the
// agents a test harness starts never reach a server.
func TestHooksOffSilencesHooksAndCollector(t *testing.T) {
	t.Setenv(envHooks, "off")
	t.Setenv("GHOSTTREE_CONFIG", t.TempDir()+"/missing.json")
	for _, event := range []string{"session-start", "user-prompt-submit", "pre-tool-use", "post-tool-use", "pause-gate", "stop"} {
		var out bytes.Buffer
		if code := cmdHookWith(strings.NewReader(`{"session_id":"s","cwd":"/x"}`), []string{event, "--harness", "claude"}, &out); code != 0 || out.Len() != 0 {
			t.Errorf("%s: exit %d, output %q", event, code, out.String())
		}
	}
	var out bytes.Buffer
	if code := cmdWatch(nil, &out); code != 0 || !strings.Contains(out.String(), "switched off") {
		t.Errorf("watch: %d %q", code, out.String())
	}
	t.Setenv(envHooks, "on")
	if hooksOff() {
		t.Error("on reads as off")
	}
}
