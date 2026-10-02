package store

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var loopT0 = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

// loopMsg builds the nth message of a synthetic conversation, 20 s apart.
func loopMsg(n int, sender, body string) CoordMessage {
	return CoordMessage{
		ID: int64(n), SenderExternalID: sender, AuthorKind: AuthorAgent, Body: body,
		CreatedAt: loopT0.Add(time.Duration(n) * 20 * time.Second).Format(time.RFC3339),
	}
}

func loopRun(msgs []CoordMessage) []LoopState {
	var t LoopTracker
	out := make([]LoopState, len(msgs))
	for i, m := range msgs {
		out[i] = t.Add(m)
	}
	return out
}

// pingPong is the negative control: an acknowledgement ping-pong between two
// agents in which every message is an ack, a thanks or a status echo.
func pingPong(n int) []CoordMessage {
	bodies := []string{"ok?", "ja ok", "verstanden", "gern", "ok", "Danke, verstanden.", "gern geschehen", "ok, passt", "verstanden", "ok"}
	var msgs []CoordMessage
	for i := 0; i < n; i++ {
		sender := "claude:lab:a"
		if i%2 == 1 {
			sender = "claude:lab:b"
		}
		m := loopMsg(i+1, sender, bodies[i%len(bodies)])
		if i == 0 {
			m.Intent = IntentQuestion
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestLoopPingPongReachesWarnThenHold(t *testing.T) {
	states := loopRun(pingPong(10))
	var streaks []int
	for _, s := range states {
		streaks = append(streaks, s.Streak)
		if !s.Low {
			t.Fatalf("an ack ping-pong message counted as new content (%s)", s.Signal)
		}
	}
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if fmt.Sprint(streaks) != fmt.Sprint(want) {
		t.Fatalf("streaks = %v, want %v", streaks, want)
	}
	if states[2].Warn() || !states[3].Warn() || states[4].Hold() || !states[5].Hold() {
		t.Fatalf("warn at %d and hold at %d expected, got %+v", LoopWarnStreak, LoopHoldStreak, states)
	}
}

func TestLoopParaphraseOfOwnMessageIsLowContent(t *testing.T) {
	long := "Der Status ist unveraendert: die Tests laufen gruen und der Review steht noch aus bis morgen frueh"
	msgs := []CoordMessage{
		loopMsg(1, "a", "Status bleibt gleich"),
		loopMsg(2, "b", long),
		loopMsg(3, "a", "Status bleibt gleich"),
		loopMsg(4, "b", long+" bitte"),
		loopMsg(5, "a", "Status bleibt gleich!"),
		loopMsg(6, "b", long),
		loopMsg(7, "a", "Status bleibt gleich"),
		loopMsg(8, "b", long+" danke"),
	}
	states := loopRun(msgs)
	if got := states[7]; !got.Low || got.Streak < LoopHoldStreak {
		t.Fatalf("repeated status lines: %+v", states)
	}
}

// primed returns a tracker that already sits at streak 4 from a ping-pong,
// with the last low message sent by "claude:lab:a".
func primed() (*LoopTracker, []CoordMessage) {
	var tr LoopTracker
	msgs := pingPong(5)
	for _, m := range msgs {
		tr.Add(m)
	}
	return &tr, msgs
}

func TestEveryContentSignalResetsTheStreak(t *testing.T) {
	cases := []struct {
		name, signal string
		mutate       func(m *CoordMessage)
	}{
		{"commit hash", "commit", func(m *CoordMessage) { m.Body = "ok, review 9d9399e" }},
		{"file path", "path", func(m *CoordMessage) { m.Body = "ok, siehe internal/store/coordloop.go" }},
		{"bare file name", "path", func(m *CoordMessage) { m.Body = "ok, README_RATE.md" }},
		{"code block", "code", func(m *CoordMessage) { m.Body = "ok\n```go\nx := 1\n```" }},
		{"link", "link", func(m *CoordMessage) { m.Body = "ok https://example.org/a" }},
		{"REQ reference", "ref", func(m *CoordMessage) { m.Body = "ok, REQ-360" }},
		{"AC reference", "ref", func(m *CoordMessage) { m.Body = "ok, AC 1223" }},
		{"typed ref", "ref", func(m *CoordMessage) { m.Body = "ok"; m.Refs = []CoordRef{{Kind: "document", ID: "d1"}} }},
		{"new number", "number", func(m *CoordMessage) { m.Body = "ok, 42 ms" }},
		{"new decimal", "number", func(m *CoordMessage) { m.Body = "ok, 3.5" }},
		{"human", "human", func(m *CoordMessage) { m.AuthorKind = AuthorHuman; m.SenderExternalID = "person:1"; m.Body = "ok" }},
		{"new question with text", "question", func(m *CoordMessage) {
			m.Intent = IntentQuestion
			m.Body = "Welche Rotation bevorzugst du fuer die Logdateien, taeglich oder nach Groesse?"
		}},
		{"new substantial text", "text", func(m *CoordMessage) {
			m.Body = "Der Cache invalidiert beim Schreiben, nicht beim Lesen, damit Leser nie blockieren"
		}},
	}
	for _, c := range cases {
		tr, _ := primed()
		next := loopMsg(6, "claude:lab:b", "ok")
		c.mutate(&next)
		st := tr.Add(next)
		if st.Low || st.Streak != 0 || st.Signal != c.signal {
			t.Errorf("%s: state %+v, want reset by %q", c.name, st, c.signal)
		}
		// and the loop starts counting from zero again
		if again := tr.Add(loopMsg(7, "claude:lab:a", "ok")); again.Streak != 0 {
			t.Errorf("%s: streak after the reset = %d, want 0", c.name, again.Streak)
		}
	}
}

func TestRepeatedMarkersAreNotNew(t *testing.T) {
	// The same hash, number and link echoed back is padding, not content.
	msgs := []CoordMessage{
		loopMsg(1, "a", "review 9d9399e, 42 ms, https://example.org/a"),
		loopMsg(2, "b", "ok, 9d9399e"),
		loopMsg(3, "a", "ok 42 ms"),
		loopMsg(4, "b", "ok https://example.org/a"),
		loopMsg(5, "a", "ok, 9d9399e"),
	}
	states := loopRun(msgs)
	if states[4].Streak != 3 || !states[4].Low {
		t.Fatalf("echoed markers: %+v", states)
	}
}

func TestMixedConversationResetsThenWakes(t *testing.T) {
	tr, _ := primed()
	if got := tr.Add(loopMsg(6, "claude:lab:b", "ok")); !got.Hold() {
		t.Fatalf("expected a hold before the reset, got %+v", got)
	}
	got := tr.Add(loopMsg(7, "claude:lab:a", "fix in c0ffee1"))
	if got.Hold() || got.Warn() || got.Streak != 0 {
		t.Fatalf("a commit hash must lift the brake: %+v", got)
	}
}

func TestThirdParticipantEndsTheRun(t *testing.T) {
	tr, _ := primed()
	got := tr.Add(loopMsg(6, "claude:lab:c", "ok"))
	if got.Streak != 0 || !got.Low {
		t.Fatalf("a third participant starts a new pair: %+v", got)
	}
}

func TestSameSenderTalkingOnIsNoRound(t *testing.T) {
	var tr LoopTracker
	var last LoopState
	for i := 0; i < 8; i++ {
		last = tr.Add(loopMsg(i+1, "a", "ok"))
	}
	if last.Streak != 0 {
		t.Fatalf("a monologue is not a back-and-forth: %+v", last)
	}
}

func TestStreakDecaysAfterQuiet(t *testing.T) {
	tr, _ := primed()
	late := loopMsg(6, "claude:lab:b", "ok")
	late.CreatedAt = loopT0.Add(10*time.Minute + 2*time.Minute + 5*time.Minute).Format(time.RFC3339)
	if got := tr.Add(late); got.Streak != 0 {
		t.Fatalf("streak must decay after %s: %+v", LoopDecay, got)
	}
}

func TestGuardNoticesAreInvisible(t *testing.T) {
	tr, _ := primed()
	notice := loopMsg(6, "claude:lab:a", "Wake calls paused")
	notice.Kind = LoopNoticeKind
	if got := tr.Add(notice); got.Streak != 4 {
		t.Fatalf("a notice changed the streak: %+v", got)
	}
}

func TestLoopIsDeterministic(t *testing.T) {
	msgs := append(pingPong(10), loopMsg(11, "a", "fix 1a2b3c4"), loopMsg(12, "b", "ok"))
	first := fmt.Sprint(loopRun(msgs))
	for i := 0; i < 20; i++ { // map iteration order must not leak into the result
		if got := fmt.Sprint(loopRun(msgs)); got != first {
			t.Fatalf("run %d differs:\n%s\n%s", i, got, first)
		}
	}
}

func TestLoopPaddingWithRandomWordsIsAKnownBypass(t *testing.T) {
	// Documented limit, not a promise: padding every message with fresh words
	// looks like content to a token rule. The send limit stays the backstop.
	words := []string{"alpha bravo charlie delta echo foxtrot golf hotel", "india juliet kilo lima mike november oscar papa",
		"quebec romeo sierra tango uniform victor whiskey xray", "yankee zulu amber basalt cobalt dune ember fjord"}
	var msgs []CoordMessage
	for i, w := range words {
		sender := []string{"a", "b"}[i%2]
		msgs = append(msgs, loopMsg(i+1, sender, "ok "+w))
	}
	if got := loopRun(msgs)[len(msgs)-1]; got.Streak != 0 {
		t.Skipf("padding no longer evades the rule: %+v", got)
	}
}

func TestLoopModeFromEnv(t *testing.T) {
	for env, want := range map[string]LoopMode{"": LoopObserve, "observe": LoopObserve, "ENFORCE": LoopEnforce, " enforce ": LoopEnforce, "bogus": LoopObserve} {
		t.Setenv("GHOSTTREE_LOOP_GUARD", env)
		if got := LoopModeFromEnv(); got != want {
			t.Errorf("%q -> %q, want %q", env, got, want)
		}
	}
}

// experiment loads a room dump of the 2026-10-01 agent chat experiments. The
// first line is the header; sender person:* is human, the rest agent, unless
// the dump has an author_kind column.
func experiment(t *testing.T, file string) []CoordMessage {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var msgs []CoordMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan()
	hasKind := strings.Contains(sc.Text(), "author_kind")
	for sc.Scan() {
		cols := strings.SplitN(sc.Text(), "|", map[bool]int{true: 7, false: 6}[hasKind])
		id, _ := strconv.Atoi(cols[0])
		m := CoordMessage{ID: int64(id), CreatedAt: cols[1], SenderExternalID: cols[2], AuthorKind: AuthorAgent}
		rest := cols[3:]
		if hasKind {
			m.AuthorKind, rest = cols[3], cols[4:]
		} else if strings.HasPrefix(m.SenderExternalID, "person:") {
			m.AuthorKind = AuthorHuman
		}
		if rest[0] != "" {
			m.ReplyTo, _ = strconv.ParseInt(rest[0], 10, 64)
		}
		m.Intent, m.Body = rest[1], rest[2]
		msgs = append(msgs, m)
	}
	return msgs
}

// The positive controls: real, productive builder/reviewer conversations. No
// message of them may be slowed, whatever the agents do next.
func TestProductiveExperimentsAreNeverSlowed(t *testing.T) {
	for file, count := range map[string]int{"loop_experiment_2383.psv": 13, "loop_experiment_2384.psv": 9} {
		msgs := experiment(t, file)
		if len(msgs) != count {
			t.Fatalf("%s: %d messages, want %d", file, len(msgs), count)
		}
		max := 0
		var streaks []int
		for _, s := range loopRun(msgs) {
			streaks = append(streaks, s.Streak)
			if s.Streak > max {
				max = s.Streak
			}
		}
		t.Logf("%s streaks: %v", file, streaks)
		if max >= LoopWarnStreak {
			t.Errorf("%s: productive conversation reached streak %d (%v)", file, max, streaks)
		}
	}
}

// A productive conversation that goes on: the experiment repeated with fresh
// hashes stays at the same low streaks however many rounds follow.
func TestProductiveConversationStaysFreeOverFortyRounds(t *testing.T) {
	base := experiment(t, "loop_experiment_2383.psv")
	var all []CoordMessage
	for round := 0; round < 40; round++ {
		for _, m := range base {
			m.ID = int64(len(all) + 1)
			m.CreatedAt = loopT0.Add(time.Duration(len(all)) * 20 * time.Second).Format(time.RFC3339)
			m.Body = strings.NewReplacer("f8afe10", fmt.Sprintf("%07xa", round*3+1), "b90b019", fmt.Sprintf("%07xb", round*3+2),
				"4bc1d92", fmt.Sprintf("%07xc", round*3+3)).Replace(m.Body)
			all = append(all, m)
		}
	}
	for i, s := range loopRun(all) {
		if s.Streak >= LoopWarnStreak {
			t.Fatalf("message %d reached streak %d", i, s.Streak)
		}
	}
}
