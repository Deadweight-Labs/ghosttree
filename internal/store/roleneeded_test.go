package store

import (
	"errors"
	"slices"
	"testing"
)

// REQ-704: a refusal of the matrix says which role would have allowed it and
// who can give that role; it stays ErrAccessForbidden for every old caller.
func TestARefusalNamesTheRoleItNeedsAndWhoGivesIt(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	pa := func(id int64) *ProjectAccess { return st.Access(Principal{ID: principalOfID(id)}) }
	cases := []struct {
		name     string
		who      int64
		res      Resource
		act      Action
		have     string
		need     string
		grantors []string
	}{
		// mia (member) and a workflow: only an owner gives lead.
		{"member edits a workflow", 3, ResWorkflow, ActEdit, RoleMember, RoleLead, []string{"robin"}},
		{"member renames a label", 3, ResLabel, ActEdit, RoleMember, RoleLead, []string{"robin"}},
		// gus (guest) creating an issue: a lead (lena) or the owner gives member.
		{"guest creates an issue", 5, ResRequest, ActCreate, RoleGuest, RoleMember, []string{"lena", "robin"}},
		// lena (lead) is herself left out of those who may give it.
		{"lead approves nothing above", 2, ResWorkflow, ActEdit, RoleLead, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := pa(c.who).Check(roleProject, c.res, c.act, Object{})
			if c.need == "" {
				if err != nil {
					t.Fatalf("allowed action refused: %v", err)
				}
				return
			}
			var need *RoleNeeded
			if !errors.Is(err, ErrAccessForbidden) || !errors.As(err, &need) {
				t.Fatalf("refusal %v is no RoleNeeded", err)
			}
			if need.Project != roleProject || need.Have != c.have || need.Need != c.need || !slices.Equal(need.Grantors(), c.grantors) {
				t.Fatalf("got %s/%s/%s %v, want %s/%s %v", need.Project, need.Have, need.Need, need.Grantors(), c.have, c.need, c.grantors)
			}
		})
	}
	// Someone else's own object: no role helps, the plain refusal stays.
	if err := pa(3).Check(roleProject, ResComment, ActEdit, Object{}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("someone else's comment: %v", err)
	} else if errors.As(err, new(*RoleNeeded)) {
		t.Fatalf("a role is named where none helps: %v", err)
	}
}

// REQ-704 (S03): changing workflows is refused in the log mode as well. The
// save itself always wanted a lead; the buttons followed the log mode and
// showed a member what the save then refused.
func TestWorkflowEditIsRefusedInTheLogModeToo(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: false})
	mia := st.Access(Principal{ID: principalOfID(3)})
	lena := st.Access(Principal{ID: principalOfID(2)})
	if mia.CanEditWorkflows(roleProject) || !lena.CanEditWorkflows(roleProject) {
		t.Fatalf("log mode: member %v, lead %v", mia.CanEditWorkflows(roleProject), lena.CanEditWorkflows(roleProject))
	}
	if err := mia.Check(roleProject, ResWorkflow, ActRead, Object{}); err != nil {
		t.Fatalf("a member reads workflows: %v", err)
	}
	// Other writes of a member keep the log mode.
	if err := mia.Check(roleProject, ResLabel, ActEdit, Object{}); err != nil {
		t.Fatalf("the log mode changed for labels: %v", err)
	}
}
