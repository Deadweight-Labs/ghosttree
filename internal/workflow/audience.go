package workflow

import (
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The audience of a run (KN-4817): its page with the inputs, the tool calls
// and the proposals is read by every member of its project, while it reads
// with the rights of one account, often a lead or the owner of a private
// session. So whatever reaches the model, whether assembled as an input or
// returned by a read tool, passes one filter: the account may read it, it
// belongs to the run's project, and nothing keeps it from the members (held
// for a reviewer, distilled from a private session, staged or quarantined,
// bound to a machine). The account's right alone is not enough, and there is
// no principal "any member": the two conditions together are what every
// member reads.

// membersReadKnowledge: an active, confirmed entry of the project, not bound
// to a machine and not held from its members, that the run's account may read.
func (a *assembler) membersReadKnowledge(k store.Knowledge) bool {
	return a.pa != nil && k.Scope.Project == a.run.Project && k.Scope.Machine == "" && k.Status == "active" &&
		k.Confidence != "staged" && k.Confidence != "quarantined" && a.pa.CanSeeKnowledge(k) && !a.st.HeldFromMembers(store.HeldKnowledge, k.ID)
}

// membersReadKnowledgeID is membersReadKnowledge for an entry known by its
// number only.
func (a *assembler) membersReadKnowledgeID(id int64) (store.Knowledge, bool) {
	k, err := a.st.KnowledgeByID(id)
	if err != nil || !a.membersReadKnowledge(k) {
		return store.Knowledge{}, false
	}
	return k, true
}

// membersReadRequest: an issue of the project the run's account may read and
// nothing keeps from the members (held, or distilled from a private session
// and not claimed).
func (a *assembler) membersReadRequest(id int64) bool {
	if a.pa == nil || a.st.HeldFromMembers(store.HeldRequest, id) {
		return false
	}
	ref, err := a.st.RequestRef(store.RefRequest, id)
	if err != nil || ref.Project != a.run.Project {
		return false
	}
	obj, visible := a.pa.RequestObject(ref)
	return visible && a.pa.Allow(ref.Project, store.ResRequest, store.ActRead, obj)
}

// membersReadIdea: an idea of the project the run's account may read and that
// is not held from the members.
func (a *assembler) membersReadIdea(i idea.Idea) bool {
	return a.pa != nil && i.Project == a.run.Project && a.pa.CanSeeIdea(i) && !a.st.HeldFromMembers(store.HeldIdea, i.ID)
}

// membersReadPain: a problem of the project the run's account may read and
// that is not held from the members.
func (a *assembler) membersReadPain(p pain.Pain) bool {
	return a.pa != nil && p.Project == a.run.Project && a.pa.CanSeePain(p) && !a.st.HeldFromMembers(store.HeldPain, p.ID)
}
