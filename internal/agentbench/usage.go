package agentbench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// MemoryUsage is how often an arm actually opened the material it was given.
type MemoryUsage struct {
	Arm      ArmName `json:"arm"`
	Category string  `json:"category"`
	Runs     int     `json:"runs"`
	Touched  int     `json:"touched"`
}

// memoryMarkers are the paths that only exist because an arm was equipped.
// A tool call naming one of them is the arm reaching for its memory.
//
// Only arms whose material must be OPENED are listed, and the distinction
// matters more than it looks. Claude Code loads CLAUDE.md and its own
// auto-memory into the context itself; the agent never issues a tool call for
// them. Measuring those arms this way produced "claudemd: 0 of 34 runs" on the
// first try — a confident finding that the arm ignored its material, when in
// truth the material had been in front of it the whole time. A check meant to
// stop unfounded claims must not start by making one.
//
// A ghost tree, by contrast, is a directory the agent has to decide to read.
// Zero accesses there means zero.
var memoryMarkers = map[ArmName][]string{
	ArmGhosttree:   {".ghosttree"},
	ArmClaudeMem:   {".memory", "claude-mem"},
	ArmAgentMemory: {".memory", "agentmemory"},
}

// SummariseMemoryUsage counts, per arm and category, the runs whose transcript
// contains at least one tool call touching that arm's own material.
//
// Every other number in the report says WHERE a difference appears. This one
// says whether the arm was even using the thing it is named after, and it is
// the only check that reads what the agents did rather than comparing means.
//
// It was written after a near-miss. Robcord-Zentrale showed `ghosttree` needing
// 5.4 fewer tool calls than `bare` with an interval excluding zero, and the
// obvious reading — the described file tree shortens the search even where it
// knows nothing — was already written down. The transcripts say the arm opened
// the tree in 0 of 12 runs there. An effect whose instrument is never picked up
// has no mechanism, and at six tasks noise is the cheaper explanation.
//
// Where the mechanism does hold, the pattern is sharp: on NurProxy the arm
// touched the tree in 8 of 8 ledger runs and 0 of 20 pure-code runs.
//
// Missing or unreadable transcripts are skipped rather than counted as misses:
// a run nobody can read is not evidence that the arm ignored its memory.
func SummariseMemoryUsage(records []RunRecord) []MemoryUsage {
	type key struct {
		arm      ArmName
		category string
	}
	agg := map[key]*MemoryUsage{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		markers := memoryMarkers[record.Arm]
		if len(markers) == 0 {
			continue
		}
		raw, err := os.ReadFile(record.Transcript.RawPath)
		if err != nil {
			continue
		}
		k := key{record.Arm, string(record.Category)}
		if agg[k] == nil {
			agg[k] = &MemoryUsage{Arm: record.Arm, Category: string(record.Category)}
		}
		agg[k].Runs++
		if transcriptTouches(raw, markers) {
			agg[k].Touched++
		}
	}

	out := make([]MemoryUsage, 0, len(agg))
	for _, usage := range agg {
		out = append(out, *usage)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Arm != out[j].Arm {
			return out[i].Arm < out[j].Arm
		}
		return out[i].Category < out[j].Category
	})
	return out
}

// transcriptTouches reports whether any tool call in the transcript names one
// of the markers.
//
// Only the tool call's own arguments count, not the surrounding prose: a model
// that muses about ".ghosttree" in its reasoning has not read anything, and an
// answer quoting the path is a result, not a use.
func transcriptTouches(raw []byte, markers []string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Billiger Vorfilter: die allermeisten Zeilen nennen keinen Marker,
		// und jede davon zu entpacken kostet ueber hunderte Laeufe spuerbar.
		if !containsAny(line, markers) {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "assistant" {
			continue
		}
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" && containsAny(block.Input, markers) {
				return true
			}
		}
	}
	return false
}

func containsAny(haystack []byte, needles []string) bool {
	for _, needle := range needles {
		if bytes.Contains(haystack, []byte(needle)) {
			return true
		}
	}
	return false
}

// UsageWithoutMechanism names the arms that won on effort without ever opening
// their own material — the shape of a result that has no mechanism behind it.
func UsageWithoutMechanism(usage []MemoryUsage) []ArmName {
	touched := map[ArmName]int{}
	runs := map[ArmName]int{}
	for _, u := range usage {
		touched[u.Arm] += u.Touched
		runs[u.Arm] += u.Runs
	}
	var out []ArmName
	for arm, n := range runs {
		if n > 0 && touched[arm] == 0 {
			out = append(out, arm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// armList renders arm names for a report line.
func armList(arms []ArmName) string {
	names := make([]string, len(arms))
	for i, arm := range arms {
		names[i] = string(arm)
	}
	return strings.Join(names, ", ")
}
