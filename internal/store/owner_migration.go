package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Migration of an existing instance to the owner model (REQ-623), for the
// operator with the database in hand (`ctx db migrate-owners`). Everything runs
// in one transaction: a snapshot of who sees which project before, the steps,
// the same snapshot after, and the comparison. Only two deviations are
// allowed: a service loses what "shared with the org" gave it, and a person
// gains projects in their own namespace. Anything else rolls the whole
// migration back. A dry run is the same transaction, always rolled back.
//
// Every choice an operator may want to make differently is a field of the plan;
// the zero plan follows the recommendations of the concept: what a claimed
// prefix covers stays in the organization, everything else goes to the person
// who used it first, throwaway remotes are archived.

// OwnerMigrationPlan says what the migration does beyond the rules.
type OwnerMigrationPlan struct {
	// Org is the organization that holds today's projects; "" means the one
	// organization with a connected GitHub source.
	Org string `json:"org,omitempty"`
	// OrgSlug and OrgName are its new address and name; an empty slug takes the
	// login of its GitHub source, an empty name keeps the name.
	OrgSlug string `json:"org_slug,omitempty"`
	OrgName string `json:"org_name,omitempty"`
	// FollowGitHub lets name and address of the organization follow its GitHub
	// organization from now on (display_source 'github'); without it they stay
	// what the plan sets (display_source 'user').
	FollowGitHub bool `json:"follow_github,omitempty"`
	// DefaultOwner (name or person:N) gets what nobody else used; "" is the
	// instance owner.
	DefaultOwner string `json:"default_owner,omitempty"`
	// Keep are remote prefixes that stay in the organization although no
	// claimed prefix covers them.
	Keep []string `json:"keep,omitempty"`
	// To sends remotes under a prefix to the namespace of an account (name or
	// person:N), whoever used them first, or to an organization (its slug; the
	// migrated organization also by the address it gets): there it is shared
	// with the members, as an organization's repository is.
	To map[string]string `json:"to,omitempty"`
	// Merge moves the entries of one project into another and archives the
	// empty one; Rename gives a project a new id. Both run after the moves.
	Merge  map[string]string `json:"merge,omitempty"`
	Rename map[string]string `json:"rename,omitempty"`
	// Archive are remote prefixes or ids to archive besides the throwaway
	// remotes; ArchiveOrgs archives every project of these organizations.
	Archive     []string `json:"archive,omitempty"`
	ArchiveOrgs []string `json:"archive_orgs,omitempty"`
	// ArchiveMachines, DisableAccounts and Services (account -> organization
	// slug, "" for the instance) round it off.
	ArchiveMachines []string          `json:"archive_machines,omitempty"`
	DisableAccounts []string          `json:"disable_accounts,omitempty"`
	Services        map[string]string `json:"services,omitempty"`
	// SkipUnclaimed leaves remotes without a project row as they are; by
	// default each becomes a project of the person who used it first.
	SkipUnclaimed bool `json:"skip_unclaimed,omitempty"`
	// SkipSessions leaves sessions without an account alone; by default they
	// are stamped with the default owner.
	SkipSessions bool `json:"skip_sessions,omitempty"`
}

// OwnerMigrationReport is what happened (or would happen).
type OwnerMigrationReport struct {
	Org           string `json:"org"`
	OrgMoved      string `json:"org_moved,omitempty"`
	DisplaySource string `json:"display_source,omitempty"`
	// Held counts the entries of other accounts held back in the projects the
	// migration creates (see held_entries.go).
	Held             int                 `json:"held"`
	Namespaces       []string            `json:"namespaces"`
	Moved            map[string][]string `json:"moved"`
	Claimed          map[string][]string `json:"claimed"`
	Stayed           int                 `json:"stayed"`
	Renamed          []string            `json:"renamed"`
	Merged           []string            `json:"merged"`
	Archived         []string            `json:"archived"`
	ArchivedMachines []string            `json:"archived_machines"`
	Disabled         []string            `json:"disabled"`
	Services         []string            `json:"services"`
	SessionsStamped  int64               `json:"sessions_stamped"`
	Gained           map[string][]string `json:"gained"`
	// GainedInOrg are gains through the plan's "to": projects given to an
	// organization, shared with its members.
	GainedInOrg map[string][]string `json:"gained_in_org,omitempty"`
	Lost        map[string][]string `json:"lost"`
	Unexpected  []string            `json:"unexpected"`
	// Assigned are the remotes the plan's "to" gives to an organization; who
	// gains them through that is no deviation.
	Assigned []string `json:"assigned,omitempty"`
	// Contested are remotes several people wrote to: they stay where they are
	// (in the organization, or without a project row) until the plan says
	// whose they are ("to").
	Contested []string `json:"contested,omitempty"`
	Applied   bool     `json:"applied"`
}

var (
	// ErrMigrationDeviates: the snapshot after differs from before in a way
	// the migration may not cause; nothing was changed.
	ErrMigrationDeviates = errors.New("the migration would change who sees what beyond the allowed deviations; nothing was changed")
	// ErrOperatorOnly: only for a store opened directly on the database file.
	ErrOperatorOnly = errors.New("only with the database in hand (ctx db ... --db), not on a running server's store")
)

// visibility is account -> project -> role.
type visibility map[int64]map[string]string

func visibilityTx(q queryer) (visibility, error) {
	out := visibility{}
	rows, err := q.Query(`SELECT p.remote, om.account_id, om.role, COALESCE(pm.role,''), COALESCE(pm.can_review,0),
			EXISTS(SELECT 1 FROM shared_projects sp WHERE sp.project_id=p.id)
			AND NOT EXISTS(SELECT 1 FROM org_guests og WHERE og.org_id=p.org_id AND og.account_id=om.account_id)
			AND NOT EXISTS(SELECT 1 FROM persons ps WHERE ps.id=om.account_id AND ps.kind='service')
		FROM projects p
		JOIN org_members om ON om.org_id=p.org_id
		LEFT JOIN project_members pm ON pm.project_id=p.id AND pm.account_id=om.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var remote, orgRole, role string
		var acct int64
		var review int
		var share bool
		if err := rows.Scan(&remote, &acct, &orgRole, &role, &review, &share); err != nil {
			return nil, err
		}
		if r := deriveRole(orgRole, role, review == 1, share).Role; r != "" {
			if out[acct] == nil {
				out[acct] = map[string]string{}
			}
			out[acct][remote] = r
		}
	}
	return out, rows.Err()
}

func accountRefTx(q rowQuerier, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if id, err := parsePersonPrincipalID(ref); err == nil {
		return id, nil
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	var id int64
	if err := q.QueryRow(`SELECT id FROM persons WHERE name=?`, ref).Scan(&id); err != nil {
		return 0, fmt.Errorf("account %q: %w", ref, errAccountNotFound)
	}
	return id, nil
}

func underPrefix(remote, prefix string) bool {
	prefix = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(prefix), "*"), "/")
	return prefix != "" && (remote == prefix || strings.HasPrefix(remote, prefix+"/"))
}

func anyPrefix(remote string, prefixes []string) bool {
	for _, p := range prefixes {
		if underPrefix(remote, p) {
			return true
		}
	}
	return false
}

// usersTx are the people whose entries of any kind name a project: sessions by
// the account the server stamped on them (never a time the client sent;
// sessions without an account are the instance owner's), knowledge and
// requests by the author account recorded at writing, and the other author
// tables by the name of the moment, resolved as everywhere else (nameIndex:
// the current name first, a former one fills gaps, an unknown name is the
// instance owner's). Services and disabled accounts are no users. ambiguous
// is true when an author cannot be told apart (a name two accounts had, or an
// entry recorded without owner): such a remote is contested.
func usersTx(q queryer, remote string, instanceOwner int64, byName map[string]int64) (users []int64, ambiguous bool) {
	seen := map[int64]bool{}
	add := func(id int64) {
		switch {
		case id < 0:
			ambiguous = true
		case id > 0:
			seen[id] = true
		}
	}
	byAuthor := func(name string) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			return
		}
		id, known := byName[key]
		if !known {
			id = instanceOwner
		}
		add(id)
	}
	if rows, err := q.Query(`SELECT DISTINCT account_id FROM sessions WHERE project=?`, remote); err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				add(effectiveOwner(id, instanceOwner))
			}
		}
		rows.Close()
	}
	recorded := map[string]string{"knowledge": "knowledge_authors a ON a.knowledge_id=t.id", "requests": "request_authors a ON a.request_id=t.id"}
	for _, t := range authorTables {
		query := `SELECT DISTINCT -2, person FROM ` + t + ` t WHERE project=? AND person<>''`
		if join, ok := recorded[t]; ok {
			query = `SELECT DISTINCT COALESCE(a.account_id, -2), t.person FROM ` + t + ` t LEFT JOIN ` + join + ` WHERE t.project=?`
		}
		r, err := q.Query(query, remote)
		if err != nil {
			continue
		}
		for r.Next() {
			var id int64
			var name string
			if r.Scan(&id, &name) != nil {
				continue
			}
			switch id {
			case -2:
				byAuthor(name)
			case 0:
				ambiguous = true // recorded without owner: the name fit two accounts
			default:
				add(id)
			}
		}
		r.Close()
	}
	for id := range seen {
		var kind, state string
		if q.QueryRow(`SELECT kind, state FROM persons WHERE id=?`, id).Scan(&kind, &state) != nil || kind == AccountKindService || state == "disabled" {
			continue
		}
		users = append(users, id)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	return users, ambiguous
}

// MigrateOwners runs the migration; apply=false is the dry run.
func (s *Store) MigrateOwners(plan OwnerMigrationPlan, apply bool) (OwnerMigrationReport, error) {
	rep := OwnerMigrationReport{Moved: map[string][]string{}, Claimed: map[string][]string{}, Gained: map[string][]string{}, GainedInOrg: map[string][]string{}, Lost: map[string][]string{}}
	if s.writer != nil || s.reader != nil {
		return rep, ErrOperatorOnly
	}
	tx, err := s.db.Begin()
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()
	before, err := visibilityTx(tx)
	if err != nil {
		return rep, err
	}
	var heldBefore int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM held_entries`).Scan(&heldBefore)
	if err := migrateOwnersTx(tx, plan, &rep); err != nil {
		return rep, err
	}
	_ = tx.QueryRow(`SELECT COUNT(*) FROM held_entries`).Scan(&rep.Held)
	rep.Held -= heldBefore
	after, err := visibilityTx(tx)
	if err != nil {
		return rep, err
	}
	compareVisibility(tx, before, after, &rep)
	if len(rep.Unexpected) > 0 {
		return rep, ErrMigrationDeviates
	}
	if !apply {
		return rep, nil
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.Applied = true
	return rep, nil
}

func migrateOwnersTx(tx *sql.Tx, plan OwnerMigrationPlan, rep *OwnerMigrationReport) error {
	owner := instanceOwnerID(tx)
	if plan.DefaultOwner != "" {
		id, err := accountRefTx(tx, plan.DefaultOwner)
		if err != nil {
			return err
		}
		owner = id
	}
	if owner == 0 {
		return fmt.Errorf("no default owner: %w", errAccountNotFound)
	}
	// Sessions from before accounts belong to the instance owner (the rule
	// effectiveOwner applies everywhere else), never to the plan's default
	// owner. Stamping them first keeps an old repository from going to whoever
	// left the first session with an account.
	instanceOwner := instanceOwnerID(tx)
	if !plan.SkipSessions && instanceOwner > 0 {
		res, err := tx.Exec(`UPDATE sessions SET account_id=? WHERE account_id=0`, instanceOwner)
		if err != nil {
			return err
		}
		rep.SessionsStamped, _ = res.RowsAffected()
	}
	// Services and disabled accounts first: they decide who may own anything.
	names := make([]string, 0, len(plan.Services))
	for n := range plan.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		id, err := accountRefTx(tx, n)
		if err != nil {
			return err
		}
		// The organization a service works for is set at the end: the plan may
		// name it by the address it gets in this very migration.
		if _, err := tx.Exec(`UPDATE persons SET kind='service' WHERE id=?`, id); err != nil {
			return err
		}
		rep.Services = append(rep.Services, n)
	}
	for _, n := range plan.DisableAccounts {
		id, err := accountRefTx(tx, n)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE persons SET state='disabled' WHERE id=?`, id); err != nil {
			return err
		}
		rep.Disabled = append(rep.Disabled, n)
	}
	for _, m := range plan.ArchiveMachines {
		res, err := tx.Exec(`UPDATE machines SET archived_at=? WHERE hostname=? AND archived_at=''`, now(), m)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			rep.ArchivedMachines = append(rep.ArchivedMachines, m)
		}
	}
	// Archive: throwaway remotes, listed prefixes, whole test organizations.
	archiveOrgs := map[int64]bool{}
	for _, slug := range plan.ArchiveOrgs {
		o, err := orgByRefTx(tx, slug)
		if err != nil {
			return err
		}
		archiveOrgs[o.ID] = true
	}
	type proj struct {
		id, org int64
		remote  string
	}
	var all []proj
	rows, err := tx.Query(`SELECT id, org_id, remote FROM projects WHERE archived_at='' ORDER BY remote`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p proj
		if err := rows.Scan(&p.id, &p.org, &p.remote); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	rows.Close()
	live := all[:0]
	for _, p := range all {
		reason := ""
		switch {
		case scope.IsThrowawayRemote(p.remote):
			reason = "throwaway remote"
		case anyPrefix(p.remote, plan.Archive):
			reason = "listed for archiving"
		case archiveOrgs[p.org]:
			reason = "test organization"
		}
		if reason == "" {
			live = append(live, p)
			continue
		}
		if _, err := tx.Exec(`UPDATE projects SET archived_at=?, archived_reason=? WHERE id=?`, now(), "migrate-owners: "+reason, p.id); err != nil {
			return err
		}
		rep.Archived = append(rep.Archived, p.remote)
	}
	// The organization that holds today's projects.
	var org Org
	if plan.Org != "" {
		if org, err = orgByRefTx(tx, plan.Org); err != nil {
			return err
		}
	} else {
		var ids []int64
		r2, err := tx.Query(`SELECT DISTINCT org_id FROM git_sources WHERE kind=?`, SourceGitHub)
		if err != nil {
			return err
		}
		for r2.Next() {
			var id int64
			if r2.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		r2.Close()
		if len(ids) != 1 {
			return fmt.Errorf("%w: name the organization (--org), %d have a GitHub source", ErrInvalidInput, len(ids))
		}
		if org, err = orgByIDTx(tx, ids[0]); err != nil {
			return err
		}
	}
	rep.Org = org.Slug
	var prefixes []string
	r3, err := tx.Query(`SELECT prefix FROM org_prefixes WHERE org_id=?`, org.ID)
	if err != nil {
		return err
	}
	for r3.Next() {
		var p string
		if r3.Scan(&p) == nil {
			prefixes = append(prefixes, p)
		}
	}
	r3.Close()
	nsOf := func(acct int64) (Org, error) {
		ns, err := ensureUserNamespaceTx(tx, acct)
		if err == nil && !containsString(rep.Namespaces, ns.Slug) {
			rep.Namespaces = append(rep.Namespaces, ns.Slug)
		}
		return ns, err
	}
	// targetOf is whose a remote becomes: the plan's word, else the one person
	// who used it, else the default owner (who holds it today through the
	// organization). contested is true when several people used it.
	byName := nameIndex(tx)
	finalSlug := strings.ToLower(strings.TrimSpace(plan.OrgSlug))
	if finalSlug == "" {
		_ = tx.QueryRow(`SELECT login FROM git_sources WHERE org_id=? AND kind=? ORDER BY id LIMIT 1`, org.ID, SourceGitHub).Scan(&finalSlug)
	}
	// toOrg: the plan gives the remote to an organization.
	toOrg := func(remote string) (Org, bool, error) {
		best := ""
		for prefix := range plan.To {
			if underPrefix(remote, prefix) && len(prefix) > len(best) {
				best = prefix
			}
		}
		if best == "" {
			return Org{}, false, nil
		}
		v := plan.To[best]
		if _, err := accountRefTx(tx, v); err == nil {
			return Org{}, false, nil
		}
		if strings.EqualFold(v, org.Slug) || (finalSlug != "" && strings.EqualFold(v, finalSlug)) {
			return org, true, nil
		}
		o, err := orgByRefTx(tx, v)
		if err != nil || o.IsUser() {
			return Org{}, false, fmt.Errorf("to %s: %q is no account and no organization: %w", best, v, ErrInvalidInput)
		}
		return o, true, nil
	}
	assignToOrg := func(pid int64, remote string, o Org) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, pid); err != nil {
			return err
		}
		if err := holdForeignEntriesTx(tx, remote, o.ID, true); err != nil {
			return err
		}
		rep.Assigned = append(rep.Assigned, remote)
		return nil
	}
	targetOf := func(remote string) (acct int64, contested bool, err error) {
		best := ""
		for prefix := range plan.To {
			if underPrefix(remote, prefix) && len(prefix) > len(best) {
				best = prefix
			}
		}
		if best != "" {
			acct, err = accountRefTx(tx, plan.To[best])
			return acct, false, err
		}
		users, ambiguous := usersTx(tx, remote, instanceOwner, byName)
		if ambiguous {
			return owner, true, nil
		}
		switch len(users) {
		case 0:
			return owner, false, nil
		case 1:
			return users[0], false, nil
		default:
			return owner, true, nil
		}
	}
	// Move what no claimed prefix covers into personal namespaces.
	for _, p := range live {
		if p.org != org.ID {
			continue
		}
		if anyPrefix(p.remote, prefixes) || anyPrefix(p.remote, plan.Keep) {
			rep.Stayed++
			continue
		}
		if o, ok, err := toOrg(p.remote); err != nil {
			return err
		} else if ok {
			if o.ID != org.ID {
				if _, err := tx.Exec(`UPDATE projects SET org_id=? WHERE id=?`, o.ID, p.id); err != nil {
					return err
				}
				rep.Moved[o.Slug] = append(rep.Moved[o.Slug], p.remote)
			} else {
				rep.Stayed++
			}
			if err := assignToOrg(p.id, p.remote, o); err != nil {
				return err
			}
			continue
		}
		acct, contested, err := targetOf(p.remote)
		if err != nil {
			return err
		}
		if contested {
			// Several people wrote here: it stays in the organization, as
			// today, until the plan says whose it is.
			rep.Contested = append(rep.Contested, p.remote)
			rep.Stayed++
			continue
		}
		ns, err := nsOf(acct)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE projects SET org_id=? WHERE id=?`, ns.ID, p.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM shared_projects WHERE project_id=?`, p.id); err != nil {
			return err
		}
		// Who held an explicit role keeps it, as a collaborator of the namespace.
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_members(org_id, account_id, role, joined_at)
			SELECT ?, account_id, 'member', ? FROM project_members WHERE project_id=? AND account_id<>?`, ns.ID, now(), p.id, acct); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_guests(org_id, account_id)
			SELECT ?, account_id FROM project_members WHERE project_id=? AND account_id<>?`, ns.ID, p.id, acct); err != nil {
			return err
		}
		if err := orgEvent(tx, ns.ID, "migrate_project", "", p.remote, "from "+org.Slug); err != nil {
			return err
		}
		remote := p.remote
		// A named project carries the slug of its owner in its id.
		var named int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM named_projects WHERE project_id=?`, p.id).Scan(&named)
		if named > 0 && strings.HasPrefix(p.remote, org.Slug+"/") {
			remote = ns.Slug + "/" + strings.TrimPrefix(p.remote, org.Slug+"/")
			if err := renameProjectTx(tx, p.remote, remote); err != nil {
				return fmt.Errorf("named project %s: %w", p.remote, err)
			}
			rep.Renamed = append(rep.Renamed, p.remote+" -> "+remote)
		}
		// A change of owner like any other (held_entries.go).
		if err := holdForeignEntriesTx(tx, remote, ns.ID, true); err != nil {
			return err
		}
		rep.Moved[ns.Slug] = append(rep.Moved[ns.Slug], remote)
	}
	// Remotes that never got a project row become projects of their first user.
	if !plan.SkipUnclaimed {
		var unclaimed []string
		r4, err := tx.Query(`SELECT DISTINCT project FROM sessions WHERE project<>'' AND project NOT IN (SELECT remote FROM projects) ORDER BY project`)
		if err != nil {
			return err
		}
		for r4.Next() {
			var r string
			if r4.Scan(&r) == nil {
				unclaimed = append(unclaimed, r)
			}
		}
		r4.Close()
		for _, remote := range unclaimed {
			if scope.IsThrowawayRemote(remote) || len(remote) > maxRemoteLen || anyPrefix(remote, plan.Archive) {
				continue
			}
			if _, _, ok := prefixOrgTx(tx, remote); ok {
				// A connected organization's repository is never a person's; it
				// becomes the organization's on its next write.
				continue
			}
			if o, ok, err := toOrg(remote); err != nil {
				return err
			} else if ok {
				pid, err := insertProjectTx(tx, o.ID, remote)
				if err != nil {
					return err
				}
				if err := assignToOrg(pid, remote, o); err != nil {
					return err
				}
				label := o.Slug
				if o.ID == org.ID && finalSlug != "" {
					label = finalSlug
				}
				rep.Claimed[label] = append(rep.Claimed[label], remote)
				continue
			}
			acct, contested, err := targetOf(remote)
			if err != nil {
				return err
			}
			if contested {
				rep.Contested = append(rep.Contested, remote)
				continue
			}
			ns, err := nsOf(acct)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, remote, ns.ID, now()); err != nil {
				return err
			}
			if err := holdForeignEntriesTx(tx, remote, ns.ID, false); err != nil {
				return err
			}
			rep.Claimed[ns.Slug] = append(rep.Claimed[ns.Slug], remote)
		}
	}
	// Merges and renames the plan names.
	tables, err := projectTextTables(tx)
	if err != nil {
		return err
	}
	// The plan names projects by their ids before the migration; a move may
	// have renamed one (a named project into a personal namespace), so each
	// name follows the renames so far. A second run finds the work done and
	// skips it.
	current := func(id string) string {
		renames := map[string]string{}
		for _, r := range rep.Renamed {
			if a, b, ok := strings.Cut(r, " -> "); ok {
				renames[a] = b
			}
		}
		for i := 0; i < 8; i++ {
			next, ok := renames[id]
			if !ok {
				break
			}
			id = next
		}
		return id
	}
	for _, key := range sortedKeys(plan.Merge) {
		from, into := current(key), current(plan.Merge[key])
		if _, ok := projectTx(tx, into); !ok {
			return fmt.Errorf("merge %s into %s: %w", from, into, ErrProjectNotFound)
		}
		if _, ok := projectTx(tx, from); !ok {
			continue // gone
		}
		var reason string
		_ = tx.QueryRow(`SELECT archived_reason FROM projects WHERE remote=?`, from).Scan(&reason)
		if reason == "migrate-owners: merged into "+into {
			continue // merged by an earlier run
		}
		for _, t := range tables {
			if t == "projects" {
				continue
			}
			if _, err := tx.Exec(`UPDATE OR IGNORE `+t+` SET project=? WHERE project=?`, into, from); err != nil {
				return fmt.Errorf("merge %s: %s: %w", from, t, err)
			}
		}
		if _, err := tx.Exec(`UPDATE projects SET archived_at=?, archived_reason=? WHERE remote=?`, now(), "migrate-owners: merged into "+into, from); err != nil {
			return err
		}
		rep.Merged = append(rep.Merged, from+" -> "+into)
	}
	for _, key := range sortedKeys(plan.Rename) {
		from, to := current(key), plan.Rename[key]
		if from == to {
			continue
		}
		if _, ok := projectTx(tx, from); !ok {
			if _, done := projectTx(tx, to); done {
				continue // renamed by an earlier run
			}
		}
		if err := renameProjectTx(tx, from, to); err != nil {
			return fmt.Errorf("rename %s: %w", from, err)
		}
		rep.Renamed = append(rep.Renamed, from+" -> "+to)
	}
	// The organization's address and name.
	slug := strings.ToLower(strings.TrimSpace(plan.OrgSlug))
	if slug == "" {
		_ = tx.QueryRow(`SELECT login FROM git_sources WHERE org_id=? AND kind=? ORDER BY id LIMIT 1`, org.ID, SourceGitHub).Scan(&slug)
	}
	if slug != "" && slug != org.Slug {
		if !slugPattern.MatchString(slug) || slugTakenTx(tx, slug, org.ID) {
			return fmt.Errorf("%w: %q", ErrOrgSlugTaken, slug)
		}
		// Named projects that stay carry the new address in their id.
		var named []string
		if rows, err := tx.Query(`SELECT p.remote FROM projects p JOIN named_projects n ON n.project_id=p.id
			WHERE p.org_id=? AND substr(p.remote, 1, ?) = ?`, org.ID, len(org.Slug)+1, org.Slug+"/"); err == nil {
			for rows.Next() {
				var r string
				if rows.Scan(&r) == nil {
					named = append(named, r)
				}
			}
			rows.Close()
		}
		if _, err := tx.Exec(`UPDATE orgs SET slug=? WHERE id=?`, slug, org.ID); err != nil {
			return err
		}
		if err := moveSlugTx(tx, org.ID, org.Slug, slug); err != nil {
			return err
		}
		for _, r := range named {
			rep.Renamed = append(rep.Renamed, r+" -> "+slug+r[len(org.Slug):])
		}
		rep.OrgMoved = org.Slug + " -> " + slug
	}
	if name := strings.TrimSpace(plan.OrgName); name != "" && name != org.Name {
		if len(name) > maxOrgNameLen || hasControlRunes(name) {
			return fmt.Errorf("%w: organization name", ErrInvalidInput)
		}
		if _, err := tx.Exec(`UPDATE orgs SET name=? WHERE id=?`, name, org.ID); err != nil {
			return err
		}
	}
	display := DisplayUser
	if plan.FollowGitHub {
		display = DisplayGitHub
	}
	if _, err := tx.Exec(`UPDATE orgs SET display_source=? WHERE id=?`, display, org.ID); err != nil {
		return err
	}
	rep.DisplaySource = display
	for _, n := range names {
		slug := plan.Services[n]
		if slug == "" {
			continue
		}
		o, err := orgByRefTx(tx, slug)
		if err != nil {
			return fmt.Errorf("service %s: %w", n, err)
		}
		id, _ := accountRefTx(tx, n)
		if _, err := tx.Exec(`UPDATE persons SET service_org_id=? WHERE id=?`, o.ID, id); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// compareVisibility fills Gained, Lost and Unexpected. Allowed: a service
// loses roles; a person gains (or becomes owner of) projects in their own
// namespace. Everything else is unexpected.
func compareVisibility(q queryer, before, after visibility, rep *OwnerMigrationReport) {
	name := func(id int64) string {
		var n string
		if q.QueryRow(`SELECT name FROM persons WHERE id=?`, id).Scan(&n) != nil {
			return principalOfID(id)
		}
		return n
	}
	isService := func(id int64) bool { return isServiceTx(q, id) }
	ownNamespace := func(id int64, remote string) bool {
		var kind string
		var acct int64
		if q.QueryRow(`SELECT o.kind, o.account_id FROM projects p JOIN orgs o ON o.id=p.org_id WHERE p.remote=?`, remote).Scan(&kind, &acct) != nil {
			return false
		}
		return kind == OrgKindUser && acct == id
	}
	ids := map[int64]bool{}
	for id := range before {
		ids[id] = true
	}
	for id := range after {
		ids[id] = true
	}
	var order []int64
	for id := range ids {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, id := range order {
		b, a := before[id], after[id]
		remotes := map[string]bool{}
		for r := range b {
			remotes[r] = true
		}
		for r := range a {
			remotes[r] = true
		}
		var list []string
		for r := range remotes {
			list = append(list, r)
		}
		sort.Strings(list)
		who := name(id)
		for _, r := range list {
			was, is := b[r], a[r]
			if was == is {
				continue
			}
			// A project that was renamed or merged shows as lost under its old
			// id; the new id carries the role.
			if was != "" && is == "" {
				if renamedAway(rep, r, a) {
					continue
				}
				rep.Lost[who] = append(rep.Lost[who], r)
				if !isService(id) {
					rep.Unexpected = append(rep.Unexpected, fmt.Sprintf("%s would lose %s (%s)", who, r, was))
				}
				continue
			}
			if was == "" && renamedTo(rep, r) {
				continue
			}
			if was == "" && containsString(rep.Assigned, r) {
				rep.GainedInOrg[who] = append(rep.GainedInOrg[who], r)
				continue
			}
			if ownNamespace(id, r) {
				if was == "" {
					rep.Gained[who] = append(rep.Gained[who], r)
				}
				continue
			}
			rep.Unexpected = append(rep.Unexpected, fmt.Sprintf("%s: %s would change from %q to %q", who, r, was, is))
		}
	}
}

func renamedAway(rep *OwnerMigrationReport, old string, after map[string]string) bool {
	// Follow a chain (a named project the move renamed, then the plan's rename).
	id := old
	for i := 0; i < 8; i++ {
		next := ""
		for _, line := range append(append([]string{}, rep.Renamed...), rep.Merged...) {
			if from, to, _ := strings.Cut(line, " -> "); from == id {
				next = to
			}
		}
		if next == "" {
			return false
		}
		if after[next] != "" {
			return true
		}
		id = next
	}
	return false
}
func renamedTo(rep *OwnerMigrationReport, id string) bool {
	for _, line := range append(append([]string{}, rep.Renamed...), rep.Merged...) {
		if _, to, _ := strings.Cut(line, " -> "); to == id {
			return true
		}
	}
	return false
}
