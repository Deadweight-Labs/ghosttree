package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Features (REQ-612) auf der Kommandozeile: die lebende Beschreibung dessen, was
// das Produkt tut. Ein Aufruf mit dem Token der Konfiguration gilt dem Server als
// Agent; Features anlegen und fortschreiben dürfen Agenten.

const featureUsage = `usage: ctx feature <command>

  add NAME [--summary T] [--behavior T | --behavior-file F] [--limits T] [--cli T] [--mcp T] [--api T] [--ui T]
           [--area PATH ...] [--idea N ...] [--request N ...] [--from-idea N] [--pr N] [--message M] [--project P]
                                  create a feature; --from-idea turns an implemented idea into it
  list [--q WORDS] [--path PATH] [--state active|retired|all] [--project P]
  show FEAT-n [--versions] [--rev N]
                                  the page of a feature; --versions shows what triggered each
  update FEAT-n [the fields of add, those you pass replace the old text] [--idea N] [--request N ...] [--pr N] [--base REV] [--message M]
                                  write how it is NOW as a new version; --idea and --pr say what triggered it
  retire FEAT-n --reason R        the product no longer does this (a lead)      reopen FEAT-n  take it up again
  link FEAT-n idea|request N [--rel origin|affects]     unlink FEAT-n idea|request N [--rel ...]
                                  tie it to an idea or issue; an idea that affects it shows its description next to the wish
  paths --pr N [--base REF] [PATH ...]
                                  report the paths a pull request changes (default: git diff --name-only REF...HEAD, REF origin/main)
                                  and print the checklist items 'Feature X: Beschreibung angepasst?'
  checklist --pr N                the items of a pull request
  check FEAT-n --pr N --note T    the description was looked at for that PR and still holds
  context PATH ...                the features whose paths contain these paths, as an agent gets them
  export [--out FILE] [--title T] [--project P]
                                  the active features as a page (docs/features.md)
  import [FILE] [--map FILE] [--project P] [--apply]
                                  take the items of docs/features.md as features (names and paths from docs/features-map.json);
                                  without --apply only a dry run

Every command takes --json.`

var featureRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/~^@{}-]{0,120}$`)

func parseFeatureRef(s string) (int64, error) {
	n, ok := feature.ParseRef(s)
	if !ok {
		return 0, fmt.Errorf("%q is not a feature number like FEAT-3", s)
	}
	return n, nil
}

func parseRequestNumber(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "REQ-"), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a request number like REQ-12", s)
	}
	return n, nil
}

func parseIdeaNumber(s string) (int64, error) {
	n, ok := idea.ParseRef(s)
	if !ok {
		return 0, fmt.Errorf("%q is not an idea number like IDEA-3", s)
	}
	return n, nil
}

func featureLines(stdout io.Writer, o featureView) {
	fmt.Fprint(stdout, renderFeatureText(o))
}

type featureView struct {
	Feature  feature.Feature
	Versions []feature.Version
	Version  *feature.Version
}

// renderFeatureText ist die Seite eines Features, dieselbe Darstellung wie in
// feature_get (MCP): der Ist-Stand zuerst.
func renderFeatureText(o featureView) string {
	f := o.Feature
	c := f.Content
	rev := f.Revision
	if o.Version != nil {
		c, rev = o.Version.Content, o.Version.Revision
	}
	var b strings.Builder
	state := f.State
	if f.State == feature.Retired && f.Reason != "" {
		state += ": " + f.Reason
	}
	fmt.Fprintf(&b, "%s [%s, version %d of %d] %s\n", f.HumanID(), state, rev, f.Versions, c.Name)
	section := func(title, text string) {
		if strings.TrimSpace(text) != "" {
			fmt.Fprintf(&b, "\n%s\n%s\n", title, strings.TrimSpace(text))
		}
	}
	section("What it is", c.Summary)
	section("How it behaves now", c.Behavior)
	section("Limits", c.Limits)
	for _, u := range []struct{ name, text string }{{"CLI", c.CLI}, {"MCP", c.MCP}, {"API", c.API}, {"Web interface", c.UI}} {
		if strings.TrimSpace(u.text) != "" {
			fmt.Fprintf(&b, "\n%s: %s\n", u.name, strings.TrimSpace(u.text))
		}
	}
	if len(c.Areas) > 0 {
		fmt.Fprintf(&b, "\nPaths: %s\n", strings.Join(c.Areas, ", "))
	}
	for _, g := range []struct {
		title string
		refs  []feature.Ref
	}{{"came out of", f.Origin}, {"changed by", f.Affects}} {
		for _, r := range g.refs {
			extra := ""
			if g.title == "changed by" && !r.Advanced {
				extra = " (description not carried forward yet)"
			}
			fmt.Fprintf(&b, "%s %s %s%s\n", g.title, r.HumanID(), r.Title, extra)
		}
	}
	for _, v := range o.Versions {
		var cause []string
		if v.Idea > 0 {
			cause = append(cause, fmt.Sprintf("IDEA-%d", v.Idea))
		}
		for _, r := range v.Requests {
			cause = append(cause, fmt.Sprintf("REQ-%d", r))
		}
		for _, pr := range v.PRs() {
			cause = append(cause, fmt.Sprintf("PR #%d", pr))
		}
		line := fmt.Sprintf("v%d %s %s", v.Revision, v.CreatedAt, v.Person)
		if v.Message != "" {
			line += ": " + v.Message
		}
		if len(cause) > 0 {
			line += " (" + strings.Join(cause, ", ") + ")"
		}
		if v.GitAnchor != nil && v.GitAnchor.Commit != "" {
			line += " @ " + v.GitAnchor.Short()
		}
		fmt.Fprintln(&b, line)
	}
	return b.String()
}

func cmdFeature(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, featureUsage); return 2 }
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "list", "show", "update", "retire", "reopen", "link", "unlink", "paths", "checklist", "check", "context", "export", "import":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)

	fs := flag.NewFlagSet("feature "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.String("project", "", "project (remote or unique short name); default: the current one")
	summary := fs.String("summary", "", "one to three sentences")
	behavior := fs.String("behavior", "", "how it behaves now")
	behaviorFile := fs.String("behavior-file", "", "read the behavior from a file (- is standard input)")
	limits := fs.String("limits", "", "what it deliberately does not do")
	cliText := fs.String("cli", "", "how to use it from the command line")
	mcpText := fs.String("mcp", "", "which MCP tools use it")
	apiText := fs.String("api", "", "which API routes it has")
	uiText := fs.String("ui", "", "what the web interface shows")
	message := fs.String("message", "", "what this version is, one line")
	reason := fs.String("reason", "", "why (retire)")
	rel := fs.String("rel", "", "origin or affects (link)")
	q := fs.String("q", "", "words (list)")
	path := fs.String("path", "", "only features whose paths contain this path (list)")
	state := fs.String("state", "", "active, retired or all (list)")
	pr := fs.Int64("pr", 0, "pull request number")
	base := fs.Int("base", 0, "the version you build on (update)")
	baseRef := fs.String("ref", "origin/main", "the Git ref a pull request is compared with (paths)")
	fromIdea := fs.Int64("from-idea", 0, "turn this implemented idea into the feature (add)")
	note := fs.String("note", "", "why the description still holds (check)")
	versions := fs.Bool("versions", false, "list the versions (show)")
	rev := fs.Int("rev", 0, "show this version (show)")
	out := fs.String("out", "", "write to this file (export)")
	title := fs.String("title", "", "title of the page (export)")
	mapFile := fs.String("map", "", "names and paths of the items (import); default docs/features-map.json next to the file")
	apply := fs.Bool("apply", false, "really write (import)")
	var areas, ideaRefs, reqRefs repeatedString
	fs.Var(&areas, "area", "a path prefix the feature lives in; repeatable (the full list is replaced)")
	fs.Var(&ideaRefs, "idea", "IDEA-n it came out of (add) or that this version carries forward (update, one); repeatable")
	fs.Var(&reqRefs, "request", "REQ-n it came out of or that triggered this version; repeatable")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return usageErr()
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	fail := func(err error) int { return writeRequestCLIError(stdout, jsonMode, 1, err) }
	target := func() (string, error) { return c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project) }
	nums := func(parse func(string) (int64, error), in repeatedString) ([]int64, error) {
		var out []int64
		for _, s := range in {
			n, err := parse(s)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	}
	input := func() (client.FeatureInput, error) {
		var in client.FeatureInput
		str := func(name string, v *string) *string {
			if set[name] {
				s := *v
				return &s
			}
			return nil
		}
		if set["behavior"] && set["behavior-file"] {
			return in, fmt.Errorf("give --behavior or --behavior-file, not both")
		}
		in.Patch = feature.Patch{Summary: str("summary", summary), Behavior: str("behavior", behavior), Limits: str("limits", limits),
			CLI: str("cli", cliText), MCP: str("mcp", mcpText), API: str("api", apiText), UI: str("ui", uiText)}
		if set["behavior-file"] {
			text, err := readTextArg(*behaviorFile, os.Stdin, feature.MaxBehavior*4)
			if err != nil {
				return in, err
			}
			in.Patch.Behavior = &text
		}
		if set["area"] {
			a := []string(areas)
			in.Patch.Areas = &a
		}
		in.Message, in.Base, in.PR, in.SessionID = *message, *base, *pr, cliSessionID(c, cfg)
		reqs, err := nums(parseRequestNumber, reqRefs)
		if err != nil {
			return in, err
		}
		in.Requests = reqs
		return in, nil
	}
	card := func(f feature.Feature) string { return cliBuilder(cfg, f.Project).FeatureCard(f).String() }
	show := func(v featureView, err error) int {
		if err != nil {
			return fail(err)
		}
		if jsonMode {
			return writeRequestCLIResult(stdout, true, map[string]any{"feature": v.Feature, "versions": v.Versions, "version": v.Version}, nil, nil)
		}
		featureLines(stdout, v)
		fmt.Fprintln(stdout, card(v.Feature))
		return 0
	}

	switch sub {
	case "add":
		name := strings.TrimSpace(strings.Join(positional, " "))
		if name == "" {
			return usageErr()
		}
		in, err := input()
		if err != nil {
			return fail(err)
		}
		in.Patch.Name = &name
		if in.Project, err = target(); err != nil {
			return fail(err)
		}
		if in.Ideas, err = nums(parseIdeaNumber, ideaRefs); err != nil {
			return fail(err)
		}
		in.FromIdea = *fromIdea
		f, err := c.CreateFeature(in)
		return show(featureView{Feature: f}, err)
	case "list":
		tgt := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if tgt, err = c.WriteTarget(*project, ""); err != nil {
				return fail(err)
			}
		}
		list, err := c.SearchFeatures(tgt, *state, *q, *path, 200)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no features")
		}
		for _, f := range list {
			fmt.Fprintf(stdout, "%s [%s] v%d %s", f.HumanID(), f.State, f.Revision, f.Name)
			if f.Summary != "" {
				fmt.Fprintf(stdout, ": %s", oneLineCLI(f.Summary))
			}
			fmt.Fprintln(stdout)
		}
		return 0
	case "show", "update", "retire", "reopen", "check":
		if len(positional) < 1 {
			return usageErr()
		}
		id, err := parseFeatureRef(positional[0])
		if err != nil {
			return fail(err)
		}
		switch sub {
		case "show":
			f, err := c.GetFeature(id)
			if err != nil {
				return fail(err)
			}
			v := featureView{Feature: f}
			if *rev > 0 {
				ver, err := c.FeatureVersion(id, *rev)
				if err != nil {
					return fail(err)
				}
				v.Version = &ver
			}
			if *versions {
				if v.Versions, err = c.FeatureVersions(id, false); err != nil {
					return fail(err)
				}
			}
			return show(v, nil)
		case "update":
			in, err := input()
			if err != nil {
				return fail(err)
			}
			if set["name"] {
				return usageErr()
			}
			if len(ideaRefs) > 1 {
				return fail(fmt.Errorf("a version carries forward one idea; give --idea once"))
			}
			if len(ideaRefs) == 1 {
				if in.Idea, err = parseIdeaNumber(ideaRefs[0]); err != nil {
					return fail(err)
				}
			}
			res, err := c.UpdateFeature(id, in)
			if err != nil {
				return fail(err)
			}
			f, err := c.GetFeature(id)
			if err != nil {
				return fail(err)
			}
			if jsonMode {
				return writeRequestCLIResult(stdout, true, map[string]any{"feature": f, "version": res.Version, "unchanged": res.Unchanged}, nil, nil)
			}
			if res.Unchanged {
				fmt.Fprintf(stdout, "%s unchanged: stays at version %d (no new version)\n", f.HumanID(), res.Version.Revision)
			} else {
				fmt.Fprintf(stdout, "%s is at version %d\n", f.HumanID(), res.Version.Revision)
			}
			fmt.Fprintln(stdout, card(f))
			return 0
		case "retire", "reopen":
			next := feature.Active
			if sub == "retire" {
				next = feature.Retired
			}
			f, err := c.SetFeatureState(id, next, *reason)
			return show(featureView{Feature: f}, err)
		case "check":
			if err := c.ConfirmFeatureUnchanged(id, *pr, *note); err != nil {
				return fail(err)
			}
			return writeRequestCLIResult(stdout, jsonMode, map[string]any{"feature": id, "pr": *pr, "state": "unchanged"}, nil, nil)
		}
	case "link", "unlink":
		if len(positional) != 3 {
			return usageErr()
		}
		id, err := parseFeatureRef(positional[0])
		if err != nil {
			return fail(err)
		}
		kind := strings.ToLower(positional[1])
		var ref int64
		switch kind {
		case feature.KindIdea:
			ref, err = parseIdeaNumber(positional[2])
		case feature.KindRequest:
			ref, err = parseRequestNumber(positional[2])
		default:
			err = fmt.Errorf("link idea or request, not %q", positional[1])
		}
		if err != nil {
			return fail(err)
		}
		r := *rel
		if r == "" {
			r = feature.RelOrigin
			if kind == feature.KindIdea {
				r = feature.RelAffects
			}
		}
		var f feature.Feature
		if sub == "link" {
			f, err = c.LinkFeature(id, kind, ref, r)
		} else {
			f, err = c.UnlinkFeature(id, kind, ref, r)
		}
		return show(featureView{Feature: f}, err)
	case "paths", "checklist":
		if *pr <= 0 {
			return fail(fmt.Errorf("--pr N is required"))
		}
		gc := collector.ResolveGitContext(mustGetwd())
		tgt := gc.Project
		if *project != "" {
			if tgt, err = c.WriteTarget(*project, ""); err != nil {
				return fail(err)
			}
		}
		if tgt == "" {
			return fail(fmt.Errorf("this folder belongs to no project: run it inside a repository or pass --project"))
		}
		var list []requestdomain.FeatureCheck
		var stored *client.PullPathsResult
		if sub == "paths" {
			paths := positional
			if len(paths) == 0 {
				if paths, err = gitChangedPaths(gc.Root, *baseRef); err != nil {
					return fail(err)
				}
			}
			res, err := c.ReportPullPaths(tgt, *pr, paths)
			if err != nil {
				return fail(err)
			}
			list, stored = res.Checklist, &res
		} else if list, err = c.FeatureChecklist(tgt, *pr); err != nil {
			return fail(err)
		}
		if jsonMode {
			return writeRequestCLIResult(stdout, true, map[string]any{"stored": stored, "checklist": list}, nil, nil)
		}
		if stored != nil {
			fmt.Fprintf(stdout, "stored %d paths for PR #%d (%d dropped)\n", stored.Stored, *pr, stored.Dropped)
		}
		fmt.Fprint(stdout, renderChecklistText(*pr, list))
		return 0
	case "context":
		if len(positional) == 0 {
			return usageErr()
		}
		gc := collector.ResolveGitContext(mustGetwd())
		tgt := gc.Project
		if *project != "" {
			if tgt, err = c.WriteTarget(*project, ""); err != nil {
				return fail(err)
			}
		}
		fc, err := c.FeatureContext(tgt, positional, "", 0)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, fc, nil, err)
		}
		if fc.Text == "" {
			fmt.Fprintln(stdout, "no feature has these paths")
		}
		fmt.Fprint(stdout, fc.Text)
		return 0
	case "export":
		tgt := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if tgt, err = c.WriteTarget(*project, ""); err != nil {
				return fail(err)
			}
		}
		text, err := c.ExportFeatures(tgt, *title)
		if err != nil {
			return fail(err)
		}
		if *out != "" {
			if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", *out, len(text))
			return 0
		}
		fmt.Fprint(stdout, text)
		return 0
	case "import":
		file := "docs/features.md"
		if len(positional) > 1 {
			return usageErr()
		}
		if len(positional) == 1 {
			file = positional[0]
		}
		tgt := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if tgt, err = c.WriteTarget(*project, ""); err != nil {
				return fail(err)
			}
		}
		return featureImport(stdout, jsonMode, c, tgt, file, *mapFile, *apply)
	}
	return usageErr()
}

func oneLineCLI(s string) string { return strings.Join(strings.Fields(s), " ") }

func mustGetwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func renderChecklistText(pr int64, list []requestdomain.FeatureCheck) string {
	if len(list) == 0 {
		return fmt.Sprintf("no feature description is touched by PR #%d (or no paths were reported: ctx feature paths --pr %d)\n", pr, pr)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "feature descriptions PR #%d touches:\n", pr)
	for _, c := range list {
		mark, how := "[ ]", fmt.Sprintf("%d changed paths, e.g. %s", c.PathCount, strings.Join(c.Paths, ", "))
		switch c.State {
		case requestdomain.FeatureCheckUpdated:
			mark, how = "[x]", fmt.Sprintf("a new version carries this PR (%v)", c.PRRevisions)
		case requestdomain.FeatureCheckUnchanged:
			mark, how = "[x]", "checked, still right: "+c.Note
		}
		fmt.Fprintf(&b, "- %s %s — %s\n", mark, c.Text, how)
	}
	b.WriteString("open item: ctx feature update FEAT-n --pr N (behavior changed) or ctx feature check FEAT-n --pr N --note T (it did not)\n")
	return b.String()
}

// gitChangedPaths fragt git nach den Pfaden, die der Branch gegenüber dem Stand
// ref ändert. Umbenennungen zählen mit altem und neuem Pfad. Die Referenz darf
// nicht wie eine Option aussehen.
func gitChangedPaths(root, ref string) ([]string, error) {
	if root == "" {
		return nil, fmt.Errorf("this folder is not a Git checkout: pass the paths as arguments")
	}
	if !featureRefRE.MatchString(ref) || strings.Contains(ref, "..") {
		return nil, fmt.Errorf("%q is not a Git ref", ref)
	}
	cmd := exec.Command("git", "-C", root, "diff", "--name-only", "--no-renames", ref+"...HEAD", "--")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff against %s failed: pass --ref with the branch the pull request is compared with, or the paths as arguments", ref)
	}
	var paths []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	return paths, nil
}

// featureImport nimmt die Punkte der Seite als Features: je Name anlegen oder, wenn
// es das Feature gibt, fortschreiben. Ohne --apply nur ein Probelauf.
func featureImport(stdout io.Writer, jsonMode bool, c *client.Client, project, file, mapFile string, apply bool) int {
	fail := func(err error) int { return writeRequestCLIError(stdout, jsonMode, 1, err) }
	md, err := readTextArg(file, nil, 1<<20)
	if err != nil {
		return fail(err)
	}
	var mapping []feature.DocMap
	if mapFile == "" {
		mapFile = file
		if i := strings.LastIndex(mapFile, "/"); i >= 0 {
			mapFile = mapFile[:i+1] + "features-map.json"
		} else {
			mapFile = "features-map.json"
		}
		if _, err := os.Stat(mapFile); err != nil {
			mapFile = ""
		}
	}
	if mapFile != "" {
		raw, err := readTextArg(mapFile, nil, 1<<20)
		if err != nil {
			return fail(err)
		}
		if mapping, err = feature.ParseDocMap([]byte(raw)); err != nil {
			return fail(err)
		}
	}
	feats, err := feature.FromDoc(feature.ParseDoc(md), mapping)
	if err != nil {
		return fail(err)
	}
	if len(feats) == 0 {
		return fail(fmt.Errorf("no list items found in %s", file))
	}
	existing, err := c.SearchFeatures(project, "all", "", "", 500)
	if err != nil {
		return fail(err)
	}
	byName := map[string]feature.Feature{}
	for _, f := range existing {
		byName[strings.ToLower(f.Name)] = f
	}
	type step struct {
		Action  string `json:"action"`
		Name    string `json:"name"`
		Feature string `json:"feature,omitempty"`
		Mapped  bool   `json:"mapped"`
		Areas   int    `json:"areas"`
	}
	var steps []step
	for _, df := range feats {
		name := df.Name
		patch := feature.Patch{Name: &name, Summary: &df.Summary, Behavior: &df.Behavior, Areas: &df.Areas}
		st := step{Action: "create", Name: df.Name, Mapped: df.Mapped, Areas: len(df.Areas)}
		old, exists := byName[strings.ToLower(df.Name)]
		if exists {
			st.Action, st.Feature = "update", old.HumanID()
		}
		if apply {
			if exists {
				if _, err := c.UpdateFeature(old.ID, client.FeatureInput{Patch: patch, Message: "imported from " + file}); err != nil {
					return fail(fmt.Errorf("%s: %w", df.Name, err))
				}
			} else {
				created, err := c.CreateFeature(client.FeatureInput{Patch: patch, Project: project, Message: "imported from " + file})
				if err != nil {
					return fail(fmt.Errorf("%s: %w", df.Name, err))
				}
				st.Feature = created.HumanID()
			}
		}
		steps = append(steps, st)
	}
	if jsonMode {
		return writeRequestCLIResult(stdout, true, map[string]any{"applied": apply, "steps": steps}, nil, nil)
	}
	for _, st := range steps {
		note := ""
		if !st.Mapped {
			note = " (no entry in the map: name guessed, no paths)"
		}
		fmt.Fprintf(stdout, "%s %s %s, %d paths%s\n", st.Action, st.Feature, st.Name, st.Areas, note)
	}
	if !apply {
		fmt.Fprintf(stdout, "dry run: %d items of %s, nothing was written; run again with --apply\n", len(steps), file)
	}
	return 0
}
