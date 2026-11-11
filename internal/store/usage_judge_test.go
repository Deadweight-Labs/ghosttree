package store

import (
	"fmt"
	"testing"
)

func asst(at, text string) judgeLine { return judgeLine{Role: "assistant", Text: text, At: at} }

func call(at, tool, args string) judgeLine {
	return judgeLine{Role: "assistant", At: at, Text: "[tool call: " + tool + "] " + args}
}

func verdictOf(t *testing.T, items []judgeItem, lines ...judgeLine) map[int64]int {
	t.Helper()
	j := newJudger(items)
	for _, l := range lines {
		j.feed(l)
	}
	return j.verdicts()
}

const t0 = "2026-11-10T10:00:00Z"

func TestJudgeKnowledgePickedUpByNumberOrTitleWordsOrLookup(t *testing.T) {
	item := func(key, id int64, title string) judgeItem {
		return judgeItem{Key: key, Kind: "knowledge", ID: id, Since: t0, Title: title}
	}
	items := []judgeItem{
		item(1, 4251, "Zustellprotokoll schreibt der Hook, nicht der Server"),
		item(2, 4252, "Lesepool liefert Sitzungstext abgeleitet"),
		item(3, 4253, "Schemaupgrade blockiert Trigger beim Umbenennen"),
		item(4, 4254, "Budgetkonto zählt Zeichen"),
	}
	got := verdictOf(t, items,
		asst("2026-11-10T10:00:05.100Z", "Laut KN-4251 steht das so."),
		asst("2026-11-10T10:00:06.100Z", "Der Lesepool liefert den Sitzungstext, wie gedacht."),
		call("2026-11-10T10:00:07.100Z", "mcp__ghosttree__context_search", `{"knowledge_id":4253}`),
		asst("2026-11-10T10:00:08.100Z", "Das Budgetkonto ist nicht betroffen."),
	)
	want := map[int64]int{1: UsedPickedUp, 2: UsedPickedUp, 3: UsedPickedUp, 4: UsedNone}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("item %d: used = %d, want %d", k, got[k], v)
		}
	}
}

func TestJudgeOnlyCountsWhatTheAgentDidAfterTheDelivery(t *testing.T) {
	it := judgeItem{Key: 1, Kind: "knowledge", ID: 4251, Since: t0, Title: "Zustellprotokoll schreibt der Hook"}
	cases := map[string]judgeLine{
		"before the delivery":      asst("2026-11-10T09:59:00Z", "KN-4251 und das Zustellprotokoll schreibt der Hook"),
		"in the second of it":      asst("2026-11-10T10:00:00.900Z", "KN-4251"),
		"a message of the human":   {Role: "user", Text: "Siehe KN-4251", At: "2026-11-10T10:05:00Z"},
		"a tool result":            {Role: "tool", Text: "KN-4251 Zustellprotokoll schreibt der Hook", At: "2026-11-10T10:05:00Z"},
		"one title word only":      asst("2026-11-10T10:05:00Z", "Das Zustellprotokoll ist neu."),
		"a number inside another":  asst("2026-11-10T10:05:00Z", "KN-42510 und #14251 und KN-4251x"),
		"a pull request same-ish":  asst("2026-11-10T10:05:00Z", "PR 4251 ist gemergt"),
		"another tool's text only": {Role: "tool", Text: "no call marker KN-4251", At: "2026-11-10T10:05:00Z"},
		// Whether a line without a time came before or after the delivery is
		// unknown, so it never counts.
		"a line without a time": asst("", "Wie in KN-4251 beschrieben"),
	}
	for name, l := range cases {
		if got := verdictOf(t, []judgeItem{it}, l)[1]; got != UsedNone {
			t.Errorf("%s: used = %d, want 0", name, got)
		}
	}
	j := newJudger([]judgeItem{it})
	j.feed(asst("", "KN-4251"))
	if j.untimed != 1 || j.timed != 0 {
		t.Errorf("timed/untimed = %d/%d, want 0/1", j.timed, j.untimed)
	}
	// Codex writes calls as their own tool lines.
	if got := verdictOf(t, []judgeItem{it}, judgeLine{Role: "tool", At: "2026-11-10T10:05:00Z", Text: `[tool call: context_search] {"knowledge_id": "4251"}`})[1]; got != UsedPickedUp {
		t.Errorf("codex call: used = %d, want 1", got)
	}
}

func TestJudgeFollowedWhenPathOrCommandIsUsed(t *testing.T) {
	body := "Der Hook schreibt es: siehe `internal/deliverylog/deliverylog.go` und führe `ctx usage deliveries` aus. " +
		"Dazu `ctx upgrade-schema --db <pfad>` und https://example.org/a/b.html sowie ein Wort wie und/oder."
	items := []judgeItem{
		{Key: 1, Kind: "knowledge", ID: 10, Since: t0, Title: "Zustellung Hook", Body: body},
		{Key: 2, Kind: "knowledge", ID: 11, Since: t0, Title: "Anderes Thema", Body: body},
		{Key: 3, Kind: "knowledge", ID: 12, Since: t0, Title: "Dritter Eintrag", Body: "Kein Pfad, kein Befehl."},
	}
	for name, tc := range map[string]struct {
		lines []judgeLine
		want  [3]int
	}{
		"opens the path":                         {[]judgeLine{call("2026-11-10T10:01:00Z", "Read", `{"file_path":"/home/x/repo/internal/deliverylog/deliverylog.go"}`)}, [3]int{2, 2, 0}},
		"runs the command":                       {[]judgeLine{call("2026-11-10T10:01:00Z", "Bash", `{"command":"cd /x && ctx   usage deliveries --since 2026-11-01"}`)}, [3]int{2, 2, 0}},
		"runs the command up to the placeholder": {[]judgeLine{call("2026-11-10T10:01:00Z", "Bash", `{"command":"ctx upgrade-schema --db /tmp/x.db"}`)}, [3]int{2, 2, 0}},
		"names the path in text only":            {[]judgeLine{asst("2026-11-10T10:01:00Z", "ich schaue in internal/deliverylog/deliverylog.go")}, [3]int{0, 0, 0}},
		"an address is no path":                  {[]judgeLine{call("2026-11-10T10:01:00Z", "WebFetch", `{"url":"https://example.org/a/b.html"}`)}, [3]int{0, 0, 0}},
		"path before the delivery":               {[]judgeLine{call("2026-11-10T09:50:00Z", "Read", `{"file_path":"internal/deliverylog/deliverylog.go"}`)}, [3]int{0, 0, 0}},
	} {
		j := newJudger(items)
		for _, l := range tc.lines {
			j.feed(l)
		}
		v := j.verdicts()
		got := [3]int{v[1], v[2], v[3]}
		if got != tc.want {
			t.Errorf("%s: used = %v, want %v", name, got, tc.want)
		}
	}
}

func TestJudgeRunbookIsPickedUpByNameAndFollowedByTheReport(t *testing.T) {
	items := []judgeItem{
		{Key: 1, Kind: "runbook", ID: 3, Since: t0, Title: "rollout-prod"},
		{Key: 2, Kind: "runbook", ID: 4, Since: t0, Title: "build"}, // too short to name a runbook
		{Key: 3, Kind: "runbook", ID: 5, Since: t0, Title: "check-release"},
	}
	got := verdictOf(t, items,
		asst("2026-11-10T10:01:00Z", "Ich folge rollout-prod, aber nicht rollout-prod-test; build ist auch dabei."),
		call("2026-11-10T10:02:00Z", "mcp__ghosttree__runbook", `{"action":"report","name":"check-release","outcome":"ok"}`),
	)
	want := map[int64]int{1: UsedPickedUp, 2: UsedNone, 3: UsedFollowed}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("runbook item %d: used = %d, want %d", k, got[k], v)
		}
	}
	if got := verdictOf(t, items[:1], asst("2026-11-10T10:01:00Z", "nur rollout-prod-test hier"))[1]; got != UsedNone {
		t.Errorf("longer name matched: %d", got)
	}
}

// The matcher keeps nothing of what it read: its result is numbers by key.
func TestJudgeResultCarriesOnlyNumbers(t *testing.T) {
	items := []judgeItem{{Key: 7, Kind: "knowledge", ID: 1, Since: t0, Title: "Geheimnis Passwort Tresor"}}
	v := verdictOf(t, items, asst("2026-11-10T10:01:00Z", "Passwort Tresor"))
	if len(v) != 1 || v[7] != UsedPickedUp {
		t.Fatalf("verdicts = %v", v)
	}
	if s := fmt.Sprint(v); len(s) > 20 {
		t.Errorf("verdicts look like more than numbers: %q", s)
	}
}

// A bare #n is a pull request or an issue in the text of a repository ("PR #249"),
// not an entry: only KN-n, an address of the knowledge page and knowledge_id count.
func TestJudgeBareHashNumberIsNoEntry(t *testing.T) {
	item := func(key, id int64) judgeItem {
		return judgeItem{Key: key, Kind: "knowledge", ID: id, Since: t0, Title: "Eintrag ohne passende Wörter " + fmt.Sprint(id)}
	}
	items := []judgeItem{item(1, 249), item(2, 12), item(3, 5)}
	for name, tc := range map[string]struct {
		text string
		want [3]int
	}{
		"a pull request":       {"Das steht in PR #249 und ist gemergt.", [3]int{0, 0, 0}},
		"a bare number":        {"Siehe #12 und #5.", [3]int{0, 0, 0}},
		"an entry":             {"Siehe KN-12.", [3]int{0, 1, 0}},
		"an entry lower case":  {"siehe kn-5 dazu", [3]int{0, 0, 1}},
		"entry beside the PR":  {"PR #249 setzt KN-12 um", [3]int{0, 1, 0}},
		"the page address":     {"https://ghosttree.example/ui/o/r/knowledge/249", [3]int{1, 0, 0}},
		"the lookup in a call": {`[tool call: x] {"knowledge_id": 12}`, [3]int{0, 1, 0}},
		"a number in the PR":   {"fix #12 (#5)", [3]int{0, 0, 0}},
	} {
		v := verdictOf(t, items, asst("2026-11-10T10:05:00Z", tc.text))
		if got := [3]int{v[1], v[2], v[3]}; got != tc.want {
			t.Errorf("%s: used = %v, want %v", name, got, tc.want)
		}
	}
}

// A path that an entry names counts as followed only where the agent was not at
// that path already: an entry delivered because a tool call touched a path (via
// path) is not followed by the next call on the same path. A command still counts.
func TestJudgeFollowedByPathNeedsMoreThanBeingThereAnyway(t *testing.T) {
	body := "Siehe `internal/deliverylog/deliverylog.go`, dazu `ctx usage deliveries`."
	mk := func(via string) []judgeItem {
		return []judgeItem{{Key: 1, Kind: "knowledge", ID: 10, Since: t0, Via: via, Title: "Zustellung Hook", Body: body}}
	}
	edit := call("2026-11-10T10:01:00Z", "Edit", `{"file_path":"internal/deliverylog/deliverylog.go"}`)
	run := call("2026-11-10T10:02:00Z", "Bash", `{"command":"ctx usage deliveries"}`)
	for name, tc := range map[string]struct {
		via   string
		lines []judgeLine
		want  int
	}{
		"edits the path, delivered by path":     {"path", []judgeLine{edit}, UsedNone},
		"edits the path, delivered by briefing": {"bootstrap", []judgeLine{edit}, UsedFollowed},
		"edits the path, delivered by prompt":   {"relevance", []judgeLine{edit}, UsedFollowed},
		"runs the command, delivered by path":   {"path", []judgeLine{edit, run}, UsedFollowed},
	} {
		if got := verdictOf(t, mk(tc.via), tc.lines...)[1]; got != tc.want {
			t.Errorf("%s: used = %d, want %d", name, got, tc.want)
		}
	}
}
