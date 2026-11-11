package client

import (
	"errors"
	"net/url"
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// IsPackageNotFound sagt, dass die Antwort "kein (offenes) Arbeitspaket" war.
// Das ist bei der Abfrage nach Branch oder PR der Normalfall, kein Fehler.
func IsPackageNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == "package_not_found"
}

type packageMemberBody struct {
	RequestID int64  `json:"request_id"`
	Role      string `json:"role,omitempty"`
}

func packageMemberBodies(members []requestdomain.PackageMember) []packageMemberBody {
	out := make([]packageMemberBody, 0, len(members))
	for _, m := range members {
		out = append(out, packageMemberBody{RequestID: m.RequestID, Role: m.Role})
	}
	return out
}

func packagePath(id int64, rest string) string {
	return "/api/packages/" + strconv.FormatInt(id, 10) + rest
}

// CreatePackage legt ein Arbeitspaket für einen Branch an.
func (c *Client) CreatePackage(project, branch, name string, pr int64, members []requestdomain.PackageMember) (requestdomain.PackageDetail, error) {
	var out requestdomain.PackageDetail
	err := c.do("POST", "/api/packages", nil, map[string]any{
		"project": project, "branch": branch, "name": name, "pr": pr, "requests": packageMemberBodies(members),
	}, &out)
	return out, err
}

// FindPackage fragt nach dem Paket eines Branches (mit der Kette ax.Lineage für
// das Erbe) oder, mit pr > 0, nach dem Paket eines PR. IsPackageNotFound sagt,
// dass es keines gibt.
func (c *Client) FindPackage(ax scope.Axes, pr int64, full bool) (requestdomain.PackageDetail, error) {
	q := url.Values{"project": {ax.Project}}
	if pr > 0 {
		q.Set("pr", strconv.FormatInt(pr, 10))
	} else {
		q = axesQuery(scope.Axes{Project: ax.Project, Branch: ax.Branch, Lineage: ax.Lineage})
	}
	if full {
		q.Set("full", "1")
	}
	var out requestdomain.PackageDetail
	err := c.do("GET", "/api/packages", q, nil, &out)
	return out, err
}

// GetPackage liest ein Paket; mit full samt Kriterien und Nachweisen.
func (c *Client) GetPackage(id int64, full bool) (requestdomain.PackageDetail, error) {
	q := url.Values{}
	if full {
		q.Set("full", "1")
	}
	var out requestdomain.PackageDetail
	err := c.do("GET", packagePath(id, ""), q, nil, &out)
	return out, err
}

// ReviewPackage liefert das vollständige Lastenheft eines Pakets.
func (c *Client) ReviewPackage(id int64) (requestdomain.PackageDetail, error) {
	var out requestdomain.PackageDetail
	err := c.do("GET", packagePath(id, "/review"), nil, nil, &out)
	return out, err
}

func (c *Client) UpdatePackage(id int64, patch requestdomain.PackagePatch) (requestdomain.PackageDetail, error) {
	var out requestdomain.PackageDetail
	err := c.do("PATCH", packagePath(id, ""), nil, patch, &out)
	return out, err
}

func (c *Client) AddPackageRequests(id int64, members []requestdomain.PackageMember, reason string) (requestdomain.PackageDetail, error) {
	var out requestdomain.PackageDetail
	err := c.do("POST", packagePath(id, "/requests"), nil,
		map[string]any{"requests": packageMemberBodies(members), "reason": reason}, &out)
	return out, err
}

func (c *Client) RemovePackageRequest(id, requestID int64, reason string) (requestdomain.PackageDetail, error) {
	var out requestdomain.PackageDetail
	err := c.do("POST", packagePath(id, "/requests/remove"), nil,
		map[string]any{"request_id": requestID, "reason": reason}, &out)
	return out, err
}

// ImportBranchRelations übernimmt die Relationen "branch:<projekt>@<branch>" in
// Pakete; mit dryRun nur der Bericht.
func (c *Client) ImportBranchRelations(project string, dryRun bool) (requestdomain.ImportReport, error) {
	var out requestdomain.ImportReport
	err := c.do("POST", "/api/packages/import-branch-relations", nil,
		map[string]any{"project": project, "dry_run": dryRun}, &out)
	return out, err
}

// ReopenCriterion setzt ein erfülltes Kriterium mit Begründung wieder auf offen.
func (c *Client) ReopenCriterion(criterionID int64, reason string) error {
	return c.do("POST", "/api/criteria/"+strconv.FormatInt(criterionID, 10)+"/reopen", nil,
		map[string]string{"reason": reason}, nil)
}

// PullKnowledge liefert, was zu einem PR gesagt wurde: das Wissen mit dem Anker
// des PRs (nach dem Merge das, was mit hineingegangen ist) und die Ghost-Pfade.
func (c *Client) PullKnowledge(project string, pr int64) (store.PullRequestBrief, error) {
	var out store.PullRequestBrief
	err := c.do("GET", "/api/pulls/"+strconv.FormatInt(pr, 10)+"/knowledge", url.Values{"project": {project}}, nil, &out)
	return out, err
}

// CountRequests zählt Requests mit der Semantik der Liste (REQ-601). state ""
// zählt alle, sonst open, done oder dropped.
func (c *Client) CountRequests(ax scope.Axes, state string) (int, error) {
	q := axesQuery(ax)
	q.Set("state", state)
	var out struct {
		Count int `json:"count"`
	}
	err := c.do("GET", "/api/requests/count", q, nil, &out)
	return out.Count, err
}

// PackagesOfRequest nennt die Pakete, in denen ein Request steckt.
func (c *Client) PackagesOfRequest(requestID int64) ([]requestdomain.PackageOfRequest, error) {
	var out []requestdomain.PackageOfRequest
	err := c.do("GET", "/api/requests/"+strconv.FormatInt(requestID, 10)+"/packages", nil, nil, &out)
	return out, err
}

// ListPackages ist die Paketliste eines Projekts; state "" für alle.
func (c *Client) ListPackages(project, state string) ([]requestdomain.PackageSummary, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	var out []requestdomain.PackageSummary
	err := c.do("GET", "/api/packages/list", q, nil, &out)
	return out, err
}
