package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Teilen wie bei GitHub (REQ-490): ein Projekt ist "mit der Org geteilt" oder
// privat, eine Org kann ein Remote-Präfix beanspruchen, und ein Präfix gibt es
// nur mit Nachweis über eine verbundene Quelle.
const (
	// DefaultSharedRole ist die Rolle, die ein geteiltes Projekt jedem
	// Org-Mitglied ohne eigene Zeile gibt.
	DefaultSharedRole = RoleMember

	VisibilityShared  = "shared"
	VisibilityPrivate = "private"

	SourceGitHub = "github"

	maxSourceRepos = 2000
)

var (
	ErrNotProjectOwner = errors.New("only a project owner may change who the project is shared with")
	// ErrPrefixUnproven: es gibt keine verbundene Quelle, die das Präfix belegt.
	ErrPrefixUnproven = errors.New("that prefix is not proven by a connected source of this organization")
	ErrPrefixTaken    = errors.New("another organization holds that prefix")
	ErrSourceTaken    = errors.New("that source is connected to another organization")
	ErrSourceNotFound = errors.New("source not found")
)

var loginRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38})$`)

// OrgPrefix ist ein von einer Organisation beanspruchtes Remote-Präfix.
type OrgPrefix struct {
	ID         int64  `json:"id"`
	OrgID      int64  `json:"org_id"`
	Prefix     string `json:"prefix"`
	Visibility string `json:"visibility"`
	CreatedAt  string `json:"created_at"`
}

// Source ist eine verbundene Quelle (eine GitHub-Org), mit der eine Organisation
// ihre Präfixe belegt.
type Source struct {
	ID          int64  `json:"id"`
	OrgID       int64  `json:"org_id"`
	Kind        string `json:"kind"`
	Login       string `json:"login"`
	ConnectedAt string `json:"connected_at"`
	Repos       int    `json:"repos"`
	Imported    int    `json:"imported"`
}

// SourceRepo ist ein Repo einer Quelle; Imported sagt, ob es schon ein Projekt ist.
type SourceRepo struct {
	Remote   string `json:"remote"`
	Imported bool   `json:"imported"`
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func projectSharedTx(q rowQuerier, pid int64) bool {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM shared_projects WHERE project_id=?`, pid).Scan(&n)
	return n > 0
}

func orgGuestTx(q rowQuerier, orgID, account int64) bool {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM org_guests WHERE org_id=? AND account_id=?`, orgID, account).Scan(&n)
	return n > 0
}

// projectShareDefaultTx: das Projekt ist geteilt und das Konto kein Gast der
// Organisation. Nur dann gilt die Standardrolle.
func projectShareDefaultTx(q rowQuerier, pid, orgID, account int64) bool {
	return projectSharedTx(q, pid) && !orgGuestTx(q, orgID, account)
}

// backfillOrgGuests markiert einmalig, wer vor dieser Version nur über eine
// Gast-Rolle in die Organisation kam: Konten, die dort nichts außer guest sind.
func backfillOrgGuests(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM org_state WHERE key='guest_backfill'`).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_guests(org_id, account_id)
		SELECT DISTINCT p.org_id, pm.account_id FROM project_members pm
		JOIN projects p ON p.id=pm.project_id
		JOIN org_members om ON om.org_id=p.org_id AND om.account_id=pm.account_id AND om.role='member'
		WHERE pm.role='guest' AND NOT EXISTS (
			SELECT 1 FROM project_members o JOIN projects op ON op.id=o.project_id
			WHERE o.account_id=pm.account_id AND op.org_id=p.org_id AND o.role<>'guest')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_state(key, value) VALUES('guest_backfill', ?)`, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectShared sagt, ob das Projekt mit seiner Organisation geteilt ist.
func (s *Store) ProjectShared(remote string) bool {
	if s.reader != nil {
		return s.reader.ProjectShared(remote)
	}
	p, ok := projectTx(s.db, strings.TrimSpace(remote))
	return ok && p.Shared
}

// SetProjectVisibility stellt ein Projekt auf "mit der Org geteilt" (jedes
// Org-Mitglied hat darin die Standardrolle, Gäste ausgenommen, ausdrückliche
// Rollen übersteuern) oder auf privat. Das darf ein Projekt-Owner, auch ein
// impliziter. Wer nicht in der Organisation ist, erfährt nichts vom Projekt.
func (s *Store) SetProjectVisibility(actorPrincipal, remote string, shared bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, remote, shared}, func(d *Store, p []any) error {
			return d.SetProjectVisibility(p[0].(string), p[1].(string), p[2].(bool))
		})
	}
	remote = strings.TrimSpace(remote)
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, ok := projectTx(tx, remote)
	if !ok || orgRoleTx(tx, p.OrgID, actor) == "" {
		return ErrProjectNotFound
	}
	rank := RoleRank(projectRoleTx(tx, remote, actor).Role)
	if rank == 0 {
		return ErrProjectNotFound
	}
	if rank < RoleRank(RoleOwner) {
		return ErrNotProjectOwner
	}
	if p.Shared == shared {
		return nil
	}
	action := "unshare_project"
	if shared {
		action = "share_project"
		_, err = tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, p.ID)
	} else {
		_, err = tx.Exec(`DELETE FROM shared_projects WHERE project_id=?`, p.ID)
	}
	if err != nil {
		return err
	}
	if err := orgEvent(tx, p.OrgID, action, principalOfID(actor), remote, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- Quellen

func normalizeLogin(login string) (string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if !loginRE.MatchString(login) || strings.HasSuffix(login, "-") {
		return "", fmt.Errorf("%w: not a valid account name on the source", ErrInvalidInput)
	}
	return login, nil
}

func sourceHost(kind string) (string, bool) {
	if kind == SourceGitHub {
		return "github.com", true
	}
	return "", false
}

func requireOrgOwnerTx(tx rowQuerier, orgID int64, actorPrincipal string) (int64, error) {
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return 0, err
	}
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return 0, ErrNotOrgOwner
	}
	return actor, nil
}

// ConnectSource trägt eine Quelle ein, deren Besitz der Aufrufer (der Server)
// zuvor nachgewiesen hat: bei GitHub, dass der Konto-Inhaber Admin der
// GitHub-Org ist. Der Store prüft diesen Nachweis nicht selbst, er ist die
// Voraussetzung für jedes Präfix. repos ist die Repo-Liste der Quelle als
// Remotes; Fremdes und Ungültiges wird verworfen. Eine Quelle gehört genau einer
// Organisation (ErrSourceTaken).
func (s *Store) ConnectSource(actorPrincipal string, orgID int64, kind, login string, repos []string) (Source, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, orgID, kind, login, repos}, func(d *Store, p []any) (Source, error) {
			return d.ConnectSource(p[0].(string), p[1].(int64), p[2].(string), p[3].(string), p[4].([]string))
		})
	}
	host, ok := sourceHost(kind)
	if !ok {
		return Source{}, fmt.Errorf("%w: unknown source kind", ErrInvalidInput)
	}
	login, err := normalizeLogin(login)
	if err != nil {
		return Source{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Source{}, err
	}
	defer tx.Rollback()
	actor, err := requireOrgOwnerTx(tx, orgID, actorPrincipal)
	if err != nil {
		return Source{}, err
	}
	var id, owner int64
	err = tx.QueryRow(`SELECT id, org_id FROM git_sources WHERE kind=? AND login=?`, kind, login).Scan(&id, &owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.Exec(`INSERT INTO git_sources(org_id, kind, login, connected_by, connected_at) VALUES(?,?,?,?,?)`,
			orgID, kind, login, actor, now())
		if err != nil {
			return Source{}, err
		}
		id, _ = res.LastInsertId()
		if err := orgEvent(tx, orgID, "connect_source", principalOfID(actor), kind+":"+login, ""); err != nil {
			return Source{}, err
		}
	case err != nil:
		return Source{}, err
	case owner != orgID:
		return Source{}, ErrSourceTaken
	}
	if repos != nil {
		if _, err := tx.Exec(`DELETE FROM git_source_repos WHERE source_id=?`, id); err != nil {
			return Source{}, err
		}
		seen := map[string]bool{}
		for _, raw := range repos {
			remote, err := normalizeRemote(raw)
			if err != nil || !strings.HasPrefix(remote, host+"/"+login+"/") || seen[remote] {
				continue
			}
			if len(seen) >= maxSourceRepos {
				break
			}
			seen[remote] = true
			if _, err := tx.Exec(`INSERT INTO git_source_repos(source_id, remote) VALUES(?,?)`, id, remote); err != nil {
				return Source{}, err
			}
		}
	}
	src, err := sourceTx(tx, id)
	if err != nil {
		return Source{}, err
	}
	return src, tx.Commit()
}

const sourceSelect = `SELECT g.id, g.org_id, g.kind, g.login, g.connected_at,
	(SELECT COUNT(*) FROM git_source_repos r WHERE r.source_id=g.id),
	(SELECT COUNT(*) FROM git_source_repos r JOIN projects p ON p.remote=r.remote WHERE r.source_id=g.id AND p.org_id=g.org_id)
	FROM git_sources g`

func scanSource(r rowScanner) (Source, error) {
	var s Source
	err := r.Scan(&s.ID, &s.OrgID, &s.Kind, &s.Login, &s.ConnectedAt, &s.Repos, &s.Imported)
	return s, err
}

func sourceTx(q rowQuerier, id int64) (Source, error) {
	return scanSource(q.QueryRow(sourceSelect+` WHERE g.id=?`, id))
}

// DisconnectSource trennt eine Quelle und hebt die Präfixe auf, die sie belegte.
// Bestehende Projekte bleiben.
func (s *Store) DisconnectSource(actorPrincipal string, orgID int64, kind, login string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, kind, login}, func(d *Store, p []any) error {
			return d.DisconnectSource(p[0].(string), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	host, ok := sourceHost(kind)
	if !ok {
		return fmt.Errorf("%w: unknown source kind", ErrInvalidInput)
	}
	login, err := normalizeLogin(login)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := requireOrgOwnerTx(tx, orgID, actorPrincipal)
	if err != nil {
		return err
	}
	var id int64
	if tx.QueryRow(`SELECT id FROM git_sources WHERE org_id=? AND kind=? AND login=?`, orgID, kind, login).Scan(&id) != nil {
		return ErrSourceNotFound
	}
	for _, q := range []string{`DELETE FROM git_source_repos WHERE source_id=?`, `DELETE FROM git_sources WHERE id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM org_prefixes WHERE org_id=? AND prefix=?`, orgID, host+"/"+login); err != nil {
		return err
	}
	if err := orgEvent(tx, orgID, "disconnect_source", principalOfID(actor), kind+":"+login, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// ListSources zeigt die verbundenen Quellen einer Organisation ihren Mitgliedern.
func (s *Store) ListSources(viewerPrincipal string, orgID int64) ([]Source, error) {
	if s.reader != nil {
		return s.reader.ListSources(viewerPrincipal, orgID)
	}
	viewer, err := parsePersonPrincipalID(viewerPrincipal)
	if err != nil {
		return nil, err
	}
	if orgRoleTx(s.db, orgID, viewer) == "" {
		return nil, ErrNotOrgMember
	}
	rows, err := s.db.Query(sourceSelect+` WHERE g.org_id=? ORDER BY g.login`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListSourceRepos nennt die Repos einer Quelle (nur Owner), mit der Angabe, ob sie
// schon ein Projekt der Organisation sind.
func (s *Store) ListSourceRepos(actorPrincipal string, orgID, sourceID int64) ([]SourceRepo, error) {
	if s.reader != nil {
		return s.reader.ListSourceRepos(actorPrincipal, orgID, sourceID)
	}
	if _, err := requireOrgOwnerTx(s.db, orgID, actorPrincipal); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT r.remote, EXISTS(SELECT 1 FROM projects p WHERE p.remote=r.remote AND p.org_id=g.org_id)
		FROM git_source_repos r JOIN git_sources g ON g.id=r.source_id WHERE g.id=? AND g.org_id=? ORDER BY r.remote`, sourceID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceRepo{}
	for rows.Next() {
		var r SourceRepo
		if err := rows.Scan(&r.Remote, &r.Imported); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportSourceRepos legt die Repos einer Quelle als Projekte der Organisation an
// (Owner). Was schon ein Projekt irgendeiner Organisation ist, bleibt
// unberührt. Die Sichtbarkeit nimmt die Präfix-Regel, sonst privat. Liefert die
// Zahl der neu angelegten Projekte.
func (s *Store) ImportSourceRepos(actorPrincipal string, orgID, sourceID int64) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, orgID, sourceID}, func(d *Store, p []any) (int, error) {
			return d.ImportSourceRepos(p[0].(string), p[1].(int64), p[2].(int64))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := requireOrgOwnerTx(tx, orgID, actorPrincipal)
	if err != nil {
		return 0, err
	}
	var kind, login string
	if tx.QueryRow(`SELECT kind, login FROM git_sources WHERE id=? AND org_id=?`, sourceID, orgID).Scan(&kind, &login) != nil {
		return 0, ErrSourceNotFound
	}
	rows, err := tx.Query(`SELECT remote FROM git_source_repos WHERE source_id=? ORDER BY remote`, sourceID)
	if err != nil {
		return 0, err
	}
	var remotes []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return 0, err
		}
		remotes = append(remotes, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	added := 0
	for _, remote := range remotes {
		if _, known := projectTx(tx, remote); known {
			continue
		}
		pid, err := insertProjectTx(tx, orgID, remote)
		if err != nil {
			return 0, err
		}
		if porg, vis, ok := prefixOrgTx(tx, remote); ok && porg == orgID && vis == VisibilityShared {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, pid); err != nil {
				return 0, err
			}
		}
		added++
	}
	if added > 0 {
		if err := orgEvent(tx, orgID, "import_repos", principalOfID(actor), kind+":"+login, fmt.Sprintf("%d projects", added)); err != nil {
			return 0, err
		}
	}
	return added, tx.Commit()
}

func insertProjectTx(tx execQueryer, orgID int64, remote string) (int64, error) {
	res, err := tx.Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, remote, orgID, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---------------------------------------------------------------- Präfixe

// normalizePrefix macht aus "github.com/Org/*" das Präfix "github.com/org" und
// gibt Host und Konto zurück. Beansprucht wird die Ebene "Host/Konto", nichts
// Feineres: Nachweisen lässt sich das Konto, nicht ein einzelnes Repo.
func normalizePrefix(raw string) (prefix, host, login string, err error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	p = strings.TrimSuffix(p, "/*")
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || !strings.Contains(parts[0], ".") || parts[1] == "" || len(p) > maxRemoteLen || strings.ContainsAny(p, " \t\r\n*") {
		return "", "", "", fmt.Errorf("%w: a prefix looks like github.com/<organization>/*", ErrInvalidInput)
	}
	return p, parts[0], parts[1], nil
}

// prefixOrgTx findet die Organisation, deren Präfix eine Remote deckt, mit der
// Standard-Sichtbarkeit der Regel.
func prefixOrgTx(q rowQuerier, remote string) (orgID int64, visibility string, ok bool) {
	err := q.QueryRow(`SELECT org_id, visibility FROM org_prefixes
		WHERE ?1 = prefix OR substr(?1, 1, length(prefix)+1) = prefix || '/'
		ORDER BY length(prefix) DESC LIMIT 1`, remote).Scan(&orgID, &visibility)
	return orgID, visibility, err == nil
}

// ClaimPrefix beansprucht ein Remote-Präfix für die Organisation (Owner). Ohne
// Nachweis wird abgelehnt: die Organisation braucht eine verbundene Quelle, die
// genau dieses Konto auf genau diesem Host belegt. Ein zweites Beanspruchen
// desselben Präfixes durch dieselbe Organisation ändert nur die Sichtbarkeit,
// die neue Projekte bekommen ("shared" oder "private"); ein Präfix einer anderen
// Organisation ist nie frei.
func (s *Store) ClaimPrefix(actorPrincipal string, orgID int64, prefix, visibility string) (OrgPrefix, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, orgID, prefix, visibility}, func(d *Store, p []any) (OrgPrefix, error) {
			return d.ClaimPrefix(p[0].(string), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	if visibility != VisibilityShared && visibility != VisibilityPrivate {
		return OrgPrefix{}, fmt.Errorf("%w: visibility is shared or private", ErrInvalidInput)
	}
	prefix, host, login, err := normalizePrefix(prefix)
	if err != nil {
		return OrgPrefix{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return OrgPrefix{}, err
	}
	defer tx.Rollback()
	actor, err := requireOrgOwnerTx(tx, orgID, actorPrincipal)
	if err != nil {
		return OrgPrefix{}, err
	}
	var proven int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM git_sources WHERE org_id=? AND login=? AND kind=?`, orgID, login, SourceGitHub).Scan(&proven)
	if h, _ := sourceHost(SourceGitHub); proven == 0 || h != host {
		return OrgPrefix{}, ErrPrefixUnproven
	}
	var holder int64
	switch err := tx.QueryRow(`SELECT org_id FROM org_prefixes WHERE prefix=?`, prefix).Scan(&holder); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO org_prefixes(org_id, prefix, visibility, created_by, created_at) VALUES(?,?,?,?,?)`,
			orgID, prefix, visibility, actor, now()); err != nil {
			return OrgPrefix{}, err
		}
		if err := orgEvent(tx, orgID, "claim_prefix", principalOfID(actor), prefix, visibility); err != nil {
			return OrgPrefix{}, err
		}
	case err != nil:
		return OrgPrefix{}, err
	case holder != orgID:
		return OrgPrefix{}, ErrPrefixTaken
	default:
		if _, err := tx.Exec(`UPDATE org_prefixes SET visibility=? WHERE prefix=?`, visibility, prefix); err != nil {
			return OrgPrefix{}, err
		}
	}
	out, err := prefixRowTx(tx, prefix)
	if err != nil {
		return OrgPrefix{}, err
	}
	return out, tx.Commit()
}

func prefixRowTx(q rowQuerier, prefix string) (OrgPrefix, error) {
	var p OrgPrefix
	err := q.QueryRow(`SELECT id, org_id, prefix, visibility, created_at FROM org_prefixes WHERE prefix=?`, prefix).
		Scan(&p.ID, &p.OrgID, &p.Prefix, &p.Visibility, &p.CreatedAt)
	return p, err
}

// ReleasePrefix gibt ein Präfix auf (Owner). Bestehende Projekte bleiben.
func (s *Store) ReleasePrefix(actorPrincipal string, orgID int64, prefix string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, prefix}, func(d *Store, p []any) error {
			return d.ReleasePrefix(p[0].(string), p[1].(int64), p[2].(string))
		})
	}
	prefix, _, _, err := normalizePrefix(prefix)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := requireOrgOwnerTx(tx, orgID, actorPrincipal)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM org_prefixes WHERE org_id=? AND prefix=?`, orgID, prefix)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSourceNotFound
	}
	if err := orgEvent(tx, orgID, "release_prefix", principalOfID(actor), prefix, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// ListOrgPrefixes zeigt die Präfixe einer Organisation ihren Mitgliedern.
func (s *Store) ListOrgPrefixes(viewerPrincipal string, orgID int64) ([]OrgPrefix, error) {
	if s.reader != nil {
		return s.reader.ListOrgPrefixes(viewerPrincipal, orgID)
	}
	viewer, err := parsePersonPrincipalID(viewerPrincipal)
	if err != nil {
		return nil, err
	}
	if orgRoleTx(s.db, orgID, viewer) == "" {
		return nil, ErrNotOrgMember
	}
	rows, err := s.db.Query(`SELECT id, org_id, prefix, visibility, created_at FROM org_prefixes WHERE org_id=? ORDER BY prefix`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgPrefix{}
	for rows.Next() {
		var p OrgPrefix
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Prefix, &p.Visibility, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// assignByPrefixTx legt eine unbekannte Remote an, wenn ein Präfix sie deckt.
// handled sagt, dass ein Präfix greift (dann entscheidet es allein): ein Mitglied
// der Organisation bekommt das Projekt dort angelegt, mit der Sichtbarkeit der
// Regel; alle anderen, auch Gäste und eine ausdrücklich andere Ziel-Org, bekommen
// ErrProjectClaimed und nichts wird angelegt.
func assignByPrefixTx(tx execQueryer, account int64, remote string, wantOrg int64) (p Project, handled bool, err error) {
	porg, vis, ok := prefixOrgTx(tx, remote)
	if !ok {
		return Project{}, false, nil
	}
	if (wantOrg != 0 && wantOrg != porg) || orgRoleTx(tx, porg, account) == "" || orgGuestTx(tx, porg, account) {
		return Project{}, true, ErrProjectClaimed
	}
	pid, err := insertProjectTx(tx, porg, remote)
	if err != nil {
		return Project{}, true, err
	}
	if vis == VisibilityShared {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO shared_projects(project_id) VALUES(?)`, pid); err != nil {
			return Project{}, true, err
		}
	}
	if err := orgEvent(tx, porg, "prefix_project", principalOfID(account), remote, vis); err != nil {
		return Project{}, true, err
	}
	p, _ = projectTx(tx, remote)
	return p, true, nil
}
