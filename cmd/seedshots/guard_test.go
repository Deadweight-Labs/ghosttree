package main

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestSeedshotsRefusesADatabaseWithPeople(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := requireEmptyDatabase(st, "fresh.db"); err != nil {
		t.Fatalf("an empty database is refused: %v", err)
	}
	if _, err := st.AddAccount("Robin", "", true); err != nil {
		t.Fatal(err)
	}
	err = requireEmptyDatabase(st, "live.db")
	if err == nil || !strings.Contains(err.Error(), "live.db") || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a database with accounts is not refused clearly: %v", err)
	}
}
