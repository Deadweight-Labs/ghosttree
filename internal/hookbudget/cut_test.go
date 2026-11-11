package hookbudget

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/profile"
)

// runbookText is a runbook the way the server delivers it: a header, steps, and
// the closing line that asks for the report.
func runbookText(name string, steps int) string {
	var b strings.Builder
	b.WriteString("[runbook " + name + ", triggered by keyword go]\n# Runbook " + name + " (version 1)\n")
	for i := range steps {
		b.WriteString(strings.Repeat("s", 40) + " step " + string(rune('a'+i%26)) + "\n")
	}
	b.WriteString("\nWhen done, report the outcome: ctx runbook report " + name + " ok|failed.\n")
	return b.String()
}

func TestCutAtLineKeepsWholeLines(t *testing.T) {
	text := "first line\nsecond line\nthird line\n"
	for max, want := range map[int]string{
		11: "first line\n",
		10: "first line", // the line break is the next character: the line is whole
		15: "first line\n",
		21: "first line\n",
		22: "first line\nsecond line", // again the line break is next
		23: "first line\nsecond line\n",
	} {
		if got := cutAtLine(text, max); got != want {
			t.Errorf("cutAtLine(max %d) = %q, want %q", max, got, want)
		}
	}
	if got := cutAtLine(text, 1000); got != text {
		t.Errorf("a text that fits was changed: %q", got)
	}
	// No line break to cut at: the cut is where the budget ends, as before.
	if got := cutAtLine(strings.Repeat("界", 50), 20); got != strings.Repeat("界", 20) {
		t.Errorf("a single long line: %q", got)
	}
}

func TestCutAtLineNeverLeavesHalfARunbook(t *testing.T) {
	rb := runbookText("rollout", 10)
	text := "## Known context\n- a pitfall\n\n" + rb + "\nmore knowledge\n"
	start := strings.Index(text, "[runbook ")
	for max := start; max < len(text); max++ {
		got := cutAtLine(text, max)
		if !strings.HasPrefix(text, got) || utf8.RuneCountInString(got) > max {
			t.Fatalf("max %d: %q is no prefix of the text or longer than max", max, got)
		}
		// Either the whole runbook is in or no part of it.
		if strings.Contains(got, "[runbook ") && !strings.Contains(got, "ok|failed.") {
			t.Fatalf("max %d: half a runbook:\n%s", max, got)
		}
		if !strings.Contains(got, "[runbook ") && strings.Contains(got, "# Runbook") {
			t.Fatalf("max %d: a runbook without its header:\n%s", max, got)
		}
	}
	// A cut that leaves the runbook whole keeps it.
	end := strings.Index(text, "ok|failed.\n") + len("ok|failed.")
	if got := cutAtLine(text, end); !strings.HasSuffix(got, "ok|failed.") {
		t.Errorf("a runbook that fits was dropped: %q", got)
	}
}

// REQ-643 review: under lean a runbook that does not fit in what the budget has
// left is not delivered half. The budget cuts after a whole line, and with the
// notice the cut is still within the limit.
func TestLeanBudgetDoesNotDeliverARunbookHalf(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(profile.EnvVar, "lean")
	deliver := func(text string) string {
		var got string
		if err := Deliver("lean-runbooks", text, func(s string) error { got = s; return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	bootstrap := "## Known context (ghosttree)\n" + strings.Repeat("- a rule of the project that binds the session\n", 60)
	first := deliver(bootstrap)
	if utf8.RuneCountInString(first) > 2900 {
		t.Fatalf("the fixture's bootstrap is too long: %d", utf8.RuneCountInString(first))
	}
	// Two runbooks of about 1,800 characters each; one fits next to the
	// bootstrap in 6,000, the second does not.
	rbs := []string{runbookText("one", 36), runbookText("two", 36)}
	delivered := 0
	for _, rb := range rbs {
		before, _ := Remaining("lean-runbooks", ChannelMemory)
		out := deliver(rb)
		if strings.Contains(out, "[runbook ") && !strings.Contains(out, "ok|failed.") {
			t.Fatalf("a runbook went out half:\n%s", out)
		}
		if strings.Contains(out, "ok|failed.") {
			delivered++
		} else if utf8.RuneCountInString(rb) <= before {
			t.Fatalf("a runbook that fit in the %d characters left was not delivered:\n%s", before, out)
		}
	}
	if delivered != 1 {
		t.Errorf("%d runbooks delivered whole, want exactly the one that fits", delivered)
	}
}

func TestRemainingFollowsTheProfileAndWhatWasDelivered(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for profileValue, limit := range map[string]int{"": Limit, "lean": profile.LeanHookBudget} {
		t.Setenv(profile.EnvVar, profileValue)
		session := "remaining-" + profileValue
		fresh, ok := Remaining(session, ChannelMemory)
		if want := limit - utf8.RuneCountInString(noticeFor(limit)); !ok || fresh != want {
			t.Fatalf("profile %q: %d, %v before anything was delivered, want %d", profileValue, fresh, ok, want)
		}
		if err := Deliver(session, strings.Repeat("x", 1000), func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if after, _ := Remaining(session, ChannelMemory); after != fresh-1000 {
			t.Errorf("profile %q: %d left after 1000 characters, want %d", profileValue, after, fresh-1000)
		}
		// What Remaining says fits is delivered whole.
		rest, _ := Remaining(session, ChannelMemory)
		text := strings.Repeat("y", rest)
		var got string
		_ = Deliver(session, text, func(s string) error { got = s; return nil })
		if got != text {
			t.Errorf("profile %q: a text of exactly the remaining %d characters was cut (%d)", profileValue, rest, utf8.RuneCountInString(got))
		}
		if gone, ok := Remaining(session, ChannelMemory); !ok || gone != 0 {
			t.Errorf("profile %q: %d left after the budget was used up", profileValue, gone)
		}
	}
	if _, ok := Remaining("", ChannelMemory); ok {
		t.Error("a session without an identity has no budget to report")
	}
}
