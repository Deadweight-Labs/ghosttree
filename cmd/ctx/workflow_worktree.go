package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// The worktree of workflows (REQ-699): .ghosttree/workflows/<name>.md, pulled
// from and pushed to the server like the document worktree. Nothing lies in
// the repository itself; see internal/workflow/worktree.go.

const workflowWorktreeUsage = `       ctx workflow ls
       ctx workflow new <name> [--template <t>]
       ctx workflow pull [<name>] [--force]
       ctx workflow push <name> -m "..."
       ctx workflow diff <name> [--remote]

The files of the workflows of this repository are .ghosttree/workflows/<name>.md,
beside the document worktree. The truth is on the server: pull brings the
current version of one workflow (or of all) into a file and remembers which
version that was; push checks the file like the web interface and the MCP tool
do and saves it as a new version, and refuses when the workflow was changed on
the server since the pull (it shows what changed; pull --force takes the server's
version over your file). new makes a file that passes the check, or one from a
template (wissen-nach-merge, wuensche-im-pr, idee-abgleich, kriterien-aus-diskussion).
Nothing of this is committed.`

// templateNames is the list for the messages; a test keeps the usage text above
// in step with it.
var templateNames = strings.Join(workflow.TemplateNames, ", ")

// isWorkflowWorktreeCommand tells the commands of the worktree from check and
// trigger.
func isWorkflowWorktreeCommand(arg string) bool {
	switch arg {
	case "ls", "new", "pull", "push", "diff":
		return true
	}
	return false
}

func cmdWorkflowWorktree(command string, args []string, stdout io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		fmt.Fprintln(stdout, "ctx workflow "+command+" must run inside a repository with an origin remote")
		return 1
	}
	if command == "new" {
		name, template, ok := parseWorkflowNewArgs(args)
		if !ok {
			return workflowWorktreeCLIUsage(stdout)
		}
		return workflowNew(gitCtx.Root, name, template, stdout)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s); run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)
	switch command {
	case "ls":
		if len(args) != 0 {
			return workflowWorktreeCLIUsage(stdout)
		}
		return workflowLs(gitCtx.Root, gitCtx.Project, c, stdout)
	case "pull":
		rest, force := removeBoolArg(args, "--force")
		if len(rest) > 1 {
			return workflowWorktreeCLIUsage(stdout)
		}
		name := ""
		if len(rest) == 1 {
			name = rest[0]
		}
		return workflowPull(gitCtx.Root, gitCtx.Project, c, name, force, stdout)
	case "push":
		name, message, ok := parseDocPushArgsNoClean(args)
		if !ok {
			return workflowWorktreeCLIUsage(stdout)
		}
		return workflowPush(gitCtx.Root, gitCtx.Project, c, name, message, stdout)
	default: // diff
		rest, remote := removeBoolArg(args, "--remote")
		if len(rest) != 1 {
			return workflowWorktreeCLIUsage(stdout)
		}
		return workflowDiff(gitCtx.Root, gitCtx.Project, c, rest[0], remote, stdout)
	}
}

func workflowWorktreeCLIUsage(stdout io.Writer) int {
	fmt.Fprintln(stdout, "usage:")
	fmt.Fprintln(stdout, strings.TrimPrefix(workflowWorktreeUsage, "       "))
	return 2
}

func parseWorkflowNewArgs(args []string) (name, template string, ok bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--template":
			if i+1 >= len(args) {
				return "", "", false
			}
			i++
			template = args[i]
		case strings.HasPrefix(args[i], "-") || name != "":
			return "", "", false
		default:
			name = args[i]
		}
	}
	return name, template, name != ""
}

// parseDocPushArgsNoClean reads <name> -m "..." (the document push has --clean
// on top; a workflow has no second copy to remove).
func parseDocPushArgsNoClean(args []string) (name, message string, ok bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-m":
			if i+1 >= len(args) {
				return "", "", false
			}
			i++
			message = args[i]
		case strings.HasPrefix(args[i], "-") || name != "":
			return "", "", false
		default:
			name = args[i]
		}
	}
	return name, message, name != ""
}

func workflowNew(repoRoot, name, template string, stdout io.Writer) int {
	if err := workflow.CheckWorktreeName(name); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	text := workflow.SkeletonText
	if template != "" {
		t, ok := workflow.TemplateByName(template)
		if !ok {
			fmt.Fprintf(stdout, "unknown template %q; expected one of %s\n", template, templateNames)
			return 2
		}
		text = t.Text
	}
	state, err := workflow.LoadWorktreeState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if _, err := os.Lstat(workflow.WorktreeFile(repoRoot, name)); err == nil {
		fmt.Fprintf(stdout, "%s already exists; edit it, or pull it with --force to start over\n", workflow.WorktreeFile(repoRoot, name))
		return 1
	}
	if err := workflow.WriteWorktreeFile(repoRoot, name, text); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	state[name] = workflow.WorktreeEntry{}
	if err := workflow.SaveWorktreeState(repoRoot, state); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintln(stdout, workflow.WorktreeFile(repoRoot, name))
	return 0
}

// workflowLocalState says what the file of a workflow is: absent, a draft
// that was never pushed, clean, or modified since the pull.
func workflowLocalState(repoRoot, name string, entry workflow.WorktreeEntry, known bool) (text string, status string, err error) {
	text, err = workflow.ReadWorktreeFile(repoRoot, name)
	switch {
	case os.IsNotExist(err):
		return "", "", nil
	case err != nil:
		return "", "", err
	case !known || entry.Version == 0:
		return text, "draft", nil
	case store.WorkflowDefHash(text) != entry.Hash:
		return text, "modified", nil
	}
	return text, "clean", nil
}

func workflowLs(repoRoot, project string, c *client.Client, stdout io.Writer) int {
	defs, err := c.ListWorkflows(project)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	state, err := workflow.LoadWorktreeState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	onServer := map[string]bool{}
	for _, d := range defs {
		onServer[d.Name] = true
		entry, known := state[d.Name]
		_, status, err := workflowLocalState(repoRoot, d.Name, entry, known)
		if err != nil {
			status = err.Error()
		}
		switch {
		case status == "":
			status = "not pulled"
		case known && entry.Version > 0 && entry.Version < d.Version:
			status += ", behind v" + fmt.Sprint(d.Version)
		}
		on := "on"
		if !d.Enabled {
			on = "off"
		}
		fmt.Fprintf(stdout, "%-32s v%-4d %-4s %-18s %s\n", d.Name, d.Version, on, d.Event, status)
	}
	names, err := workflow.WorktreeNames(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	sort.Strings(names)
	for _, name := range names {
		if !onServer[name] {
			fmt.Fprintf(stdout, "%-32s %-5s %-4s %-18s %s\n", name, "-", "-", "-", "draft, not on the server")
		}
	}
	return 0
}

// workflowPull brings the current version of one workflow, or of all, into
// the worktree. A file with changes that were not pushed is not overwritten
// without --force.
func workflowPull(repoRoot, project string, c *client.Client, name string, force bool, stdout io.Writer) int {
	var defs []store.WorkflowDef
	if name != "" {
		if err := workflow.CheckWorktreeName(name); err != nil {
			fmt.Fprintln(stdout, err)
			return 2
		}
		detail, err := c.GetWorkflow(project, name, false)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		defs = []store.WorkflowDef{detail.Workflow}
	} else {
		var err error
		if defs, err = c.ListWorkflows(project); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
	}
	state, err := workflow.LoadWorktreeState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	code := 0
	for _, d := range defs {
		entry, known := state[d.Name]
		_, status, readErr := workflowLocalState(repoRoot, d.Name, entry, known)
		if !force && (readErr != nil || status == "modified" || status == "draft") {
			if readErr != nil {
				fmt.Fprintf(stdout, "refusing to overwrite %s: %v\n", workflow.WorktreeFile(repoRoot, d.Name), readErr)
			} else {
				fmt.Fprintf(stdout, "%s has changes that were not pushed; push it with 'ctx workflow push %s -m ...' or pull with --force\n", workflow.WorktreeFile(repoRoot, d.Name), d.Name)
			}
			code = 1
			continue
		}
		if err := workflow.WriteWorktreeFile(repoRoot, d.Name, d.Text); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		state[d.Name] = workflow.WorktreeEntry{Version: d.Version, Hash: store.WorkflowDefHash(d.Text)}
		// The state is saved after each file, so that a stop in the middle
		// of "pull all" leaves every written file with its base.
		if err := workflow.SaveWorktreeState(repoRoot, state); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s v%d -> %s\n", d.Name, d.Version, workflow.WorktreeFile(repoRoot, d.Name))
	}
	if name == "" && len(defs) == 0 {
		fmt.Fprintln(stdout, "no workflows on the server yet; 'ctx workflow new <name>' starts one")
	}
	return code
}

// workflowConflict reports a refused save: the server's version is not the one
// the file was pulled at. The difference shown is what changed on the server
// since then, so that it can be merged by hand.
func workflowConflict(stdout io.Writer, c *client.Client, project, name string, entry workflow.WorktreeEntry, exists bool) {
	if exists {
		fmt.Fprintf(stdout, "CONFLICT %s\n  the server has a workflow of this name that this file was never pulled from\n\n  ctx workflow diff %s --remote   (pull it first to compare)\n  ctx workflow pull %s --force     (replaces your file with the server's version)\n", name, name, name)
		return
	}
	detail, err := c.GetWorkflow(project, name, true)
	if err != nil {
		fmt.Fprintf(stdout, "CONFLICT %s: it was changed on the server since version %d (%v)\n", name, entry.Version, err)
		return
	}
	head := detail.Workflow
	fmt.Fprintf(stdout, "CONFLICT %s\n  your base: version %d\n  server: version %d, %s\n\n", name, entry.Version, head.Version, head.UpdatedAt)
	for _, v := range detail.Versions {
		if v.Version == entry.Version {
			printLineDiff(stdout, fmt.Sprintf("base (v%d)", v.Version), fmt.Sprintf("server (v%d)", head.Version), v.Text, head.Text)
			break
		}
	}
	fmt.Fprintf(stdout, "\n  ctx workflow diff %s --remote\n  ctx workflow pull %s --force\n", name, name)
}

func workflowPush(repoRoot, project string, c *client.Client, name, message string, stdout io.Writer) int {
	if err := workflow.CheckWorktreeName(name); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	state, err := workflow.LoadWorktreeState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	entry, known := state[name]
	body, err := workflow.ReadWorktreeFile(repoRoot, name)
	if os.IsNotExist(err) {
		fmt.Fprintf(stdout, "no file for %q; run 'ctx workflow pull %s' or 'ctx workflow new %s'\n", name, name, name)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if !known {
		// A file written by hand into the directory is a draft like one that
		// 'new' made.
		entry = workflow.WorktreeEntry{}
	}
	if entry.Version > 0 && store.WorkflowDefHash(body) == entry.Hash {
		fmt.Fprintf(stdout, "unchanged: %s (v%d)\n", name, entry.Version)
		return 0
	}
	if message == "" {
		fmt.Fprintln(stdout, "push requires a note for the version: -m \"...\"")
		return 2
	}
	if len(body) > workflow.MaxDefBytes {
		fmt.Fprintf(stdout, "%s: the file has %d KiB; a workflow has at most %d KiB; nothing was sent\n", name, len(body)>>10, workflow.MaxDefBytes>>10)
		return 1
	}
	saved, err := c.SaveWorkflowVia(project, name, body, message, entry.Version, store.WorkflowViaAPI)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == "workflow_conflict" || apiErr.Code == "workflow_exists") {
		workflowConflict(stdout, c, project, name, entry, apiErr.Code == "workflow_exists")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stdout, "push: %v\n", err)
		return 1
	}
	if saved.Saved == nil {
		fmt.Fprint(stdout, "not saved, the file has errors:\n"+workflow.FormatReport(saved.Check))
		return 1
	}
	def := saved.Saved.Def
	state[name] = workflow.WorktreeEntry{Version: def.Version, Hash: store.WorkflowDefHash(body)}
	if err := workflow.SaveWorktreeState(repoRoot, state); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	switch {
	case saved.Saved.Created:
		fmt.Fprintf(stdout, "%s created, v%d; it applies to the next run\n", name, def.Version)
	case saved.Saved.Changed:
		fmt.Fprintf(stdout, "%s v%d; it applies to the next run\n", name, def.Version)
	default:
		fmt.Fprintf(stdout, "%s unchanged, still v%d\n", name, def.Version)
	}
	for _, w := range saved.Check.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w.String())
	}
	cliCard(stdout, clientBuilder(c, project).WorkflowCard(def))
	return 0
}

func workflowDiff(repoRoot, project string, c *client.Client, name string, remote bool, stdout io.Writer) int {
	if err := workflow.CheckWorktreeName(name); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	state, err := workflow.LoadWorktreeState(repoRoot)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	entry, ok := state[name]
	if !ok || entry.Version == 0 {
		fmt.Fprintf(stdout, "%q has no pulled base version\n", name)
		return 1
	}
	detail, err := c.GetWorkflow(project, name, true)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	base, found := "", false
	for _, v := range detail.Versions {
		if v.Version == entry.Version {
			base, found = v.Text, true
		}
	}
	if !found {
		fmt.Fprintf(stdout, "version %d of %q is not on the server\n", entry.Version, name)
		return 1
	}
	other, label := detail.Workflow.Text, fmt.Sprintf("server (v%d)", detail.Workflow.Version)
	if !remote {
		if other, err = workflow.ReadWorktreeFile(repoRoot, name); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		label = "local"
	}
	printLineDiff(stdout, fmt.Sprintf("base (v%d)", entry.Version), label, base, other)
	return 0
}
