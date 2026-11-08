package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const githubUsage = `usage: ctx github sync-prs --db PATH [--project github.com/owner/repo]

Operator command, runs on the server against its database and needs no client
configuration. It reads the pull requests of every branch whose entries still
carry no PR number (through the GitHub App, which needs "Pull requests: read")
and records them the way the webhook does: anchors get their PR number, packages
are bound to their PR, merged PRs lift the branch descriptions.`

// pullChange übersetzt ein pull_request-Ereignis in die Form des Stores.
func pullChange(ev forge.PullEvent) store.PullRequestChange {
	return store.PullRequestChange{
		InstallationID: ev.InstallationID, Remote: ev.Remote, HeadRemote: ev.HeadRemote, Number: int64(ev.Number),
		Action: ev.Action, Title: ev.Title, Head: ev.Head, Base: ev.Base, DefaultBranch: ev.DefaultBranch,
		MergeSHA: ev.MergeSHA, Author: ev.Author, State: ev.State,
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

// pullCommitTimeout begrenzt den Abruf der Commits eines PRs im Webhook; GitHub
// wartet auf die Antwort nur zehn Sekunden.
const pullCommitTimeout = 8 * time.Second

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
		commits = append(commits, store.PullCommit{SHA: c.SHA, Author: c.Author, AuthorName: c.AuthorName, Title: c.Title, CommittedAt: c.CommittedAt})
	}
	return st.RecordPullCommits(store.PullCommitsChange{InstallationID: installationID, Remote: project, Number: number, Commits: commits})
}

// pullWebhook ist der Empfänger der pull_request-Ereignisse: er nimmt den PR auf
// und holt bei opened, reopened und synchronize seine Commits. onCommitError
// bekommt, was beim Abholen schiefging (kein Token, keine Antwort-Rümpfe); der
// Webhook antwortet trotzdem mit Erfolg.
func pullWebhook(st *store.Store, l commitLister, onCommitError func(error)) forge.PullSync {
	return func(ev forge.PullEvent) error {
		res, err := st.RecordPullRequest(pullChange(ev))
		if err != nil {
			return err
		}
		if l == nil || !res.Applied {
			return nil
		}
		switch ev.Action {
		case "opened", "reopened", "synchronize":
		default:
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), pullCommitTimeout)
		defer cancel()
		if _, err := recordPullCommits(ctx, st, l, ev.InstallationID, res.Project, ev.Number); err != nil && onCommitError != nil {
			onCommitError(fmt.Errorf("pull request commits of %s#%d: %w", res.Project, ev.Number, err))
		}
		return nil
	}
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
	return syncPulls(context.Background(), st, app, *project, stdout)
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
					Action: "sync", Title: pr.Title, Head: pr.HeadRef, Base: pr.BaseRef, Author: pr.Author, State: state,
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
