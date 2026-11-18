package web

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// orgMay says whether the viewer may use the organization right in org: the
// role in org.Role (from the viewer's own list) holds it, and the session may
// use it (a human right needs a sign-in, not a pasted token).
func orgMay(r *http.Request, org store.Org, right store.Right) bool {
	return store.OrgRoleHas(org.Role, right) && browserPrincipal(r).May(right)
}

// requireSignIn lets a request through when the session may use right: every
// session for an ordinary right, only a sign-in for a human right (DEC-50).
// What the right allows on the object the store decides; this is only the
// sign-in rule, the same one the API applies with webSessionOnly.
func (a *app) requireSignIn(right store.Right, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !browserPrincipal(r).May(right) {
			a.notInteractive(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// orgFormRights is the right each organization form asks for; the sign-in
// rule follows from it.
var orgFormRights = map[string]store.Right{
	"/ui/orgs/invite": store.RightOrgMembers, "/ui/orgs/invite/revoke": store.RightOrgMembers,
	"/ui/orgs/member/role": store.RightOrgMembers, "/ui/orgs/member/remove": store.RightOrgMembers,
	"/ui/orgs/member/login-link": store.RightOrgMembers, "/ui/orgs/project/role": store.RightRolesGrant,
	"/ui/orgs/project/move": store.RightOrgProjects, "/ui/orgs/source/connect": store.RightOrgProjects,
	"/ui/orgs/source/add": store.RightOrgProjects, "/ui/orgs/source/remove": store.RightOrgProjects,
	"/ui/orgs/source/import": store.RightOrgProjects, "/ui/orgs/prefix/visibility": store.RightOrgProjects,
	"/ui/orgs/follow": store.RightOrgProjects, "/ui/handover": store.RightOrgProjects,
	"/ui/orgs/takeover": store.RightOrgProjects, "/ui/orgs/assign": store.RightOrgProjects,
	"/ui/orgs/create": store.RightOrgCreate, "/ui/orgs/project/create": store.RightOrgProjectCreate,
	"/ui/orgs/rename": store.RightOrgManage, "/ui/orgs/workflows": store.RightOrgManage,
	"/ui/orgs/project/visibility": store.RightProjectShare,
	"/ui/orgs/garden":             store.RightOrgAI, "/ui/orgs/workflow-policy": store.RightOrgAI,
	"/ui/gardener/undo": store.RightKnowledgeVerify,
}

// mayDecide refuses closing or reopening a decision a person has to take from
// a session that may not take it (a pasted token); the store refuses it too.
func (a *app) mayDecide(w http.ResponseWriter, r *http.Request, d idea.Decision) bool {
	if d.Way == idea.WayHuman && !browserPrincipal(r).May(store.RightDecideHuman) {
		a.notInteractive(w, r)
		return false
	}
	return true
}
