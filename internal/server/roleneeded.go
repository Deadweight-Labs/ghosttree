package server

import (
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// writeRoleNeeded answers a refusal of the role matrix (REQ-704): the message
// names the role the action needs, the caller's own role and who can give the
// role, because ctx and the MCP tools show it as it is and the person (or the
// agent) has to know whom to ask. details carry the same for a program.
func writeRoleNeeded(w http.ResponseWriter, need *store.RoleNeeded) {
	msg, resolution := roleNeededText(need)
	recordResponseError(w, classifyRequestError(http.StatusForbidden, "", msg), msg)
	writeJSON(w, http.StatusForbidden, map[string]any{
		"code": "forbidden", "error": msg, "message": msg, "resolution": resolution,
		"details": map[string]any{"project": need.Project, "need_role": need.Need, "your_role": need.Have, "grantors": need.Grantors()},
	})
}

// roleNeededText is the English sentence of a RoleNeeded and what to do next.
func roleNeededText(need *store.RoleNeeded) (msg, resolution string) {
	have := "you have no role there"
	if need.Have != "" {
		have = "your role there is " + need.Have
	}
	msg = "this needs the role " + need.Need + " or higher in " + need.Project + "; " + have
	who := need.Grantors()
	switch len(who) {
	case 0:
		resolution = "an owner of the organization can give you the role " + need.Need + " (organization settings, Members)"
	case 1:
		resolution = "ask " + who[0] + " for the role " + need.Need + " (organization settings, Members)"
	default:
		resolution = "ask " + strings.Join(who[:len(who)-1], ", ") + " or " + who[len(who)-1] + " for the role " + need.Need + " (organization settings, Members)"
	}
	return msg + ". " + strings.ToUpper(resolution[:1]) + resolution[1:] + ".", resolution
}
