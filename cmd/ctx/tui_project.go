package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

// classifyErr turns a failed call into the problem a person can act on. The
// interface never sees an HTTP status.
func classifyErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *tui.ProblemError
	if errors.As(err, &pe) {
		return err
	}
	status, code := 0, ""
	var ae *client.APIError
	var se *client.StatusError
	switch {
	case errors.As(err, &ae):
		status, code = ae.Status, ae.Code
	case errors.As(err, &se):
		status = se.Status
	}
	switch {
	case code == "project_required" || code == "project_not_found":
		return &tui.ProblemError{Kind: tui.ProblemNoProject}
	case status == http.StatusUnauthorized:
		return &tui.ProblemError{Kind: tui.ProblemSignedOut}
	case status == http.StatusForbidden:
		return &tui.ProblemError{Kind: tui.ProblemForbidden}
	case status == http.StatusNotFound:
		return &tui.ProblemError{Kind: tui.ProblemNoProject}
	case status >= 500:
		return &tui.ProblemError{Kind: tui.ProblemOffline}
	case status != 0:
		return &tui.ProblemError{Kind: tui.ProblemOther, Detail: links.Clean(code, 40)}
	}
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return &tui.ProblemError{Kind: tui.ProblemOffline}
	}
	return &tui.ProblemError{Kind: tui.ProblemOther, Detail: links.Clean(err.Error(), 80)}
}

// friendlyErr keeps errors that already speak to a person (a search line
// the parser refused, an object that is not found) and turns transport and
// status errors into problems. A coded answer of the server about the input
// itself keeps its message.
func friendlyErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *client.APIError
	if errors.As(err, &ae) {
		switch {
		case strings.HasPrefix(ae.Code, "project_"), ae.Status == http.StatusUnauthorized, ae.Status == http.StatusForbidden, ae.Status >= 500:
			return classifyErr(err)
		case ae.Message != "":
			return errors.New(links.Clean(ae.Message, 160))
		}
		return classifyErr(err)
	}
	var se *client.StatusError
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &se) || errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return classifyErr(err)
	}
	return errors.New(links.Clean(err.Error(), 160))
}

// folderInfo describes the current folder: whether it is in a git repository,
// its normalized origin remote, and whether the project comes from a link.
func folderInfo() (dir string, git bool, remote string, linked bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false, "", false
	}
	g := collector.ResolveGitContext(dir)
	git = gitRoot(dir) != ""
	switch {
	case g.Project != "" && g.Root != "":
		remote = scope.NormalizeRemote(g.Project)
	case g.Project != "":
		linked = true
	}
	return dir, git, remote, linked
}

// gitRoot is the nearest folder at or above dir with a .git entry.
func gitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// visibleProjects is every project the caller may see: those of the
// caller's organizations and those it may write to.
func visibleProjects(c *client.Client) ([]tui.ProjectInfo, error) {
	list, err := c.ListProjects("")
	if err != nil {
		return nil, classifyErr(err)
	}
	seen := map[string]bool{}
	var out []tui.ProjectInfo
	for _, p := range list {
		if seen[p.Remote] {
			continue
		}
		seen[p.Remote] = true
		out = append(out, tui.ProjectInfo{ID: links.Clean(p.Remote, 200), Org: links.Clean(p.Org, 40), Named: p.Named})
	}
	if writable, err := c.WritableProjects(); err == nil {
		for _, id := range writable {
			if !seen[id] {
				seen[id] = true
				out = append(out, tui.ProjectInfo{ID: links.Clean(id, 200)})
			}
		}
	}
	// Repositories and named projects by name; folder paths (local
	// remotes) at the end, they are rarely what someone looks for.
	key := func(id string) string {
		if strings.HasPrefix(id, "/") {
			return "~" + id
		}
		return strings.ToLower(strings.TrimPrefix(id, "github.com/"))
	}
	sort.Slice(out, func(i, j int) bool { return key(out[i].ID) < key(out[j].ID) })
	return out, nil
}

func (b cliBackend) CheckProject(ctx context.Context, project string) (bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return false, classifyErr(err)
	}
	list, err := visibleProjects(b.client(cfg))
	if err != nil {
		return false, err
	}
	want := scope.NormalizeRemote(project)
	for _, p := range list {
		if p.ID == project || (want != "" && scope.NormalizeRemote(p.ID) == want) {
			return true, nil
		}
	}
	return false, nil
}

func (b cliBackend) Projects(ctx context.Context) ([]tui.ProjectInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	return visibleProjects(b.client(cfg))
}

func (b cliBackend) ProjectPreview(ctx context.Context, project string) ([]tui.Item, []tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, classifyErr(err)
	}
	c := b.client(cfg)
	page, err := c.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 20})
	if err != nil {
		return nil, nil, classifyErr(err)
	}
	reqs := make([]tui.Item, 0, len(page.Results))
	for _, h := range page.Results {
		reqs = append(reqs, tui.Item{Ref: fmt.Sprintf("REQ-%d", h.Request.ID), Kind: "issue", Title: h.Request.Title})
	}
	tasks := []tui.Item{}
	if list, err := c.ListHumanTasks(project, "open", true, 0); err == nil {
		for _, t := range list {
			tasks = append(tasks, tui.Item{Ref: t.HumanID(), Kind: "task", Title: t.Title})
		}
	}
	return cleanItems(reqs), cleanItems(tasks), nil
}

func (b cliBackend) ClaimPlan(ctx context.Context) (tui.ClaimPlan, error) {
	_, _, remote, _ := folderInfo()
	if remote == "" {
		return tui.ClaimPlan{}, &tui.ClaimError{Code: "no_remote"}
	}
	cfg, err := config.Load()
	if err != nil {
		return tui.ClaimPlan{}, classifyErr(err)
	}
	orgs, err := b.client(cfg).ListOrgsContext(ctx)
	if err != nil {
		return tui.ClaimPlan{}, classifyErr(err)
	}
	plan := tui.ClaimPlan{Remote: links.Clean(remote, 200)}
	for _, o := range orgs {
		plan.Orgs = append(plan.Orgs, tui.OrgChoice{Slug: links.Clean(o.Slug, 40), Name: links.Clean(o.Name, 60), Role: links.Clean(o.Role, 20), Default: o.Default})
	}
	return plan, nil
}

func (b cliBackend) Claim(ctx context.Context, remote, org string) (tui.ProjectInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return tui.ProjectInfo{}, classifyErr(err)
	}
	p, err := b.client(cfg).ClaimProject(remote, org)
	if err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) {
			switch ae.Code {
			case "not_org_owner", "not_org_member", "project_claimed", "org_not_found":
				return tui.ProjectInfo{}, &tui.ClaimError{Code: ae.Code}
			}
		}
		return tui.ProjectInfo{}, classifyErr(err)
	}
	return tui.ProjectInfo{ID: links.Clean(p.Remote, 200), Org: links.Clean(p.Org, 40)}, nil
}

// RecentDirs are the working folders of this machine's sessions that still
// exist, newest first.
func (b cliBackend) RecentDirs(ctx context.Context) ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	sessions, err := b.client(cfg).Sessions(scope.Axes{Machine: cfg.Machine}, 300)
	if err != nil {
		return nil, classifyErr(err)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].LastSeenAt > sessions[j].LastSeenAt })
	cwd, _ := os.Getwd()
	seen := map[string]bool{cwd: true}
	var out []string
	for _, s := range sessions {
		d := filepath.Clean(s.CWD)
		if s.CWD == "" || seen[d] {
			continue
		}
		seen[d] = true
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, d)
		if len(out) >= 30 {
			break
		}
	}
	return out, nil
}

// expandDir resolves ~ and makes the path absolute; a trailing separator
// (or an empty input) stays, so completion lists the folder's contents.
func expandDir(input string) string {
	input = strings.TrimSpace(input)
	sep := string(filepath.Separator)
	trail := input == "" || input == "~" || strings.HasSuffix(input, sep)
	if input == "~" || strings.HasPrefix(input, "~"+sep) {
		if home, err := os.UserHomeDir(); err == nil {
			input = home + strings.TrimPrefix(input, "~")
		}
	}
	if !filepath.IsAbs(input) {
		cwd, _ := os.Getwd()
		input = filepath.Join(cwd, input)
	}
	input = filepath.Clean(input)
	if trail && !strings.HasSuffix(input, sep) {
		input += sep
	}
	return input
}

// CompleteDir lists the folders whose path starts with the typed one.
// Hidden folders show only when the typed name starts with a dot.
func (cliBackend) CompleteDir(input string) []string {
	dir, base := filepath.Split(expandDir(input))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, path+string(filepath.Separator))
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// ChangeDir makes dir the working folder of this process: everything that
// asks for the current project (lists, cards, paths) follows it.
func (cliBackend) ChangeDir(dir string) error {
	full := filepath.Clean(expandDir(dir))
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("%s: no such folder", links.Clean(full, 200))
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", links.Clean(full, 200))
	}
	return os.Chdir(full)
}

// messagesNotFound is the answer for a room that does not exist yet: there
// are no messages in it.
func messagesNotFound(err error) bool {
	var se *client.StatusError
	var ae *client.APIError
	return (errors.As(err, &se) && se.Status == http.StatusNotFound) || (errors.As(err, &ae) && ae.Status == http.StatusNotFound)
}
