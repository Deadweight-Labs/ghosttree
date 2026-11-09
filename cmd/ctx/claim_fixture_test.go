package main

import (
	"errors"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// claimInOrg places a project the way the fixtures of this package mean it: in
// the organization the actor owns (an explicit claim), since a write alone puts
// an unknown remote into the writer's own namespace (REQ-623). An actor who owns
// no organization claims it for the own namespace.
func claimInOrg(st *store.Store, actor, remote string) (store.Project, error) {
	if orgs, err := st.ListOrgs(actor); err == nil {
		for _, o := range orgs {
			if o.Kind != store.OrgKindUser && o.Role == store.OrgOwner {
				return claimOrPlace(st, actor, remote, o.Slug)
			}
		}
	}
	return claimOrPlace(st, actor, remote, "")
}

// claimOrPlace claims; a github.com remote under a login the actor has not
// verified is placed directly, as a project the namespace held before the
// claim rules (review point 1 of REQ-623 limits explicit claims there).
func claimOrPlace(st *store.Store, actor, remote, orgRef string) (store.Project, error) {
	p, err := st.ClaimProject(actor, remote, orgRef)
	if !errors.Is(err, store.ErrNotGitHubOwner) {
		return p, err
	}
	var org store.Org
	if orgRef == "" {
		org, err = st.EnsureUserNamespace(actor)
	} else {
		org, err = st.OrgByRef(orgRef)
	}
	if err != nil {
		return store.Project{}, err
	}
	if _, err := st.DB().Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,'2026-01-01T00:00:00Z')`, scope.NormalizeRemote(remote), org.ID); err != nil {
		return store.Project{}, err
	}
	p, _ = st.ProjectByRemote(remote)
	return p, nil
}
