package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshotmirror"
)

const snapshotUsage = `usage:
  ctx snapshot create <name> [-m message] [--allow-dirty] [repo]
  ctx snapshot list [repo]
  ctx snapshot show <name> [repo]
  ctx snapshot export <name> [--domain D] [--key K] [-o file] [repo]
  ctx snapshot verify <name> [repo]
  ctx snapshot mirror rebuild [repo]`

func cmdSnapshot(args []string, stdout, diagnostics io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, snapshotUsage)
		return 2
	}
	switch args[0] {
	case "create":
		return snapshotCreate(args[1:], stdout, diagnostics)
	case "list":
		return snapshotList(args[1:], stdout)
	case "show":
		return snapshotShow(args[1:], stdout)
	case "export":
		return snapshotExport(args[1:], stdout, diagnostics)
	case "verify":
		return snapshotVerify(args[1:], stdout)
	case "mirror":
		return snapshotMirror(args[1:], stdout)
	default:
		fmt.Fprintln(stdout, snapshotUsage)
		return 2
	}
}

func snapshotCreate(args []string, stdout, diagnostics io.Writer) int {
	positionals, values, flags, err := parseSnapshotOptions(args, map[string]bool{"-m": true}, map[string]bool{"--allow-dirty": true})
	if err != nil || len(positionals) < 1 || len(positionals) > 2 {
		return snapshotUsageError(stdout, err)
	}
	repo, project, c, err := snapshotCommandContext(optionalRepo(positionals, 1))
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	provenance, err := collector.ResolveSnapshotGit(repo, positionals[0], flags["--allow-dirty"])
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	rechecked, err := collector.ResolveSnapshotGit(repo, positionals[0], flags["--allow-dirty"])
	if err != nil || !collector.SnapshotGitEqual(provenance, rechecked) {
		return snapshotCommandError(stdout, &snapshot.RuleError{Code: "snapshot_git_changed", Retryable: true})
	}
	message := optionalString(values["-m"])
	result, err := c.CreateContextSnapshot(context.Background(), snapshot.CreateInput{Project: project, Name: positionals[0], Git: provenance, GitRecheck: &rechecked, Message: message})
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(diagnostics, "%s: %s\n", warning.Code, warning.Message)
	}
	if err := snapshotmirror.Rebuild(context.Background(), snapshotLister{c}, repo, project); err != nil {
		fmt.Fprintf(diagnostics, "snapshot_mirror_degraded: %v\n", err)
	}
	return writeSnapshotJSON(stdout, result)
}

func snapshotList(args []string, stdout io.Writer) int {
	if len(args) > 1 {
		return snapshotUsageError(stdout, nil)
	}
	_, project, c, err := snapshotCommandContext(optionalRepo(args, 0))
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	var heads []snapshot.Head
	cursor := ""
	seen := map[string]struct{}{cursor: {}}
	for {
		page, err := c.ContextSnapshots(context.Background(), snapshot.ListFilter{Project: project, Cursor: cursor, Limit: 100})
		if err != nil {
			return snapshotCommandError(stdout, err)
		}
		heads = append(heads, page.Snapshots...)
		if page.NextCursor == "" {
			return writeSnapshotJSON(stdout, heads)
		}
		if _, duplicate := seen[page.NextCursor]; duplicate {
			return snapshotCommandError(stdout, fmt.Errorf("snapshot list returned a repeated cursor"))
		}
		cursor = page.NextCursor
		seen[cursor] = struct{}{}
	}
}

func snapshotShow(args []string, stdout io.Writer) int {
	if len(args) < 1 || len(args) > 2 {
		return snapshotUsageError(stdout, nil)
	}
	_, project, c, err := snapshotCommandContext(optionalRepo(args, 1))
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	head, _, err := c.ContextSnapshot(context.Background(), project, args[0])
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	return writeSnapshotJSON(stdout, head)
}

func snapshotExport(args []string, stdout, diagnostics io.Writer) int {
	positionals, values, _, err := parseSnapshotOptions(args, map[string]bool{"--domain": true, "--key": true, "-o": true}, nil)
	if err != nil || len(positionals) < 1 || len(positionals) > 2 {
		return snapshotUsageError(stdout, err)
	}
	if values["--key"] != "" && values["--domain"] == "" {
		fmt.Fprintln(stdout, "--key requires --domain")
		return 2
	}
	_, project, c, err := snapshotCommandContext(optionalRepo(positionals, 1))
	if err != nil {
		return snapshotCommandError(diagnostics, err)
	}
	filter := exportFilter(values["--domain"], values["--key"])
	if values["-o"] == "" {
		if err := c.ExportContextSnapshot(context.Background(), project, positionals[0], filter, stdout); err != nil {
			return snapshotCommandError(diagnostics, err)
		}
		return 0
	}
	var data bytes.Buffer
	if err := c.ExportContextSnapshot(context.Background(), project, positionals[0], filter, &data); err != nil {
		return snapshotCommandError(diagnostics, err)
	}
	if err := privatefile.WriteSyncedNoFollow(values["-o"], data.Bytes(), 0o600); err != nil {
		return snapshotCommandError(diagnostics, err)
	}
	fmt.Fprintln(stdout, values["-o"])
	return 0
}

func snapshotVerify(args []string, stdout io.Writer) int {
	if len(args) < 1 || len(args) > 2 {
		return snapshotUsageError(stdout, nil)
	}
	_, project, c, err := snapshotCommandContext(optionalRepo(args, 1))
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	verification, err := c.VerifyContextSnapshot(context.Background(), project, args[0])
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	return writeSnapshotJSON(stdout, verification)
}

func snapshotMirror(args []string, stdout io.Writer) int {
	if len(args) < 1 || args[0] != "rebuild" || len(args) > 2 {
		return snapshotUsageError(stdout, nil)
	}
	repo, project, c, err := snapshotCommandContext(optionalRepo(args, 1))
	if err != nil {
		return snapshotCommandError(stdout, err)
	}
	if err := snapshotmirror.Rebuild(context.Background(), snapshotLister{c}, repo, project); err != nil {
		return snapshotCommandError(stdout, err)
	}
	fmt.Fprintln(stdout, filepath.Join(repo, ".ghosttree", "snapshots", "INDEX.md"))
	return 0
}

type snapshotLister struct{ *client.Client }

func (l snapshotLister) ListContextSnapshots(ctx context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	return l.ContextSnapshots(ctx, filter)
}

func snapshotCommandContext(repoArg string) (string, string, *client.Client, error) {
	abs, err := filepath.Abs(repoArg)
	if err != nil {
		return "", "", nil, err
	}
	gitContext := collector.ResolveGitContext(abs)
	if gitContext.Project == "" || gitContext.Root == "" {
		return "", "", nil, fmt.Errorf("not a repository with an origin remote")
	}
	cfg, err := config.Load()
	if err != nil {
		return "", "", nil, fmt.Errorf("no config (%s); run 'ctx setup'", config.Path())
	}
	return gitContext.Root, gitContext.Project, client.New(cfg), nil
}

func parseSnapshotOptions(args []string, valueOptions, boolOptions map[string]bool) ([]string, map[string]string, map[string]bool, error) {
	values := map[string]string{}
	flags := map[string]bool{}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if valueOptions[arg] {
			if i+1 >= len(args) {
				return nil, nil, nil, fmt.Errorf("%s requires a value", arg)
			}
			i++
			values[arg] = args[i]
			continue
		}
		if boolOptions[arg] {
			flags[arg] = true
			continue
		}
		if len(arg) > 0 && arg[0] == '-' {
			return nil, nil, nil, fmt.Errorf("unknown option %s", arg)
		}
		positionals = append(positionals, arg)
	}
	return positionals, values, flags, nil
}

func exportFilter(domain, key string) *snapshot.ExportFilter {
	if domain == "" {
		return nil
	}
	filter := &snapshot.ExportFilter{Domain: domain}
	if key != "" {
		filter.Key = &key
	}
	return filter
}

func optionalRepo(args []string, index int) string {
	if len(args) > index {
		return args[index]
	}
	return "."
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func snapshotUsageError(stdout io.Writer, err error) int {
	if err != nil {
		fmt.Fprintln(stdout, err)
	}
	fmt.Fprintln(stdout, snapshotUsage)
	return 2
}

func snapshotCommandError(stdout io.Writer, err error) int {
	var rule *snapshot.RuleError
	if errors.As(err, &rule) {
		fmt.Fprintln(stdout, rule.Code)
		return 1
	}
	fmt.Fprintln(stdout, err)
	return 1
}

func writeSnapshotJSON(stdout io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	return 0
}
