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
//
// Der Check-Run steht auf GitHub und ist für jeden lesbar, der den PR lesen darf. Darum
// wird er nur geschrieben, wenn GitHub das Repo als privat meldet (store.RepoPrivate;
// ein öffentliches Repo mit privatem ghosttree-Projekt bekäme sonst Request-Nummern,
// Fortschritt und Paketnummer zu sehen), nie für ein leeres Paket, und Links stehen nur
// darin, wenn der Betreiber eine Adresse dafür gesetzt hat (--check-public-url).
//
// Es gibt zwei Bremsen: ein Commit, den GitHub nicht kennt (Force-Push), ist bis zum
// nächsten Head-Commit erledigt, und ein Ratenlimit hält die Installation bis zu dessen
// Reset an. Ein weiteres Schreiben am selben Commit aktualisiert den vorhandenen
// Check-Run, statt einen neuen anzulegen.

const (
	lastenheftCheckName = "ghosttree Lastenheft"
	// lastenheftRetryNoRight: so lange bleibt eine Installation ohne das Recht in Ruhe.
	lastenheftRetryNoRight = time.Hour
	// lastenheftRateFallback: so lange ruht eine Installation nach einem Ratenlimit,
	// dessen Reset GitHub nicht nannte oder schon verstrichen ist.
	lastenheftRateFallback = time.Minute
	// lastenheftSweepEvery: so oft sucht der Server nach geändertem Stand.
	lastenheftSweepEvery = 5 * time.Minute
	lastenheftJobTimeout = 30 * time.Second
	maxLastenheftRows    = 50
	// maxLastenheftFollowUps: so oft schreibt ein Auftrag nach, wenn sich der Head-Commit
	// während des Schreibens ändert.
	maxLastenheftFollowUps = 3
)

// checkPublisher ist, was das Schreiben von der GitHub-App braucht.
type checkPublisher interface {
	PublishCheckRun(ctx context.Context, installationID int64, repo, headSHA string, run forge.CheckRun, existingID int64) (int64, error)
}

type lastenheftPublisher struct {
	st      *store.Store
	pub     checkPublisher
	baseURL string
	onError func(error)
	now     func() time.Time

	mu sync.Mutex
	// pausedUntil: bis wann eine Installation in Ruhe bleibt (fehlendes Recht,
	// Ratenlimit).
	pausedUntil map[int64]time.Time
	// gone merkt je PR den Head-Commit, den GitHub nicht kennt.
	gone map[string]string
}

func newLastenheftPublisher(st *store.Store, pub checkPublisher, baseURL string, onError func(error)) *lastenheftPublisher {
	return &lastenheftPublisher{st: st, pub: pub, baseURL: strings.TrimRight(baseURL, "/"), onError: onError, now: time.Now, pausedUntil: map[int64]time.Time{}, gone: map[string]string{}}
}

func (l *lastenheftPublisher) report(err error) {
	if l.onError != nil {
		l.onError(err)
	}
}

// resting: die Installation ist in Ruhe (ohne das Recht oder im Ratenlimit).
func (l *lastenheftPublisher) resting(installationID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.pausedUntil[installationID]
	if !ok {
		return false
	}
	if !l.now().Before(until) {
		delete(l.pausedUntil, installationID)
		return false
	}
	return true
}

func (l *lastenheftPublisher) restUntil(installationID int64, until time.Time) {
	l.mu.Lock()
	if cur, ok := l.pausedUntil[installationID]; !ok || until.After(cur) {
		l.pausedUntil[installationID] = until
	}
	l.mu.Unlock()
}

func goneKey(project string, pr int64) string { return fmt.Sprintf("%s#%d", project, pr) }

func (l *lastenheftPublisher) isGone(t store.LastenheftTarget) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gone[goneKey(t.Project, t.PR)] == t.HeadSHA
}

func (l *lastenheftPublisher) markGone(t store.LastenheftTarget) {
	l.mu.Lock()
	l.gone[goneKey(t.Project, t.PR)] = t.HeadSHA
	l.mu.Unlock()
}

// publish schreibt den Check-Run eines PRs, wenn sich Commit oder Stand seit dem
// letzten Mal geändert haben. Es gibt zurück, ob etwas hinausging.
func (l *lastenheftPublisher) publish(ctx context.Context, t store.LastenheftTarget) (bool, error) {
	if t.HeadSHA == "" || t.InstallationID <= 0 || l.resting(t.InstallationID) || l.isGone(t) {
		return false, nil
	}
	private, err := l.st.RepoPrivate(t.Project)
	if err != nil || !private {
		return false, err
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
	// Ein leeres Paket hat nichts zu zeigen. Stand schon ein Check-Run da, wird er
	// noch auf den leeren Stand gebracht, damit dort keine alten Zahlen bleiben.
	if len(lh.Members) == 0 && was == "" {
		return false, nil
	}
	var existing int64
	if sha == t.HeadSHA {
		if existing, err = l.st.LastenheftCheckRunID(t.Project, t.PR); err != nil {
			return false, err
		}
	}
	id, err := l.pub.PublishCheckRun(ctx, t.InstallationID, t.Project, t.HeadSHA, renderLastenheft(lh, l.baseURL), existing)
	var rl *forge.RateLimitError
	switch {
	case errors.Is(err, forge.ErrPermissionMissing):
		l.restUntil(t.InstallationID, l.now().Add(lastenheftRetryNoRight))
		return false, nil
	case errors.Is(err, forge.ErrCommitUnavailable):
		l.markGone(t)
		return false, nil
	case errors.As(err, &rl):
		until := rl.Reset
		if min := l.now().Add(lastenheftRateFallback); until.Before(min) {
			until = min
		}
		l.restUntil(t.InstallationID, until)
		return false, err
	case err != nil:
		return false, err
	}
	return true, l.st.MarkLastenheftPublished(t.Project, t.PR, t.HeadSHA, digest, id)
}

// sweep geht alle offenen PRs mit Paket in privaten Repos durch und schreibt, was sich
// geändert hat. Ruht eine Installation (kein Recht, Ratenlimit), überspringt es ihre
// PRs, ohne zu fragen.
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
// Frist: der Webhook antwortet vorher. Der Auftrag heißt wie der PR (project#n): ein
// PR hat höchstens einen, und die Aufträge anderer PRs desselben Projekts laufen
// unabhängig davon. Er liest den Stand zu Beginn und am Ende aus der Datenbank; kam
// während des Schreibens ein neuer Head-Commit an (dessen eigener Auftrag dann nicht
// startete), schreibt er auch den.
func (l *lastenheftPublisher) publishAsync(jobs *detailJobs, t store.LastenheftTarget) {
	jobs.start(fmt.Sprintf("%s#%d", t.Project, t.PR), func() {
		ctx, cancel := context.WithTimeout(context.Background(), lastenheftJobTimeout)
		defer cancel()
		for i := 0; i < maxLastenheftFollowUps; i++ {
			fresh, ok, err := l.st.LastenheftTargetFor(t.Project, t.PR)
			if err != nil {
				l.report(fmt.Errorf("check run of %s#%d: %w", t.Project, t.PR, err))
				return
			}
			if !ok || (i > 0 && fresh.HeadSHA == t.HeadSHA) {
				return
			}
			t = fresh
			if _, err := l.publish(ctx, t); err != nil {
				l.report(fmt.Errorf("check run of %s#%d: %w", t.Project, t.PR, err))
				return
			}
		}
	})
}
