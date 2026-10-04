package web

import (
	"strconv"
	"testing"
	"time"
)

func TestInviteLinksStayWithTheirCreatorUntilTheyExpire(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	l := newInviteLinks()
	l.now = func() time.Time { return now }
	l.put(7, "person:1", "code-7", true, now.Add(time.Hour))
	if got, ok := l.get(7, "person:1"); !ok || got.code != "code-7" || !got.project {
		t.Fatalf("creator: %+v %v", got, ok)
	}
	if _, ok := l.get(7, "person:2"); ok {
		t.Fatal("another account reads the link")
	}
	if _, ok := l.get(8, "person:1"); ok {
		t.Fatal("unknown invitation")
	}
	l.drop(7)
	if _, ok := l.get(7, "person:1"); ok {
		t.Fatal("dropped link still readable")
	}
	l.put(9, "person:1", "code-9", false, now.Add(time.Hour))
	now = now.Add(2 * time.Hour)
	if _, ok := l.get(9, "person:1"); ok {
		t.Fatal("expired link still readable")
	}
}

func TestInviteLinksAreBounded(t *testing.T) {
	now := time.Now()
	l := newInviteLinks()
	for i := int64(1); i <= maxInviteLinks+50; i++ {
		l.put(i, "person:"+strconv.FormatInt(i%20, 10), "c", false, now.Add(time.Hour))
	}
	if n := l.size(); n != maxInviteLinks {
		t.Fatalf("size %d", n)
	}
	if _, ok := l.get(1, "person:1"); ok {
		t.Fatal("the oldest link should have been evicted")
	}
	if _, ok := l.get(maxInviteLinks+50, "person:"+strconv.FormatInt((maxInviteLinks+50)%20, 10)); !ok {
		t.Fatal("the newest link must stay")
	}
}

func TestOneCreatorCannotCrowdOutOthersLinks(t *testing.T) {
	now := time.Now()
	l := newInviteLinks()
	l.put(1, "person:2", "other", false, now.Add(time.Hour))
	for i := int64(100); i < 100+3*maxInviteLinksPerAccount; i++ {
		l.put(i, "person:1", "c", false, now.Add(time.Hour))
	}
	if _, ok := l.get(1, "person:2"); !ok {
		t.Fatal("another creator's link was pushed out")
	}
	if n := l.size(); n != maxInviteLinksPerAccount+1 {
		t.Fatalf("size %d, want %d", n, maxInviteLinksPerAccount+1)
	}
	if _, ok := l.get(100, "person:1"); ok {
		t.Fatal("the creator's oldest link should have been dropped")
	}
	if _, ok := l.get(100+3*maxInviteLinksPerAccount-1, "person:1"); !ok {
		t.Fatal("the creator's newest link must stay")
	}
}
