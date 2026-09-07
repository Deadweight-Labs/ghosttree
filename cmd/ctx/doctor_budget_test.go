package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/hookbudget"
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
	for _, want := range []string{"321/24000", "4 unbestätigt", "seit", "UNVERIFIED"} {
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
	for _, want := range []string{"UNVERIFIED", "24000", "keine", "unbekannt"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
}
