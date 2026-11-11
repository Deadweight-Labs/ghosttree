package store

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Lastmessung der Meldung von Git-Vorgängen (Review von PR #242): GHOSTTREE_GIT_BENCH=1
// schaltet sie ein und gibt die Zeiten aus; ohne sie läuft nur die schnelle
// Fassung mit den Grenzen der Tests.

func benchEnabled() bool { return os.Getenv("GHOSTTREE_GIT_BENCH") == "1" }

func hexSHA(n int) string {
	return fmt.Sprintf("%040x", uint64(n)*2654435761+0xabcdef)
}

// TestBenchFiveHundredEventsWithTwentyShas: ein Stapel mit der Höchstzahl an
// Vorgängen, jeder mit der Höchstzahl an SHAs; einmal mit den Grenzen je Session und
// einmal ohne sie (so mass der Review den alten Code: 11,7 s).
func TestBenchFiveHundredEventsWithTwentyShas(t *testing.T) {
	if !benchEnabled() {
		t.Skip("set GHOSTTREE_GIT_BENCH=1")
	}
	for _, uncapped := range []bool{false, true} {
		if uncapped {
			withLimits(t, func() {
				maxSessionGitEventsPerSession, maxSessionGitSHAsPerSession, maxSessionGitEventsPerDay = 1<<30, 1<<30, 1<<30
			})
		}
		f := gitFixture(t)
		events := make([]SessionGitEvent, 500)
		for i := range events {
			shas := make([]string, 20)
			for j := range shas {
				shas[j] = hexSHA(i*20 + j)
			}
			sec := i % 60
			events[i] = commitEvent(f.robin, i, fmt.Sprintf("2026-11-08T10:%02d:%02dZ", i/60, sec), fmt.Sprintf("2026-11-08T10:%02d:%02dZ", i/60, sec), "feat/a", shas...)
		}
		start := time.Now()
		res, err := f.st.RecordSessionGitEvents(1, "", events)
		t.Logf("500 events x 20 SHAs, limits off=%v: %v (res %+v, err %v)", uncapped, time.Since(start), res, err)
	}
}

// TestBenchOverlappingWindowsOnOnePull: 10 000 überlappende 30-Minuten-Fenster auf
// einem PR mit 250 Commits, in Stapeln zu 500 über 20 Sessions.
func TestBenchOverlappingWindowsOnOnePull(t *testing.T) {
	if !benchEnabled() {
		t.Skip("set GHOSTTREE_GIT_BENCH=1")
	}
	f := gitFixture(t)
	base, _ := time.Parse(time.RFC3339, "2026-11-08T10:00:00Z")
	var commits []PullCommit
	for k := 0; k < 250; k++ {
		commits = append(commits, PullCommit{SHA: hexSHA(100000 + k), Title: "c", CommittedAt: base.Add(15*time.Minute + time.Duration(k)*2*time.Second).Format(time.RFC3339)})
	}
	f.pull(t, 7, "feat/a", commits...)
	total := time.Now()
	for s := 0; s < 20; s++ {
		sess := addSession(t, f.st, fmt.Sprintf("bench-%d", s), 1, prProject, "", VisPrivate, chunksOf(userLine("2026-11-08T09:00:00Z", "x")))
		events := make([]SessionGitEvent, 500)
		for i := range events {
			st := base.Add(time.Duration(i) * time.Second)
			events[i] = commitEvent(sess, i, st.Format(time.RFC3339), st.Add(30*time.Minute).Format(time.RFC3339), "feat/a")
		}
		start := time.Now()
		res, err := f.st.RecordSessionGitEvents(1, "", events)
		t.Logf("request %d (500 windows): %v (res %+v, err %v)", s+1, time.Since(start), res, err)
		if time.Since(total) > 90*time.Second && os.Getenv("GHOSTTREE_GIT_BENCH_CAP") != "off" {
			t.Logf("stopped after %d windows: %v", (s+1)*500, time.Since(total))
			return
		}
	}
	t.Logf("10 000 windows on 250 commits: %v", time.Since(total))
}

var _ = strings.Repeat
