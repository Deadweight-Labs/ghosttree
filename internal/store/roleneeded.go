package store

import (
	"slices"
	"strings"
	"sync"
)

// RoleNeeded is a refusal (403) that knows what would have allowed it: the
// lowest project role the matrix lets do the action, the viewer's own role
// there and who can give that role. errors.Is(err, ErrAccessForbidden) holds,
// so every caller that only knows the sentinel keeps working; the web
// interface, the API, ctx and the MCP tools name the role and the people.
type RoleNeeded struct {
	Project string
	// Have is the viewer's role in the project, "" for none.
	Have string
	// Need is the lowest role that may do the action.
	Need string

	once     sync.Once
	load     func() []string
	grantors []string
}

func (e *RoleNeeded) Error() string {
	return "forbidden for your role: needs " + e.Need + " in " + e.Project
}

// Is lets errors.Is(err, ErrAccessForbidden) hold.
func (e *RoleNeeded) Is(target error) bool { return target == ErrAccessForbidden }

// Grantors are the names of the people who can give Need in the project,
// without the viewer, sorted. They are read on first use: a refusal that is
// only counted (a button that stays hidden) costs no query.
func (e *RoleNeeded) Grantors() []string {
	e.once.Do(func() {
		if e.load != nil {
			e.grantors = e.load()
		}
	})
	return e.grantors
}

// neededRole is the lowest project role whose row in the matrix allows the
// action on this object, keeping the viewer's review flag; "" when no role
// does (the object is someone else's own).
func neededRole(have RoleInfo, res Resource, act Action, obj Object) string {
	roles := ProjectRoles()
	slices.Reverse(roles)
	for _, role := range roles {
		if matrixAllows(RoleInfo{Role: role, CanReview: have.CanReview}, res, act, obj) {
			return role
		}
	}
	return ""
}

// roleNeeded words a refusal of the matrix as RoleNeeded, or returns the plain
// sentinel when no role would help.
func (a *ProjectAccess) roleNeeded(project string, res Resource, act Action, obj Object) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return ErrAccessForbidden
	}
	have := a.Role(project)
	need := neededRole(have, res, act, obj)
	if need == "" || RoleAtLeast(have.Role, need) {
		return ErrAccessForbidden
	}
	self := a.principal.ID
	st := a.st
	return &RoleNeeded{Project: project, Have: have.Role, Need: need, load: func() []string {
		return st.RoleGrantors(project, need, self)
	}}
}

// RoleGrantors names the people who can give role in the project (the rules of
// SetProjectRole): an owner of the project or of its organization gives any
// role, a lead gives member and guest. The account except is left out.
func (s *Store) RoleGrantors(project, role, except string) []string {
	if s.reader != nil {
		return s.reader.RoleGrantors(project, role, except)
	}
	members, err := s.ListProjectMembers(project)
	if err != nil {
		return nil
	}
	right := roleGrantRight(role)
	var out []string
	for _, m := range members {
		if m.AccountID == except || m.Inherited || !(RoleInfo{Role: m.Role}).Has(right) {
			continue
		}
		name := NormalizeAccountName(m.Account)
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
