package transcript

import (
	"encoding/base64"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
)

// Ein Platzhalter ersetzt nur Bytes, die der Parser nie gelesen hat: Blöcke und
// Indexfelder einer Zeile sind vor und nach blobstrip.Strip dieselben.
func TestParseIsTheSameForALineWithItsImagesStripped(t *testing.T) {
	pixels := make([]byte, 120_000)
	rand.New(rand.NewSource(3)).Read(pixels)
	b64 := base64.StdEncoding.EncodeToString(pixels)
	for name, c := range map[string]struct{ harness, line string }{
		"claude tool result":       {"claude-code", `{"type":"user","timestamp":"2026-10-19T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"shot saved"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]},"toolUseResult":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}`},
		"claude prompt with image": {"claude-code", `{"type":"user","timestamp":"2026-10-19T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + b64 + `"}}]}}`},
		"claude read file":         {"claude-code", `{"type":"user","timestamp":"2026-10-19T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"read"}]},"toolUseResult":{"type":"image","file":{"base64":"` + b64 + `","type":"image/png","originalSize":9}}}`},
		"codex user message":       {"codex", `{"timestamp":"2026-10-19T10:00:00Z","type":"event_msg","payload":{"type":"user_message","message":"see this","images":["data:image/png;base64,` + b64 + `"]}}`},
		"codex output":             {"codex", `{"timestamp":"2026-10-19T10:00:00Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Exit code: 0\nOutput:\nok","content":[{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `"}]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			stripped, saved := blobstrip.Strip(c.line)
			if saved == 0 || !strings.Contains(stripped, blobstrip.Prefix) {
				t.Fatalf("nothing stripped: %.200s", stripped)
			}
			before, after := Parse(c.harness, 4, c.line), Parse(c.harness, 4, stripped)
			if len(before.Blocks) == 0 {
				t.Fatal("the fixture parses to nothing, so the comparison proves nothing")
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("blocks differ:\n%+v\n%+v", before, after)
			}
			if !reflect.DeepEqual(Fields(before), Fields(after)) {
				t.Fatal("index fields differ")
			}
		})
	}
}
