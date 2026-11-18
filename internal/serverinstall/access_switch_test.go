package serverinstall

import "testing"

func TestAccessIsOffOnlyWhenTheEnvironmentFileSaysZero(t *testing.T) {
	for text, want := range map[string]bool{
		"":                                  false,
		"GHOSTTREE_LISTEN=127.0.0.1:8474\n": false,
		"GHOSTTREE_ENFORCE_ACCESS=1\n":      false,
		"GHOSTTREE_ENFORCE_ACCESS=0\n":      true,
		"GHOSTTREE_ENFORCE_ACCESS=\"0\"\n":  true,
		"# GHOSTTREE_ENFORCE_ACCESS=0\n":    false,
		"GHOSTTREE_ENFORCE_ACCESS=0\nGHOSTTREE_ENFORCE_ACCESS=1\n": false,
	} {
		if got := accessSwitchedOff(text); got != want {
			t.Errorf("accessSwitchedOff(%q) = %v, want %v", text, got, want)
		}
	}
}
