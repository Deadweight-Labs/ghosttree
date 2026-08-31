package storebench

import "testing"

func TestPresetsAreDeterministicAndMonorepoTargetsProductionSize(t *testing.T) {
	for _, name := range []string{"small", "medium", "monorepo"} {
		left, err := Preset(name)
		if err != nil {
			t.Fatal(err)
		}
		right, err := Preset(name)
		if err != nil {
			t.Fatal(err)
		}
		if left != right {
			t.Fatalf("preset %s drifted", name)
		}
	}
	monorepo, err := Preset("monorepo")
	if err != nil {
		t.Fatal(err)
	}
	size := LogicalPayloadBytes(monorepo)
	if size < 11*int64(1<<30)/10 || size > 12*int64(1<<30)/10 {
		t.Fatalf("monorepo logical bytes = %d, want 1.10-1.20 GiB", size)
	}
}

func TestPresetRejectsUnknownName(t *testing.T) {
	if _, err := Preset("enormous"); err == nil {
		t.Fatal("unknown preset accepted")
	}
}
