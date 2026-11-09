package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Owners as on GitHub (REQ-623). Every project belongs to exactly one owner: an
// organization or a person. A person's own namespace is an orgs row of kind
// "user" with that person as its only owner, so projects.org_id stays the one
// owner pointer and ProjectAccess needs no second rule: the owner of a
// namespace is the implicit owner of everything in it. Collaborators on a
// personal project are members flagged in org_guests, exactly like guests of
// an organization, so they see only the projects they were given.
//
// Accounts have a kind: a person signs in, a service (the gardener, an
// operations runner, CI) only holds tokens. A service never gets a role through
// "shared with the org"; it reads and writes only where a project names it.
const (
	OrgKindOrg  = "org"
	OrgKindUser = "user"

	AccountKindPerson  = "person"
	AccountKindService = "service"
)

var (
	// ErrUserNamespace: personal namespaces have no members, invitations,
	// sources or prefixes; they are shared project by project.
	ErrUserNamespace = errors.New("this is a personal namespace: share single projects instead")
	// ErrInvalidRemote: the remote names no shared repository (a local path, a
	// temporary checkout, a reserved test host).
	ErrInvalidRemote = errors.New("that remote names no shared repository (a local path, a temporary checkout or a reserved test host)")
	// ErrProjectArchived: the project is archived and takes no new writes.
	ErrProjectArchived = errors.New("project is archived")
	// ErrServiceAccount: services own nothing and create no projects.
	ErrServiceAccount = errors.New("a service account cannot create projects or namespaces")
	ErrAccountKind    = errors.New("account kind must be person or service")
)

const ownerNamespaceSchema = `
CREATE TABLE IF NOT EXISTS org_slug_history(
  old_slug TEXT PRIMARY KEY,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  changed_at TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS org_slug_history_org ON org_slug_history(org_id);
`

// ensureOwnerNamespaces adds the owner model additively: new tables are empty,
// new columns carry constant defaults (every existing org is an "org", every
// account a "person", nothing archived), and no row is rewritten. A binary
// from before ignores the columns.
func ensureOwnerNamespaces(db *sql.DB) error {
	if _, err := db.Exec(ownerNamespaceSchema + handoverSchema + heldSchema + slugNoticeSchema); err != nil {
		return err
	}
	for _, c := range []struct{ table, column, ddl string }{
		{"orgs", "kind", `TEXT NOT NULL DEFAULT 'org'`},
		{"orgs", "account_id", `INTEGER NOT NULL DEFAULT 0`},
		{"orgs", "avatar_login", `TEXT NOT NULL DEFAULT ''`},
		{"orgs", "display_source", `TEXT NOT NULL DEFAULT ''`},
		{"projects", "archived_at", `TEXT NOT NULL DEFAULT ''`},
		{"projects", "archived_reason", `TEXT NOT NULL DEFAULT ''`},
		{"machines", "archived_at", `TEXT NOT NULL DEFAULT ''`},
	} {
		if err := addColumnIfMissing(db, c.table, c.column, c.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	for _, c := range []struct{ name, ddl string }{
		{"kind", `TEXT NOT NULL DEFAULT 'person'`},
		{"service_org_id", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := ensurePersonsColumn(db, c.name, c.ddl); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS orgs_user_account ON orgs(account_id) WHERE kind='user'`); err != nil {
		return err
	}
	if err := ensureGitHubIDUnique(db); err != nil {
		return err
	}
	return backfillDisplaySource(db)
}

// displaySourceBackfillKey marks that the organizations from before the owner
// model were set to display_source 'user' once.
const displaySourceBackfillKey = "owner_display_source_backfill"

// backfillDisplaySource sets every organization that exists when the owner
// model arrives to display_source 'user': its name and address stay what
// people chose, and only the picture follows GitHub until an owner opts in.
// It runs once; organizations created later keep ” until a choice is made.
func backfillDisplaySource(db *sql.DB) error {
	var done string
	if db.QueryRow(`SELECT value FROM org_state WHERE key=?`, displaySourceBackfillKey).Scan(&done) == nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE orgs SET display_source='user' WHERE display_source='' AND kind<>'user'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_state(key, value) VALUES(?, '1')`, displaySourceBackfillKey); err != nil {
		return err
	}
	return tx.Commit()
}

// orgCols and scanOrg are the one way an orgs row is read.
const orgCols = `id, slug, name, created_at, kind, account_id, avatar_login, display_source`

func scanOrg(r rowScanner, o *Org) error {
	return r.Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt, &o.Kind, &o.AccountID, &o.AvatarLogin, &o.DisplaySource)
}

// IsUser: the org row is a person's own namespace.
func (o Org) IsUser() bool { return o.Kind == OrgKindUser }

func accountKindTx(q rowQuerier, account int64) string {
	var kind string
	if q.QueryRow(`SELECT kind FROM persons WHERE id=?`, account).Scan(&kind) != nil || kind == "" {
		return AccountKindPerson
	}
	return kind
}

func isServiceTx(q rowQuerier, account int64) bool {
	return accountKindTx(q, account) == AccountKindService
}

func orgIsUserTx(q rowQuerier, orgID int64) bool {
	var kind string
	_ = q.QueryRow(`SELECT kind FROM orgs WHERE id=?`, orgID).Scan(&kind)
	return kind == OrgKindUser
}

// slugTakenTx: an org has the slug now or had it before. Old slugs are never
// handed out again, so nobody can take over an address that still redirects.
func slugTakenTx(q rowQuerier, slug string, except int64) bool {
	var n int
	_ = q.QueryRow(`SELECT (SELECT COUNT(*) FROM orgs WHERE slug=? AND id<>?) + (SELECT COUNT(*) FROM org_slug_history WHERE old_slug=? AND org_id<>?)`,
		slug, except, slug, except).Scan(&n)
	return n > 0 || slugReservedTx(q, slug, except)
}

func userNamespaceTx(q rowQuerier, account int64) (Org, bool) {
	var o Org
	if scanOrg(q.QueryRow(`SELECT `+orgCols+` FROM orgs WHERE kind='user' AND account_id=?`, account), &o) != nil {
		return Org{}, false
	}
	return o, true
}

// namespaceSlugTx picks the address of a new personal namespace: the linked
// GitHub login, else the account name, else user-<id>; a taken one gets -2 to
// -9 appended.
func namespaceSlugTx(q rowQuerier, account int64, name string) string {
	var bases []string
	var login string
	var ghID int64
	if q.QueryRow(`SELECT login, github_id FROM account_github WHERE account_id=?`, account).Scan(&login, &ghID) == nil {
		login = strings.ToLower(strings.TrimSpace(login))
		// The person's own verified login is no reservation against them.
		if ghID > 0 && slugPattern.MatchString(login) && !slugHeldTx(q, login) && !slugReservedForOthersTx(q, login, account) {
			return login
		}
		bases = append(bases, login)
	}
	bases = append(bases, slugFromName(name), "user-"+strconv.FormatInt(account, 10))
	for _, base := range bases {
		if !slugPattern.MatchString(base) {
			continue
		}
		for i := 1; i <= 9; i++ {
			slug := base
			if i > 1 {
				slug = fmt.Sprintf("%s-%d", base, i)
			}
			if slugPattern.MatchString(slug) && !slugTakenTx(q, slug, 0) {
				return slug
			}
		}
	}
	return "user-" + strconv.FormatInt(account, 10)
}

// ensureUserNamespaceTx returns the personal namespace of a person and creates
// it the first time it is needed. Services have none.
func ensureUserNamespaceTx(tx execQueryer, account int64) (Org, error) {
	if o, ok := userNamespaceTx(tx, account); ok {
		return o, nil
	}
	if isServiceTx(tx, account) {
		return Org{}, ErrServiceAccount
	}
	// A guest stays a guest: a namespace of its own would make it an owner
	// (security review, finding 1).
	if guestOnlyTx(tx, account) {
		return Org{}, ErrGuestOnly
	}
	var name string
	if err := tx.QueryRow(`SELECT name FROM persons WHERE id=?`, account).Scan(&name); err != nil {
		return Org{}, errAccountNotFound
	}
	slug := namespaceSlugTx(tx, account, name)
	res, err := tx.Exec(`INSERT INTO orgs(slug, name, created_at, kind, account_id) VALUES(?,?,?,?,?)`, slug, name, now(), OrgKindUser, account)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Org{}, ErrOrgSlugTaken
		}
		return Org{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'owner',?)`, id, account, now()); err != nil {
		return Org{}, err
	}
	if err := orgEvent(tx, id, "create_namespace", principalOfID(account), slug, name); err != nil {
		return Org{}, err
	}
	return orgByIDTx(tx, id)
}

// UserNamespace is the personal namespace of an account, if it has one yet.
func (s *Store) UserNamespace(accountPrincipal string) (Org, bool) {
	if s.reader != nil {
		return s.reader.UserNamespace(accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return Org{}, false
	}
	return userNamespaceTx(s.db, id)
}

// EnsureUserNamespace returns the personal namespace of a person, creating it
// on first use.
func (s *Store) EnsureUserNamespace(accountPrincipal string) (Org, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal}, func(d *Store, p []any) (Org, error) {
			return d.EnsureUserNamespace(p[0].(string))
		})
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return Org{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback()
	o, err := ensureUserNamespaceTx(tx, id)
	if err != nil {
		return Org{}, err
	}
	return o, tx.Commit()
}

// OrgSlugTarget follows a former slug to the current one. ok is false when the
// slug was never moved.
func (s *Store) OrgSlugTarget(slug string) (string, bool) {
	if s.reader != nil {
		return s.reader.OrgSlugTarget(slug)
	}
	var cur string
	err := s.db.QueryRow(`SELECT o.slug FROM org_slug_history h JOIN orgs o ON o.id = h.org_id WHERE h.old_slug=?`,
		strings.ToLower(strings.TrimSpace(slug))).Scan(&cur)
	return cur, err == nil
}

// AllOwners lists every organization and person's namespace, for the
// instance admin's choice of a project's owner.
func (s *Store) AllOwners() ([]Org, error) {
	if s.reader != nil {
		return s.reader.AllOwners()
	}
	rows, err := s.db.Query(`SELECT `+orgCols+` FROM orgs ORDER BY kind DESC, slug LIMIT ?`, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		if err := scanOrg(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Display sources of an organization: whether its name and address follow the
// connected GitHub organization or stay what an owner chose.
const (
	DisplayGitHub = "github"
	DisplayUser   = "user"
)

// SetOrgFollowGitHub lets an owner decide whether name and address of the
// organization follow its GitHub organization. Switching it on changes nothing
// by itself; the next mirror (on connect, the daily run, or right after this
// choice in the web) takes the name and address over. Off keeps what is there.
func (s *Store) SetOrgFollowGitHub(actorPrincipal string, orgID int64, follow bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, follow}, func(d *Store, p []any) error {
			return d.SetOrgFollowGitHub(p[0].(string), p[1].(int64), p[2].(bool))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	cur, err := orgByIDTx(tx, orgID)
	if err != nil {
		return err
	}
	if cur.IsUser() {
		return ErrUserNamespace
	}
	src := DisplayUser
	if follow {
		src = DisplayGitHub
	}
	if _, err := tx.Exec(`UPDATE orgs SET display_source=? WHERE id=?`, src, orgID); err != nil {
		return err
	}
	if err := orgEvent(tx, orgID, "display_source", principalOfID(actor), cur.Slug, cur.DisplaySource+" -> "+src); err != nil {
		return err
	}
	return tx.Commit()
}

// MovedProject follows a named project id of a former address ("<old
// slug>/<name>") to the id it has now. ok is false for every id that is a
// project itself, has no former slug in front, or whose project is gone, so a
// caller may pass any id.
func (s *Store) MovedProject(id string) (string, bool) {
	if s.reader != nil {
		return s.reader.MovedProject(id)
	}
	first, rest, found := strings.Cut(strings.TrimSpace(id), "/")
	first = strings.ToLower(first)
	if !found || rest == "" || !slugPattern.MatchString(first) {
		return "", false
	}
	var cur string
	err := s.db.QueryRow(`SELECT p.remote FROM org_slug_history h JOIN orgs o ON o.id=h.org_id
		JOIN projects p ON p.remote = o.slug || '/' || ? AND p.org_id = o.id
		WHERE h.old_slug=? AND NOT EXISTS (SELECT 1 FROM projects WHERE remote=?)`, rest, first, id).Scan(&cur)
	return cur, err == nil && cur != id
}

// AccountKind is "person" or "service".
func (s *Store) AccountKind(accountPrincipal string) string {
	if s.reader != nil {
		return s.reader.AccountKind(accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return AccountKindPerson
	}
	return accountKindTx(s.db, id)
}

// SetAccountKindAsOperator turns an account into a service or back into a
// person. Like the gardener setting it is reachable only with the database in
// hand (`ctx account set-kind --db`), never over HTTP: a token holder must not
// be able to change what an account is. serviceOrg names the organization a
// service works for (0 = the instance).
func (s *Store) SetAccountKindAsOperator(accountID int64, kind string, serviceOrg int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountID, kind, serviceOrg}, func(d *Store, p []any) error {
			return d.SetAccountKindAsOperator(p[0].(int64), p[1].(string), p[2].(int64))
		})
	}
	if kind != AccountKindPerson && kind != AccountKindService {
		return ErrAccountKind
	}
	if kind == AccountKindPerson {
		serviceOrg = 0
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow(`SELECT kind FROM persons WHERE id=?`, accountID).Scan(&old); err != nil {
		return errAccountNotFound
	}
	if kind == AccountKindService {
		if _, ok := userNamespaceTx(tx, accountID); ok {
			return fmt.Errorf("%w: the account owns a personal namespace", ErrServiceAccount)
		}
		var owner int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM org_members WHERE account_id=? AND role='owner'`, accountID).Scan(&owner)
		if owner > 0 {
			return fmt.Errorf("%w: the account owns an organization", ErrServiceAccount)
		}
		if serviceOrg > 0 {
			if _, err := orgByIDTx(tx, serviceOrg); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE persons SET kind=?, service_org_id=? WHERE id=?`, kind, serviceOrg, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO role_events(scope, subject, old_role, new_role, actor, via, created_at) VALUES('account',?,?,?,'',?,?)`,
		principalOfID(accountID), "kind:"+old, "kind:"+kind, RoleViaCLIDB, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ServiceInfo is a service account as an organization shows it: what it may
// touch (only its explicit project roles) and when its tokens were last used.
type ServiceInfo struct {
	AccountID string            `json:"account_id"`
	Name      string            `json:"name"`
	OfOrg     bool              `json:"of_org"` // works for this organization (else for the instance)
	Gardener  bool              `json:"gardener,omitempty"`
	Roles     map[string]string `json:"roles"` // remote -> role, projects of this org only
	LastUsed  string            `json:"last_used,omitempty"`
}

// Viewer kinds of an organization (decision 14 A of #4376): guests see only
// their project, never the organization's pages.
const (
	OrgViewerOwner  = "owner"
	OrgViewerMember = "member"
	OrgViewerGuest  = "guest"
)

// orgViewerKindTx says how an account stands in an organization: owner,
// member, guest or "" (not in it). A guest came in through a guest invitation
// (org_guests), or holds nothing but guest roles in the organization's
// projects. A member without any project role is still a member.
func orgViewerKindTx(q queryer, orgID, account int64) string {
	switch role := orgRoleTx(q, orgID, account); {
	case role == "":
		return ""
	case role == OrgOwner:
		return OrgViewerOwner
	case orgGuestTx(q, orgID, account):
		return OrgViewerGuest
	}
	var guestOnly, above int
	_ = q.QueryRow(`SELECT COALESCE(SUM(pm.role='guest'),0), COALESCE(SUM(pm.role<>'guest'),0) FROM project_members pm
		JOIN projects p ON p.id=pm.project_id WHERE p.org_id=? AND pm.account_id=?`, orgID, account).Scan(&guestOnly, &above)
	if guestOnly > 0 && above == 0 {
		// A shared project gives the default role, which is more than guest.
		var shared int
		_ = q.QueryRow(`SELECT COUNT(*) FROM shared_projects sp JOIN projects p ON p.id=sp.project_id
			WHERE p.org_id=? AND NOT EXISTS(SELECT 1 FROM project_members pm WHERE pm.project_id=p.id AND pm.account_id=?)`, orgID, account).Scan(&shared)
		if shared == 0 || isServiceTx(q, account) {
			return OrgViewerGuest
		}
	}
	return OrgViewerMember
}

// worksInOrgTx: owner or member, not a guest.
func worksInOrgTx(q queryer, orgID, account int64) bool {
	k := orgViewerKindTx(q, orgID, account)
	return k == OrgViewerOwner || k == OrgViewerMember
}

// OrgViewerKind is orgViewerKindTx for a principal.
func (s *Store) OrgViewerKind(orgID int64, accountPrincipal string) string {
	if s.reader != nil {
		return s.reader.OrgViewerKind(orgID, accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return ""
	}
	return orgViewerKindTx(s.db, orgID, id)
}

// OrgServices lists the services of an organization: service accounts that
// are its members or work for it, with their explicit roles in its projects.
// Members see it; guests and outsiders get nothing (ErrNotOrgMember).
func (s *Store) OrgServices(viewerPrincipal string, orgID int64) ([]ServiceInfo, error) {
	if s.reader != nil {
		return s.reader.OrgServices(viewerPrincipal, orgID)
	}
	viewer, err := parsePersonPrincipalID(viewerPrincipal)
	if err != nil {
		return nil, err
	}
	if !worksInOrgTx(s.db, orgID, viewer) {
		return nil, ErrNotOrgMember
	}
	rows, err := s.db.Query(`SELECT p.id, p.name, p.service_org_id,
			COALESCE((SELECT MAX(t.last_used_at) FROM api_tokens t WHERE t.account_id=p.id AND t.revoked_at=''), '')
		FROM persons p WHERE p.kind='service'
			AND (p.service_org_id=? OR EXISTS(SELECT 1 FROM org_members m WHERE m.org_id=? AND m.account_id=p.id))
		ORDER BY p.name LIMIT ?`, orgID, orgID, maxListRows)
	if err != nil {
		return nil, err
	}
	var out []ServiceInfo
	var ids []int64
	for rows.Next() {
		var id, ofOrg int64
		var si ServiceInfo
		if err := rows.Scan(&id, &si.Name, &ofOrg, &si.LastUsed); err != nil {
			rows.Close()
			return nil, err
		}
		si.AccountID, si.OfOrg, si.Roles = principalOfID(id), ofOrg == orgID, map[string]string{}
		out, ids = append(out, si), append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	gardener := int64(0)
	var v string
	if s.db.QueryRow(`SELECT value FROM org_state WHERE key=?`, gardenerAccountKey).Scan(&v) == nil {
		gardener, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	}
	for i, id := range ids {
		out[i].Gardener = id == gardener
		rr, err := s.db.Query(`SELECT pr.remote, pm.role FROM project_members pm JOIN projects pr ON pr.id=pm.project_id
			WHERE pm.account_id=? AND pr.org_id=? ORDER BY pr.remote LIMIT ?`, id, orgID, maxListRows)
		if err != nil {
			return nil, err
		}
		var found [][2]string
		for rr.Next() {
			var remote, role string
			if rr.Scan(&remote, &role) == nil {
				found = append(found, [2]string{remote, role})
			}
		}
		rr.Close()
		// Only projects the viewer may see: a restricted project's name must
		// not leak through the role of a service.
		for _, f := range found {
			if projectRoleTx(s.db, f[0], viewer).Role != "" {
				out[i].Roles[f[0]] = f[1]
			}
		}
	}
	// A service that works for the instance and has no visible role here is
	// none of the viewer's business. The gardener is no service people set up
	// but a built-in feature (Robin's decision E6): it shows only on the
	// Gardener tab, while its account stays a service.
	visible := out[:0]
	for _, si := range out {
		if si.Gardener {
			continue
		}
		if si.OfOrg || len(si.Roles) > 0 {
			visible = append(visible, si)
		}
	}
	return visible, nil
}

// MirrorGitHubOrg takes over what the connected GitHub organization shows of
// itself (REQ-623): the picture always; the display name and the address only
// after an owner opted in (display_source "github", see SetOrgFollowGitHub). The address
// follows the GitHub login: the former slug is recorded (the web redirects it)
// and the named projects move along. sourceLogin names the connected source
// the profile was read for; a profile of another login changes nothing but the
// event log, since remotes and prefixes still carry the old login. No person
// acts here: the server calls it after connecting and on GitHub's webhook.
func (s *Store) MirrorGitHubOrg(orgID int64, sourceLogin, profileLogin, name string) (Org, error) {
	if s.writer != nil {
		return queueValue(s, []any{orgID, sourceLogin, profileLogin, name}, func(d *Store, p []any) (Org, error) {
			return d.MirrorGitHubOrg(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	src := strings.ToLower(strings.TrimSpace(sourceLogin))
	login := strings.TrimSpace(profileLogin)
	name = strings.TrimSpace(name)
	if !ValidGitHubLogin(login) || len(name) > maxOrgNameLen || hasControlRunes(name) {
		return Org{}, fmt.Errorf("%w: not a GitHub organization profile", ErrInvalidInput)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback()
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM git_sources WHERE org_id=? AND kind=? AND login=?`, orgID, SourceGitHub, src).Scan(&n)
	if n == 0 {
		return Org{}, ErrSourceNotFound
	}
	cur, err := orgByIDTx(tx, orgID)
	if err != nil {
		return Org{}, err
	}
	if cur.IsUser() {
		return Org{}, ErrUserNamespace
	}
	if !strings.EqualFold(login, src) {
		// GitHub renamed the organization. Remotes and the prefix still name the
		// old login; moving them is a migration of its own, not a side effect.
		if err := orgEvent(tx, orgID, "github_login_changed", "", login, src+" -> "+login); err != nil {
			return Org{}, err
		}
		return cur, tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE orgs SET avatar_login=? WHERE id=?`, login, orgID); err != nil {
		return Org{}, err
	}
	if cur.DisplaySource == DisplayGitHub {
		if name == "" {
			name = login
		}
		if _, err := tx.Exec(`UPDATE orgs SET name=?, display_source='github' WHERE id=?`, name, orgID); err != nil {
			return Org{}, err
		}
		if slug := strings.ToLower(login); slug != cur.Slug && slugPattern.MatchString(slug) && !slugTakenTx(tx, slug, orgID) {
			if _, err := tx.Exec(`UPDATE orgs SET slug=? WHERE id=?`, slug, orgID); err != nil {
				return Org{}, err
			}
			if err := moveSlugTx(tx, orgID, cur.Slug, slug); err != nil {
				return Org{}, err
			}
		}
		if name != cur.Name {
			if err := orgEvent(tx, orgID, "mirror_github", "", login, cur.Name+" -> "+name); err != nil {
				return Org{}, err
			}
		}
	}
	o, err := orgByIDTx(tx, orgID)
	if err != nil {
		return Org{}, err
	}
	return o, tx.Commit()
}

// OrgAvatarLogin is the GitHub login whose picture the organization shows.
func (s *Store) OrgAvatarLogin(orgID int64) (string, bool) {
	if s.reader != nil {
		return s.reader.OrgAvatarLogin(orgID)
	}
	var login string
	if s.db.QueryRow(`SELECT avatar_login FROM orgs WHERE id=?`, orgID).Scan(&login) != nil || !ValidGitHubLogin(login) {
		return "", false
	}
	return login, true
}

// SourceByInstallation names the organization and login a GitHub App
// installation was connected for.
func (s *Store) SourceByInstallation(installationID int64) (orgID int64, login string, ok bool) {
	if s.reader != nil {
		return s.reader.SourceByInstallation(installationID)
	}
	if installationID <= 0 {
		return 0, "", false
	}
	err := s.db.QueryRow(`SELECT org_id, login FROM git_sources WHERE installation_id=? AND kind=?`, installationID, SourceGitHub).Scan(&orgID, &login)
	return orgID, login, err == nil
}

// GitHubInstallation is a connected GitHub organization with the installation
// of the App that proves it.
type GitHubInstallation struct {
	OrgID          int64
	Login          string
	InstallationID int64
}

// GitHubInstallations lists every connected GitHub organization with an App
// installation, for the server's daily mirroring of names and pictures.
func (s *Store) GitHubInstallations() ([]GitHubInstallation, error) {
	if s.reader != nil {
		return s.reader.GitHubInstallations()
	}
	rows, err := s.db.Query(`SELECT org_id, login, installation_id FROM git_sources WHERE kind=? AND installation_id>0 ORDER BY id LIMIT ?`, SourceGitHub, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubInstallation
	for rows.Next() {
		var g GitHubInstallation
		if err := rows.Scan(&g.OrgID, &g.Login, &g.InstallationID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ProjectCollaborators counts who besides the owner of a personal namespace
// holds a role in one of its projects ("shared with N").
func (s *Store) ProjectCollaborators(remote string) int {
	if s.reader != nil {
		return s.reader.ProjectCollaborators(remote)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM project_members pm JOIN projects p ON p.id=pm.project_id JOIN orgs o ON o.id=p.org_id
		WHERE p.remote=? AND o.kind='user' AND pm.account_id<>o.account_id`, remote).Scan(&n)
	return n
}
