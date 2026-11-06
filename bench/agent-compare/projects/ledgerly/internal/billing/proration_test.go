package billing

import (
	"testing"
	"time"
)

func TestProrate(t *testing.T) {
	got, err := Prorate(14900, 10, 31)
	if err != nil || got != 4806 {
		t.Fatalf("got %d %v", got, err)
	}
	if _, err := Prorate(100, 5, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestPlanChangeCredit(t *testing.T) {
	got, err := PlanChangeCredit(3000, 15, 30)
	if err != nil || got != 1500 {
		t.Fatalf("got %d %v", got, err)
	}
}

func TestDaysBetween(t *testing.T) {
	a := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if DaysBetween(a, a.AddDate(0, 1, 0)) != 31 {
		t.Fatal("march")
	}
}
