package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Who may make a remote a project (REQ-623, after the security review of the
// owner model). A write claims an unclaimed remote only where nothing is in
// doubt: the prefix of a connected organization the writer is a member of,
// or the writer's own GitHub login, verified by GitHub (github_id > 0).
// Everything else stays unclaimed: entries are stored and each author sees
// only their own, sessions run without the project. An explicit claim (ctx
// project claim, the terminal interface) works only when no other account has
// entries under the remote; otherwise it is contested and an instance admin
// decides. A guest never gets a project of its own by writing.

var (
	// ErrGuestOnly: the account is a guest everywhere; it works in the projects
	// it was given and creates none.
	ErrGuestOnly = errors.New("a guest works only in the projects it was given; this creates no project")
	// ErrContested: other accounts have entries under the remote.
	ErrContested = errors.New("contested: other accounts have entries under this remote; an instance admin decides")
	// ErrNotAdmin: only the instance admin decides a contested remote.
	ErrNotAdmin = errors.New("only the instance admin may do this")
	// ErrNotGitHubOwner: a github.com/<login>/... remote is claimed explicitly
	// only by the verified owner of that login, a member of the organization
	// connected to its prefix, or the instance admin.
	ErrNotGitHubOwner = errors.New("only the verified owner of this GitHub login, a member of the organization connected to it, or the instance admin may claim it; link your GitHub account first")
	// ErrNoHandover: the project is not waiting for this organization, or its
	// owner has not agreed.
	ErrNoHandover = errors.New("the project's owner has not agreed to hand it over to this organization")
)

// ContestedError names how many entries of which kind other accounts have
// under a remote, never their content.
type ContestedError struct{ Kinds map[string]int }

func (e *ContestedError) Error() string {
	var parts []string
	for _, k := range sortedCountKeys(e.Kinds) {
		parts = append(parts, fmt.Sprintf("%s %d", k, e.Kinds[k]))
	}
	return ErrContested.Error() + " (" + strings.Join(parts, ", ") + ")"
}

func (e *ContestedError) Is(target error) bool { return target == ErrContested }

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// guestOnlyTx: the account holds guest roles and nothing more, is no owner of
// an organization (its own namespace does not count) and no instance admin.
func guestOnlyTx(q rowQuerier, account int64) bool {
	var guest bool
	_ = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM project_members WHERE account_id=? AND role='guest')
		AND NOT EXISTS(SELECT 1 FROM project_members WHERE account_id=? AND role<>'guest')
		AND NOT EXISTS(SELECT 1 FROM org_members m JOIN orgs o ON o.id=m.org_id WHERE m.account_id=? AND m.role='owner' AND o.kind<>'user')
		AND NOT EXISTS(SELECT 1 FROM persons WHERE id=? AND is_admin=1)`, account, account, account, account).Scan(&guest)
	return guest
}

// remoteGitHubOwner is the owner segment of a github.com remote, lowercased.
func remoteGitHubOwner(remote string) string {
	rest, ok := strings.CutPrefix(remote, "github.com/")
	if !ok {
		return ""
	}
	owner, _, found := strings.Cut(rest, "/")
	if !found {
		return ""
	}
	return strings.ToLower(owner)
}

// verifiedGitHubOwnerTx names the account whose GitHub login, verified by
// GitHub's own sign-in (github_id > 0), owns the remote.
func verifiedGitHubOwnerTx(q rowQuerier, remote string) (int64, bool) {
	owner := remoteGitHubOwner(remote)
	if owner == "" {
		return 0, false
	}
	var id int64
	if q.QueryRow(`SELECT account_id FROM account_github WHERE lower(login)=? AND github_id>0`, owner).Scan(&id) != nil {
		return 0, false
	}
	return id, true
}

// authorTables are the tables with a project whose rows name an author by
// account name (person). Sessions and comments carry the account number too.
var authorTables = []string{"knowledge", "requests", "documents", "pains", "ideas", "features", "runbooks", "ghost_files", "open_decisions", "comments", "attachments", "threads"}

// accountNamesTx are the names an account had and has: entries carry the name
// of the moment.
func accountNamesTx(q queryer, account int64) []string {
	var names []string
	var cur string
	if q.QueryRow(`SELECT name FROM persons WHERE id=?`, account).Scan(&cur) == nil {
		names = append(names, cur)
	}
	rows, err := q.Query(`SELECT old_name FROM person_name_history WHERE person_id=?`, account)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// foreignEntriesTx counts, per kind, the entries other accounts have under a
// remote. Legacy sessions without an account belong to the instance owner.
func foreignEntriesTx(q queryer, remote string, account int64) map[string]int {
	out := map[string]int{}
	owner := instanceOwnerID(q)
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project=? AND CASE WHEN account_id=0 THEN ? ELSE account_id END <> ?`, remote, owner, account).Scan(&n)
	if n > 0 {
		out["sessions"] = n
	}
	names := accountNamesTx(q, account)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	if ph == "" {
		ph = "''"
	}
	for _, t := range authorTables {
		args := []any{remote}
		for _, name := range names {
			args = append(args, name)
		}
		var c int
		if q.QueryRow(`SELECT COUNT(*) FROM `+t+` WHERE project=? AND person<>'' AND person NOT IN (`+ph+`)`, args...).Scan(&c) == nil && c > 0 {
			out[t] = c
		}
	}
	return out
}

// ownWorkUnderTx: the account is the instance admin, or has sessions (legacy
// ones without an account are the instance owner's) or entries of its own,
// under any name it had, under the remote.
func ownWorkUnderTx(q rowQuerier, remote string, account int64) bool {
	var yes bool
	_ = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM persons WHERE id=? AND is_admin=1)
		OR EXISTS(SELECT 1 FROM sessions WHERE project=? AND (account_id=? OR (account_id=0 AND ?=?)))`,
		account, remote, account, account, instanceOwnerID(q)).Scan(&yes)
	if yes {
		return true
	}
	for _, t := range authorTables {
		var own bool
		if q.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+t+` WHERE project=? AND person<>'' AND person IN (
			SELECT name FROM persons WHERE id=? UNION SELECT old_name FROM person_name_history WHERE person_id=?))`,
			remote, account, account).Scan(&own) == nil && own {
			return true
		}
	}
	return false
}

// userSlugOwnerTx: a hostless id whose first segment is (or was) the address
// of a person's namespace belongs to that person.
func userSlugOwnerTx(q rowQuerier, remote string) (int64, bool) {
	if !scope.IsNamedProject(remote) {
		return 0, false
	}
	first := remote[:strings.IndexByte(remote, '/')]
	var id int64
	if q.QueryRow(`SELECT account_id FROM orgs WHERE kind='user' AND slug=?`, first).Scan(&id) == nil {
		return id, true
	}
	if q.QueryRow(`SELECT o.account_id FROM org_slug_history h JOIN orgs o ON o.id=h.org_id WHERE o.kind='user' AND h.old_slug=?`, first).Scan(&id) == nil {
		return id, true
	}
	return 0, false
}

// ClaimContested decides a contested remote: the instance admin gives it to an
// organization or a person's namespace (orgRef), in spite of other accounts'
// entries under it. Everything else of a claim still holds. The web calls it
// from an interactive session only; the API refuses it (an agent's token must
// never move ownership).
func (s *Store) ClaimContested(actorPrincipal, remote, orgRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, remote, orgRef}, func(d *Store, p []any) (Project, error) {
			return d.ClaimContested(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return Project{}, err
	}
	if !isAdminTx(s.db, actor) {
		return Project{}, ErrNotAdmin
	}
	return s.claimContested(principalOfID(actor), remote, orgRef)
}

// ClaimContestedAsOperator is ClaimContested for the operator with the
// database in hand (ctx project claim --force --db), logged as "operator".
func (s *Store) ClaimContestedAsOperator(remote, orgRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{remote, orgRef}, func(d *Store, p []any) (Project, error) {
			return d.ClaimContestedAsOperator(p[0].(string), p[1].(string))
		})
	}
	return s.claimContested("operator", remote, orgRef)
}

func (s *Store) claimContested(by, remote, orgRef string) (Project, error) {
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Project{}, err
	}
	if scope.IsThrowawayRemote(remote) {
		return Project{}, ErrInvalidRemote
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if _, known := projectTx(tx, remote); known {
		return Project{}, ErrProjectClaimed
	}
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, remote, o.ID, now()); err != nil {
		return Project{}, err
	}
	if err := holdForeignEntriesTx(tx, remote, o.ID, false); err != nil {
		return Project{}, err
	}
	if porg, vis, ok := prefixOrgTx(tx, remote); ok && porg == o.ID && vis == VisibilityShared {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) SELECT id FROM projects WHERE remote=?`, remote); err != nil {
			return Project{}, err
		}
	}
	if err := orgEvent(tx, o.ID, "claim_contested", by, remote, "decided by the instance admin"); err != nil {
		return Project{}, err
	}
	p, _ := projectTx(tx, remote)
	return p, tx.Commit()
}

// isAdminTx: the account is an instance admin.
func isAdminTx(q rowQuerier, account int64) bool {
	var admin int
	_ = q.QueryRow(`SELECT is_admin FROM persons WHERE id=?`, account).Scan(&admin)
	return admin == 1
}

// ReassignProject gives a known project to another organization or person's
// namespace (toRef: its slug). Only the instance admin may, from an
// interactive web session (the API refuses it): it is the way out when
// somebody claimed a repository that is not theirs. Roles of accounts outside
// the new owner go, sharing starts over (the new owner's prefix rule applies),
// a named project takes the new address in its id, entries of whoever is no
// writer under the new owner are held again (also ones released before), and
// both owners' logs record it.
func (s *Store) ReassignProject(actorPrincipal, remote, toRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, remote, toRef}, func(d *Store, p []any) (Project, error) {
			return d.ReassignProject(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return Project{}, err
	}
	if !isAdminTx(s.db, actor) {
		return Project{}, ErrNotAdmin
	}
	return s.reassignProject(principalOfID(actor), remote, toRef)
}

// ReassignProjectAsOperator is ReassignProject for the operator with the
// database in hand (ctx project reassign --db), logged as "operator".
func (s *Store) ReassignProjectAsOperator(remote, toRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{remote, toRef}, func(d *Store, p []any) (Project, error) {
			return d.ReassignProjectAsOperator(p[0].(string), p[1].(string))
		})
	}
	return s.reassignProject("operator", remote, toRef)
}

func (s *Store) reassignProject(by, remote, toRef string) (Project, error) {
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Project{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	cur, ok := projectTx(tx, remote)
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	to, err := orgByRefTx(tx, toRef)
	if err != nil {
		return Project{}, err
	}
	if to.ID == cur.OrgID {
		return cur, nil
	}
	if _, err := tx.Exec(`UPDATE projects SET org_id=? WHERE id=?`, to.ID, cur.ID); err != nil {
		return Project{}, err
	}
	for _, q := range []string{`DELETE FROM shared_projects WHERE project_id=?`, `DELETE FROM project_handover WHERE project_id=?`} {
		if _, err := tx.Exec(q, cur.ID); err != nil {
			return Project{}, err
		}
	}
	if err := dropProjectRolesTx(tx, by, RoleViaAPI,
		`p.remote=? AND NOT EXISTS(SELECT 1 FROM org_members om WHERE om.org_id=? AND om.account_id=m.account_id)`, cur.Remote, to.ID); err != nil {
		return Project{}, err
	}
	if porg, vis, ok := prefixOrgTx(tx, cur.Remote); ok && porg == to.ID && vis == VisibilityShared {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, cur.ID); err != nil {
			return Project{}, err
		}
	}
	id := cur.Remote
	if cur.Named && strings.HasPrefix(cur.Remote, cur.Org+"/") {
		id = to.Slug + cur.Remote[len(cur.Org):]
		if err := renameProjectTx(tx, cur.Remote, id); err != nil {
			return Project{}, fmt.Errorf("named project %s: %w", cur.Remote, err)
		}
	}
	if err := holdForeignEntriesTx(tx, id, to.ID, true); err != nil {
		return Project{}, err
	}
	for _, o := range []int64{cur.OrgID, to.ID} {
		if err := orgEvent(tx, o, "reassign_project", by, id, cur.Org+" -> "+to.Slug+" by the instance admin"); err != nil {
			return Project{}, err
		}
	}
	p, _ := projectTx(tx, id)
	return p, tx.Commit()
}

// ---------------------------------------------------------------- handover

const handoverSchema = `
CREATE TABLE IF NOT EXISTS project_handover(
  project_id INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  consented_by INTEGER NOT NULL, consented_at TEXT NOT NULL);
`

// HandoverProject is a project in a person's namespace that a connected
// organization's prefix covers: it waits for the organization to take it over.
type HandoverProject struct {
	Remote    string `json:"remote"`
	Owner     string `json:"owner"`     // the namespace holding it
	Org       string `json:"org"`       // the organization whose prefix covers it
	Consented bool   `json:"consented"` // the owner agreed
}

func pendingHandoversTx(q queryer, where string, args ...any) ([]HandoverProject, error) {
	rows, err := q.Query(`SELECT p.remote, ns.slug, op.slug, EXISTS(SELECT 1 FROM project_handover h WHERE h.project_id=p.id AND h.org_id=op.id)
		FROM projects p JOIN orgs ns ON ns.id=p.org_id AND ns.kind='user'
		JOIN org_prefixes pr ON substr(p.remote, 1, length(pr.prefix)+1) = pr.prefix || '/'
		JOIN orgs op ON op.id=pr.org_id
		WHERE p.archived_at='' AND `+where+` ORDER BY p.remote LIMIT ?`, append(args, maxListRows)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoverProject
	for rows.Next() {
		var h HandoverProject
		if err := rows.Scan(&h.Remote, &h.Owner, &h.Org, &h.Consented); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PendingTakeovers lists, for an owner of the organization, the projects in
// people's namespaces that its prefix covers.
func (s *Store) PendingTakeovers(viewerPrincipal string, orgID int64) ([]HandoverProject, error) {
	if s.reader != nil {
		return s.reader.PendingTakeovers(viewerPrincipal, orgID)
	}
	viewer, err := parsePersonPrincipalID(viewerPrincipal)
	if err != nil {
		return nil, err
	}
	if orgRoleTx(s.db, orgID, viewer) != OrgOwner {
		return nil, ErrNotOrgOwner
	}
	return pendingHandoversTx(s.db, `op.id=?`, orgID)
}

// PendingHandovers lists, for a person, their projects that a connected
// organization's prefix covers.
func (s *Store) PendingHandovers(ownerPrincipal string) ([]HandoverProject, error) {
	if s.reader != nil {
		return s.reader.PendingHandovers(ownerPrincipal)
	}
	owner, err := parsePersonPrincipalID(ownerPrincipal)
	if err != nil {
		return nil, err
	}
	return pendingHandoversTx(s.db, `ns.account_id=?`, owner)
}

// ConsentHandover is the owner's agreement that the organization whose prefix
// covers the project may take it over.
func (s *Store) ConsentHandover(ownerPrincipal, remote string) error {
	if s.writer != nil {
		return queueWrite(s, []any{ownerPrincipal, remote}, func(d *Store, p []any) error { return d.ConsentHandover(p[0].(string), p[1].(string)) })
	}
	owner, err := parsePersonPrincipalID(ownerPrincipal)
	if err != nil {
		return err
	}
	list, err := pendingHandoversTx(s.db, `ns.account_id=? AND p.remote=?`, owner, remote)
	if err != nil {
		return err
	}
	if len(list) != 1 {
		return ErrProjectNotFound
	}
	o, err := orgByRefTx(s.db, list[0].Org)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO project_handover(project_id, org_id, consented_by, consented_at) SELECT id, ?, ?, ? FROM projects WHERE remote=?`,
		o.ID, owner, now(), remote)
	return err
}

// TakeOverProject moves a waiting project into the organization: an owner of
// the organization with the former owner's agreement, or the instance admin.
// The former owner keeps the project as its owner (a guest of the organization
// if not a member), and the prefix's visibility applies.
func (s *Store) TakeOverProject(actorPrincipal, remote string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, remote}, func(d *Store, p []any) (Project, error) {
			return d.TakeOverProject(p[0].(string), p[1].(string))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return Project{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	list, err := pendingHandoversTx(tx, `p.remote=?`, remote)
	if err != nil {
		return Project{}, err
	}
	if len(list) != 1 {
		return Project{}, ErrProjectNotFound
	}
	h := list[0]
	org, err := orgByRefTx(tx, h.Org)
	if err != nil {
		return Project{}, err
	}
	var admin int
	_ = tx.QueryRow(`SELECT is_admin FROM persons WHERE id=?`, actor).Scan(&admin)
	if admin != 1 {
		if orgRoleTx(tx, org.ID, actor) != OrgOwner {
			return Project{}, ErrNotOrgOwner
		}
		if !h.Consented {
			return Project{}, ErrNoHandover
		}
	}
	p, _ := projectTx(tx, remote)
	ns, err := orgByIDTx(tx, p.OrgID)
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.Exec(`UPDATE projects SET org_id=? WHERE id=?`, org.ID, p.ID); err != nil {
		return Project{}, err
	}
	if _, vis, ok := prefixOrgTx(tx, remote); ok && vis == VisibilityShared {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, p.ID); err != nil {
			return Project{}, err
		}
	}
	former := ns.AccountID
	if orgRoleTx(tx, org.ID, former) == "" {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'member',?)`, org.ID, former, now()); err != nil {
			return Project{}, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_guests(org_id, account_id) VALUES(?,?)`, org.ID, former); err != nil {
			return Project{}, err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) VALUES(?,?,?,0,?,?)`,
		p.ID, former, RoleOwner, actor, now()); err != nil {
		return Project{}, err
	}
	if _, err := tx.Exec(`DELETE FROM project_handover WHERE project_id=?`, p.ID); err != nil {
		return Project{}, err
	}
	how := "with the owner's agreement"
	if admin == 1 && !h.Consented {
		how = "by the instance admin"
	}
	if err := holdForeignEntriesTx(tx, remote, org.ID, true); err != nil {
		return Project{}, err
	}
	if err := orgEvent(tx, org.ID, "take_over", principalOfID(actor), remote, "from "+ns.Slug+", "+how); err != nil {
		return Project{}, err
	}
	p, _ = projectTx(tx, remote)
	return p, tx.Commit()
}
