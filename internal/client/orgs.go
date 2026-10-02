package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// CreatedInvitation ist die Antwort auf das Ausstellen einer Einladung. Der
// Code kommt nur hier vor.
type CreatedInvitation struct {
	Code       string           `json:"code"`
	Org        string           `json:"org"`
	Invitation store.Invitation `json:"invitation"`
}

func (c *Client) ListOrgs() ([]store.Org, error) {
	var out []store.Org
	err := c.do("GET", "/api/orgs", nil, nil, &out)
	return out, err
}

func (c *Client) CreateOrg(name, slug string) (store.Org, error) {
	var out store.Org
	err := c.do("POST", "/api/orgs", nil, map[string]string{"name": name, "slug": slug}, &out)
	return out, err
}

func orgPath(org string) string { return "/api/orgs/" + url.PathEscape(org) }

func (c *Client) OrgMembers(org string) ([]store.OrgMemberInfo, error) {
	var out []store.OrgMemberInfo
	err := c.do("GET", orgPath(org)+"/members", nil, nil, &out)
	return out, err
}

func (c *Client) SetOrgMemberRole(org, account, role string) error {
	return c.do("PUT", orgPath(org)+"/members/"+url.PathEscape(account), nil, map[string]string{"role": role}, nil)
}

func (c *Client) RemoveOrgMember(org, account string) error {
	return c.do("DELETE", orgPath(org)+"/members/"+url.PathEscape(account), nil, nil, nil)
}

func (c *Client) CreateInvitation(org, email, role string, ttlHours int) (CreatedInvitation, error) {
	var out CreatedInvitation
	err := c.do("POST", orgPath(org)+"/invitations", nil, map[string]any{"email": email, "role": role, "ttl_hours": ttlHours}, &out)
	return out, err
}

func (c *Client) ListInvitations(org string) ([]store.Invitation, error) {
	var out []store.Invitation
	err := c.do("GET", orgPath(org)+"/invitations", nil, nil, &out)
	return out, err
}

func (c *Client) RevokeInvitation(org string, id int64) error {
	return c.do("DELETE", orgPath(org)+"/invitations/"+strconv.FormatInt(id, 10), nil, nil, nil)
}

func (c *Client) AcceptInvitation(code string) (store.Org, error) {
	var out store.Org
	err := c.do("POST", "/api/invitations/accept", nil, map[string]string{"code": code}, &out)
	return out, err
}

func (c *Client) SetDefaultOrg(org string) (store.Org, error) {
	var out store.Org
	err := c.do("PUT", "/api/account/default-org", nil, map[string]string{"org": org}, &out)
	return out, err
}

func (c *Client) ListProjects(org string) ([]store.Project, error) {
	q := url.Values{}
	if org != "" {
		q.Set("org", org)
	}
	var out []store.Project
	err := c.do("GET", "/api/projects", q, nil, &out)
	return out, err
}

func (c *Client) ClaimProject(remote, org string) (store.Project, error) {
	var out store.Project
	err := c.do("POST", "/api/projects/claim", nil, map[string]string{"remote": remote, "org": org}, &out)
	return out, err
}

func (c *Client) MoveProject(remote, org string) (store.Project, error) {
	var out store.Project
	err := c.do("POST", "/api/projects/move", nil, map[string]string{"remote": remote, "org": org}, &out)
	return out, err
}

func (c *Client) RenameOrg(org, name, slug string) (store.Org, error) {
	var out store.Org
	err := c.do("PATCH", orgPath(org), nil, map[string]string{"name": name, "slug": slug}, &out)
	return out, err
}

// ProjectRoles ist die Antwort auf die Rollenliste eines Projekts.
type ProjectRoles struct {
	Project store.Project         `json:"project"`
	Members []store.ProjectMember `json:"members"`
	You     store.RoleInfo        `json:"you"`
}

// projectID sucht die numerische Id der Remote unter den Projekten des Kontos.
func (c *Client) projectID(remote string) (int64, error) {
	projects, err := c.ListProjects("")
	if err != nil {
		return 0, err
	}
	want := scope.NormalizeRemote(remote)
	for _, p := range projects {
		if p.Remote == want {
			return p.ID, nil
		}
	}
	return 0, &APIError{Status: 404, Code: "project_not_found", Message: "project not found: " + remote}
}

func projectRolePath(id int64) string {
	return "/api/projects/" + strconv.FormatInt(id, 10) + "/members"
}

func (c *Client) ProjectRoles(remote string) (ProjectRoles, error) {
	var out ProjectRoles
	id, err := c.projectID(remote)
	if err != nil {
		return out, err
	}
	err = c.do("GET", projectRolePath(id), nil, nil, &out)
	return out, err
}

func (c *Client) SetProjectRole(remote, account, role string, canReview bool) error {
	id, err := c.projectID(remote)
	if err != nil {
		return err
	}
	return c.do("PUT", projectRolePath(id)+"/"+url.PathEscape(account), nil, map[string]any{"role": role, "can_review": canReview}, nil)
}

func (c *Client) RemoveProjectRole(remote, account string) error {
	id, err := c.projectID(remote)
	if err != nil {
		return err
	}
	return c.do("DELETE", projectRolePath(id)+"/"+url.PathEscape(account), nil, nil, nil)
}
