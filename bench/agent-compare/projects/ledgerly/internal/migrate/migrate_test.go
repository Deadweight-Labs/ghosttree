package migrate

import "testing"

func TestRepositoryMigrationsAreValid(t *testing.T) {
	migs, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) < 6 {
		t.Fatalf("expected at least 6 migrations, got %d", len(migs))
	}
}
