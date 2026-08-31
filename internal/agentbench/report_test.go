package agentbench

import (
	"bytes"
	"strings"
	"testing"
)

func TestExposureClassSplitsThreeWays(t *testing.T) {
	cases := []struct {
		exposure Exposure
		want     ExposureClass
	}{
		{Exposure{ExactPromptSeen: true}, ExposureRetrospective},
		{Exposure{EquivalentSeen: true}, ExposureRetrospective},
		{Exposure{AnswerFactSeen: true}, ExposureTransfer},
		{Exposure{}, ExposureProspective},
	}
	for _, c := range cases {
		if got := c.exposure.Class(); got != c.want {
			t.Fatalf("%+v: want %q, got %q", c.exposure, c.want, got)
		}
	}
}

func TestBuildReportSplitsByExposure(t *testing.T) {
	records := []RunRecord{
		{TaskID: "t1", Arm: ArmGhosttree, Exposure: Exposure{ExactPromptSeen: true},
			Score: Score{FactRecall: 1}},
		{TaskID: "t2", Arm: ArmGhosttree, Exposure: Exposure{AnswerFactSeen: true},
			Score: Score{FactRecall: 0.5}},
	}
	report := BuildReport(Campaign{Name: "pilot", Arms: []ArmName{ArmGhosttree}}, records)
	if len(report.ByExposure) != 2 {
		t.Fatalf("expected retrospective and transfer groups, got %+v", report.ByExposure)
	}
	for _, group := range report.ByExposure {
		if group.Runs != 1 {
			t.Fatalf("each group holds one run here: %+v", group)
		}
	}
}

func TestBuildReportCountsFailuresAndExcludesThemFromMeans(t *testing.T) {
	records := []RunRecord{
		{TaskID: "t1", Arm: ArmGhosttree, Category: CategoryLocalization, Score: Score{FactRecall: 1}},
		{TaskID: "t2", Arm: ArmGhosttree, Category: CategoryLocalization,
			Failure: FailureProduct, Score: Score{FactRecall: 0}},
	}
	report := BuildReport(Campaign{Name: "pilot"}, records)
	if report.Failures[FailureProduct] != 1 {
		t.Fatalf("failure not counted: %+v", report.Failures)
	}
	if len(report.ByCategory) != 1 || report.ByCategory[0].FactRecall != 1 {
		t.Fatalf("a failed run must not drag the mean down: %+v", report.ByCategory)
	}
}

func TestWriteMarkdownNamesTheEffectAndInterval(t *testing.T) {
	report := Report{Effects: []PairedEffect{
		{Treatment: ArmGhosttree, Control: ArmClaudeNative, Mean: 0.18, LowerCI: 0.04, UpperCI: 0.31, Tasks: 8},
	}}
	var buf bytes.Buffer
	if err := report.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"0.04", "0.31", "ghosttree", "claude-native"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown must state %q:\n%s", want, out)
		}
	}
}

func TestWriteJSONLEmitsOneLinePerRecord(t *testing.T) {
	report := Report{Records: []RunRecord{
		{TaskID: "t1", Arm: ArmGhosttree}, {TaskID: "t2", Arm: ArmClaudeNative},
	}}
	var buf bytes.Buffer
	if err := report.WriteJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two JSONL lines, got %d:\n%s", len(lines), buf.String())
	}
}

func TestWriteMarkdownNamesEveryArmsMemoryBuild(t *testing.T) {
	campaign := Campaign{
		Name: "pilot", RepoCommit: "a1b2c3d",
		Arms: []ArmName{ArmGhosttree, ArmClaudeNative},
		Builds: map[ArmName]MemoryBuild{
			ArmGhosttree: {
				SnapshotName: "pilot-2026-08-31", DatabaseSHA256: "abc123",
				SourceSessionCount: 184, ToolVersion: "0.2.0",
			},
			ArmClaudeNative: {DatabaseSHA256: "def456", SummarizerModel: "claude-opus-5"},
		},
	}
	var buf bytes.Buffer
	if err := (Report{Campaign: campaign}).WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Ohne diese Angaben ist die Frage "welchen Baum hast du gemessen"
	// unbeantwortbar, und der Bericht belegt nichts.
	for _, want := range []string{"pilot-2026-08-31", "abc123", "def456", "claude-opus-5", "184"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report must state %q:\n%s", want, out)
		}
	}
}

func TestWriteMarkdownReportsAnArmWithoutABuild(t *testing.T) {
	campaign := Campaign{Name: "pilot", Arms: []ArmName{ArmBare}}
	var buf bytes.Buffer
	if err := (Report{Campaign: campaign}).WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "bare") {
		t.Fatalf("an arm without a memory build must still be listed:\n%s", buf.String())
	}
}
