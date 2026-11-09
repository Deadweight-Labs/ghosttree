package web

import (
	"testing"
	"time"
)

// Many link flows of one account must not push out the flows of another, and a
// full table loses its oldest entry only.
func TestSourceFlowsOneAccountCannotEvictOthers(t *testing.T) {
	f := newSourceFlows()
	clock := time.Unix(1_000_000, 0)
	f.now = func() time.Time { clock = clock.Add(time.Millisecond); return clock }
	owner, err := f.start("person:1", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 0; i < maxSourceFlows*2; i++ {
		if last, err = f.startLink("person:2"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := f.get(owner, "person:1"); !ok {
		t.Fatal("the owner's org flow was evicted by another account")
	}
	n := 0
	for _, v := range f.flows {
		if v.account == "person:2" {
			n++
		}
	}
	if n > maxSourceFlowsPerAccount {
		t.Fatalf("one account holds %d open flows", n)
	}
	if _, ok := f.take(last, "person:2"); !ok {
		t.Fatal("the newest flow of an account was dropped")
	}
}
