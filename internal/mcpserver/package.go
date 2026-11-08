package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Live ist der Git-Stand des Arbeitsverzeichnisses jetzt. Der MCP-Prozess
// läuft so lange wie die Session; der Agent wechselt in der Zeit den Branch
// (git switch -c, REQ-601), und was beim Start galt, stimmt dann nicht mehr.
type Live struct {
	Branch  string
	Lineage []string
	CWD     string
	// Trunk: der Branch ist der Default-Branch des Repos oder der Default ist
	// unbekannt. Beschreibungen von Ghost-Dateien gehören dann auf die
	// Projektebene statt auf eine Branch-Ebene.
	Trunk bool
}

// SetLive setzt, wie der aktuelle Git-Stand ermittelt wird. Als Rückruf, weil
// git in collector sitzt.
func (s *Server) SetLive(f func() Live) { s.live = f }

// currentAxes sind die Achsen der Session jetzt: das Projekt vom Start, Branch
// und Kette vom Arbeitsverzeichnis, wie es gerade ist. Ohne Rückruf (Tests,
// kein Repo) gilt der Stand vom Start.
func (s *Server) currentAxes() scope.Axes {
	ax := s.ctxAxes
	if s.live == nil {
		return ax
	}
	if l := s.live(); l.Branch != "" {
		ax.Branch, ax.Lineage = l.Branch, l.Lineage
	}
	return ax
}

type PackageRequestRef struct {
	RequestID int64  `json:"request_id" jsonschema:"the request, REQ-n as a number"`
	Role      string `json:"role,omitempty" jsonschema:"pflicht (part of the requirements, the default) or optional"`
}

type PackageGetInput struct {
	PackageID int64  `json:"package_id,omitempty" jsonschema:"the package, PKG-n as a number"`
	Branch    string `json:"branch,omitempty" jsonschema:"the branch whose package to read; a branch cut from another inherits that one's package"`
	PR        int64  `json:"pr,omitempty" jsonschema:"the pull request number the package is bound to"`
}

type PackageDetailOutput struct {
	Detail requestdomain.PackageDetail `json:"detail"`
	// Found is false when the branch or pull request has no open package; that
	// is an answer, not a failure.
	Found bool `json:"found"`
}

type PackageUpdateInput struct {
	Action    string              `json:"action" jsonschema:"create (new package for a branch), add (put requests into it), remove (take one out, needs reason), set (change name, pr or state) or import_relations (turn the old branch:<project>@<branch> relations into packages)"`
	PackageID int64               `json:"package_id,omitempty" jsonschema:"the package; omit it and the package of the current branch is meant"`
	Branch    string              `json:"branch,omitempty" jsonschema:"with create: the branch it is bound to; default is the current branch"`
	Name      string              `json:"name,omitempty" jsonschema:"with create or set: the package name; default for create is the branch name"`
	PR        int64               `json:"pr,omitempty" jsonschema:"with create or set: the pull request number"`
	UnlinkPR  bool                `json:"unlink_pr,omitempty" jsonschema:"with set: release the pull request number"`
	State     string              `json:"state,omitempty" jsonschema:"with set: open or closed. Close a package when its branch is merged or abandoned; a closed package is not inherited and not briefed"`
	Requests  []PackageRequestRef `json:"requests,omitempty" jsonschema:"with create or add: the requests and their role"`
	RequestID int64               `json:"request_id,omitempty" jsonschema:"with remove: the request to take out"`
	Reason    string              `json:"reason,omitempty" jsonschema:"required for remove; optional for add. It goes into the request's activity list"`
	DryRun    bool                `json:"dry_run,omitempty" jsonschema:"with import_relations: only report what would happen"`
	Project   string              `json:"project,omitempty" jsonschema:"the project when it is not the session's own: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to"`
}

type PackageMemberRef struct {
	RequestID int64  `json:"request_id"`
	Role      string `json:"role"`
}

type PackageChangeOutput struct {
	Changed   string                      `json:"changed"`
	PackageID int64                       `json:"package_id,omitempty"`
	Name      string                      `json:"name,omitempty"`
	Branch    string                      `json:"branch,omitempty"`
	PR        int64                       `json:"pr,omitempty"`
	State     string                      `json:"state,omitempty"`
	Members   []PackageMemberRef          `json:"members,omitempty"`
	Import    *requestdomain.ImportReport `json:"import,omitempty"`
}

func changeOfPackage(changed string, d requestdomain.PackageDetail) PackageChangeOutput {
	out := PackageChangeOutput{Changed: changed, PackageID: d.ID, Name: d.Name, Branch: d.Branch, PR: d.PR, State: d.State}
	for _, m := range d.Members {
		out.Members = append(out.Members, PackageMemberRef{RequestID: m.RequestID, Role: m.Role})
	}
	return out
}

func (s *Server) registerPackageTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "package_get", Description: "Read a work package: the named set of requests that belongs to a branch or pull request, with how many criteria of each are still open and which human tasks wait. Without arguments it answers for the current branch; a branch cut from another one inherits that one's package. No package is an answer (found=false), not an error.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePackageGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "package_update", Description: "Create and keep a work package: create one for a branch, add requests to it with a role (pflicht or optional), remove one with a reason, rename it, bind it to a pull request or close it, or turn the old branch:<project>@<branch> relations into packages (import_relations, with dry_run). Adding a request that is already in with the same role changes nothing. Answers with what changed; call package_get for the picture.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handlePackageUpdate)
	mcp.AddTool(srv, &mcp.Tool{Name: "package_review", Description: "The complete requirements list of a work package for a reviewer: every mandatory request, every criterion with its state and its evidence (with the commit, branch and pull request the evidence was written at), the human tasks still waiting, and a verdict. Check each criterion against the code; confirm with request_record_progress criterion_met and evidence, or reopen with criterion_reopen and a reason. Without arguments it answers for the current branch.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePackageReview)
}

// resolvePackage findet das gemeinte Paket: nach Nummer, nach PR oder nach
// Branch (Vorgabe: der aktuelle, mit Erbe). found ist false, wenn es keins gibt.
func (s *Server) resolvePackage(id, pr int64, branch string, full bool) (requestdomain.PackageDetail, bool, error) {
	var d requestdomain.PackageDetail
	var err error
	switch {
	case id != 0:
		d, err = s.client.GetPackage(id, full)
	default:
		ax := s.currentAxes()
		if branch != "" && branch != ax.Branch {
			ax.Branch, ax.Lineage = branch, nil
			if s.lineageOf != nil {
				ax.Lineage = s.lineageOf(branch)
			}
		}
		if pr == 0 && ax.Branch == "" {
			return d, false, fmt.Errorf("no branch here: name package_id, branch or pr")
		}
		d, err = s.client.FindPackage(ax, pr, full)
	}
	if client.IsPackageNotFound(err) && id == 0 {
		return requestdomain.PackageDetail{}, false, nil
	}
	return d, err == nil, err
}

func (s *Server) handlePackageGet(_ context.Context, _ *mcp.CallToolRequest, in PackageGetInput) (*mcp.CallToolResult, PackageDetailOutput, error) {
	d, found, err := s.resolvePackage(in.PackageID, in.PR, in.Branch, false)
	if err != nil {
		return nil, PackageDetailOutput{}, err
	}
	if !found {
		return textResult("No open work package for this branch (and none inherited from the branches it was cut from). Create one with package_update action=create."), PackageDetailOutput{}, nil
	}
	return s.withCard(textResult(requestdomain.RenderPackage(d)), s.builder().PackageCard(d)), PackageDetailOutput{Detail: d, Found: true}, nil
}

func (s *Server) handlePackageReview(_ context.Context, _ *mcp.CallToolRequest, in PackageGetInput) (*mcp.CallToolResult, PackageDetailOutput, error) {
	d, found, err := s.resolvePackage(in.PackageID, in.PR, in.Branch, true)
	if err != nil {
		return nil, PackageDetailOutput{}, err
	}
	if !found {
		return textResult("No open work package for this branch or pull request: there is no requirements list to review."), PackageDetailOutput{}, nil
	}
	text := requestdomain.RenderPackageReview(d)
	if d.PR > 0 {
		text += s.renderPullKnowledge(d.Project, d.PR)
	}
	return s.withCard(textResult(text), s.builder().PackageCard(d)), PackageDetailOutput{Detail: d, Found: true}, nil
}

// renderPullKnowledge hängt an das Lastenheft eines PRs, welches Branch-Wissen
// auf seinen Commits entstanden ist (nach dem Merge: mit hineingegangen). Fehlt
// die Auskunft, bleibt das Lastenheft wie es ist.
func (s *Server) renderPullKnowledge(project string, pr int64) string {
	brief, err := s.client.PullKnowledge(project, pr)
	if err != nil || (len(brief.Knowledge) == 0 && len(brief.Ghosts) == 0) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nKnowledge written on PR #%d (%d entries, %d described paths):\n", pr, len(brief.Knowledge), len(brief.Ghosts))
	for _, k := range brief.Knowledge {
		fmt.Fprintf(&b, "- #%d [%s] %s\n", k.ID, k.Type, oneLine(k.Title))
	}
	for _, p := range brief.Ghosts {
		fmt.Fprintf(&b, "- path %s\n", oneLine(p))
	}
	return b.String()
}

func membersOf(refs []PackageRequestRef) []requestdomain.PackageMember {
	out := make([]requestdomain.PackageMember, 0, len(refs))
	for _, r := range refs {
		out = append(out, requestdomain.PackageMember{RequestID: r.RequestID, Role: r.Role})
	}
	return out
}

func (s *Server) handlePackageUpdate(_ context.Context, _ *mcp.CallToolRequest, in PackageUpdateInput) (*mcp.CallToolResult, PackageChangeOutput, error) {
	if in.Action == "import_relations" {
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, PackageChangeOutput{}, err
		}
		report, err := s.client.ImportBranchRelations(project, in.DryRun)
		if err != nil {
			return nil, PackageChangeOutput{}, err
		}
		out := PackageChangeOutput{Changed: "import_relations", Import: &report}
		return requestResult(out), out, nil
	}
	if in.Action == "create" {
		project, err := s.writeTarget(in.Project)
		if err != nil {
			return nil, PackageChangeOutput{}, err
		}
		branch := in.Branch
		if branch == "" {
			branch = s.currentAxes().Branch
		}
		d, err := s.client.CreatePackage(project, branch, in.Name, in.PR, membersOf(in.Requests))
		if err != nil {
			return nil, PackageChangeOutput{}, err
		}
		out := changeOfPackage("created", d)
		return s.withCard(requestResult(out), s.builder().PackageCard(d)), out, nil
	}
	id := in.PackageID
	if id == 0 {
		d, found, err := s.resolvePackage(0, 0, "", false)
		if err != nil {
			return nil, PackageChangeOutput{}, err
		}
		if !found {
			return nil, PackageChangeOutput{}, fmt.Errorf("this branch has no open work package: create one first (action=create), or name package_id")
		}
		id = d.ID
	}
	var d requestdomain.PackageDetail
	var err error
	switch in.Action {
	case "add":
		d, err = s.client.AddPackageRequests(id, membersOf(in.Requests), in.Reason)
	case "remove":
		d, err = s.client.RemovePackageRequest(id, in.RequestID, in.Reason)
	case "set":
		patch := requestdomain.PackagePatch{}
		if in.Name != "" {
			patch.Name = &in.Name
		}
		if in.State != "" {
			patch.State = &in.State
		}
		switch {
		case in.UnlinkPR && in.PR != 0:
			return nil, PackageChangeOutput{}, fmt.Errorf("pr and unlink_pr exclude each other")
		case in.UnlinkPR:
			zero := int64(0)
			patch.PR = &zero
		case in.PR != 0:
			patch.PR = &in.PR
		}
		d, err = s.client.UpdatePackage(id, patch)
	default:
		return nil, PackageChangeOutput{}, fmt.Errorf("unknown package action %q; use create, add, remove, set or import_relations", in.Action)
	}
	if err != nil {
		return nil, PackageChangeOutput{}, err
	}
	out := changeOfPackage(in.Action, d)
	return s.withCard(requestResult(out), s.builder().PackageCard(d)), out, nil
}
