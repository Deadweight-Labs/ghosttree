package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Der Check-Run „Lastenheft“ am PR (REQ-601 AC-3). Der Server schreibt ihn über die
// GitHub-App, wenn ein PR mit Paket aufgeht oder einen neuen Commit bekommt, und
// wieder, wenn sich der Stand der Kriterien ändert. Das braucht das Recht
// „Checks: write“. Hat die App es nicht, bleibt alles, wie es ist: kein Fehler, kein
// Log je PR, und ein Versuch pro Stunde und Installation, damit ein später erteiltes
// Recht ohne Neustart wirkt.

const (
	lastenheftCheckName = "ghosttree Lastenheft"
	// lastenheftRetryNoRight: so lange bleibt eine Installation ohne das Recht in Ruhe.
	lastenheftRetryNoRight = time.Hour
	// lastenheftSweepEvery: so oft sucht der Server nach geändertem Stand.
	lastenheftSweepEvery = 5 * time.Minute
	lastenheftJobTimeout = 30 * time.Second
	maxLastenheftRows    = 50
)

// checkPublisher ist, was das Schreiben von der GitHub-App braucht.
type checkPublisher interface {
	PublishCheckRun(ctx context.Context, installationID int64, repo, headSHA string, run forge.CheckRun) error
}

type lastenheftPublisher struct {
	st      *store.Store
	pub     checkPublisher
	baseURL string
	onError func(error)
	now     func() time.Time

	mu      sync.Mutex
	noRight map[int64]time.Time
}

func newLastenheftPublisher(st *store.Store, pub checkPublisher, baseURL string, onError func(error)) *lastenheftPublisher {
	return &lastenheftPublisher{st: st, pub: pub, baseURL: strings.TrimRight(baseURL, "/"), onError: onError, now: time.Now, noRight: map[int64]time.Time{}}
}

func (l *lastenheftPublisher) report(err error) {
	if l.onError != nil {
		l.onError(err)
	}
}

// resting: die Installation hat das Recht (noch) nicht und ist in Ruhe.
func (l *lastenheftPublisher) resting(installationID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.noRight[installationID]
	if !ok {
		return false
	}
	if l.now().Sub(at) >= lastenheftRetryNoRight {
		delete(l.noRight, installationID)
		return false
	}
	return true
}

func (l *lastenheftPublisher) rest(installationID int64) {
	l.mu.Lock()
	l.noRight[installationID] = l.now()
	l.mu.Unlock()
}

// publish schreibt den Check-Run eines PRs, wenn sich Commit oder Stand seit dem
// letzten Mal geändert haben. Es gibt zurück, ob etwas hinausging.
func (l *lastenheftPublisher) publish(ctx context.Context, t store.LastenheftTarget) (bool, error) {
	if t.HeadSHA == "" || t.InstallationID <= 0 || l.resting(t.InstallationID) {
		return false, nil
	}
	lh, ok, err := l.st.PullLastenheft(t.Project, t.PR)
	if err != nil || !ok {
		return false, err
	}
	digest := lh.Digest()
	sha, was, err := l.st.LastenheftPublished(t.Project, t.PR)
	if err != nil {
		return false, err
	}
	if sha == t.HeadSHA && was == digest {
		return false, nil
	}
	err = l.pub.PublishCheckRun(ctx, t.InstallationID, t.Project, t.HeadSHA, renderLastenheft(lh, l.baseURL))
	switch {
	case errors.Is(err, forge.ErrPermissionMissing):
		l.rest(t.InstallationID)
		return false, nil
	case err != nil:
		return false, err
	}
	return true, l.st.MarkLastenheftPublished(t.Project, t.PR, t.HeadSHA, digest)
}

// sweep geht alle offenen PRs mit Paket durch und schreibt, was sich geändert hat.
// Ruht eine Installation, überspringt es ihre PRs, ohne zu fragen.
func (l *lastenheftPublisher) sweep(ctx context.Context) {
	targets, err := l.st.LastenheftTargets()
	if err != nil {
		l.report(fmt.Errorf("check run targets: %w", err))
		return
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, lastenheftJobTimeout)
		_, err := l.publish(cctx, t)
		cancel()
		if err != nil {
			l.report(fmt.Errorf("check run of %s#%d: %w", t.Project, t.PR, err))
			var rl *forge.RateLimitError
			if errors.As(err, &rl) {
				return
			}
		}
	}
}

// loop ruft sweep, bis ctx endet.
func (l *lastenheftPublisher) loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.sweep(ctx)
		}
	}
}

// renderLastenheft macht aus dem Stand den Check-Run. Er zeigt nur Nummern und
// Zahlen: der Check-Run steht auf GitHub, wo ihn jeder liest, der den PR liest, und
// Titel und Kriterien eines Requests gehören dem, der ihn in ghosttree lesen darf.
func renderLastenheft(lh store.Lastenheft, baseURL string) forge.CheckRun {
	requests, open, met, waived := lh.Required()
	run := forge.CheckRun{Name: lastenheftCheckName, Conclusion: lh.Conclusion()}
	repo := strings.TrimPrefix(lh.Project, "github.com/")
	link := func(path string) string {
		if baseURL == "" {
			return ""
		}
		return baseURL + "/ui/" + repo + path
	}
	run.DetailsURL = link("/packages/" + fmt.Sprint(lh.PackageID))
	switch {
	case requests == 0:
		run.Title = "No mandatory request in the work package"
	case open == 0:
		run.Title = fmt.Sprintf("All %d mandatory criteria met or waived", met+waived)
	default:
		run.Title = fmt.Sprintf("%d of %d mandatory criteria still open", open, open+met+waived)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Requirements list of this pull request: %d mandatory requests, %d criteria met, %d waived, %d open.\n\n", requests, met, waived, open)
	b.WriteString("| Request | Role | Criteria |\n|---|---|---|\n")
	for i, m := range lh.Members {
		if i == maxLastenheftRows {
			fmt.Fprintf(&b, "| … | | %d more requests |\n", len(lh.Members)-maxLastenheftRows)
			break
		}
		ref := fmt.Sprintf("REQ-%d", m.RequestID)
		if u := link("/issues/" + fmt.Sprint(m.RequestID)); u != "" {
			ref = fmt.Sprintf("[%s](%s)", ref, u)
		}
		role := "mandatory"
		if m.Role != "pflicht" {
			role = "optional"
		}
		fmt.Fprintf(&b, "| %s | %s | %d met, %d waived, %d open |\n", ref, role, m.Met, m.Waived, m.Open)
	}
	if u := run.DetailsURL; u != "" {
		fmt.Fprintf(&b, "\nThe criteria and their evidence are on the [work package page](%s); you need access to the repository in ghosttree to read them.\n", u)
	}
	run.Summary = b.String()
	return run
}

// publishAsync schreibt den Check-Run nach einem Ereignis im Hintergrund, mit eigener
// Frist: der Webhook antwortet vorher.
func (l *lastenheftPublisher) publishAsync(jobs *detailJobs, t store.LastenheftTarget) {
	jobs.start(t.Project+"#lastenheft", func() {
		ctx, cancel := context.WithTimeout(context.Background(), lastenheftJobTimeout)
		defer cancel()
		if _, err := l.publish(ctx, t); err != nil {
			l.report(fmt.Errorf("check run of %s#%d: %w", t.Project, t.PR, err))
		}
	})
}
