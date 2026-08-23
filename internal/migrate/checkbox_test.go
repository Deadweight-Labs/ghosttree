package migrate

import (
	"strings"
	"testing"
)

func TestParseStepsSeparatesDoneFromOpen(t *testing.T) {
	md := "# Plan\n\n- [x] **Step 1: Write the test**\n\nsome prose\n\n- [ ] **Step 2: Implement**\n- [X] Step 3: Commit\n- not a checkbox\n"
	got := ParseSteps(md)
	if len(got) != 3 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	if !got[0].Done || got[0].Line != 3 {
		t.Errorf("step 1: %+v", got[0])
	}
	if got[1].Done || !got[2].Done {
		t.Errorf("done flags: %+v", got)
	}
	if strings.Contains(got[0].Text, "**") {
		t.Errorf("emphasis remains: %q", got[0].Text)
	}
}
