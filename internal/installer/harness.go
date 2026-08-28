package installer

import (
	"os"
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
	Name       string
	Components []Component
	// Channels are the routes ghosttree registers for this harness.
	Channels []Channel
	// Delivers is the subset that measurably reaches the model, and it is a
	// separate list because the two came apart the first time anyone checked.
	// A hook can be written into the right file, be reported as completed, and
	// still put nothing in front of the model — Codex went 482 sessions that
	// way, because its hooks additionally need a one-off trust confirmation
	// that lives outside the file the installer writes.
	//
	// The entry here is a claim about reality and belongs measured: a
	// transcript that shows the text, not a config that shows the wiring.
	// Only this list may decide that a rule section no longer has to ask.
	Delivers []Channel
	// HooksPath is the file that carries lifecycle hooks, empty when the
	// harness has none. Both supported harnesses use the same JSON shape under
	// a "hooks" key; only the location differs, which is exactly the kind of
	// difference that belongs in an adapter rather than in a delivery rule.
	HooksPath func(home string) string
	// RulePath is the markdown file that carries the ghosttree section.
	RulePath func(home string) string
	// SkillsRoot is the directory a harness reads agent skills from, nil when
	// it has none. Claude Code and Codex read the same SKILL.md plus
	// references/ shape; only the location differs, which is exactly the kind
	// of difference that belongs in an adapter rather than in two copies of the
	// text.
	SkillsRoot func(home string) string
}

func Harnesses() []Harness {
	return []Harness{
		{
			Name:       "claude",
			Components: append([]Component(nil), componentOrder...),
			Channels:   []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			Delivers:   []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			HooksPath:  func(home string) string { return filepath.Join(home, ".claude", "settings.json") },
			RulePath:   func(home string) string { return filepath.Join(home, ".claude", "CLAUDE.md") },
			SkillsRoot: func(home string) string { return filepath.Join(home, ".claude", "skills") },
		},
		{
			// The payload formats match. Checked against the JSON schemas
			// embedded in codex-cli 0.149.0, and confirmed in practice on
			// 2026-08-25: session-start and user-prompt-submit both deliver
			// their additionalContext into the model's context. The bootstrap
			// appears in the rollout as a response_item with role "developer".
			//
			// GETTING THERE TOOK A DETOUR THAT IS WORTH KEEPING. For 482
			// sessions the context did not arrive, and the reason recorded here
			// was "Codex runs the hook and drops additionalContext". That was
			// wrong. Codex requires every unmanaged command hook to be trusted
			// once via /hooks, keyed by a hash of its definition; ghosttree's
			// entries sit in group 1 of hooks.json and had never been trusted,
			// so they never ran at all. From the outside "ran and returned
			// nothing" and "never ran" look identical — the installer wrote the
			// entry, doctor found it, and all of that was true while nothing
			// happened. See #794 and #859.
			//
			// PreToolUse was measured end to end on Codex 0.150.1: a fresh session
			// used Bash to read internal/mirror/mirror.go without MCP, and answered
			// with the description delivered only by this hook. Current Codex sends
			// tool_name Bash and tool_input.command; older raw-string exec payloads
			// remain supported by the adapter.
			Name:       "codex",
			Components: append([]Component(nil), componentOrder...),
			Channels:   []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			Delivers:   []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse, ChannelMCP},
			HooksPath:  func(home string) string { return filepath.Join(home, ".codex", "hooks.json") },
			RulePath:   func(home string) string { return filepath.Join(home, ".codex", "AGENTS.md") },
			// The documented user location is $HOME/.agents/skills, next to
			// .agents/skills scanned upwards from the working directory and
			// /etc/codex/skills machine-wide. Measured on the development
			// machine 2026-08-26, BOTH ~/.codex/skills and ~/.agents/skills
			// exist and hold skills, so which one 0.149.0 actually reads is an
			// open question — the binary is an npm wrapper and gives nothing
			// away. Until a fresh session settles it, the documented path wins
			// and Codex is not advertised as supported. "Installed" has been
			// false here twice already (#794, #859).
			SkillsRoot: func(home string) string { return filepath.Join(home, ".agents", "skills") },
		},
		{
			// Die erste Umgebung ohne kommandobasierten Hook. Was opencode hat,
			// sind Plugins — JavaScript-Module unter ~/.config/opencode/plugins/,
			// die auf Ereignisse wie session.created hören. Ein Plugin könnte
			// `ctx hook session-start` aufrufen, aber ob es Text in den
			// Modellkontext bekommt statt nur zuzusehen, ist ungeprüft, und ein
			// JS-Artefakt auszuliefern wäre eine eigene Entscheidung. Bis dahin
			// gilt: kein Hook-Pfad, kein Push-Kanal, und der Rückfallweg trägt
			// allein. Recherchiert an opencode.ai/docs, Wissen #1427.
			Name:       "opencode",
			Components: []Component{ComponentMCP, ComponentRules},
			Channels:   []Channel{ChannelMCP},
			Delivers:   []Channel{ChannelMCP},
			HooksPath:  nil,
			RulePath:   opencodeRulePath,
		},
	}
}

// opencodeRulePath sucht die Datei, die opencode ohnehin liest, statt eine neue
// anzulegen.
//
// Der Grund ist eine Falle: opencode liest ~/.claude/CLAUDE.md als Ersatz für
// seine globale AGENTS.md — aber nur, SOLANGE die nicht existiert. Eine
// anzulegen und dort bloss die eigene Sektion hineinzuschreiben, nähme opencode
// alles andere weg, was in CLAUDE.md steht. Der Doctor hätte danach grün
// gemeldet und der Rest wäre still verschwunden.
func opencodeRulePath(home string) string {
	agents := filepath.Join(home, ".config", "opencode", "AGENTS.md")
	if _, err := os.Stat(agents); err == nil {
		return agents
	}
	claude := filepath.Join(home, ".claude", "CLAUDE.md")
	if _, err := os.Stat(claude); err == nil {
		return claude
	}
	return agents
}

// Serves reports that ghosttree registers this channel for the harness.
func (h Harness) Serves(c Channel) bool { return slices.Contains(h.Channels, c) }

// DeliversContext reports that the channel measurably reaches the model.
func (h Harness) DeliversContext(c Channel) bool { return slices.Contains(h.Delivers, c) }

// hookCommandFor names the subcommand that serves a channel.
func (h Harness) hookCommandFor(c Channel) (event, command, matcher string, ok bool) {
	switch c {
	case ChannelSessionStart:
		return "SessionStart", hookCommand + " --harness " + h.Name, "", true
	case ChannelUserPrompt:
		return "UserPromptSubmit", promptHookCommand + " --harness " + h.Name, "", true
	case ChannelPreToolUse:
		// Codex bekommt bewusst KEINEN Matcher. `exec` stand hier, weil das
		// Rollout-Protokoll Lesezugriffe so bündelt — nur filtert Codex damit
		// den Hook weg, statt ihn zu treffen. Gemessen am 2026-08-25: bei
		// SessionStart und UserPromptSubmit meldet Codex je zwei Hook-Läufe
		// (ein fremder ohne Matcher, unserer), bei PreToolUse nur einen — den
		// fremden. Erst ohne Matcher läuft unserer mit (#1449).
		//
		// Der Verzicht ist kein Notbehelf: `tool_name` ist im eingebetteten
		// Schema ein freier String ohne Enum, die möglichen Werte gibt das
		// Binary nicht her, und sie können sich mit jeder Version ändern. Ein
		// Matcher würde die Auslieferung an eine interne Benennung koppeln, die
		// niemand nachschlagen kann. ghostContext entscheidet ohnehin an den
		// Nutzdaten, ob ein Pfad vorkommt, und schweigt sonst — die Filterung
		// sitzt damit dort, wo sie überprüfbar ist.
		if h.Name == "codex" {
			return "PreToolUse", preToolHookCommand + " --harness " + h.Name, "", true
		}
		return "PreToolUse", preToolHookCommand + " --harness " + h.Name, "Read|Edit|Write|NotebookEdit", true
	}
	return "", "", "", false
}

// ruleTail ist der Absatz, den seit dem 2026-08-25 JEDE Umgebung bekommt, und
// das ist eine Umkehrung: vorher bekam ihn nur, wessen Sitzungsbeginn
// nachweislich nichts auslieferte.
//
// Zwei Gründe, beide aus Schaden. Erstens teilen sich Umgebungen Regeldateien —
// opencode liest ~/.claude/CLAUDE.md, wenn es keine eigene globale AGENTS.md
// hat —, und ein Absatz, der von "dieser Umgebung" spricht, wird dort zur
// falschen Aussage über den anderen Leser. Zweitens hing die Fallunterscheidung
// am Dateisystem, das die Installation selbst verändert: nach dem ersten
// Schreiben war der Pfad plötzlich geteilt, und der Doctor meldete Drift gegen
// den Text, den er eine Zeile vorher selbst geschrieben hatte.
//
// Selbstprüfend formuliert stimmt der Satz für beide. Ein Agent sieht, ob am
// Anfang Kontext erschienen ist; ihm das zu behaupten ist unnötig.
const ruleTail = "\n\nIf no ghosttree context appeared at the start of this session, this harness does\n" +
	"not receive it — then read `.ghosttree/INDEX.md` in the repository, which is\n" +
	"already on disk and costs no tool call, and call `context_get` once for what the\n" +
	"mirror deliberately leaves out."

// ruleFor baut den Regelabschnitt. Er ist für alle Umgebungen derselbe: der
// Unterschied zwischen "bekommt Kontext gepusht" und "bekommt keinen" steckt im
// selbstprüfenden Satz von ruleTail, nicht in zwei verschiedenen Texten.
func ruleFor(h Harness) string { return ruleForPath(h, "") }

// ruleForPath ist der Weg für Aufrufer, die den Zielpfad ohnehin kennen. Der
// Pfad geht heute nicht mehr in den Text ein und bleibt als Parameter stehen,
// weil er die Absicht der Aufrufstelle festhält: geschrieben wird für eine
// Datei, nicht für eine Umgebung.
func ruleForPath(h Harness, _ string) string { return ruleText + ruleTail }
