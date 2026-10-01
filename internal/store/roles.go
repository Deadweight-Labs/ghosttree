package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Projektrollen. owner > lead > member > guest; "reviewer" ist keine Stufe,
// sondern das Flag can_review. Die Zahl ist der Rang: Autorität ist ein
// Integer-Vergleich.
const (
	RoleOwner  = "owner"
	RoleLead   = "lead"
	RoleMember = "member"
	RoleGuest  = "guest"
)

// Wege, auf denen eine Rolle geändert wurde (role_events.via).
const (
	RoleViaAPI = "api"
	RoleViaCLI = "cli"
	RoleViaWeb = "web"
	// RoleViaCLIDB: Notausgang des Betreibers mit direktem Datenbankzugriff.
	RoleViaCLIDB = "cli-db"
)

var (
	// ErrNotGrantor: nur Owner und Lead vergeben Rollen.
	ErrNotGrantor = errors.New("only a project owner or lead may change roles")
	// ErrRoleForbidden: die Rolle liegt über dem, was der Vergebende vergeben darf.
	ErrRoleForbidden = errors.New("that role or account is beyond what you may change")
	// ErrSelfPromotion: die eigene Rolle darf nur sinken.
	ErrSelfPromotion = errors.New("you cannot raise your own role")
	// ErrLastProjectOwner: ein Projekt behält mindestens einen Owner.
	ErrLastProjectOwner = errors.New("a project needs at least one owner")
	// ErrImplicitOwner: Org-Owner sind implizit Projekt-Owner und lassen sich
	// nur über die Organisation ändern.
	ErrImplicitOwner = errors.New("organization owners are implicit project owners; change their organization role instead")
	// ErrNoProjectRole: es gibt nichts zu entfernen.
	ErrNoProjectRole = errors.New("account holds no role in that project")
)

// RoleRank ordnet eine Rolle ein: owner 4, lead 3, member 2, guest 1, alles
// andere (keine Rolle) 0.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 4
	case RoleLead:
		return 3
	case RoleMember:
		return 2
	case RoleGuest:
		return 1
	}
	return 0
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

// ValidAgentRole sagt, ob eine Rolle für einen Agenten angefordert werden darf.
// Leer heißt: nicht angegeben. Agenten sind nie owner.
func ValidAgentRole(role string) bool {
	return role == "" || role == RoleLead || role == RoleMember || role == RoleGuest
}

// RoleInfo ist die Rolle eines Kontos in einem Projekt.
type RoleInfo struct {
	// Role ist leer ohne jede Rolle.
	Role      string `json:"role,omitempty"`
	CanReview bool   `json:"can_review,omitempty"`
	// Implicit: die Rolle kommt aus der Organisation (Org-Owner), nicht aus einer
	// gespeicherten Zeile.
	Implicit bool `json:"implicit,omitempty"`
}

// ProjectMember ist eine Zeile der Rollenliste eines Projekts.
type ProjectMember struct {
	AccountID string `json:"account_id"`
	Account   string `json:"account"`
	Role      string `json:"role"`
	CanReview bool   `json:"can_review"`
	Implicit  bool   `json:"implicit,omitempty"`
	GrantedBy string `json:"granted_by,omitempty"`
	GrantedAt string `json:"granted_at,omitempty"`
}

// projectRoleTx ist die eine Wahrheitsquelle für die Rolle eines Kontos in
// einem Projekt. Ein Org-Owner ist implizit Owner (abgeleitet, nicht
// gespeichert). Sonst zählt die Zeile in project_members, und nur solange das
// Konto in der Organisation des Projekts ist. Ohne Projekt, Zeile und
// Org-Rolle: keine Rolle.
func projectRoleTx(q rowQuerier, remote string, account int64) RoleInfo {
	var pid, orgID int64
	if q.QueryRow(`SELECT id, org_id FROM projects WHERE remote=?`, remote).Scan(&pid, &orgID) != nil {
		return RoleInfo{}
	}
	orgRole := orgRoleTx(q, orgID, account)
	if orgRole == "" {
		return RoleInfo{}
	}
	var role string
	var review int
	_ = q.QueryRow(`SELECT role, can_review FROM project_members WHERE project_id=? AND account_id=?`, pid, account).Scan(&role, &review)
	return deriveRole(orgRole, role, review == 1)
}

// deriveRole ist die Ableitung selbst: Org-Owner sind implizit Owner, sonst
// zählt die gespeicherte Zeile. Einzel- und Sammelabfrage (AccountRoles) teilen
// sie, damit es nur eine Regel gibt.
func deriveRole(orgRole, memberRole string, review bool) RoleInfo {
	info := RoleInfo{Role: memberRole, CanReview: review}
	if orgRole == OrgOwner {
		info.Implicit = memberRole != RoleOwner
		info.Role = RoleOwner
	}
	return info
}

// AccountRoles lädt in einer Abfrage die Rollen eines Kontos in allen Projekten
// seiner Organisationen (Remote -> Rolle) und ob es Instanz-Admin ist. Das ist
// die Sammelform von ProjectRole für Listen, Suche und Bootstrap; ein Test hält
// beide gleich.
func (s *Store) AccountRoles(accountPrincipal string) (map[string]RoleInfo, bool) {
	if s.reader != nil {
		return s.reader.AccountRoles(accountPrincipal)
	}
	out := map[string]RoleInfo{}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return out, false
	}
	var admin int
	_ = s.db.QueryRow(`SELECT is_admin FROM persons WHERE id=?`, id).Scan(&admin)
	rows, err := s.db.Query(`SELECT p.remote, om.role, COALESCE(pm.role,''), COALESCE(pm.can_review,0)
		FROM projects p
		JOIN org_members om ON om.org_id=p.org_id AND om.account_id=?
		LEFT JOIN project_members pm ON pm.project_id=p.id AND pm.account_id=?`, id, id)
	if err != nil {
		return out, admin == 1
	}
	defer rows.Close()
	for rows.Next() {
		var remote, orgRole, role string
		var review int
		if rows.Scan(&remote, &orgRole, &role, &review) == nil {
			out[remote] = deriveRole(orgRole, role, review == 1)
		}
	}
	return out, admin == 1
}

// machineOwners liefert Maschine -> Besitzerkonto und den Instanz-Owner, dem
// Altbestand gehört.
func (s *Store) machineOwners() (map[string]int64, int64) {
	if s.reader != nil {
		return s.reader.machineOwners()
	}
	owner := instanceOwnerID(s.db)
	out := map[string]int64{}
	rows, err := s.db.Query(`SELECT hostname, account_id FROM machines`)
	if err != nil {
		return out, owner
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		if rows.Scan(&name, &id) == nil {
			out[canonicalMachine(name)] = effectiveOwner(id, owner)
		}
	}
	return out, owner
}

// ProjectRole liefert die Rolle des Kontos im Projekt (normalisierte Remote).
// Paket 7 und 8 bauen darauf auf; Rollenvergleiche gehen nur hierüber.
func (s *Store) ProjectRole(remote, accountPrincipal string) RoleInfo {
	if s.reader != nil {
		return s.reader.ProjectRole(remote, accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return RoleInfo{}
	}
	return projectRoleTx(s.db, strings.TrimSpace(remote), id)
}

// CapAgentRole ist min(angefordert, Deckel des Kontos), nie über lead und nie
// unter guest. Der Deckel ist der Rang des Kontos, höchstens lead; ein Konto
// ohne Rolle ist guest. Eine unbekannte oder leere Anforderung zählt als member.
func CapAgentRole(requested, account string) string {
	req := RoleRank(requested)
	if req == 0 || req > RoleRank(RoleLead) {
		req = RoleRank(RoleMember)
		if requested == RoleOwner {
			req = RoleRank(RoleLead)
		}
	}
	ceiling := RoleRank(account)
	if ceiling > RoleRank(RoleLead) {
		ceiling = RoleRank(RoleLead)
	}
	if ceiling < 1 {
		ceiling = 1
	}
	if req > ceiling {
		req = ceiling
	}
	return roleOfRank(req)
}

// agentAccountTx liefert die angeforderte Rolle und das Konto eines Agenten.
// Altbestand ohne Konto gehört dem Instanz-Owner.
func agentAccountTx(q rowQuerier, externalID string) (requested string, account int64, ok bool) {
	var principal string
	if q.QueryRow(`SELECT role, principal_id FROM coord_agents WHERE external_id=?`, externalID).Scan(&requested, &principal) != nil {
		return "", 0, false
	}
	if id, err := parsePersonPrincipalID(principal); err == nil {
		return requested, id, true
	}
	return requested, instanceOwnerID(q), true
}

func effectiveAgentRoleTx(q rowQuerier, remote, externalID string) RoleInfo {
	requested, account, ok := agentAccountTx(q, externalID)
	if !ok || account == 0 {
		return RoleInfo{Role: RoleGuest}
	}
	acct := projectRoleTx(q, remote, account)
	role := CapAgentRole(requested, acct.Role)
	// can_review gehört dem Konto und gilt für den Agenten nur, wenn er
	// mindestens member ist.
	return RoleInfo{Role: role, CanReview: acct.CanReview && RoleRank(role) >= RoleRank(RoleMember)}
}

// EffectiveAgentRole berechnet live die Rolle eines Agenten in einem Projekt:
// min(angeforderte Rolle, Rang seines Kontos dort), höchstens lead. Wer das
// Konto herabstuft, stuft seine Agenten sofort herab. Agenten vergeben keine
// Rollen.
func (s *Store) EffectiveAgentRole(remote, agentExternalID string) RoleInfo {
	if s.reader != nil {
		return s.reader.EffectiveAgentRole(remote, agentExternalID)
	}
	return effectiveAgentRoleTx(s.db, strings.TrimSpace(remote), agentExternalID)
}

func roleLabel(role string, review bool) string {
	if review {
		return role + "+review"
	}
	return role
}

func roleEvent(q execQueryer, scopeRemote, subject, old, new, actor, via string) error {
	_, err := q.Exec(`INSERT INTO role_events(scope, subject, old_role, new_role, actor, via, created_at) VALUES(?,?,?,?,?,?,?)`,
		"project:"+scopeRemote, subject, old, new, actor, via, now())
	return err
}

// projectOwnerCountTx zählt die Owner eines Projekts: gespeicherte Owner, die
// noch in der Organisation sind, und alle Org-Owner (ohne Doppelzählung).
func projectOwnerCountTx(q rowQuerier, pid, orgID int64) int {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM (
		SELECT m.account_id FROM project_members m
			JOIN org_members o ON o.org_id=? AND o.account_id=m.account_id
			WHERE m.project_id=? AND m.role='owner'
		UNION SELECT account_id FROM org_members WHERE org_id=? AND role='owner')`, orgID, pid, orgID).Scan(&n)
	return n
}

// changeProjectRole ist der gemeinsame Kern von Setzen und Entfernen. newRole
// leer heißt: Zeile entfernen.
func (s *Store) changeProjectRole(actorPrincipal, remote, targetPrincipal, newRole string, canReview bool, via string) error {
	remote, err := normalizeRemote(remote)
	if err != nil {
		return err
	}
	if newRole != "" && RoleRank(newRole) == 0 {
		return fmt.Errorf("%w: role must be owner, lead, member or guest", ErrInvalidInput)
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	target, err := parsePersonPrincipalID(targetPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pid, orgID int64
	if tx.QueryRow(`SELECT id, org_id FROM projects WHERE remote=?`, remote).Scan(&pid, &orgID) != nil {
		return ErrProjectNotFound
	}
	// Wer nicht in der Organisation ist, sieht das Projekt nicht.
	if orgRoleTx(tx, orgID, actor) == "" {
		return ErrProjectNotFound
	}
	actorRank := RoleRank(projectRoleTx(tx, remote, actor).Role)
	if actorRank < RoleRank(RoleLead) {
		return ErrNotGrantor
	}
	if orgRoleTx(tx, orgID, target) == "" {
		return ErrNotOrgMember
	}
	if orgRoleTx(tx, orgID, target) == OrgOwner {
		return ErrImplicitOwner
	}
	var curRole string
	var curReview int
	hasRow := tx.QueryRow(`SELECT role, can_review FROM project_members WHERE project_id=? AND account_id=?`, pid, target).Scan(&curRole, &curReview) == nil
	if newRole == "" && !hasRow {
		return ErrNoProjectRole
	}
	if newRole != "" && hasRow && curRole == newRole && (curReview == 1) == canReview {
		return nil
	}
	curRank, newRank := RoleRank(curRole), RoleRank(newRole)
	switch {
	case actor == target:
		// Die eigene Rolle darf nur sinken. Gleicher Rang ist nur erlaubt, wenn
		// dabei das Flag nicht gesetzt, sondern höchstens abgelegt wird.
		if newRank > actorRank || (newRank == actorRank && (curRole != newRole || (canReview && curReview == 0))) {
			return ErrSelfPromotion
		}
	case actorRank == RoleRank(RoleOwner):
	default: // lead
		if newRank > RoleRank(RoleMember) || curRank >= RoleRank(RoleLead) {
			return ErrRoleForbidden
		}
	}
	if curRole == RoleOwner && newRank < RoleRank(RoleOwner) && projectOwnerCountTx(tx, pid, orgID) <= 1 {
		return ErrLastProjectOwner
	}
	old := ""
	if hasRow {
		old = roleLabel(curRole, curReview == 1)
	}
	if newRole == "" {
		if _, err := tx.Exec(`DELETE FROM project_members WHERE project_id=? AND account_id=?`, pid, target); err != nil {
			return err
		}
	} else {
		review := 0
		if canReview {
			review = 1
		}
		if _, err := tx.Exec(`INSERT INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(project_id, account_id) DO UPDATE SET role=excluded.role, can_review=excluded.can_review, granted_by=excluded.granted_by, granted_at=excluded.granted_at`,
			pid, target, newRole, review, actor, now()); err != nil {
			return err
		}
	}
	if err := roleEvent(tx, remote, principalOfID(target), old, roleLabel(newRole, canReview && newRole != ""), principalOfID(actor), via); err != nil {
		return err
	}
	return tx.Commit()
}

// SetProjectRole setzt Rolle und can_review eines Kontos in einem Projekt.
//
//   - Owner (Projekt- oder Org-Owner): jede Rolle an jedes Konto.
//   - Lead: member oder guest (mit can_review) an Konten unter lead.
//   - member, guest: nichts.
//   - Die eigene Rolle darf nur sinken.
//   - Der letzte Owner bleibt (ErrLastProjectOwner).
//
// Org-Owner sind implizit Owner und hier nicht änderbar. Jede Änderung hängt
// ein role_events an; via nennt den Weg (api, cli, web). Nur ein Konto in
// eigenem Namen darf das; Agenten haben keinen Zugang (die Server-Routen
// lehnen Agentenaufrufe ab).
func (s *Store) SetProjectRole(actorPrincipal, remote, targetPrincipal, role string, canReview bool, via string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, remote, targetPrincipal, role, canReview, via}, func(d *Store, p []any) error {
			return d.SetProjectRole(p[0].(string), p[1].(string), p[2].(string), p[3].(string), p[4].(bool), p[5].(string))
		})
	}
	if role == "" {
		return fmt.Errorf("%w: role is required", ErrInvalidInput)
	}
	return s.changeProjectRole(actorPrincipal, remote, targetPrincipal, role, canReview, via)
}

// RemoveProjectRole entfernt die gespeicherte Rolle eines Kontos; danach hat es
// im Projekt keine Rolle. Es gelten dieselben Vergaberechte wie bei SetProjectRole.
func (s *Store) RemoveProjectRole(actorPrincipal, remote, targetPrincipal, via string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, remote, targetPrincipal, via}, func(d *Store, p []any) error {
			return d.RemoveProjectRole(p[0].(string), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	return s.changeProjectRole(actorPrincipal, remote, targetPrincipal, "", false, via)
}

// GrantableRoles nennt die Rollen, die actor dem Konto target im Projekt geben
// darf, absteigend. Die Weboberfläche zeigt nur sie; die Regeln selbst stehen
// in SetProjectRole.
func (s *Store) GrantableRoles(actorPrincipal, remote, targetPrincipal string) []string {
	if s.reader != nil {
		return s.reader.GrantableRoles(actorPrincipal, remote, targetPrincipal)
	}
	remote = strings.TrimSpace(remote)
	actor, err1 := parsePersonPrincipalID(actorPrincipal)
	target, err2 := parsePersonPrincipalID(targetPrincipal)
	if err1 != nil || err2 != nil {
		return nil
	}
	var orgID int64
	if s.db.QueryRow(`SELECT org_id FROM projects WHERE remote=?`, remote).Scan(&orgID) != nil {
		return nil
	}
	if orgRoleTx(s.db, orgID, target) == "" || orgRoleTx(s.db, orgID, target) == OrgOwner {
		return nil
	}
	actorRank := RoleRank(projectRoleTx(s.db, remote, actor).Role)
	if actorRank < RoleRank(RoleLead) {
		return nil
	}
	cur := projectRoleTx(s.db, remote, target).Role
	curRank := RoleRank(cur)
	var out []string
	for _, r := range []string{RoleOwner, RoleLead, RoleMember, RoleGuest} {
		rank := RoleRank(r)
		switch {
		case actor == target:
			// Die eigene Stufe bleibt wählbar, damit sich allein das
			// Reviewer-Flag ändern lässt; höher geht nicht.
			if rank > actorRank || (rank == actorRank && r != cur) {
				continue
			}
		case actorRank == RoleRank(RoleLead):
			if rank > RoleRank(RoleMember) || curRank >= RoleRank(RoleLead) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// ListProjectMembers liefert alle Konten mit einer Rolle im Projekt, absteigend
// nach Rang. Org-Owner stehen als implizite Owner darin.
func (s *Store) ListProjectMembers(remote string) ([]ProjectMember, error) {
	if s.reader != nil {
		return s.reader.ListProjectMembers(remote)
	}
	remote = strings.TrimSpace(remote)
	var pid, orgID int64
	if err := s.db.QueryRow(`SELECT id, org_id FROM projects WHERE remote=?`, remote).Scan(&pid, &orgID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	rows, err := s.db.Query(`SELECT o.account_id, p.name, o.role, COALESCE(m.role,''), COALESCE(m.can_review,0), COALESCE(m.granted_by,0), COALESCE(m.granted_at,'')
		FROM org_members o JOIN persons p ON p.id=o.account_id
		LEFT JOIN project_members m ON m.project_id=? AND m.account_id=o.account_id
		WHERE o.org_id=? AND (o.role='owner' OR m.role IS NOT NULL) ORDER BY p.name LIMIT ?`, pid, orgID, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectMember
	for rows.Next() {
		var id, by int64
		var m ProjectMember
		var orgRole, stored string
		var review int
		if err := rows.Scan(&id, &m.Account, &orgRole, &stored, &review, &by, &m.GrantedAt); err != nil {
			return nil, err
		}
		m.AccountID, m.CanReview, m.Role = principalOfID(id), review == 1, stored
		if by > 0 {
			m.GrantedBy = principalOfID(by)
		}
		if orgRole == OrgOwner {
			m.Implicit, m.Role = stored != RoleOwner, RoleOwner
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortProjectMembers(out)
	return out, nil
}

func sortProjectMembers(m []ProjectMember) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && RoleRank(m[j].Role) > RoleRank(m[j-1].Role); j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

// dropProjectRolesTx löscht gespeicherte Rollen und protokolliert sie. where
// wählt Zeilen aus project_members (Alias m) aus.
func dropProjectRolesTx(tx execQueryer, actor, via, where string, args ...any) error {
	rows, err := tx.Query(`SELECT m.project_id, m.account_id, m.role, m.can_review, p.remote FROM project_members m JOIN projects p ON p.id=m.project_id WHERE `+where, args...)
	if err != nil {
		return err
	}
	type dropped struct {
		pid, account int64
		role, remote string
		review       int
	}
	var list []dropped
	for rows.Next() {
		var d dropped
		if err := rows.Scan(&d.pid, &d.account, &d.role, &d.review, &d.remote); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		if _, err := tx.Exec(`DELETE FROM project_members WHERE project_id=? AND account_id=?`, d.pid, d.account); err != nil {
			return err
		}
		if err := roleEvent(tx, d.remote, principalOfID(d.account), roleLabel(d.role, d.review == 1), "", actor, via); err != nil {
			return err
		}
	}
	return nil
}

// ensureCoordAgentRole ergänzt auf einer alten Datenbank die angeforderte
// Rolle der Agenten. Bestehende Agenten bekommen den Default member.
func ensureCoordAgentRole(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(coord_agents)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "role" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE coord_agents ADD COLUMN role TEXT NOT NULL DEFAULT 'member'`)
	return err
}

// ProjectByID löst die numerische Projekt-Id der API auf.
func (s *Store) ProjectByID(id int64) (Project, bool) {
	if s.reader != nil {
		return s.reader.ProjectByID(id)
	}
	var remote string
	if s.db.QueryRow(`SELECT remote FROM projects WHERE id=?`, id).Scan(&remote) != nil {
		return Project{}, false
	}
	return projectTx(s.db, remote)
}

// OrgOwnerPrincipal nennt einen Owner der Organisation (den ältesten). Der
// Betreiber-Notausgang mit --db handelt in dessen Namen, weil die Vergaberegeln
// einen handelnden Owner verlangen.
func (s *Store) OrgOwnerPrincipal(orgID int64) (string, bool) {
	if s.reader != nil {
		return s.reader.OrgOwnerPrincipal(orgID)
	}
	var id int64
	if s.db.QueryRow(`SELECT account_id FROM org_members WHERE org_id=? AND role='owner' ORDER BY joined_at, account_id LIMIT 1`, orgID).Scan(&id) != nil {
		return "", false
	}
	return principalOfID(id), true
}
