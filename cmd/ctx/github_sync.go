package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const githubUsage = `usage: ctx github sync-prs --db PATH [--project github.com/owner/repo] [--commits]

Operator command, runs on the server against its database and needs no client
configuration. It reads the pull requests of every branch whose entries still
carry no PR number (through the GitHub App, which needs "Pull requests: read")
and records them the way the webhook does: anchors get their PR number, packages
are bound to their PR, merged PRs lift the branch descriptions. With --commits
it also fetches the commits of every known pull request that has none stored.`

// pullChange übersetzt ein pull_request-Ereignis in die Form des Stores.
func pullChange(ev forge.PullEvent) store.PullRequestChange {
	return store.PullRequestChange{
		InstallationID: ev.InstallationID, Remote: ev.Remote, HeadRemote: ev.HeadRemote, Number: int64(ev.Number),
		Action: ev.Action, Title: ev.Title, Head: ev.Head, Base: ev.Base, DefaultBranch: ev.DefaultBranch,
		MergeSHA: ev.MergeSHA, HeadSHA: ev.HeadSHA, HeadAt: ev.UpdatedAt, RepoPrivate: ev.RepoPrivate, Author: ev.Author, State: ev.State, Body: ev.Body,
	}
}

// pullLister ist, was das Nachholen von der GitHub-App braucht.
type pullLister interface {
	PullRequests(ctx context.Context, installationID int64, repo, branch string) ([]forge.PullRequest, error)
}

// commitLister ist, was das Abholen der PR-Commits von der GitHub-App braucht
// (Recht "Pull requests: read"). Ein pullLister, der es nicht kann, bekommt keine.
type commitLister interface {
	PullRequestCommits(ctx context.Context, installationID int64, repo string, number int64) ([]forge.PullCommit, error)
}

// detailFetcher holt zu einem Commit die geänderten Dateien ("Contents: read") und
// den Stand der Prüfungen ("Checks: read"). Das sind eigene Rechte der App; fehlt
// eines, bleibt der Block auf der Commit-Seite einfach weg.
type detailFetcher interface {
	CommitFiles(ctx context.Context, installationID int64, repo, sha string) (forge.CommitFiles, error)
	CommitCI(ctx context.Context, installationID int64, repo, sha string) (string, error)
}

// detailPlan merkt, welche Gruppen die App für eine Installation abrufen darf: fehlt
// ein Recht, wird es für den Rest des Laufs nicht mehr erfragt, und die Warteschlange
// (store.CommitsNeedingDetails) wird nur noch nach den übrigen Gruppen gefüllt.
type detailPlan struct{ files, ci bool }

func newDetailPlan() *detailPlan { return &detailPlan{files: true, ci: true} }

func (p *detailPlan) any() bool { return p.files || p.ci }

// detailPlans führt den Plan je Installation: ein Recht, das einer Installation
// fehlt, sagt nichts über die anderen.
type detailPlans map[int64]*detailPlan

func (p detailPlans) of(installationID int64) *detailPlan {
	if p[installationID] == nil {
		p[installationID] = newDetailPlan()
	}
	return p[installationID]
}

// recordCommitDetails holt Dateien und Prüfstand eines Commits und speichert sie.
// Ein fehlendes Recht ist kein Fehler des Commits: die Gruppe wird im Plan der
// Installation abgeschaltet. Kennt GitHub den Commit nicht (mehr), kommt
// forge.ErrCommitUnavailable zurück, und nur dieser Commit wird übersprungen; was
// schon da war, steht trotzdem. Ein Ratenlimit und jeder andere Fehler kommen
// ebenfalls zurück.
func recordCommitDetails(ctx context.Context, st *store.Store, f detailFetcher, plan *detailPlan, installationID int64, project, sha string) error {
	ch := store.CommitDetailsChange{InstallationID: installationID, Remote: project, SHA: sha}
	var skipped error
	if plan.files {
		files, err := f.CommitFiles(ctx, installationID, project, sha)
		switch {
		case err == nil:
			ch.HaveFiles, ch.FilesTotal, ch.Additions, ch.Deletions = true, files.Total, files.Additions, files.Deletions
			for _, x := range files.Files {
				ch.Files = append(ch.Files, store.CommitFileStat{Path: x.Path, Additions: x.Additions, Deletions: x.Deletions})
			}
		case errors.Is(err, forge.ErrPermissionMissing):
			plan.files = false
		case errors.Is(err, forge.ErrCommitUnavailable):
			// Der Commit ist weg; den Prüfstand zu erfragen wäre dieselbe Antwort.
			return err
		default:
			return err
		}
	}
	if plan.ci {
		ci, err := f.CommitCI(ctx, installationID, project, sha)
		switch {
		case err == nil:
			ch.HaveCI, ch.CI = true, ci
		case errors.Is(err, forge.ErrPermissionMissing):
			plan.ci = false
		case errors.Is(err, forge.ErrCommitUnavailable):
			skipped = err
		default:
			return err
		}
	}
	if ch.HaveFiles || ch.HaveCI {
		if _, err := st.RecordCommitDetails(ch); err != nil {
			return err
		}
	}
	return skipped
}

// fetchProjectDetails holt Dateien und Prüfstand der Commits eines Projekts nach,
// jüngste zuerst, höchstens limit Commits. Weiß der Plan der Installation von
// Anfang an oder lernt er unterwegs, dass ein Recht fehlt, wird die Warteschlange
// neu und nur nach den übrigen Gruppen gefüllt; so bleiben Commits, denen nur diese
// eine Gruppe fehlt, nicht ewig vorn stehen und verdrängen die älteren. Der Commit,
// an dem das Recht aufgefallen ist, zählt nicht gegen limit.
//
// Ein Fehler eines Commits (auch forge.ErrCommitUnavailable) geht an onError; gibt
// es true zurück, hört der Abruf auf. Zurück kommt, bei wie vielen Commits nichts
// schiefging, und ein Fehler beim Lesen der Warteschlange.
func fetchProjectDetails(ctx context.Context, st *store.Store, f detailFetcher, plan *detailPlan, installationID int64, project string, limit int, perCommit time.Duration, onError func(sha string, err error) (stop bool)) (int, error) {
	tried := map[string]bool{}
	var done, counted int
	for plan.any() && counted < limit {
		shas, err := st.CommitsNeedingDetails(project, limit+len(tried), plan.files, plan.ci)
		if err != nil {
			return done, err
		}
		before := *plan
		for _, sha := range shas {
			if tried[sha] || counted >= limit {
				continue
			}
			tried[sha] = true
			cctx, cancel := context.WithTimeout(ctx, perCommit)
			err := recordCommitDetails(cctx, st, f, plan, installationID, project, sha)
			cancel()
			if err != nil {
				if onError(sha, err) {
					return done, nil
				}
			} else {
				done++
			}
			if *plan != before {
				break
			}
			counted++
		}
		if *plan == before {
			break
		}
	}
	return done, nil
}

// webhookDetailCommits ist, wie viele Commits der Webhook höchstens mit Dateien und
// Prüfstand versieht; den Rest holt ctx github sync-prs --commits nach.
const webhookDetailCommits = 5

// pullCommitTimeout begrenzt den Abruf der Commits eines PRs im Webhook; GitHub
// wartet auf die Antwort nur zehn Sekunden.
const pullCommitTimeout = 8 * time.Second

// detailJobTimeout begrenzt den Hintergrundabruf der Commit-Details nach einem
// Webhook; detailCommitTimeout jeden einzelnen Commit darin.
const (
	detailJobTimeout    = 2 * time.Minute
	detailCommitTimeout = 30 * time.Second
)

// detailJobs führt die Hintergrundabrufe der Commit-Details nach einem Webhook:
// nach der Antwort an GitHub, mit eigener Frist, und höchstens einer je Projekt (ein
// weiteres Ereignis, solange einer läuft, holt nichts doppelt; was dann fehlt, holt
// der nächste Abruf).
type detailJobs struct {
	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// start führt job in einer eigenen Goroutine aus, wenn für project keiner läuft.
func (j *detailJobs) start(project string, job func()) {
	j.mu.Lock()
	if j.running[project] {
		j.mu.Unlock()
		return
	}
	if j.running == nil {
		j.running = map[string]bool{}
	}
	j.running[project] = true
	j.wg.Add(1)
	j.mu.Unlock()
	go func() {
		defer func() {
			j.mu.Lock()
			delete(j.running, project)
			j.mu.Unlock()
			j.wg.Done()
		}()
		job()
	}()
}

// wait wartet auf alle laufenden Abrufe (für Tests und ein sauberes Beenden).
func (j *detailJobs) wait() { j.wg.Wait() }

// recordPullCommits holt die Commits eines PRs und speichert sie (siehe
// store.RecordPullCommits). Ein Fehler ist nie für den Webhook fatal: die PR-Zeile
// steht schon, die Commits kommen beim nächsten Ereignis oder mit sync-prs nach.
func recordPullCommits(ctx context.Context, st *store.Store, l commitLister, installationID int64, project string, number int64) (store.PullCommitsResult, error) {
	list, err := l.PullRequestCommits(ctx, installationID, project, number)
	if err != nil {
		return store.PullCommitsResult{}, err
	}
	commits := make([]store.PullCommit, 0, len(list))
	for _, c := range list {
		commits = append(commits, store.PullCommit{SHA: c.SHA, Author: c.Author, AuthorName: c.AuthorName, Title: c.Title, CommittedAt: c.CommittedAt, Message: c.Message, Parents: c.Parents})
	}
	return st.RecordPullCommits(store.PullCommitsChange{InstallationID: installationID, Remote: project, Number: number, Commits: commits})
}

// pullWebhook ist der Empfänger der pull_request-Ereignisse: er nimmt den PR auf
// und holt bei opened, reopened und synchronize seine Commits. Dateien und Prüfstand
// der neuesten Commits holt er danach im Hintergrund (siehe detailJobs). onCommitError
// bekommt, was beim Abholen schiefging (kein Token, keine Antwort-Rümpfe); der
// Webhook antwortet trotzdem mit Erfolg.
func pullWebhook(st *store.Store, l commitLister, onCommitError func(error)) forge.PullSync {
	hook, _ := pullWebhookJobs(st, l, onCommitError)
	return hook
}

// pullWebhookJobs ist pullWebhook mit den Hintergrundabrufen, auf die ein Test
// warten kann.
func pullWebhookJobs(st *store.Store, l commitLister, onCommitError func(error)) (forge.PullSync, *detailJobs) {
	return pullWebhookWith(st, l, nil, onCommitError)
}

// pullWebhookWith ist pullWebhookJobs, das bei offenen PRs mit Paket auch den
// Check-Run „Lastenheft“ schreibt, wenn lh gesetzt ist (lastenheft.go).
func pullWebhookWith(st *store.Store, l commitLister, lh *lastenheftPublisher, onCommitError func(error)) (forge.PullSync, *detailJobs) {
	jobs := &detailJobs{}
	report := func(err error) {
		if onCommitError != nil {
			onCommitError(err)
		}
	}
	return func(ev forge.PullEvent) error {
		res, err := st.RecordPullRequest(pullChange(ev))
		if err != nil {
			return err
		}
		if !res.Applied {
			return nil
		}
		switch ev.Action {
		case "opened", "reopened", "synchronize":
		default:
			return nil
		}
		if lh != nil && ev.State == "open" && ev.HeadSHA != "" {
			lh.publishAsync(jobs, store.LastenheftTarget{Project: res.Project, PR: ev.Number, HeadSHA: ev.HeadSHA, InstallationID: ev.InstallationID})
		}
		if l == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), pullCommitTimeout)
		defer cancel()
		if _, err := recordPullCommits(ctx, st, l, ev.InstallationID, res.Project, ev.Number); err != nil {
			report(fmt.Errorf("pull request commits of %s#%d: %w", res.Project, ev.Number, err))
			return nil
		}
		if f, ok := l.(detailFetcher); ok {
			// Nach der Antwort und mit eigener Frist: der Abruf kostet je Commit mehrere
			// Aufrufe bei GitHub, das nach zehn Sekunden aufgibt.
			installation, project := ev.InstallationID, res.Project
			jobs.start(project, func() {
				jctx, jcancel := context.WithTimeout(context.Background(), detailJobTimeout)
				defer jcancel()
				_, err := fetchProjectDetails(jctx, st, f, newDetailPlan(), installation, project, webhookDetailCommits, detailCommitTimeout,
					func(sha string, err error) bool {
						if !errors.Is(err, forge.ErrCommitUnavailable) {
							report(fmt.Errorf("commit details of %s %s: %w", project, sha[:7], err))
							return true
						}
						return false
					})
				if err != nil {
					report(fmt.Errorf("commit details of %s: %w", project, err))
				}
			})
		}
		return nil
	}, jobs
}

func cmdGithub(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "sync-prs" {
		fmt.Fprintln(stdout, githubUsage)
		return 2
	}
	fs := flag.NewFlagSet("github sync-prs", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "", "path of the server database")
	project := fs.String("project", "", "only this project (default: all with an installation)")
	withCommits := fs.Bool("commits", false, "also fetch the commits of every known pull request that has none stored yet")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *db == "" {
		fmt.Fprintln(stdout, githubUsage)
		return 2
	}
	app := forge.NewGitHubApp(forge.AppConfigFromOS())
	if !app.Configured() {
		fmt.Fprintln(stdout, "the GitHub App is not set up on this machine (GHOSTTREE_GITHUB_APP_* is missing)")
		return 1
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer st.Close()
	code := syncPulls(context.Background(), st, app, *project, stdout)
	if *withCommits && code == 0 {
		code = syncPullCommits(context.Background(), st, app, *project, stdout)
	}
	return code
}

// syncPullCommits holt die Commits aller bekannten PRs nach, zu denen noch keine
// gespeichert sind. Ein Ratenlimit oder ein fehlendes Recht bricht ab; der
// nächste Lauf macht dort weiter, wo dieser aufhörte.
func syncPullCommits(ctx context.Context, st *store.Store, cl commitLister, project string, stdout io.Writer) int {
	targets, err := st.PullSyncTargets(scope.NormalizeRemote(project))
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	var fetched, failed int
	for _, t := range targets {
		numbers, err := st.PullsWithoutCommits(t.Project)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		for _, n := range numbers {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, cerr := recordPullCommits(cctx, st, cl, t.InstallationID, t.Project, n)
			cancel()
			switch {
			case cerr == nil:
				fetched++
			case errors.Is(cerr, forge.ErrRateLimited):
				fmt.Fprintf(stdout, "GitHub rate limit reached (%v); run the command again later\n", cerr)
				return 1
			case errors.Is(cerr, forge.ErrPermissionMissing):
				fmt.Fprintln(stdout, "the GitHub App lacks \"Pull requests: read\"; commits were not fetched")
				return 1
			default:
				failed++
				fmt.Fprintf(stdout, "%s #%d: could not read the commits (%v)\n", t.Project, n, cerr)
			}
		}
	}
	fmt.Fprintf(stdout, "%d pull requests without stored commits fetched, %d failed\n", fetched, failed)
	if failed > 0 {
		return 1
	}
	if f, ok := cl.(detailFetcher); ok {
		return syncCommitDetails(ctx, st, f, targets, stdout)
	}
	return 0
}

// detailsPerRun begrenzt, wie viele Commits ein Lauf je Projekt mit Dateien und
// Prüfstand versieht (jeder kostet zwei bis drei Abrufe); der nächste Lauf macht
// dort weiter.
var detailsPerRun = 150

// syncCommitDetails holt Dateien und Prüfstand der Commits nach, die sie noch nicht
// haben (oder deren Prüfung noch lief), jüngste zuerst. Ein Ratenlimit bricht ab.
// Welche Rechte fehlen, merkt es je Installation (detailPlans); ein Commit, den
// GitHub nicht mehr kennt, wird übersprungen und zählt nicht als Fehler.
func syncCommitDetails(ctx context.Context, st *store.Store, f detailFetcher, targets []store.PullSyncTarget, stdout io.Writer) int {
	var done, failed, gone int
	plans := detailPlans{}
	limited := false
	for _, t := range targets {
		plan := plans.of(t.InstallationID)
		n, err := fetchProjectDetails(ctx, st, f, plan, t.InstallationID, t.Project, detailsPerRun, 30*time.Second,
			func(sha string, err error) bool {
				switch {
				case errors.Is(err, forge.ErrRateLimited):
					fmt.Fprintf(stdout, "GitHub rate limit reached (%v); run the command again later\n", err)
					limited = true
					return true
				case errors.Is(err, forge.ErrCommitUnavailable):
					gone++
					fmt.Fprintf(stdout, "%s %s: GitHub does not know the commit (any more); skipped\n", t.Project, sha[:7])
				default:
					failed++
					fmt.Fprintf(stdout, "%s %s: could not read the commit details (%v)\n", t.Project, sha[:7], err)
				}
				return false
			})
		done += n
		if limited {
			return 1
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
	}
	ids := make([]int64, 0, len(plans))
	for id := range plans {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if !plans[id].files {
			fmt.Fprintf(stdout, "installation %d: the GitHub App lacks \"Contents: read\"; changed files were not fetched\n", id)
		}
		if !plans[id].ci {
			fmt.Fprintf(stdout, "installation %d: the GitHub App lacks \"Checks: read\"; the test state of commits was not fetched\n", id)
		}
	}
	fmt.Fprintf(stdout, "%d commits with their files and checks fetched, %d skipped, %d failed\n", done, gone, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// syncPulls holt die PR-Nummern für bestehende Branches nach. Fehlt das App-
// Recht, wird abgebrochen (für alle Branches gleich); ein einzelner Branch ohne
// PR ist kein Fehler.
func syncPulls(ctx context.Context, st *store.Store, lister pullLister, project string, stdout io.Writer) int {
	targets, err := st.PullSyncTargets(scope.NormalizeRemote(project))
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if len(targets) == 0 {
		fmt.Fprintln(stdout, "no project with a GitHub installation")
		return 0
	}
	var branches, prs, tagged, failed, commits int
	for _, t := range targets {
		for _, b := range t.Branches {
			branches++
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			list, err := lister.PullRequests(cctx, t.InstallationID, t.Project, b)
			cancel()
			if err != nil {
				if err == forge.ErrPermissionMissing {
					fmt.Fprintln(stdout, "the GitHub App lacks \"Pull requests: read\"; nothing was changed")
					return 1
				}
				failed++
				fmt.Fprintf(stdout, "%s %s: could not read (%v)\n", t.Project, b, err)
				continue
			}
			// Nur die jüngsten PRs dieses Branches; älteste zuerst, damit ein
			// späterer PR gleichen Namens den früheren nicht überschreibt.
			for i := len(list) - 1; i >= 0; i-- {
				pr := list[i]
				state := pr.State
				if state != "open" && state != "merged" {
					state = "closed"
				}
				res, err := st.RecordPullRequest(store.PullRequestChange{
					InstallationID: t.InstallationID, Remote: t.Project, HeadRemote: t.Project, Number: int64(pr.Number),
					Action: "sync", Title: pr.Title, Head: pr.HeadRef, Base: pr.BaseRef, Author: pr.Author, State: state, Body: &pr.Body,
				})
				if err != nil {
					fmt.Fprintf(stdout, "%s #%d: %v\n", t.Project, pr.Number, err)
					failed++
					continue
				}
				if !res.Applied {
					continue
				}
				prs++
				tagged += res.AnchorsTagged
				cl, ok := lister.(commitLister)
				if !ok {
					continue
				}
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, cerr := recordPullCommits(cctx, st, cl, t.InstallationID, t.Project, int64(pr.Number))
				cancel()
				switch {
				case cerr == nil:
					commits++
				case errors.Is(cerr, forge.ErrRateLimited):
					fmt.Fprintf(stdout, "GitHub rate limit reached (%v); the commits of the remaining pull requests were not fetched, run the command again later\n", cerr)
					return 1
				case errors.Is(cerr, forge.ErrPermissionMissing):
					fmt.Fprintln(stdout, "the GitHub App lacks \"Pull requests: read\"; commits were not fetched")
					return 1
				default:
					failed++
					fmt.Fprintf(stdout, "%s #%d: could not read the commits (%v)\n", t.Project, pr.Number, cerr)
				}
			}
		}
	}
	fmt.Fprintf(stdout, "%d branches asked, %d pull requests recorded, %d anchors tagged, %d failed\n", branches, prs, tagged, failed)
	if _, ok := lister.(commitLister); ok {
		fmt.Fprintf(stdout, "%d pull requests with their commits fetched\n", commits)
	}
	if failed > 0 {
		return 1
	}
	return 0
}
