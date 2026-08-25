package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const mirrorUsage = `usage: ctx mirror [repo]

Write .ghosttree/ for a repository: knowledge, documents and the work ledger as
files. Runs by itself at session start on harnesses with hooks; this is the way
to refresh it anywhere else.`

// cmdMirror ist der Weg für Umgebungen ohne Sitzungsbeginn-Hook. Auf opencode
// etwa schreibt den Spiegel sonst niemand, und ein alter Spiegel sieht aus wie
// ein frischer — deshalb gibt es das Kommando, und deshalb steht der Stand im
// Index.
func cmdMirror(args []string, stdout io.Writer) int {
	repoArg := "."
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(stdout, mirrorUsage)
			return 0
		}
		repoArg = args[0]
	}
	repo, err := filepath.Abs(repoArg)
	if err != nil {
		fmt.Fprintf(stdout, "bad repository path: %v\n", err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	gitCtx := collector.ResolveGitContext(repo)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		fmt.Fprintln(stdout, "not a repository with an origin remote; nothing to mirror")
		return 1
	}
	ax := scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch,
		Lineage: gitCtx.Lineage, Machine: cfg.Machine}
	if err := WriteMirror(client.New(cfg), ax, gitCtx.Root); err != nil {
		fmt.Fprintf(stdout, "mirror: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", filepath.Join(gitCtx.Root, ".ghosttree"))
	return 0
}
