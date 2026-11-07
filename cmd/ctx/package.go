package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const packageUsage = `usage: ctx package <command>

  create [--branch B] [--name N] [--pr N] [--project P] [REQ[:optional]...]   new package for a branch (default: the current one)
  add [--package N] [--reason R] REQ[:optional]...                            put requests into a package (default: the current branch's)
  remove [--package N] --reason R REQ                                         take a request out; the reason goes into its activity list
  set [--package N] [--name N] [--pr N | --unlink-pr] [--state open|closed]   rename, bind to a pull request, close
  show [--package N | --pr N | --branch B] [--project P] [--review] [--json]                the package (default: the current branch's, inherited from the branch it was cut from)
  import-relations [--project P] [--dry-run]                                  turn branch:<project>@<branch> relations into packages

A request is written REQ or REQ:optional; the default role is pflicht.`

func cmdPackage(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	if len(args) == 0 {
		fmt.Fprintln(stdout, packageUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create", "add", "remove", "set", "show", "import-relations":
	default:
		fmt.Fprintln(stdout, packageUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	git := currentGitContext(cfg.Machine).axes
	usageErr := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(stdout, msg)
		}
		fmt.Fprintln(stdout, packageUsage)
		return 2
	}
	switch sub {
	case "create":
		fs := flag.NewFlagSet("package create", flag.ContinueOnError)
		fs.SetOutput(stdout)
		branch := fs.String("branch", git.Branch, "branch the package is bound to (default: the current one)")
		name := fs.String("name", "", "package name (default: the branch name)")
		pr := fs.Int64("pr", 0, "pull request number")
		project := fs.String("project", "", "project (remote or unique short name); default: the current one")
		positional, err := parseInterspersed(fs, rest)
		if err != nil {
			return usageErr("")
		}
		members, err := parsePackageRequests(positional)
		if err != nil {
			return usageErr(err.Error())
		}
		target, err := c.WriteTarget(*project, git.Project)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		if *branch == "" {
			return usageErr("no branch here: pass --branch")
		}
		d, err := c.CreatePackage(target, *branch, *name, *pr, members)
		return writePackageResult(stdout, jsonMode, d, err, false)
	case "add", "remove", "set":
		fs := flag.NewFlagSet("package "+sub, flag.ContinueOnError)
		fs.SetOutput(stdout)
		id := fs.Int64("package", 0, "package number (default: the current branch's)")
		reason := fs.String("reason", "", "why (required for remove)")
		name := fs.String("name", "", "new name")
		pr := fs.Int64("pr", 0, "pull request number")
		unlink := fs.Bool("unlink-pr", false, "release the pull request number")
		state := fs.String("state", "", "open or closed")
		positional, err := parseInterspersed(fs, rest)
		if err != nil {
			return usageErr("")
		}
		if *id == 0 {
			d, err := c.FindPackage(git, 0, false)
			if err != nil {
				if client.IsPackageNotFound(err) {
					return writeRequestCLIError(stdout, jsonMode, 1, fmt.Errorf("branch %s has no open package: create one, or pass --package", git.Branch))
				}
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
			*id = d.ID
		}
		var d requestdomain.PackageDetail
		switch sub {
		case "add":
			members, err := parsePackageRequests(positional)
			if err != nil || len(members) == 0 {
				return usageErr("name at least one request")
			}
			d, err = c.AddPackageRequests(*id, members, *reason)
			return writePackageResult(stdout, jsonMode, d, err, false)
		case "remove":
			members, err := parsePackageRequests(positional)
			if err != nil || len(members) != 1 || *reason == "" {
				return usageErr("name exactly one request and give --reason")
			}
			d, err = c.RemovePackageRequest(*id, members[0].RequestID, *reason)
			return writePackageResult(stdout, jsonMode, d, err, false)
		default:
			patch := requestdomain.PackagePatch{}
			if *name != "" {
				patch.Name = name
			}
			if *state != "" {
				patch.State = state
			}
			switch {
			case *unlink && *pr != 0:
				return usageErr("--pr and --unlink-pr exclude each other")
			case *unlink:
				zero := int64(0)
				patch.PR = &zero
			case *pr != 0:
				patch.PR = pr
			}
			d, err = c.UpdatePackage(*id, patch)
			return writePackageResult(stdout, jsonMode, d, err, false)
		}
	case "show":
		fs := flag.NewFlagSet("package show", flag.ContinueOnError)
		fs.SetOutput(stdout)
		id := fs.Int64("package", 0, "package number")
		pr := fs.Int64("pr", 0, "pull request number")
		branch := fs.String("branch", "", "branch")
		project := fs.String("project", "", "project of the branch (remote or unique short name); default: the current one")
		review := fs.Bool("review", false, "the complete requirements list with every criterion and its evidence")
		if _, err := parseInterspersed(fs, rest); err != nil {
			return usageErr("")
		}
		var d requestdomain.PackageDetail
		switch {
		case *id != 0:
			d, err = c.GetPackage(*id, *review)
		default:
			ax := git
			if *project != "" {
				target, err := c.WriteTarget(*project, git.Project)
				if err != nil {
					return writeRequestCLIError(stdout, jsonMode, 1, err)
				}
				ax.Project = target
			}
			if *branch != "" && *branch != ax.Branch {
				ax.Branch, ax.Lineage = *branch, nil
			}
			if *pr == 0 && ax.Branch == "" {
				return usageErr("no branch here: pass --package, --branch or --pr")
			}
			d, err = c.FindPackage(ax, *pr, *review)
		}
		return writePackageResult(stdout, jsonMode, d, err, *review)
	default: // import-relations
		fs := flag.NewFlagSet("package import-relations", flag.ContinueOnError)
		fs.SetOutput(stdout)
		project := fs.String("project", "", "project (remote or unique short name); default: the current one")
		dry := fs.Bool("dry-run", false, "only report what would happen")
		if _, err := parseInterspersed(fs, rest); err != nil {
			return usageErr("")
		}
		target, err := c.WriteTarget(*project, git.Project)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		report, err := c.ImportBranchRelations(target, *dry)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		if jsonMode {
			return writeRequestCLIResult(stdout, true, report, nil, nil)
		}
		writeImportReport(stdout, report)
		return 0
	}
}

// parseInterspersed liest Flags auch hinter Positionsargumenten:
// "add 596 597 --reason x" ist so gemeint, und flag.Parse hielte an der 596 an.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// parsePackageRequests liest "596" und "596:optional".
func parsePackageRequests(args []string) ([]requestdomain.PackageMember, error) {
	var out []requestdomain.PackageMember
	for _, arg := range args {
		num, role, _ := strings.Cut(strings.TrimPrefix(strings.ToUpper(arg), "REQ-"), ":")
		id, err := strconv.ParseInt(num, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a request: write 596 or 596:optional", arg)
		}
		role = strings.ToLower(role)
		if _, err := requestdomain.ValidateRole(role); err != nil {
			return nil, fmt.Errorf("%q: role must be pflicht or optional", arg)
		}
		out = append(out, requestdomain.PackageMember{RequestID: id, Role: role})
	}
	return out, nil
}

func writePackageResult(stdout io.Writer, jsonMode bool, d requestdomain.PackageDetail, err error, review bool) int {
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	if jsonMode {
		return writeRequestCLIResult(stdout, true, d, nil, nil)
	}
	if review {
		fmt.Fprint(stdout, requestdomain.RenderPackageReview(d))
	} else {
		fmt.Fprint(stdout, requestdomain.RenderPackage(d))
	}
	return 0
}

func writeImportReport(stdout io.Writer, r requestdomain.ImportReport) {
	mode := ""
	if r.DryRun {
		mode = " (dry run, nothing written)"
	}
	if len(r.Items) == 0 {
		fmt.Fprintf(stdout, "no branch relations to import%s\n", mode)
		return
	}
	for _, it := range r.Items {
		line := fmt.Sprintf("REQ-%d  %s  %s@%s", it.RequestID, it.Result, it.Project, it.Branch)
		if it.PackageID != 0 {
			line += fmt.Sprintf("  PKG-%d", it.PackageID)
		}
		if it.Reason != "" {
			line += "  — " + it.Reason
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "\n%d added, %d already members, %d skipped; %d packages created%s\n", r.Added, r.AlreadyMember, r.Skipped, len(r.PackagesCreated), mode)
}
