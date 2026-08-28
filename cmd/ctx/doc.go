package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const docUsage = `usage: ctx doc <command>

  new <kind> <slug>              create a local draft
  pull <slug> [--force]          fetch the head revision into the worktree
  push <slug> -m "..." [--clean] publish a new revision
  diff <slug> [--remote]         compare local, base, and remote text
  log <slug>                     list revisions
  ls [--kind K] [--all]          list documents and local drafts
  show <slug> [--rev N]          write one revision to stdout
  mv <slug> <new-slug>            rename without losing history
  close <slug>                   archive a document
  import <file> --kind K [--slug S] [--clean]

  kinds: spec, plan, investigation, report, other`

func cmdDoc(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, docUsage)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		fmt.Fprintln(stdout, "ctx doc must run inside a repository with an origin remote")
		return 1
	}
	if args[0] == "new" {
		return docNew(gitCtx.Root, args[1:], stdout)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s); run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)
	switch args[0] {
	case "pull":
		rest, force := removeBoolArg(args[1:], "--force")
		if len(rest) != 1 {
			return docCLIUsage(stdout)
		}
		return docPull(gitCtx.Root, gitCtx.Project, c, rest[0], force, stdout)
	case "push":
		slug, message, clean, ok := parseDocPushArgs(args[1:])
		if !ok {
			return docCLIUsage(stdout)
		}
		return docPush(gitCtx.Root, gitCtx.Project, c, slug, message, clean, stdout)
	case "diff":
		rest, remote := removeBoolArg(args[1:], "--remote")
		if len(rest) != 1 {
			return docCLIUsage(stdout)
		}
		return docDiff(gitCtx.Root, gitCtx.Project, c, rest[0], remote, stdout)
	case "log":
		if len(args) != 2 {
			return docCLIUsage(stdout)
		}
		return docLog(gitCtx.Project, c, args[1], stdout)
	case "ls":
		fs := flag.NewFlagSet("doc ls", flag.ContinueOnError)
		fs.SetOutput(stdout)
		kind := fs.String("kind", "", "document kind")
		all := fs.Bool("all", false, "include archived documents")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return docCLIUsage(stdout)
		}
		return docList(gitCtx.Root, gitCtx.Project, c, *kind, *all, stdout)
	case "show":
		slug, revision, ok := parseDocShowArgs(args[1:])
		if !ok {
			return docCLIUsage(stdout)
		}
		return docShow(gitCtx.Project, c, slug, revision, stdout)
	case "mv":
		if len(args) != 3 {
			return docCLIUsage(stdout)
		}
		return docMove(gitCtx.Root, gitCtx.Project, c, args[1], args[2], stdout)
	case "close":
		if len(args) != 2 {
			return docCLIUsage(stdout)
		}
		return docClose(gitCtx.Project, c, args[1], stdout)
	case "import":
		return docImport(gitCtx.Root, gitCtx.Project, c, args[1:], stdout)
	default:
		return docCLIUsage(stdout)
	}
}

func docCLIUsage(stdout io.Writer) int {
	fmt.Fprintln(stdout, docUsage)
	return 2
}

func docNew(repoRoot string, args []string, stdout io.Writer) int {
	if len(args) != 2 {
		return docCLIUsage(stdout)
	}
	kind, slug := args[0], args[1]
	rel, err := docwork.RelPath(kind, time.Now().UTC().Format(time.RFC3339), slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if _, exists := state[slug]; exists {
		fmt.Fprintf(stdout, "a local document named %q already exists\n", slug)
		return 1
	}
	if err := docwork.WriteFile(repoRoot, rel, "# "+slug+"\n"); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	state[slug] = docwork.Entry{Path: rel}
	if err := docwork.SaveState(repoRoot, state); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintln(stdout, filepath.Join(docwork.Dir(repoRoot), filepath.FromSlash(rel)))
	return 0
}

func docHasChanged(repoRoot string, entry docwork.Entry) (bool, error) {
	body, err := docwork.ReadFile(repoRoot, entry.Path)
	if err != nil {
		return false, err
	}
	return store.Digest(body) != entry.BaseDigest, nil
}

func findDocument(c *client.Client, project, slug string) (store.Document, error) {
	documents, err := c.Documents(project, "", true)
	if err != nil {
		return store.Document{}, err
	}
	for _, document := range documents {
		if document.Slug == slug {
			return document, nil
		}
	}
	return store.Document{}, fmt.Errorf("document %q not found", slug)
}

func docPull(repoRoot, project string, c *client.Client, slug string, force bool, stdout io.Writer) int {
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if entry, ok := state[slug]; ok && entry.BaseDigest != "" && !force {
		changed, readErr := docHasChanged(repoRoot, entry)
		if readErr != nil && !os.IsNotExist(readErr) {
			fmt.Fprintf(stdout, "refusing to overwrite %s: %v\n", entry.Path, readErr)
			return 1
		}
		if readErr == nil && changed {
			fmt.Fprintf(stdout, "%s is modified; commit it with 'ctx doc push' or use --force\n", entry.Path)
			return 1
		}
	}
	document, err := findDocument(c, project, slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	revision, err := c.DocumentRevision(document.ID, document.HeadRevision)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	rel, err := docwork.RelPath(document.Kind, document.CreatedAt, document.Slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if err := docwork.WriteFile(repoRoot, rel, revision.Body); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	state[slug] = docwork.Entry{DocumentID: document.ID, BaseRevision: document.HeadRevision, BaseDigest: revision.Digest, Path: rel}
	if err := docwork.SaveState(repoRoot, state); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s rev %d -> %s\n", slug, document.HeadRevision, filepath.Join(docwork.Dir(repoRoot), rel))
	return 0
}

func docPush(repoRoot, project string, c *client.Client, slug, message string, clean bool, stdout io.Writer) int {
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	entry, ok := state[slug]
	if !ok {
		fmt.Fprintf(stdout, "no local copy for %q; run 'ctx doc pull %s'\n", slug, slug)
		return 1
	}
	body, err := docwork.ReadFile(repoRoot, entry.Path)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if entry.DocumentID != 0 && store.Digest(body) == entry.BaseDigest {
		fmt.Fprintf(stdout, "unchanged: %s (rev %d)\n", slug, entry.BaseRevision)
		return 0
	}
	if message == "" {
		fmt.Fprintln(stdout, "push requires a revision message: -m \"...\"")
		return 2
	}
	var document store.Document
	if entry.DocumentID == 0 {
		kind, kindErr := docwork.KindOfDir(strings.Split(filepath.ToSlash(entry.Path), "/")[0])
		if kindErr != nil {
			fmt.Fprintln(stdout, kindErr)
			return 1
		}
		document, err = c.CreateDocument(store.Document{Project: project, Slug: slug, Kind: kind, Title: docTitle(body, slug)}, body, message)
	} else {
		document, err = c.PushDocumentRevision(entry.DocumentID, entry.BaseRevision, body, message)
	}
	var conflict *client.ConflictError
	if errors.As(err, &conflict) {
		fmt.Fprintf(stdout, "CONFLICT %s\n  your base: rev %d\n  head: rev %d, %s, %s, %q\n\n  ctx doc diff %s --remote\n  ctx doc pull %s --force\n", slug, entry.BaseRevision, conflict.HeadRevision, conflict.Person, conflict.At, conflict.Message, slug, slug)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stdout, "push: %v\n", err)
		return 1
	}
	entry.DocumentID = document.ID
	entry.BaseRevision = document.HeadRevision
	entry.BaseDigest = store.Digest(body)
	state[slug] = entry
	if err := docwork.SaveState(repoRoot, state); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s rev %d\n", slug, document.HeadRevision)
	if clean {
		if err := os.Remove(filepath.Join(docwork.Dir(repoRoot), filepath.FromSlash(entry.Path))); err != nil {
			fmt.Fprintf(stdout, "remove worktree copy: %v\n", err)
			return 1
		}
		delete(state, slug)
		if err := docwork.SaveState(repoRoot, state); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintln(stdout, "local copy removed")
	}
	return 0
}

func docTitle(body, fallback string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# ") && strings.TrimSpace(line[2:]) != "" {
			return strings.TrimSpace(line[2:])
		}
	}
	return fallback
}

func docLog(project string, c *client.Client, slug string, stdout io.Writer) int {
	document, err := findDocument(c, project, slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	revisions, err := c.DocumentRevisions(document.ID)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	for _, revision := range revisions {
		fmt.Fprintf(stdout, "%d  %s  %s  %s\n", revision.Revision, revision.CreatedAt, revision.Person, revision.Message)
	}
	return 0
}

func docList(repoRoot, project string, c *client.Client, kind string, all bool, stdout io.Writer) int {
	documents, err := c.Documents(project, kind, all)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	seen := map[string]bool{}
	for _, document := range documents {
		seen[document.Slug] = true
		fmt.Fprintf(stdout, "%-32s %-14s rev %-4d %s\n", document.Slug, document.Kind, document.HeadRevision, document.Status)
	}
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	var drafts []string
	for slug, entry := range state {
		if entry.DocumentID == 0 && !seen[slug] {
			drafts = append(drafts, slug)
		}
	}
	sort.Strings(drafts)
	for _, slug := range drafts {
		entry := state[slug]
		dir := strings.Split(filepath.ToSlash(entry.Path), "/")[0]
		entryKind, _ := docwork.KindOfDir(dir)
		if kind == "" || kind == entryKind {
			fmt.Fprintf(stdout, "%-32s %-14s %-8s %s\n", slug, entryKind, "draft", "local")
		}
	}
	return 0
}

func docShow(project string, c *client.Client, slug string, revision int, stdout io.Writer) int {
	document, err := findDocument(c, project, slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if revision == 0 {
		revision = document.HeadRevision
	}
	got, err := c.DocumentRevision(document.ID, revision)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprint(stdout, got.Body)
	return 0
}

func docDiff(repoRoot, project string, c *client.Client, slug string, remote bool, stdout io.Writer) int {
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	entry, ok := state[slug]
	if !ok || entry.DocumentID == 0 {
		fmt.Fprintf(stdout, "%q has no published base revision\n", slug)
		return 1
	}
	base, err := c.DocumentRevision(entry.DocumentID, entry.BaseRevision)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	other, err := docwork.ReadFile(repoRoot, entry.Path)
	label := "local"
	if remote {
		document, findErr := findDocument(c, project, slug)
		if findErr != nil {
			fmt.Fprintln(stdout, findErr)
			return 1
		}
		head, readErr := c.DocumentRevision(document.ID, document.HeadRevision)
		if readErr != nil {
			fmt.Fprintln(stdout, readErr)
			return 1
		}
		other, err, label = head.Body, nil, "remote"
	}
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	printLineDiff(stdout, "base", label, base.Body, other)
	return 0
}

func printLineDiff(stdout io.Writer, oldLabel, newLabel, oldText, newText string) {
	fmt.Fprintf(stdout, "--- %s\n+++ %s\n", oldLabel, newLabel)
	if oldText == newText {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
		fmt.Fprintf(stdout, "-%s\n", line)
	}
	for _, line := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
		fmt.Fprintf(stdout, "+%s\n", line)
	}
}

func docMove(repoRoot, project string, c *client.Client, slug, newSlug string, stdout io.Writer) int {
	if err := docwork.ValidateSlug(newSlug); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	state, err := docwork.LoadState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	entry, local := state[slug]
	if _, exists := state[newSlug]; exists {
		fmt.Fprintf(stdout, "a local document named %q already exists\n", newSlug)
		return 1
	}
	if local && entry.DocumentID == 0 {
		kind, kindErr := docwork.KindOfDir(strings.Split(filepath.ToSlash(entry.Path), "/")[0])
		if kindErr != nil {
			fmt.Fprintln(stdout, kindErr)
			return 1
		}
		base := filepath.Base(entry.Path)
		createdAt := base
		if len(base) >= 10 {
			createdAt = base[:10]
		}
		newPath, _ := docwork.RelPath(kind, createdAt, newSlug)
		oldPath := entry.Path
		if err := docwork.MoveFile(repoRoot, oldPath, newPath); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		entry.Path = newPath
		delete(state, slug)
		state[newSlug] = entry
		if err := docwork.SaveState(repoRoot, state); err != nil {
			_ = docwork.MoveFile(repoRoot, newPath, oldPath)
			fmt.Fprintln(stdout, err)
			return 1
		}
	} else {
		document, findErr := findDocument(c, project, slug)
		if findErr != nil {
			fmt.Fprintln(stdout, findErr)
			return 1
		}
		if local {
			newPath, _ := docwork.RelPath(document.Kind, document.CreatedAt, newSlug)
			oldPath := entry.Path
			if err := docwork.MoveFile(repoRoot, oldPath, newPath); err != nil {
				fmt.Fprintln(stdout, err)
				return 1
			}
			entry.Path = newPath
			if _, patchErr := c.PatchDocument(document.ID, map[string]string{"slug": newSlug}); patchErr != nil {
				_ = docwork.MoveFile(repoRoot, newPath, oldPath)
				fmt.Fprintln(stdout, patchErr)
				return 1
			}
			delete(state, slug)
			state[newSlug] = entry
			if err := docwork.SaveState(repoRoot, state); err != nil {
				_, _ = c.PatchDocument(document.ID, map[string]string{"slug": slug})
				_ = docwork.MoveFile(repoRoot, newPath, oldPath)
				fmt.Fprintln(stdout, err)
				return 1
			}
		} else if _, patchErr := c.PatchDocument(document.ID, map[string]string{"slug": newSlug}); patchErr != nil {
			fmt.Fprintln(stdout, patchErr)
			return 1
		}
	}
	fmt.Fprintf(stdout, "%s -> %s\n", slug, newSlug)
	return 0
}

func docClose(project string, c *client.Client, slug string, stdout io.Writer) int {
	document, err := findDocument(c, project, slug)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if _, err := c.PatchDocument(document.ID, map[string]string{"status": "archived"}); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s archived\n", slug)
	return 0
}

func docImport(repoRoot, project string, c *client.Client, args []string, stdout io.Writer) int {
	source, kind, slug, clean, ok := parseDocImportArgs(args)
	if !ok {
		return docCLIUsage(stdout)
	}
	return importDocument(repoRoot, project, c, source, kind, slug, clean, stdout)
}

func importDocument(repoRoot, project string, c *client.Client, source, kind, slug string, clean bool, stdout io.Writer) int {
	if _, err := docwork.KindDir(kind); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if !utf8.Valid(raw) {
		fmt.Fprintf(stdout, "%s is not valid UTF-8; nothing was imported\n", source)
		return 1
	}
	body := string(raw)
	digest := store.Digest(body)
	if slug == "" {
		base := filepath.Base(abs)
		slug = strings.TrimSuffix(base, filepath.Ext(base))
	}
	evidenceSource := filepath.ToSlash(abs)
	if rel, relErr := filepath.Rel(repoRoot, abs); relErr == nil && rel != "." && rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		evidenceSource = filepath.ToSlash(rel)
	}
	runID, err := c.BeginMigration(project, map[string]string{evidenceSource: digest})
	if err != nil {
		fmt.Fprintf(stdout, "begin import: %v\n", err)
		return 1
	}
	document, err := c.ImportDocument(store.MigratedDocument{
		Document: store.Document{Project: project, Slug: slug, Kind: kind, Title: docTitle(body, slug)},
		RunID:    runID, Source: evidenceSource, Digest: digest, Body: body, Message: "import " + evidenceSource,
	})
	if err != nil {
		fmt.Fprintf(stdout, "import document: %v\n", err)
		return 1
	}
	if err := c.CompleteMigration(runID); err != nil {
		fmt.Fprintf(stdout, "complete import: %v\n", err)
		return 1
	}
	if clean {
		proven, proofErr := c.CompletedDocumentArtifacts(project)
		if proofErr != nil || !containsString(proven[evidenceSource], digest) {
			fmt.Fprintf(stdout, "refusing --clean: exact import proof is missing for %s\n", evidenceSource)
			return 1
		}
		current, readErr := os.ReadFile(abs)
		if readErr != nil || !utf8.Valid(current) || store.Digest(string(current)) != digest {
			fmt.Fprintf(stdout, "refusing --clean: %s changed after import\n", source)
			return 1
		}
		if err := os.Remove(abs); err != nil {
			fmt.Fprintf(stdout, "remove source: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "%s -> document %d rev 1 sha256:%s\n", evidenceSource, document.ID, digest)
	return 0
}

func parseDocImportArgs(args []string) (source, kind, slug string, clean, ok bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clean":
			clean = true
		case "--kind", "--slug":
			flagName := args[i]
			if i+1 >= len(args) {
				return "", "", "", false, false
			}
			i++
			if flagName == "--kind" {
				kind = args[i]
			} else {
				slug = args[i]
			}
		default:
			if strings.HasPrefix(args[i], "-") || source != "" {
				return "", "", "", false, false
			}
			source = args[i]
		}
	}
	return source, kind, slug, clean, source != "" && kind != ""
}

func parseDocPushArgs(args []string) (slug, message string, clean, ok bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clean":
			clean = true
		case "-m":
			if i+1 >= len(args) {
				return "", "", false, false
			}
			i++
			message = args[i]
		default:
			if strings.HasPrefix(args[i], "-") || slug != "" {
				return "", "", false, false
			}
			slug = args[i]
		}
	}
	return slug, message, clean, slug != ""
}

func parseDocShowArgs(args []string) (slug string, revision int, ok bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--rev" {
			if i+1 >= len(args) {
				return "", 0, false
			}
			i++
			if _, err := fmt.Sscanf(args[i], "%d", &revision); err != nil || revision < 1 {
				return "", 0, false
			}
			continue
		}
		if strings.HasPrefix(args[i], "-") || slug != "" {
			return "", 0, false
		}
		slug = args[i]
	}
	return slug, revision, slug != ""
}
