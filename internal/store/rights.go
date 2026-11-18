package store

import (
	"slices"
	"strings"
)

// Rights are the named permissions every access decision asks for (REQ-722,
// IDEA-46). The catalog below is the only place that knows which role holds
// which right; matrixAllows, the Tx checks of the store, the web and the API
// all ask through Has, Can, canTx, orgCanTx or instanceCanTx. A test fails
// when code outside this file compares role ranks or role names directly.

// Right is one named permission, written scope.verb (knowledge.verify).
type Right string

// RightScope is where a right is held: in a project (through the project
// role), in an organization (through the organization role), on the instance
// (through the admin flag) or by every account for itself.
type RightScope string

const (
	ScopeProject  RightScope = "project"
	ScopeOrg      RightScope = "org"
	ScopeInstance RightScope = "instance"
	ScopeAccount  RightScope = "account"
)

// Project rights. The presets give them as the matrix of Spec 8.1 did; a
// right ending in _own (or read_trusted, read_assigned, read_guests) covers
// the objects of the caller, or the visible subset, where the plain right
// covers all of them.
const (
	RightProjectRead   Right = "project.read"
	RightProjectInside Right = "project.internals"

	RightKnowledgeRead        Right = "knowledge.read"
	RightKnowledgeReadTrusted Right = "knowledge.read_trusted"
	RightKnowledgeCreate      Right = "knowledge.create"
	RightKnowledgeEdit        Right = "knowledge.edit"
	RightKnowledgeEditOwn     Right = "knowledge.edit_own"
	RightKnowledgeVerify      Right = "knowledge.verify"
	RightKnowledgeReleaseOwn  Right = "knowledge.release_own"

	RightRequestRead    Right = "request.read"
	RightRequestCreate  Right = "request.create"
	RightRequestEdit    Right = "request.edit"
	RightRequestEditOwn Right = "request.edit_own"

	RightPainRead    Right = "pain.read"
	RightPainCreate  Right = "pain.create"
	RightPainEdit    Right = "pain.edit"
	RightPainEditOwn Right = "pain.edit_own"

	RightIdeaRead    Right = "idea.read"
	RightIdeaCreate  Right = "idea.create"
	RightIdeaEdit    Right = "idea.edit"
	RightIdeaEditOwn Right = "idea.edit_own"
	RightIdeaApprove Right = "idea.approve"

	// RightDecideHuman: take a decision with way=human and answer a task for
	// people; the store records who did it as a person.
	RightDecideHuman Right = "decision.human"

	RightFeatureRead   Right = "feature.read"
	RightFeatureWrite  Right = "feature.write"
	RightFeatureManage Right = "feature.manage"
	RightPackageManage Right = "package.manage"

	RightCommentRead     Right = "comment.read"
	RightCommentCreate   Right = "comment.create"
	RightCommentModerate Right = "comment.moderate"

	RightLabelRead   Right = "label.read"
	RightLabelCreate Right = "label.create"
	RightLabelManage Right = "label.manage"

	RightTaskRead         Right = "task.read"
	RightTaskReadAssigned Right = "task.read_assigned"
	RightTaskCreate       Right = "task.create"
	RightTaskWork         Right = "task.work"
	RightTaskWorkOpen     Right = "task.work_open"
	RightTaskEdit         Right = "task.edit"
	RightTaskEditOwn      Right = "task.edit_own"

	RightSessionRead          Right = "session.read"
	RightSessionReadGuests    Right = "session.read_guests"
	RightTranscriptReadShared Right = "transcript.read_shared"
	RightTranscriptReadGuests Right = "transcript.read_guests"
	RightTranscriptUnshare    Right = "transcript.unshare"
	RightSessionIDs           Right = "session.ids"

	RightRunbookRead    Right = "runbook.read"
	RightRunbookWrite   Right = "runbook.write"
	RightRunbookApprove Right = "runbook.approve"

	RightWorkflowRead Right = "workflow.read"
	RightWorkflowEdit Right = "workflow.edit"

	RightSnapshotUse Right = "snapshot.use"

	RightRoomRead   Right = "room.read"
	RightRoomPost   Right = "room.post"
	RightRoomManage Right = "room.manage"

	RightAgentsRead   Right = "agents.read"
	RightAgentControl Right = "agents.control"
	RightMembersRead  Right = "members.read"

	RightDocumentRead    Right = "document.read"
	RightDocumentCreate  Right = "document.create"
	RightDocumentEdit    Right = "document.edit"
	RightDocumentEditOwn Right = "document.edit_own"

	RightGhostRead  Right = "ghost.read"
	RightGhostWrite Right = "ghost.write"

	RightRolesGrant    Right = "roles.grant"
	RightRolesGrantAny Right = "roles.grant_any"
	RightProjectShare  Right = "project.share"
)

// Organization, instance and account rights.
const (
	RightOrgManage   Right = "org.manage"
	RightOrgMembers  Right = "org.members"
	RightOrgAI       Right = "org.ai"
	RightOrgProjects Right = "org.projects"
	RightOrgMachines Right = "org.machines"
	// RightOrgProjectCreate: every member creates projects in the organization (17 A, #4376).
	RightOrgProjectCreate Right = "org.project_create"

	RightInstanceAdmin Right = "instance.admin"
	RightInstanceAI    Right = "instance.ai"
	// RightInstanceOrgRename: the admin renames an organization it does not
	// own; a person decides that, never a token.
	RightInstanceOrgRename Right = "instance.org_rename"

	RightAccountSecurity Right = "account.security"
	RightOrgCreate       Right = "account.org_create"
)

// RightInfo describes one right of the catalog.
type RightInfo struct {
	Right Right
	Scope RightScope
	// Recommend is the preset the right is meant for from: owner, lead,
	// member or guest in a project, owner or member in an organization. The
	// presets give every right to the recommended role and all above it.
	Recommend string
	// Human: the right is only used from a real sign-in (a web session from a
	// login, or a ctx web confirmation), never with a token an agent holds
	// (DEC-49, DEC-50).
	Human bool
	DE    string
	EN    string
}

// rightCatalog lists every right once. The order is the order of the pages
// that show it.
var rightCatalog = []RightInfo{
	{RightProjectRead, ScopeProject, RoleGuest, false, "Projekt sehen", "See the project"},
	{RightProjectInside, ScopeProject, RoleMember, false, "Branches, Pakete, Maschinennamen und Sitzungsnummern sehen", "See branches, packages, machine names and session numbers"},

	{RightKnowledgeReadTrusted, ScopeProject, RoleGuest, false, "Geprüftes Wissen lesen", "Read trusted knowledge"},
	{RightKnowledgeRead, ScopeProject, RoleMember, false, "Alles Wissen lesen", "Read all knowledge"},
	{RightKnowledgeCreate, ScopeProject, RoleMember, false, "Wissen anlegen", "Write knowledge"},
	{RightKnowledgeEditOwn, ScopeProject, RoleMember, false, "Eigenes Wissen ändern", "Change own knowledge"},
	{RightKnowledgeReleaseOwn, ScopeProject, RoleMember, false, "Funde aus eigenen Sitzungen freigeben", "Release findings from own sessions"},
	{RightKnowledgeEdit, ScopeProject, RoleLead, false, "Jedes Wissen ändern", "Change any knowledge"},
	{RightKnowledgeVerify, ScopeProject, RoleLead, false, "Wissen prüfen und Zurückgehaltenes freigeben", "Verify knowledge and release held entries"},

	{RightRequestRead, ScopeProject, RoleGuest, false, "Issues lesen", "Read issues"},
	{RightRequestCreate, ScopeProject, RoleMember, false, "Issues anlegen und daran arbeiten", "Open issues and work on them"},
	{RightRequestEditOwn, ScopeProject, RoleMember, false, "Eigene Issues ändern", "Change own issues"},
	{RightRequestEdit, ScopeProject, RoleLead, false, "Jedes Issue ändern", "Change any issue"},

	{RightPainRead, ScopeProject, RoleGuest, false, "Pains lesen", "Read pains"},
	{RightPainCreate, ScopeProject, RoleMember, false, "Pains anlegen", "Record pains"},
	{RightPainEditOwn, ScopeProject, RoleMember, false, "Eigene Pains ändern", "Change own pains"},
	{RightPainEdit, ScopeProject, RoleLead, false, "Jeden Pain ändern", "Change any pain"},

	{RightIdeaRead, ScopeProject, RoleGuest, false, "Ideen lesen", "Read ideas"},
	{RightIdeaCreate, ScopeProject, RoleMember, false, "Ideen festhalten", "Record ideas"},
	{RightIdeaEditOwn, ScopeProject, RoleMember, false, "Eigene Ideen ändern", "Change own ideas"},
	{RightIdeaEdit, ScopeProject, RoleLead, false, "Jede Idee ändern", "Change any idea"},
	{RightIdeaApprove, ScopeProject, RoleMember, true, "Konzepte eigener Ideen freigeben", "Approve the concept of own ideas"},
	{RightDecideHuman, ScopeProject, RoleMember, true, "Entscheidungen und Aufgaben übernehmen, die ein Mensch übernehmen muss", "Take decisions and tasks that need a person"},

	{RightFeatureRead, ScopeProject, RoleGuest, false, "Features lesen", "Read features"},
	{RightFeatureWrite, ScopeProject, RoleMember, false, "Features anlegen und fortschreiben", "Create features and write new versions"},
	{RightPackageManage, ScopeProject, RoleMember, false, "Pakete anlegen und ändern", "Create and change packages"},
	{RightFeatureManage, ScopeProject, RoleLead, false, "Features umbenennen, einstellen und wieder aufnehmen", "Rename, retire and revive features"},

	{RightCommentRead, ScopeProject, RoleGuest, false, "Kommentare lesen", "Read comments"},
	{RightCommentCreate, ScopeProject, RoleMember, false, "Kommentieren", "Comment"},
	{RightCommentModerate, ScopeProject, RoleLead, false, "Fremde Kommentare löschen", "Delete comments of others"},

	{RightLabelRead, ScopeProject, RoleGuest, false, "Labels sehen", "See labels"},
	{RightLabelCreate, ScopeProject, RoleMember, false, "Labels anlegen", "Create labels"},
	{RightLabelManage, ScopeProject, RoleLead, false, "Labels ändern und löschen", "Change and delete labels"},

	{RightTaskReadAssigned, ScopeProject, RoleGuest, false, "Eigene Aufgaben lesen", "Read own tasks"},
	{RightTaskRead, ScopeProject, RoleMember, false, "Alle Aufgaben lesen", "Read all tasks"},
	{RightTaskCreate, ScopeProject, RoleMember, false, "Aufgaben stellen", "Ask for tasks"},
	{RightTaskWorkOpen, ScopeProject, RoleMember, false, "Eigene und offene Aufgaben erledigen", "Complete own and open tasks"},
	{RightTaskEditOwn, ScopeProject, RoleMember, false, "Eigene Aufgaben ändern", "Change own tasks"},
	{RightTaskWork, ScopeProject, RoleLead, false, "Jede Aufgabe erledigen", "Complete any task"},
	{RightTaskEdit, ScopeProject, RoleLead, false, "Jede Aufgabe ändern", "Change any task"},

	{RightSessionReadGuests, ScopeProject, RoleGuest, false, "Für Gäste freigegebene Sitzungen sehen", "See sessions shared with guests"},
	{RightTranscriptReadGuests, ScopeProject, RoleGuest, false, "Für Gäste freigegebene Transkripte lesen", "Read transcripts shared with guests"},
	{RightSessionRead, ScopeProject, RoleMember, false, "Sitzungen sehen", "See sessions"},
	{RightTranscriptReadShared, ScopeProject, RoleMember, false, "Freigegebene Transkripte lesen", "Read shared transcripts"},
	{RightSessionIDs, ScopeProject, RoleLead, false, "Fremde freigegebene Sitzungen in der Pfad-Aktivität erkennen", "Recognise others' shared sessions in path activity"},
	{RightTranscriptUnshare, ScopeProject, RoleOwner, false, "Freigaben fremder Sitzungen zurücknehmen", "Withdraw the sharing of others' sessions"},

	{RightRunbookRead, ScopeProject, RoleMember, false, "Runbooks lesen", "Read runbooks"},
	{RightRunbookWrite, ScopeProject, RoleMember, false, "Runbooks schreiben und Läufe melden", "Write runbooks and report runs"},
	{RightRunbookApprove, ScopeProject, RoleLead, false, "Runbook-Fassungen freigeben", "Approve runbook versions"},

	{RightWorkflowRead, ScopeProject, RoleMember, false, "Workflows und Läufe sehen", "See workflows and runs"},
	{RightWorkflowEdit, ScopeProject, RoleLead, false, "Workflows anlegen, ändern, schalten und erneut ausführen", "Create, change, switch and rerun workflows"},

	{RightSnapshotUse, ScopeProject, RoleMember, false, "Kontext-Snapshots anlegen und lesen", "Create and read context snapshots"},

	{RightRoomRead, ScopeProject, RoleGuest, false, "Projektraum lesen", "Read the project room"},
	{RightRoomPost, ScopeProject, RoleMember, false, "Im Projektraum schreiben", "Write in the project room"},
	{RightRoomManage, ScopeProject, RoleLead, true, "Threads anderer schließen und archivieren", "Close and archive threads of others"},

	{RightMembersRead, ScopeProject, RoleGuest, false, "Mitglieder sehen", "See members"},
	{RightAgentsRead, ScopeProject, RoleMember, false, "Agenten sehen", "See agents"},
	{RightAgentControl, ScopeProject, RoleLead, true, "Agenten anhalten, unterbrechen und fortsetzen", "Pause, interrupt and resume agents"},

	{RightDocumentRead, ScopeProject, RoleGuest, false, "Dokumente lesen", "Read documents"},
	{RightDocumentCreate, ScopeProject, RoleMember, false, "Dokumente anlegen", "Create documents"},
	{RightDocumentEditOwn, ScopeProject, RoleMember, false, "Eigene Dokumente ändern", "Change own documents"},
	{RightDocumentEdit, ScopeProject, RoleLead, false, "Jedes Dokument ändern", "Change any document"},

	{RightGhostRead, ScopeProject, RoleGuest, false, "Dateibeschreibungen lesen", "Read file descriptions"},
	{RightGhostWrite, ScopeProject, RoleMember, false, "Dateibeschreibungen schreiben", "Write file descriptions"},

	{RightRolesGrant, ScopeProject, RoleLead, true, "Mitglied- und Gastrollen vergeben", "Give member and guest roles"},
	{RightRolesGrantAny, ScopeProject, RoleOwner, true, "Jede Projektrolle vergeben", "Give any project role"},
	{RightProjectShare, ScopeProject, RoleOwner, true, "Projekt mit der Organisation teilen", "Share the project with the organization"},

	{RightOrgManage, ScopeOrg, OrgOwner, false, "Organisation umbenennen und einstellen", "Rename and set up the organization"},
	{RightOrgMembers, ScopeOrg, OrgOwner, true, "Mitglieder einladen, Rollen ändern, entfernen", "Invite members, change roles, remove"},
	{RightOrgAI, ScopeOrg, OrgOwner, true, "KI-Schlüssel, Budget und Grenzen der KI-Läufe", "AI keys, budget and limits of AI runs"},
	{RightOrgProjects, ScopeOrg, OrgOwner, true, "Projekte übernehmen, verschieben und zuordnen", "Take over, move and assign projects"},
	{RightOrgProjectCreate, ScopeOrg, OrgMember, false, "Projekte in der Organisation anlegen", "Create projects in the organization"},
	{RightOrgMachines, ScopeOrg, OrgOwner, true, "Maschinen der Organisation verwalten", "Manage the organization's machines"},

	{RightInstanceAdmin, ScopeInstance, "admin", false, "Instanz verwalten", "Run the instance"},
	{RightInstanceOrgRename, ScopeInstance, "admin", true, "Fremde Organisationen umbenennen", "Rename organizations of others"},
	{RightInstanceAI, ScopeInstance, "admin", true, "KI-Schlüssel der Instanz", "AI keys of the instance"},

	{RightOrgCreate, ScopeAccount, "", true, "Organisationen anlegen", "Create organizations"},
	{RightAccountSecurity, ScopeAccount, "", true, "Anmeldung, E-Mail, Passwort und Geräte", "Sign-in, email, password and devices"},
}

var rightIndex = func() map[Right]RightInfo {
	m := make(map[Right]RightInfo, len(rightCatalog))
	for _, r := range rightCatalog {
		if _, dup := m[r.Right]; dup {
			panic("rights: duplicate " + string(r.Right))
		}
		m[r.Right] = r
	}
	return m
}()

// Rights returns the catalog in display order.
func Rights() []RightInfo { return slices.Clone(rightCatalog) }

// RightOf looks one right up.
func RightOf(r Right) (RightInfo, bool) {
	info, ok := rightIndex[r]
	return info, ok
}

// HumanRights is the list of rights that need a real sign-in (DEC-49): an
// agent, a CLI token or a pasted token never uses them.
func HumanRights() []Right {
	var out []Right
	for _, r := range rightCatalog {
		if r.Human {
			out = append(out, r.Right)
		}
	}
	return out
}

// ---------------------------------------------------------------- presets

// Roles ordered from most to fewest rights. The rank only lives here.
var projectRoleOrder = []string{RoleOwner, RoleLead, RoleMember, RoleGuest}
var orgRoleOrder = []string{OrgOwner, OrgMember}

func roleRank(role string) int {
	if i := slices.Index(projectRoleOrder, role); i >= 0 {
		return len(projectRoleOrder) - i
	}
	return 0
}

func orgRoleRank(role string) int {
	if i := slices.Index(orgRoleOrder, role); i >= 0 {
		return len(orgRoleOrder) - i
	}
	return 0
}

// RightSet is a set of rights.
type RightSet map[Right]bool

// Has says whether the set holds r.
func (s RightSet) Has(r Right) bool { return s[r] }

// presets are the four project presets and two organization presets. Each
// gives the rights recommended for it and for every role below it.
var (
	projectPresets = buildPresets(ScopeProject, projectRoleOrder, roleRank)
	orgPresets     = buildPresets(ScopeOrg, orgRoleOrder, orgRoleRank)
)

func buildPresets(scope RightScope, order []string, rank func(string) int) map[string]RightSet {
	out := map[string]RightSet{}
	for _, role := range order {
		set := RightSet{}
		for _, r := range rightCatalog {
			if r.Scope == scope && rank(r.Recommend) > 0 && rank(r.Recommend) <= rank(role) {
				set[r.Right] = true
			}
		}
		out[role] = set
	}
	return out
}

// PresetRights returns a copy of the rights of a project preset (owner, lead,
// member, guest); nil for anything else.
func PresetRights(role string) RightSet {
	p, ok := projectPresets[role]
	if !ok {
		return nil
	}
	out := make(RightSet, len(p))
	for r := range p {
		out[r] = true
	}
	return out
}

// ProjectRoles are the project presets, most rights first.
func ProjectRoles() []string { return slices.Clone(projectRoleOrder) }

// ValidProjectRole: role is one of the project presets.
func ValidProjectRole(role string) bool { return roleRank(role) > 0 }

// ValidOrgRole: role is one of the organization presets.
func ValidOrgRole(role string) bool { return orgRoleRank(role) > 0 }

// reviewGrant is what the can_review flag of a project role adds. It counts
// from member up; a guest with the flag gets nothing from it.
var reviewGrant = []Right{RightKnowledgeVerify}

// Has says whether the role holds the right in its project.
func (ri RoleInfo) Has(r Right) bool {
	if projectPresets[ri.Role][r] {
		return true
	}
	return ri.CanReview && slices.Contains(reviewGrant, r) && projectPresets[ri.Role][RightKnowledgeCreate]
}

// Any: the account holds any role in the project at all.
func (ri RoleInfo) Any() bool { return ri.Has(RightProjectRead) }

// ---------------------------------------------------------------- principals

// SignedIn: the principal comes from a real sign-in (a web session from a
// login or a ctx web confirmation), not from a token. Only such a principal
// uses human rights.
func (p Principal) SignedIn() bool { return p.TokenKind == WebSessionKind }

// humanOK says whether the principal may use r as far as the human rule goes.
func humanOK(p Principal, r Right) bool {
	info, ok := rightIndex[r]
	if !ok {
		return false
	}
	return !info.Human || p.SignedIn()
}

// Can is the one question every project check asks: does the caller hold the
// right in the project? Unknown rights are refused.
func (a *ProjectAccess) Can(project string, r Right) bool {
	if !humanOK(a.principal, r) {
		return false
	}
	return a.Role(project).Has(r)
}

// CanAny: the caller holds r in any project it has a role in.
func (a *ProjectAccess) CanAny(r Right) bool {
	for _, p := range a.Projects() {
		if a.Can(p, r) {
			return true
		}
	}
	return false
}

// CanOrg: the caller holds the organization right r in org.
func (a *ProjectAccess) CanOrg(orgID int64, r Right) bool {
	if !a.valid || !humanOK(a.principal, r) {
		return false
	}
	return orgCanTx(a.st.db, orgID, a.account, r)
}

// CanInstance: the caller holds the instance right r (instance admin).
func (a *ProjectAccess) CanInstance(r Right) bool {
	if !humanOK(a.principal, r) {
		return false
	}
	return a.IsAdmin() && rightIndex[r].Scope == ScopeInstance
}

// CanAccount: the caller may use the account right r on its own account.
func (a *ProjectAccess) CanAccount(r Right) bool {
	return a.valid && rightIndex[r].Scope == ScopeAccount && humanOK(a.principal, r)
}

// canTx is Can inside a transaction, for store functions that take an
// account id. The human rule is checked where the principal is known (the web
// routes, the API's webSessionOnly); store functions that know the principal
// call canPrincipalTx.
func canTx(q rowQuerier, project string, account int64, r Right) bool {
	return projectRoleTx(q, project, account).Has(r)
}

// canPrincipalTx is canTx with the human rule.
func canPrincipalTx(q rowQuerier, project string, p Principal, account int64, r Right) bool {
	return humanOK(p, r) && canTx(q, project, account, r)
}

// orgCanTx: the account holds the organization right r in org.
func orgCanTx(q rowQuerier, orgID, account int64, r Right) bool {
	return OrgRoleHas(orgRoleTx(q, orgID, account), r)
}

// OrgRoleHas says whether an organization role holds r.
func OrgRoleHas(orgRole string, r Right) bool { return orgPresets[orgRole][r] }

// instanceCanTx: the account holds the instance right r.
func instanceCanTx(q rowQuerier, account int64, r Right) bool {
	return rightIndex[r].Scope == ScopeInstance && isServerAdminTx(q, account)
}

// ---------------------------------------------------------------- order

// RoleAtLeast compares two project roles by their presets: a has every right
// b has. It is for authority in the room and for limits on what one may give,
// never for a single action.
func RoleAtLeast(a, b string) bool { return roleRank(a) >= roleRank(b) }

// RoleAbove: a has strictly more rights than b.
func RoleAbove(a, b string) bool { return roleRank(a) > roleRank(b) }

// HighestRole is the role with the most rights among roles ("" without any).
func HighestRole(roles ...string) string {
	best := ""
	for _, r := range roles {
		if roleRank(r) > roleRank(best) {
			best = r
		}
	}
	return best
}

// LowerRole is the role with fewer rights of the two (guest when both are
// unknown).
func LowerRole(a, b string) string {
	if roleRank(a) <= roleRank(b) {
		if roleRank(a) == 0 {
			return RoleGuest
		}
		return a
	}
	return b
}

// SortRoles sorts in place, most rights first; ties keep their order.
func SortByRole[T any](items []T, role func(T) string) {
	slices.SortStableFunc(items, func(x, y T) int { return roleRank(role(y)) - roleRank(role(x)) })
}

// IsOrgOwnerRole: the organization role is owner, which makes its holder an
// implicit owner of every project of the organization.
func IsOrgOwnerRole(orgRole string) bool { return orgRole == OrgOwner }

// roleGrantRight is the right one needs to give role: member and guest need
// roles.grant, lead and owner roles.grant_any.
func roleGrantRight(role string) Right {
	if roleRank(role) <= roleRank(RoleMember) {
		return RightRolesGrant
	}
	return RightRolesGrantAny
}

// mayChangeRole says whether actor (with its role) may move target from cur to
// next (next "" removes). It is the rule of SetProjectRole: the own role only
// sinks, an owner gives anything, a lead gives member and guest to accounts
// below lead.
func mayChangeRole(actor RoleInfo, self bool, cur, next string, curReview, nextReview bool) error {
	if !actor.Has(RightRolesGrant) {
		return ErrNotGrantor
	}
	switch {
	case self:
		if roleRank(next) > roleRank(actor.Role) || (roleRank(next) == roleRank(actor.Role) && (cur != next || (nextReview && !curReview))) {
			return ErrSelfPromotion
		}
	case actor.Has(RightRolesGrantAny):
	default:
		if !actor.Has(roleGrantRight(next)) || !actor.Has(roleGrantRight(cur)) {
			return ErrRoleForbidden
		}
	}
	return nil
}

// grantableRoles lists what actor may give target (current role cur).
func grantableRoles(actor RoleInfo, self bool, cur string) []string {
	if !actor.Has(RightRolesGrant) {
		return nil
	}
	var out []string
	for _, r := range projectRoleOrder {
		if mayChangeRole(actor, self, cur, r, false, false) == nil || (self && r == cur) {
			out = append(out, r)
		}
	}
	return out
}

// lastOwnerAffected: the change takes the owner role from cur.
func lastOwnerAffected(cur, next string) bool {
	return cur == RoleOwner && roleRank(next) < roleRank(RoleOwner)
}

// capAgentRole is CapAgentRole on ranks; the rule moves to the human list in
// a later step of REQ-722.
func capAgentRole(requested, account string) string {
	req := roleRank(requested)
	if req == 0 || req > roleRank(RoleLead) {
		req = roleRank(RoleMember)
		if requested == RoleOwner {
			req = roleRank(RoleLead)
		}
	}
	ceiling := min(roleRank(account), roleRank(RoleLead))
	ceiling = max(ceiling, 1)
	return roleOfRank(min(req, ceiling))
}

func roleOfRank(rank int) string {
	switch {
	case rank >= 4:
		return RoleOwner
	case rank == 3:
		return RoleLead
	case rank == 2:
		return RoleMember
	}
	return RoleGuest
}

// normRole trims and lowercases a role name from input.
func normRole(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// GuestOnly: the role sees the project but not its insides (the guest
// preset).
func (ri RoleInfo) GuestOnly() bool { return ri.Any() && !ri.Has(RightProjectInside) }

// deriveRole ist die Ableitung selbst: Org-Owner sind implizit Owner, sonst
// zählt die gespeicherte Zeile. Einzel- und Sammelabfrage (AccountRoles) teilen
// sie, damit es nur eine Regel gibt.
func deriveRole(orgRole, memberRole string, review, shareDefault bool) RoleInfo {
	info := RoleInfo{Role: memberRole, CanReview: review}
	if memberRole == "" && shareDefault {
		info.Role, info.Inherited = DefaultSharedRole, true
	}
	if IsOrgOwnerRole(orgRole) {
		info.Implicit = memberRole != RoleOwner
		info.Role = RoleOwner
	}
	return info
}

// ValidAgentRole sagt, ob eine Rolle für einen Agenten angefordert werden darf.
// Leer heißt: nicht angegeben. Agenten sind nie owner.
func ValidAgentRole(role string) bool {
	return role == "" || role == RoleLead || role == RoleMember || role == RoleGuest
}

// IsOrgPlainMemberRole: the organization role is member, not owner.
func IsOrgPlainMemberRole(orgRole string) bool { return orgRole == OrgMember }

// linkProjectRole: an invitation link may carry this project role (member or
// guest, what roles.grant gives; never lead or owner).
func linkProjectRole(role string) bool {
	return ValidProjectRole(role) && roleGrantRight(role) == RightRolesGrant
}

// agentRoleInfo is the role of an agent: the capped role, with the review
// flag of its account only when the capped role can hold the verify right.
func agentRoleInfo(role string, account RoleInfo) RoleInfo {
	return RoleInfo{Role: role, CanReview: account.CanReview && RoleInfo{Role: role}.Has(RightKnowledgeCreate)}
}

// May says whether the principal may use r as far as the human rule goes: a
// human right needs a sign-in.
func (p Principal) May(r Right) bool { return humanOK(p, r) }
