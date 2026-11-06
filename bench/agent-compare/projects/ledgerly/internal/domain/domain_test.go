package domain

import "testing"

func TestPublicIDRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 2, 42, 1000, 987654321} {
		c := Customer{ID: id}
		got, ok := ParsePublicID("cus", c.PublicID())
		if !ok || got != id {
			t.Fatalf("id %d: %q -> %d %v", id, c.PublicID(), got, ok)
		}
	}
	if _, ok := ParsePublicID("acc", Customer{ID: 1}.PublicID()); ok {
		t.Fatal("prefix mismatch accepted")
	}
}

func TestPublicIDsDoNotLookSequential(t *testing.T) {
	a, b := Invoice{ID: 1}.PublicID(), Invoice{ID: 2}.PublicID()
	if a[len(a)-1] == b[len(b)-1]-1 {
		t.Fatalf("ids look sequential: %s %s", a, b)
	}
}
