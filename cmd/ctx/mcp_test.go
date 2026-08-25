package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// Der Fall, den ein externer Review gefunden hat: timer.Stop() greift nicht
// mehr, wenn der Timer schon gefeuert hat. Dauert f laenger als die Ruhezeit,
// startet der naechste Termin einen zweiten Lauf NEBEN dem ersten — und weil
// beide im selben .tmp-Verzeichnis bauen, reisst das RemoveAll des einen dem
// anderen den Bau unter den Fuessen weg.
func TestDebounceNeverRunsTwoAtOnce(t *testing.T) {
	var inFlight, maxSeen, runs int32
	slow := func() {
		now := atomic.AddInt32(&inFlight, 1)
		for {
			seen := atomic.LoadInt32(&maxSeen)
			if now <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, now) {
				break
			}
		}
		atomic.AddInt32(&runs, 1)
		time.Sleep(80 * time.Millisecond) // laenger als die Ruhezeit unten
		atomic.AddInt32(&inFlight, -1)
	}

	trigger := debounce(10*time.Millisecond, slow)
	for i := 0; i < 6; i++ {
		trigger()
		time.Sleep(15 * time.Millisecond) // laesst jeden Timer feuern
	}
	time.Sleep(400 * time.Millisecond)

	if got := atomic.LoadInt32(&maxSeen); got > 1 {
		t.Fatalf("%d Laeufe gleichzeitig — sie bauen im selben .tmp und zerstoeren sich gegenseitig", got)
	}
	if atomic.LoadInt32(&runs) == 0 {
		t.Fatal("der Test hat gar nichts ausgeloest und beweist damit nichts")
	}
}

// Die Sammelwirkung selbst: ein Schwung Aufrufe wird zu einem Lauf.
func TestDebounceCollapsesABurstIntoOneRun(t *testing.T) {
	var runs int32
	trigger := debounce(40*time.Millisecond, func() { atomic.AddInt32(&runs, 1) })
	for i := 0; i < 18; i++ {
		trigger()
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("18 Beschreibungen am Stueck sollen einen Neuschrieb ergeben, waren %d", got)
	}
}
