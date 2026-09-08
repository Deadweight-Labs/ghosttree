package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func ghostArchive(args []string, stdout io.Writer) int {
	var paths []string
	reason := ""
	confirm, literal := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case literal:
			paths = append(paths, arg)
		case arg == "--":
			literal = true
		case arg == "--confirm-deleted":
			confirm = true
		case arg == "--reason" && i+1 < len(args):
			i++
			reason = args[i]
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintln(stdout, "ungültige Option:", arg)
			return 2
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 || len(paths) > store.MaxGhostArchivePaths || len(reason) > 1024 || (confirm && strings.TrimSpace(reason) == "") {
		fmt.Fprintln(stdout, "usage: ctx ghost archive <pfad>... [--reason <grund> --confirm-deleted] (1..64 genaue Pfade; ohne Bestätigung nur Vorschau)")
		return 2
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if err := store.ValidateGhostArchivePath(p); err != nil {
			fmt.Fprintln(stdout, err)
			return 2
		}
		if seen[p] {
			fmt.Fprintln(stdout, "doppelter Pfad:", p)
			return 2
		}
		seen[p] = true
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Root == "" || gitCtx.Project == "" {
		fmt.Fprintln(stdout, "kein Repository mit Projektzuordnung")
		return 1
	}
	entries, err := archiveRepoEntries(gitCtx.Root)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	live := map[string]bool{}
	for _, e := range entries {
		live[e.Path] = true
	}
	c := client.New(cfg)
	stored, err := c.GhostTree(gitCtx.Project, "")
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	described := map[string]store.GhostFile{}
	for _, g := range stored {
		described[g.Path] = g
	}
	moves, err := ghost.DetectMovesStrict(gitCtx.Root, entries, described)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	var targets []store.GhostArchiveTarget
	for _, p := range paths {
		if live[p] {
			fmt.Fprintf(stdout, "%s ist noch versioniert; keine Archivierung\n", p)
			return 1
		}
		if err := archivePathAbsent(gitCtx.Root, p); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		if to, ok := moves[p]; ok {
			fmt.Fprintf(stdout, "%s ist möglicherweise nach %s verschoben; zuerst ctx mirror ausführen\n", p, to)
			return 1
		}
		g, ok := described[p]
		if !ok {
			fmt.Fprintf(stdout, "%s: keine aktive Beschreibung\n", p)
			continue
		}
		candidate, err := c.PrepareGhostArchive(gitCtx.Project, p)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		if candidate.File != g {
			fmt.Fprintf(stdout, "%s wurde inzwischen geändert; Vorschau erneut prüfen\n", p)
			return 1
		}
		targets = append(targets, candidate.Target)
		if !confirm {
			fmt.Fprintf(stdout, "Vorschau: %s [%s]\n", p, g.Kind)
		}
	}
	if !confirm {
		fmt.Fprintln(stdout, "Keine Änderung. Nach Prüfung dieselben Pfade mit --reason <grund> --confirm-deleted bestätigen.")
		return 0
	}
	if len(targets) == 0 {
		return 0
	}
	result, err := c.ArchiveGhosts(store.GhostArchiveInput{Project: gitCtx.Project, Targets: targets, Reason: reason, ConfirmDeleted: true})
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	for _, p := range result.Archived {
		fmt.Fprintf(stdout, "archiviert: %s — %s\n", p, reason)
	}
	for _, p := range result.AlreadyArchived {
		fmt.Fprintf(stdout, "bereits archiviert: %s\n", p)
	}
	return 0
}

func archiveRepoEntries(root string) ([]ghost.Entry, error) {
	raw, err := exec.Command("git", "-C", root, "ls-files", "--stage", "-z").Output()
	if err != nil {
		return nil, err
	}
	var entries []ghost.Entry
	dirs := map[string]bool{}
	for _, record := range strings.Split(string(raw), "\x00") {
		if record == "" {
			continue
		}
		metadata, p, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || fields[2] != "0" {
			return nil, fmt.Errorf("ungelöster oder unlesbarer Git-Index; keine Archivierung")
		}
		if p == "" || strings.HasPrefix(p, ".ghosttree/") {
			continue
		}
		kind := "file"
		if fields[0] == "160000" || fields[0] == "120000" {
			kind = "dir"
		}
		entries = append(entries, ghost.Entry{Path: p, Kind: kind})
		for _, parent := range store.ParentPaths(p) {
			dirs[parent] = true
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("leere Git-Dateiliste; keine sichere Archivierung möglich")
	}
	for p := range dirs {
		entries = append(entries, ghost.Entry{Path: p, Kind: "dir"})
	}
	return entries, nil
}

func archivePathAbsent(root, p string) error {
	full := root
	parts := strings.Split(p, "/")
	for i, part := range parts {
		full = filepath.Join(full, part)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s enthält einen Symlink; keine Archivierung", p)
		}
		if i == len(parts)-1 || !info.IsDir() {
			return fmt.Errorf("%s ist noch vorhanden; keine Archivierung", p)
		}
	}
	return nil
}
