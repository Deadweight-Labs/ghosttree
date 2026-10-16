package main

import (
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestFileStateRemembersChecksAndUsageAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := openFileState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGardenCheck("p", store.GardenCheck{KnowledgeID: 7, CheckedAt: "2026-10-16T03:05:00Z", Commit: "abc", EntryHash: "h", Verdict: "holds"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLLMUsage("garden", "2026-10-16", llmusage.Usage{Calls: 2, InputTokens: 10, CostMicroUSD: 500}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLLMUsage("garden", "2026-10-16", llmusage.Usage{Calls: 1, CostMicroUSD: 250}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLLMUsage("garden", "2026-10-02", llmusage.Usage{Calls: 4, CostMicroUSD: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLLMUsage("explain", "2026-10-16", llmusage.Usage{Calls: 9}); err != nil {
		t.Fatal(err)
	}

	again, err := openFileState(path)
	if err != nil {
		t.Fatal(err)
	}
	checks, _ := again.GardenChecks("p")
	if c := checks[7]; c.Verdict != "holds" || c.Commit != "abc" {
		t.Errorf("check lost: %+v", checks)
	}
	if other, _ := again.GardenChecks("q"); len(other) != 0 {
		t.Errorf("another project sees checks: %+v", other)
	}
	day, _ := again.LLMUsageOn("garden", "2026-10-16")
	if day.Calls != 3 || day.CostMicroUSD != 750 || day.InputTokens != 10 {
		t.Errorf("day: %+v", day)
	}
	if none, _ := again.LLMUsageOn("garden", "2026-10-17"); none.Calls != 0 || none.Day != "2026-10-17" {
		t.Errorf("empty day: %+v", none)
	}
	month, _ := again.LLMUsageMonth("garden", "2026-10")
	if month.Calls != 7 || month.CostMicroUSD != 1750 {
		t.Errorf("month: %+v", month)
	}
	if err := again.AddLLMUsage("", "2026-10-16", llmusage.Usage{}); err == nil {
		t.Error("usage without a job accepted")
	}
}

func TestFileStateNeedsAnAbsolutePathItCanWrite(t *testing.T) {
	if _, err := openFileState("relativ.json"); err == nil {
		t.Error("relative path accepted")
	}
	if _, err := openFileState("/nicht/da/garantiert/state.json"); err == nil {
		t.Error("an unwritable state file would run the gardener unmetered")
	}
}
