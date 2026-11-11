package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/hookbudget"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
)

func TestDoctorReportsActualHookTextAndPendingReservations(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := hookbudget.Deliver("doctor-session", strings.Repeat("界", 321), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = hookbudget.Deliver("doctor-session", "lost", func(string) error { return errors.New("stdout failed") })
	var out bytes.Buffer
	if code := cmdDoctor([]string{"--only", "budget"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, &out)
	}
	for _, want := range []string{"321 Zeichen ausgegeben (von bis zu 24.000 (lean: 6.000))", "4 unbestätigt", "seit", "UNVERIFIED", "Profil dieser Shell: full"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "doctor-session") {
		t.Fatal("raw session identity leaked")
	}
	before, _ := hookbudget.Recent()
	out.Reset()
	cmdDoctor([]string{"--only", "budget"}, &out)
	after, _ := hookbudget.Recent()
	if before[0] != after[0] {
		t.Fatal("doctor changed accounting")
	}
}

func TestDoctorBudgetWithoutReceiptsIsExplicitlyUnknown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	if code := cmdDoctor([]string{"--only", "budget"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, &out)
	}
	for _, want := range []string{"UNVERIFIED", "bis zu 24.000 (lean: 6.000)", "keine", "unbekannt"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
}

// REQ-643 review: the budget of a lean session is 6,000 characters, so doctor
// names both limits and the profile of the shell it runs in, and a lean shell
// is said to be one.
func TestDoctorNamesTheBudgetOfEveryProfile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for value, want := range map[string]string{"": "Profil dieser Shell: full", "lean": "Profil dieser Shell: lean"} {
		t.Setenv(profile.EnvVar, value)
		var out bytes.Buffer
		if code := cmdDoctor([]string{"--only", "budget"}, &out); code != 0 {
			t.Fatalf("exit %d: %s", code, &out)
		}
		for _, w := range []string{"bis zu 24.000 (lean: 6.000)", want} {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s=%q: missing %q:\n%s", profile.EnvVar, value, w, &out)
			}
		}
	}
	for n, want := range map[int]string{0: "0", 999: "999", 6000: "6.000", 24000: "24.000", 1234567: "1.234.567"} {
		if got := groupThousands(n); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", n, got, want)
		}
	}
}
