package main

import (
	"context"
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
		MergeSHA: ev.MergeSHA, State: ev.State,
	}
}

// pullLister ist, was das Nachholen von der GitHub-App braucht.
type pullLister interface {
	PullRequests(ctx context.Context, installationID int64, repo, branch string) ([]forge.PullRequest, error)
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
	var branches, prs, tagged, failed int
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
					Action: "sync", Title: pr.Title, Head: pr.HeadRef, Base: pr.BaseRef, State: state,
				})
				if err != nil {
					fmt.Fprintf(stdout, "%s #%d: %v\n", t.Project, pr.Number, err)
					failed++
					continue
				}
				if res.Applied {
					prs++
					tagged += res.AnchorsTagged
				}
			}
		}
	}
	fmt.Fprintf(stdout, "%d branches asked, %d pull requests recorded, %d anchors tagged, %d failed\n", branches, prs, tagged, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
