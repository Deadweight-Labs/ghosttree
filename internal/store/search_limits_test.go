package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchTermsAreBounded(t *testing.T) {
	var words []string
	for i := 0; i < 400; i++ {
		words = append(words, "wort"+strconv.Itoa(i))
	}
	if got := len(searchTerms(strings.Join(words, " "))); got != maxSearchTerms {
		t.Fatalf("terms = %d, want %d", got, maxSearchTerms)
	}
	if strings.Count(ftsQuery(strings.Join(words, " ")), " OR ") != maxSearchTerms-1 {
		t.Fatal("the OR expression is not bounded")
	}
}

// Ein Konto, das beide Plätze hält, lässt ein anderes nicht in search_busy laufen.
func TestSearchSlotsAreSharedBetweenAccounts(t *testing.T) {
	st := openTest(t)
	g := &st.embedShared().gate
	g.init(st)
	n := cap(g.slots)
	ctx := context.Background()
	var releases []func()
	// Allein darf das Konto alle Plätze nehmen.
	for i := 0; i < n; i++ {
		release, ok := st.AcquireSearchSlotFor(ctx, "person:1", 50*time.Millisecond)
		if !ok {
			t.Fatalf("slot %d refused while nobody else waits", i)
		}
		releases = append(releases, release)
	}
	// Ein anderes Konto wartet; sobald ein Platz frei wird, bekommt es ihn und nicht das erste.
	got := make(chan bool, 1)
	go func() {
		release, ok := st.AcquireSearchSlotFor(ctx, "person:2", time.Second)
		if ok {
			defer release()
		}
		got <- ok
	}()
	time.Sleep(30 * time.Millisecond)
	greedy := make(chan bool, 1)
	go func() {
		release, ok := st.AcquireSearchSlotFor(ctx, "person:1", 300*time.Millisecond)
		if ok {
			release()
		}
		greedy <- ok
	}()
	time.Sleep(30 * time.Millisecond)
	releases[0]()
	if ok := <-got; !ok {
		t.Fatal("the waiting account did not get the freed place")
	}
	for _, r := range releases[1:] {
		r()
	}
	<-greedy
}
