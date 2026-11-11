package hookbudget

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/profile"
)

// A lean session gets the hook budget of 6,000 characters, notice included, and
// says so in the notice; the memory channel alone follows the profile.
func TestLeanSessionHasTheSmallerBudget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(profile.EnvVar, "lean")
	total, notices := 0, 0
	for range 40 {
		if err := Deliver("lean-session", strings.Repeat("a", 400), func(text string) error {
			total += utf8.RuneCountInString(text)
			notices += strings.Count(text, "Session hook context budget reached (6000 characters)")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if total != profile.LeanHookBudget || notices != 1 {
		t.Fatalf("a lean session got %d characters and %d notices, want %d and 1", total, notices, profile.LeanHookBudget)
	}
	if got := LimitFor(ChannelCoord, profile.Lean); got != CoordLimit {
		t.Errorf("the coordination channel follows the profile: %d, want %d", got, CoordLimit)
	}
}

func TestWithoutAProfileTheBudgetIsTheFullOne(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(profile.EnvVar, "")
	if LimitFor(ChannelMemory, profile.FromEnv()) != Limit || Limit != 24000 {
		t.Fatalf("limit %d, want 24000", LimitFor(ChannelMemory, profile.FromEnv()))
	}
	total := 0
	for range 100 {
		_ = Deliver("full-session", strings.Repeat("b", 400), func(text string) error {
			total += utf8.RuneCountInString(text)
			return nil
		})
	}
	if total != Limit {
		t.Fatalf("a full session got %d characters, want %d", total, Limit)
	}
	if !strings.Contains(notice, "(24000 characters)") {
		t.Errorf("the notice of a full session changed: %q", notice)
	}
}
