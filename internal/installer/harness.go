package installer

import (
	"path/filepath"
	"slices"
)

// Channel is one route by which context reaches a session.
//
// Naming them is what lets the rest of the system stop guessing. Until
// 2026-08-24 the code assumed Claude Code had hooks and Codex did not, so Codex
// got an instruction in AGENTS.md asking the agent to fetch its own context —
// 482 sessions of asking politely instead of delivering. Codex has had the same
// five lifecycle events all along.
type Channel string

const (
	// ChannelSessionStart fires once when a session opens: what holds for the
	// whole session, delivered without anyone asking.
	ChannelSessionStart Channel = "session-start"
	// ChannelUserPrompt fires per prompt: what the last sentence gave a reason
	// to mention.
	ChannelUserPrompt Channel = "user-prompt-submit"
	// ChannelPreToolUse feuert vor einem Werkzeugaufruf und trägt den Pfad, den
	// der Aufruf anfasst: der einzige Kanal, über den eine Dateibeschreibung
	// genau dann ankommt, wenn sie zählt.
	ChannelPreToolUse Channel = "pre-tool-use"
	// ChannelMCP is the pull side. Every harness ghosttree supports has it, and
	// it is the only channel that answers a question rather than anticipating
	// one.
	ChannelMCP Channel = "mcp"
)

// Harness is what one agent runtime can be made to do, and where its wiring
// lives. The declaration is the single place that answers "which channels does
// this harness offer" — the installer wires them, the doctor checks them, and
// the rule section compensates for the ones that are missing.
type Harness struct {
	Name string
	// Channels are the routes ghosttree registers for this harness.
	Channels []Channel
	// Delivers is the subset that measurably reaches the model, and it is a
	// separate list because the two came apart the first time anyone checked.
	// Codex 0.149.0 runs its SessionStart and UserPromptSubmit hooks, reports
	// them completed, validates the reply against its own schema — and the
	// additionalContext never appears in the session transcript. Registering is
	// not delivering, so only this list may decide that a rule section no
	// longer has to ask for the context by hand.
	Delivers []Channel
	// HooksPath is the file that carries lifecycle hooks, empty when the
	// harness has none. Both supported harnesses use the same JSON shape under
	// a "hooks" key; only the location differs, which is exactly the kind of
	// difference that belongs in an adapter rather than in a delivery rule.
	HooksPath func(home string) string
	// RulePath is the markdown file that carries the ghosttree section.
	RulePath func(home string) string
}

func Harnesses() []Harness {
	return []Harness{
		{
			Name:      "claude",
			Channels:  []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			Delivers:  []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			HooksPath: func(home string) string { return filepath.Join(home, ".claude", "settings.json") },
			RulePath:  func(home string) string { return filepath.Join(home, ".claude", "CLAUDE.md") },
		},
		{
			// The payload formats match. Checked against the JSON schemas
			// embedded in codex-cli 0.149.0 — session-start.command.input
			// carries cwd, session_id and hook_event_name, user-prompt-submit
			// adds prompt, and both outputs are
			// hookSpecificOutput.additionalContext. That is exactly what ctx
			// hook already reads and writes, so the two channels need no second
			// implementation.
			//
			// The hooks are registered anyway, even though the context does not
			// arrive: they cost nothing, they are correct by the published
			// contract, and the day Codex starts honouring additionalContext
			// this harness is already wired. What must not happen is treating
			// them as delivery — hence the empty event list below and the rule
			// section that keeps asking for context_get.
			Name: "codex",
			// PreToolUse steht NICHT in Delivers. Codex dokumentiert
			// additionalContext für dieses Ereignis, aber ein nicht über /hooks
			// freigegebener Hook wird übersprungen — und unsere Einträge sind
			// nicht freigegeben (Wissenseintrag #859). Registrieren ist nicht
			// Ausliefern; das gehört gemessen, bevor es hier steht. REQ-160.
			Channels:  []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			Delivers:  []Channel{ChannelMCP},
			HooksPath: func(home string) string { return filepath.Join(home, ".codex", "hooks.json") },
			RulePath:  func(home string) string { return filepath.Join(home, ".codex", "AGENTS.md") },
		},
	}
}

// Serves reports that ghosttree registers this channel for the harness.
func (h Harness) Serves(c Channel) bool { return slices.Contains(h.Channels, c) }

// DeliversContext reports that the channel measurably reaches the model.
func (h Harness) DeliversContext(c Channel) bool { return slices.Contains(h.Delivers, c) }

// hookCommandFor names the subcommand that serves a channel.
func hookCommandFor(c Channel) (event, command, matcher string, ok bool) {
	switch c {
	case ChannelSessionStart:
		return "SessionStart", hookCommand, "", true
	case ChannelUserPrompt:
		return "UserPromptSubmit", promptHookCommand, "", true
	case ChannelPreToolUse:
		return "PreToolUse", preToolHookCommand, "Read|Edit|Write|NotebookEdit", true
	}
	return "", "", "", false
}

// ruleFor builds the harness's markdown section. A harness whose session-start
// context demonstrably arrives gets no instruction to fetch it by hand: telling
// an agent to do what already happened wastes a paragraph and invites a
// duplicate call. Anywhere else the section has to compensate, and it asks.
//
// The test is delivery, not registration. Wiring Codex's hooks and dropping the
// paragraph on that basis would have taken away the one channel that works
// there and replaced it with one that silently does nothing.
func ruleFor(h Harness) string {
	if h.DeliversContext(ChannelSessionStart) {
		return ruleText
	}
	return ruleText + "\n\nAt session start call the `context_get` tool once to load project context."
}
