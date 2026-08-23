package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/migrate"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const migrateUsage = `usage: ctx migrate [--dry-run|--clean] <repo>

Distill repository agent artifacts into ghosttree. Cleanup is a separate,
guarded operation and only removes files whose migration provenance exists.`

type migrationCandidate struct {
	item       migrate.Item
	confidence string
	status     string
	activation activation.Rule
}

func cmdMigrate(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dryRun := fs.Bool("dry-run", false, "show candidates without writing them")
	clean := fs.Bool("clean", false, "remove artifacts already proven migrated")
	fs.Usage = func() { fmt.Fprintln(stdout, migrateUsage) }
	rest, repoArg := splitLeadingOperand(args)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if repoArg == "" {
		repoArg = "."
	}
	if fs.NArg() != 0 || (*dryRun && *clean) {
		fmt.Fprintln(stdout, migrateUsage)
		return 2
	}
	repo, err := filepath.Abs(repoArg)
	if err != nil {
		fmt.Fprintf(stdout, "bad repository path: %v\n", err)
		return 2
	}
	info, err := os.Stat(repo)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stdout, "repository does not exist: %s\n", repo)
		return 2
	}
	project, _ := collector.GitInfo(repo)
	if project == "" {
		fmt.Fprintln(stdout, "repository has no origin remote; cannot derive project scope")
		return 1
	}
	artifacts, err := migrate.Scan(repo)
	if err != nil {
		fmt.Fprintf(stdout, "scan failed: %v\n", err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	api := client.New(cfg)
	if *clean {
		return cleanMigrated(repo, project, artifacts, api, stdout)
	}
	llmCfg, err := llm.LoadConfig()
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	model, err := llm.New(llmCfg)
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	existing, err := api.ProjectKnowledge(project, true)
	if err != nil {
		fmt.Fprintf(stdout, "load existing knowledge: %v\n", err)
		return 1
	}
	titles := make([]string, 0, len(existing))
	var existingInstructions []migrate.InstructionCandidate
	for _, k := range existing {
		titles = append(titles, k.Title)
		if k.Type == "instruction" {
			existingInstructions = append(existingInstructions, migrate.InstructionCandidate{Title: k.Title, Body: k.Body, Activation: k.Activation})
		}
	}
	var candidates []migrationCandidate
	var dropped []string
	completed, err := api.CompletedMigrationArtifacts(project)
	if err != nil {
		fmt.Fprintf(stdout, "load migration history: %v\n", err)
		return 1
	}
	digests := map[string]string{}
	var pendingArtifacts []migrate.Artifact
	for _, artifact := range artifacts {
		raw, err := os.ReadFile(artifact.Path)
		if err != nil {
			fmt.Fprintf(stdout, "read %s: %v\n", artifact.Rel, err)
			return 1
		}
		content := string(raw)
		digest := contentDigest(raw)
		digests[artifact.Rel] = digest
		if containsString(completed[artifact.Rel], digest) {
			fmt.Fprintf(stdout, "already migrated: %s\n", artifact.Rel)
			continue
		}
		pendingArtifacts = append(pendingArtifacts, artifact)
		if migrate.ShouldDistill(artifact) {
			result, err := migrate.Distill(context.Background(), model, artifact, content, titles)
			if err != nil {
				fmt.Fprintf(stdout, "distill %s: %v\n", artifact.Rel, err)
				return 1
			}
			for _, item := range result.Items {
				confidence := ""
				if item.Type == "instruction" {
					confidence = "staged"
				}
				activationRule := activation.Rule{}
				if item.Type == "instruction" {
					activationRule = artifact.Activation
				}
				candidates = append(candidates, migrationCandidate{item: item, confidence: confidence, activation: activationRule})
				titles = append(titles, item.Title)
			}
			for _, reason := range result.Dropped {
				dropped = append(dropped, artifact.Rel+": "+reason)
			}
		}
		if artifact.Kind == "spec" || artifact.Kind == "plan" {
			candidates = append(candidates, migrationCandidate{item: migrate.Item{Type: "plan", Title: artifact.Rel, Body: content, Source: artifact.Rel}, status: "archived"})
		}
	}
	var newInstructions []migrate.InstructionCandidate
	byTitle := map[string]migrate.InstructionCandidate{}
	for _, c := range candidates {
		if c.item.Type == "instruction" {
			item := migrate.InstructionCandidate{Title: c.item.Title, Body: c.item.Body, Activation: c.activation}
			newInstructions = append(newInstructions, item)
			byTitle[item.Title] = item
		}
	}
	for _, item := range existingInstructions {
		byTitle[item.Title] = item
	}
	conflict, err := migrate.CheckInstructionBudget(existingInstructions, newInstructions, 1500)
	if err != nil {
		fmt.Fprintf(stdout, "instruction budget check failed: %v\n", err)
		return 1
	}
	if conflict != nil {
		fmt.Fprintf(stdout, "instruction budget exceeded for repo_path=%s task=%s: %d/1500\n", conflict.Context.RepoPath, conflict.Context.Task, conflict.Chars)
		for _, title := range conflict.Titles {
			item := byTitle[title]
			fmt.Fprintf(stdout, "- %s (%d%s)\n", title, len([]rune(item.Body)), activationSuffix(item.Activation))
		}
		return 1
	}
	for _, c := range candidates {
		fmt.Fprintf(stdout, "%s %s <- %s\n", c.item.Type, c.item.Title, c.item.Source)
	}
	for _, reason := range dropped {
		fmt.Fprintf(stdout, "dropped: %s\n", reason)
	}
	if *dryRun {
		return 0
	}
	runArtifacts := map[string]string{}
	for _, a := range pendingArtifacts {
		runArtifacts[a.Rel] = digests[a.Rel]
	}
	if len(runArtifacts) == 0 {
		fmt.Fprintln(stdout, "all artifacts already migrated")
		return 0
	}
	covered := map[string]bool{}
	for _, c := range candidates {
		covered[sourceFile(c.item.Source)] = true
	}
	for source := range runArtifacts {
		if !covered[source] {
			fmt.Fprintf(stdout, "migration refused: %s produced no persisted knowledge; keep the source file\n", source)
			return 1
		}
	}
	runID, err := api.BeginMigration(project, runArtifacts)
	if err != nil {
		fmt.Fprintf(stdout, "begin migration: %v\n", err)
		return 1
	}
	for _, c := range candidates {
		if c.status == "archived" && c.confidence == "" {
			c.confidence = "trusted"
		}
		k := store.Knowledge{Type: c.item.Type, Title: c.item.Title, Body: c.item.Body, Scope: scope.Axes{Project: project}, Activation: c.activation, Confidence: c.confidence, Status: c.status, Origin: "distilled", SessionRef: sourceFile(c.item.Source)}
		source := sourceFile(c.item.Source)
		state, kind, ref := "", "", ""
		if c.item.Type == "request" {
			state = "open"
			if strings.Contains(c.item.Source, "#") {
				state, kind, ref = "done", "file", c.item.Source
			}
		}
		paths := append([]string(nil), c.activation.Paths...)
		tasks := append([]string(nil), c.activation.Tasks...)
		sort.Strings(paths)
		sort.Strings(tasks)
		key := contentDigest([]byte(strings.Join([]string{project, source, digests[source], c.item.Type, c.item.Title, c.item.Body, ref, strings.Join(paths, "\x1f"), strings.Join(tasks, "\x1f")}, "\x00")))
		_, err := api.InsertMigrated(store.MigratedEntry{Knowledge: k, RunID: runID, Digest: digests[source], Quote: c.item.Quote, ItemKey: key, RequestState: state, EvidenceKind: kind, EvidenceRef: ref})
		if err != nil {
			fmt.Fprintf(stdout, "store %s: %v\n", c.item.Title, err)
			return 1
		}
	}
	if err := api.CompleteMigration(runID); err != nil {
		fmt.Fprintf(stdout, "complete migration: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "migrated %d entries from %d artifacts\n", len(candidates), len(artifacts))
	return 0
}

func sourceFile(source string) string {
	if i := strings.LastIndex(source, "#"); i >= 0 {
		return source[:i]
	}
	return source
}

func activationSuffix(rule activation.Rule) string {
	var parts []string
	if len(rule.Paths) > 0 {
		parts = append(parts, "paths:"+strings.Join(rule.Paths, ","))
	}
	if len(rule.Tasks) > 0 {
		parts = append(parts, "tasks:"+strings.Join(rule.Tasks, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

func cleanMigrated(repo, project string, artifacts []migrate.Artifact, api *client.Client, stdout io.Writer) int {
	completed, err := api.CompletedMigrationArtifacts(project)
	if err != nil {
		fmt.Fprintf(stdout, "verify migration: %v\n", err)
		return 1
	}
	var missing []string
	for _, a := range artifacts {
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			fmt.Fprintf(stdout, "verify %s: %v\n", a.Rel, err)
			return 1
		}
		if !containsString(completed[a.Rel], contentDigest(raw)) {
			missing = append(missing, a.Rel)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "cleanup refused; no migrated entry for: %s\n", strings.Join(missing, ", "))
		return 1
	}
	if len(artifacts) == 0 {
		fmt.Fprintln(stdout, "no artifacts to clean")
		return 0
	}
	backup, err := os.MkdirTemp(filepath.Dir(repo), filepath.Base(repo)+".ghosttree-backup-")
	if err != nil {
		fmt.Fprintf(stdout, "create backup: %v\n", err)
		return 1
	}
	for _, a := range artifacts {
		dst := filepath.Join(backup, filepath.FromSlash(a.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintf(stdout, "backup failed: %v\n", err)
			return 1
		}
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			fmt.Fprintf(stdout, "backup failed: %v\n", err)
			return 1
		}
		info, err := os.Stat(a.Path)
		if err != nil {
			fmt.Fprintf(stdout, "backup failed: %v\n", err)
			return 1
		}
		if err := os.WriteFile(dst, raw, info.Mode().Perm()); err != nil {
			fmt.Fprintf(stdout, "backup failed: %v\n", err)
			return 1
		}
	}
	for _, a := range artifacts {
		if err := os.Remove(a.Path); err != nil {
			fmt.Fprintf(stdout, "remove %s: %v (backup: %s)\n", a.Rel, err, backup)
			return 1
		}
	}
	fmt.Fprintf(stdout, "removed %d artifacts; backup: %s\n", len(artifacts), backup)
	return 0
}

func contentDigest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
