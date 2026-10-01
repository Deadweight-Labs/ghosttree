package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Org-Rollen. Mehr gibt es auf Organisationsebene nicht: wer was in einem
// Projekt darf, regeln später die Projektrollen.
const (
	OrgOwner  = "owner"
	OrgMember = "member"

	DefaultInvitationTTL = 7 * 24 * time.Hour
	MaxInvitationTTL     = 30 * 24 * time.Hour

	// Grenzen, damit weder ein Konto noch ein Eingabefeld die Tabellen oder den
	// Speicher füllt.
	maxPendingInvitations = 100
	maxOrgNameLen         = 100
	maxEmailLen           = 254
	maxRemoteLen          = 300
	maxListRows           = 5000
)

var (
	ErrOrgNotFound     = errors.New("organization not found")
	ErrNotOrgOwner     = errors.New("only an organization owner may do that")
	ErrNotOrgMember    = errors.New("not a member of that organization")
	ErrLastOrgOwner    = errors.New("an organization needs at least one owner")
	ErrOrgSlugTaken    = errors.New("organization slug is taken")
	ErrInvalidInput    = errors.New("invalid input")
	ErrProjectClaimed  = errors.New("project belongs to another organization")
	ErrProjectNotFound = errors.New("project not found")
	// ErrNoOrg: das Konto gehört keiner Organisation; ein Projekt kann ihm nicht
	// zugeordnet werden.
	ErrNoOrg = errors.New("account belongs to no organization")
	// ErrInvitationEmail: die Einladung ist an eine E-Mail-Adresse gebunden, und
	// der Einlösende konnte keine verifizierte gleiche Adresse vorweisen.
	ErrInvitationEmail = errors.New("invitation is bound to another or an unverified email address")
	ErrAlreadyMember   = errors.New("already a member of that organization")
	ErrTooManyInvites  = errors.New("too many pending invitations")
	// ErrTooManyAttempts: zu viele falsche Einladungs-Codes; kurz gesperrt.
	ErrTooManyAttempts = errors.New("too many wrong codes, try again later")
)

// ProjectUnclaimedError: ein Projekt ohne Organisation, und das Konto hat
// mehrere Organisationen ohne Standard. Es wird nicht geraten.
type ProjectUnclaimedError struct{ Choices []string }

func (e *ProjectUnclaimedError) Error() string {
	return "project is unclaimed; choose an organization: " + strings.Join(e.Choices, ", ")
}

// Org ist eine Organisation, aus Sicht eines Kontos (Role, Default) wenn es
// aus ListOrgs kommt.
type Org struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Role      string `json:"role,omitempty"`
	Default   bool   `json:"default,omitempty"`
}

type OrgMemberInfo struct {
	AccountID string `json:"account_id"`
	Account   string `json:"account"`
	Role      string `json:"role"`
	JoinedAt  string `json:"joined_at"`
}

// Project ist die Zuordnung einer normalisierten Remote zu genau einer
// Organisation. Alle anderen Tabellen führen weiter `project TEXT` und werden
// über Remote verbunden; hier wird deshalb nichts umgeschrieben.
type Project struct {
	Remote    string `json:"remote"`
	OrgID     int64  `json:"org_id"`
	Org       string `json:"org"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Invitation beschreibt eine Einladung ohne ihren Code. Der Klartext
// existiert nur bei der Ausgabe.
type Invitation struct {
	ID        int64  `json:"id"`
	OrgID     int64  `json:"org_id"`
	Role      string `json:"role"`
	Email     string `json:"email,omitempty"`
	InvitedBy string `json:"invited_by"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	Status    string `json:"status"` // pending, expired, accepted, revoked
}

type queryer interface {
	rowQuerier
	Query(query string, args ...any) (*sql.Rows, error)
}

type execQueryer interface {
	queryer
	Exec(query string, args ...any) (sql.Result, error)
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

func slugFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

func orgEvent(q execQueryer, orgID int64, action, actor, subject, detail string) error {
	_, err := q.Exec(`INSERT INTO org_events(org_id, action, actor, subject, detail, created_at) VALUES(?,?,?,?,?,?)`,
		orgID, action, actor, subject, detail, now())
	return err
}

func principalOfID(id int64) string { return "person:" + strconv.FormatInt(id, 10) }

// ---------------------------------------------------------------- migration

// migrateOrgs legt auf einer bestehenden Instanz die Default-Organisation an
// und ordnet ihr alle bekannten Projekte zu. Der Instanz-Owner (kleinste
// persons.id) wird ihr Owner; andere Konten bleiben ohne Mitgliedschaft.
//
// Kosten: drei CREATE TABLE im Schema (leere Tabellen), dann Lesen der kleinen
// Tabellen requests, knowledge, documents, threads, ghost_files, coord_rooms und
// sessions (eine Zeile je Session, nie session_chunks, nie search_documents) und
// ein INSERT je Projekt, alles in einer Transaktion. Keine bestehende Tabelle
// wird verändert oder umgeschrieben; deshalb gilt das Muster aus dem
// Ownership-Schritt hier ohne neue Spalten. Der Schritt ist einmalig (Marker in
// org_state) und läuft ohne Konto gar nicht, damit die erste später angelegte
// Person ihn beim nächsten Öffnen auslöst.
func migrateOrgs(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM org_state WHERE key='backfill'`).Scan(&done); err != nil {
		return err
	}
	owner := instanceOwnerID(tx)
	if done > 0 || owner == 0 {
		return nil
	}
	var org sql.NullInt64
	if err := tx.QueryRow(`SELECT MIN(id) FROM orgs`).Scan(&org); err != nil {
		return err
	}
	if !org.Valid {
		res, err := tx.Exec(`INSERT INTO orgs(slug, name, created_at) VALUES('deadweight','Deadweight Labs',?)`, now())
		if err != nil {
			return err
		}
		if org.Int64, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'owner',?)`,
		org.Int64, owner, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE persons SET default_org_id=? WHERE id=? AND default_org_id=0`, org.Int64, owner); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(remote, org_id, created_at)
		SELECT project, ?, ? FROM (
			SELECT project FROM requests UNION SELECT project FROM knowledge
			UNION SELECT project FROM documents UNION SELECT project FROM threads
			UNION SELECT project FROM ghost_files UNION SELECT project FROM sessions
			UNION SELECT substr(room_key, 9) FROM coord_rooms WHERE kind='project')
		WHERE project <> '' AND length(project) <= ?`, org.Int64, now(), maxRemoteLen); err != nil {
		return err
	}
	if err := orgEvent(tx, org.Int64, "backfill", "", principalOfID(owner), "default organization and existing projects"); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO org_state(key, value) VALUES('backfill', ?)`, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- orgs

func (s *Store) accountRowID(principal string) (int64, error) {
	return parsePersonPrincipalID(principal)
}

func createOrgTx(tx execQueryer, name, slug string, owner int64) (Org, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxOrgNameLen {
		return Org{}, fmt.Errorf("%w: organization name must be 1 to %d characters", ErrInvalidInput, maxOrgNameLen)
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		slug = slugFromName(name)
	}
	if !slugPattern.MatchString(slug) {
		return Org{}, fmt.Errorf("%w: slug must be lowercase letters, digits and dashes (up to 40 characters)", ErrInvalidInput)
	}
	res, err := tx.Exec(`INSERT INTO orgs(slug, name, created_at) VALUES(?,?,?)`, slug, name, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Org{}, ErrOrgSlugTaken
		}
		return Org{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'owner',?)`, id, owner, now()); err != nil {
		return Org{}, err
	}
	if _, err := tx.Exec(`UPDATE persons SET default_org_id=? WHERE id=? AND default_org_id=0`, id, owner); err != nil {
		return Org{}, err
	}
	if err := orgEvent(tx, id, "create_org", principalOfID(owner), slug, name); err != nil {
		return Org{}, err
	}
	return orgByIDTx(tx, id)
}

func orgByIDTx(q rowQuerier, id int64) (Org, error) {
	var o Org
	err := q.QueryRow(`SELECT id, slug, name, created_at FROM orgs WHERE id=?`, id).Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Org{}, ErrOrgNotFound
	}
	return o, err
}

// orgByRefTx löst eine Org per Id oder Slug auf.
func orgByRefTx(q rowQuerier, ref string) (Org, error) {
	ref = strings.TrimSpace(ref)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return orgByIDTx(q, id)
	}
	var o Org
	err := q.QueryRow(`SELECT id, slug, name, created_at FROM orgs WHERE slug=?`, strings.ToLower(ref)).Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Org{}, ErrOrgNotFound
	}
	return o, err
}

// CreateOrg legt eine Organisation an; das Konto wird Owner. Wer Organisationen
// anlegen darf, entscheidet der Aufrufer.
func (s *Store) CreateOrg(ownerPrincipal, name, slug string) (Org, error) {
	if s.writer != nil {
		return queueValue(s, []any{ownerPrincipal, name, slug}, func(d *Store, p []any) (Org, error) {
			return d.CreateOrg(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	owner, err := parsePersonPrincipalID(ownerPrincipal)
	if err != nil {
		return Org{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback()
	o, err := createOrgTx(tx, name, slug, owner)
	if err != nil {
		return Org{}, err
	}
	return o, tx.Commit()
}

func (s *Store) OrgByRef(ref string) (Org, error) {
	if s.reader != nil {
		return s.reader.OrgByRef(ref)
	}
	return orgByRefTx(s.db, ref)
}

// ListOrgs liefert die Organisationen eines Kontos mit seiner Rolle.
func (s *Store) ListOrgs(accountPrincipal string) ([]Org, error) {
	if s.reader != nil {
		return s.reader.ListOrgs(accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT o.id, o.slug, o.name, o.created_at, m.role, o.id = p.default_org_id
		FROM org_members m JOIN orgs o ON o.id = m.org_id JOIN persons p ON p.id = m.account_id
		WHERE m.account_id=? ORDER BY o.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Org{}
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt, &o.Role, &o.Default); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OrgRole liefert die Rolle des Kontos in der Organisation, leer ohne
// Mitgliedschaft.
func (s *Store) OrgRole(orgID int64, accountPrincipal string) string {
	if s.reader != nil {
		return s.reader.OrgRole(orgID, accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return ""
	}
	return orgRoleTx(s.db, orgID, id)
}

func orgRoleTx(q rowQuerier, orgID, account int64) string {
	var role string
	if q.QueryRow(`SELECT role FROM org_members WHERE org_id=? AND account_id=?`, orgID, account).Scan(&role) != nil {
		return ""
	}
	return role
}

func (s *Store) ListOrgMembers(orgID int64) ([]OrgMemberInfo, error) {
	if s.reader != nil {
		return s.reader.ListOrgMembers(orgID)
	}
	rows, err := s.db.Query(`SELECT m.account_id, p.name, m.role, m.joined_at FROM org_members m
		JOIN persons p ON p.id = m.account_id WHERE m.org_id=? ORDER BY m.role, p.name LIMIT ?`, orgID, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgMemberInfo{}
	for rows.Next() {
		var m OrgMemberInfo
		var id int64
		if err := rows.Scan(&id, &m.Account, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		m.AccountID = principalOfID(id)
		out = append(out, m)
	}
	return out, rows.Err()
}

func ownerCountTx(q rowQuerier, orgID int64) int {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM org_members WHERE org_id=? AND role='owner'`, orgID).Scan(&n)
	return n
}

// SetOrgRole ändert die Rolle eines Mitglieds. Nur Owner; der letzte Owner kann
// nicht herabgestuft werden.
func (s *Store) SetOrgRole(actorPrincipal string, orgID int64, targetPrincipal, role string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, targetPrincipal, role}, func(d *Store, p []any) error {
			return d.SetOrgRole(p[0].(string), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	if role != OrgOwner && role != OrgMember {
		return fmt.Errorf("%w: role must be owner or member", ErrInvalidInput)
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
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	old := orgRoleTx(tx, orgID, target)
	if old == "" {
		return ErrNotOrgMember
	}
	if old == OrgOwner && role != OrgOwner && ownerCountTx(tx, orgID) <= 1 {
		return ErrLastOrgOwner
	}
	if old == role {
		return nil
	}
	if _, err := tx.Exec(`UPDATE org_members SET role=? WHERE org_id=? AND account_id=?`, role, orgID, target); err != nil {
		return err
	}
	if err := orgEvent(tx, orgID, "set_role", principalOfID(actor), principalOfID(target), old+" -> "+role); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveOrgMember entfernt ein Mitglied. Ein Owner darf jeden entfernen, jedes
// Mitglied sich selbst; der letzte Owner bleibt.
func (s *Store) RemoveOrgMember(actorPrincipal string, orgID int64, targetPrincipal string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, targetPrincipal}, func(d *Store, p []any) error {
			return d.RemoveOrgMember(p[0].(string), p[1].(int64), p[2].(string))
		})
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
	actorRole := orgRoleTx(tx, orgID, actor)
	if actorRole != OrgOwner && !(actor == target && actorRole != "") {
		return ErrNotOrgOwner
	}
	old := orgRoleTx(tx, orgID, target)
	if old == "" {
		return ErrNotOrgMember
	}
	if old == OrgOwner && ownerCountTx(tx, orgID) <= 1 {
		return ErrLastOrgOwner
	}
	if _, err := tx.Exec(`DELETE FROM org_members WHERE org_id=? AND account_id=?`, orgID, target); err != nil {
		return err
	}
	// Ein Standard, auf den das Konto keinen Zugriff mehr hat, wird gelöscht
	// statt zu wirken.
	if _, err := tx.Exec(`UPDATE persons SET default_org_id=0 WHERE id=? AND default_org_id=?`, target, orgID); err != nil {
		return err
	}
	if err := orgEvent(tx, orgID, "remove_member", principalOfID(actor), principalOfID(target), old); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDefaultOrg wählt die Organisation, der ein Konto neue Projekte zuordnet.
func (s *Store) SetDefaultOrg(accountPrincipal string, orgID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountPrincipal, orgID}, func(d *Store, p []any) error {
			return d.SetDefaultOrg(p[0].(string), p[1].(int64))
		})
	}
	acct, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return err
	}
	if orgRoleTx(s.db, orgID, acct) == "" {
		return ErrNotOrgMember
	}
	_, err = s.db.Exec(`UPDATE persons SET default_org_id=? WHERE id=?`, orgID, acct)
	return err
}

// ---------------------------------------------------------------- projects

func projectTx(q rowQuerier, remote string) (Project, bool) {
	var p Project
	err := q.QueryRow(`SELECT p.remote, p.org_id, o.slug, p.name, p.created_at FROM projects p JOIN orgs o ON o.id = p.org_id WHERE p.remote=?`,
		remote).Scan(&p.Remote, &p.OrgID, &p.Org, &p.Name, &p.CreatedAt)
	return p, err == nil
}

func normalizeRemote(remote string) (string, error) {
	r := scope.NormalizeRemote(remote)
	if r == "" || len(r) > maxRemoteLen || strings.ContainsAny(r, " \t\r\n") {
		return "", fmt.Errorf("%w: remote must be a normalized git remote such as github.com/owner/repo", ErrInvalidInput)
	}
	return r, nil
}

func (s *Store) ProjectByRemote(remote string) (Project, bool) {
	if s.reader != nil {
		return s.reader.ProjectByRemote(remote)
	}
	return projectTx(s.db, scope.NormalizeRemote(remote))
}

// pickOrgTx wählt die Organisation für ein unbeanspruchtes Projekt: der
// Standard des Kontos, sonst seine einzige. Bei mehreren ohne Standard wird
// nicht geraten.
func pickOrgTx(q queryer, account int64) (int64, error) {
	var def int64
	_ = q.QueryRow(`SELECT default_org_id FROM persons WHERE id=?`, account).Scan(&def)
	if def > 0 && orgRoleTx(q, def, account) != "" {
		return def, nil
	}
	rows, err := q.Query(`SELECT o.id, o.slug FROM org_members m JOIN orgs o ON o.id = m.org_id WHERE m.account_id=? ORDER BY o.id LIMIT 50`, account)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	var slugs []string
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return 0, err
		}
		ids, slugs = append(ids, id), append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	switch len(ids) {
	case 0:
		return 0, ErrNoOrg
	case 1:
		return ids[0], nil
	}
	return 0, &ProjectUnclaimedError{Choices: slugs}
}

// claimProjectTx ordnet eine unbeanspruchte Remote einer Organisation zu und
// ist bei einer schon zugeordneten idempotent. orgID 0 heißt: Regel des Kontos.
func claimProjectTx(tx execQueryer, account int64, remote string, orgID int64) (Project, error) {
	if p, ok := projectTx(tx, remote); ok {
		if orgRoleTx(tx, p.OrgID, account) == "" {
			return Project{}, ErrProjectClaimed
		}
		return p, nil
	}
	if orgID == 0 {
		var err error
		if orgID, err = pickOrgTx(tx, account); err != nil {
			return Project{}, err
		}
	} else if orgRoleTx(tx, orgID, account) == "" {
		return Project{}, ErrNotOrgMember
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, remote, orgID, now()); err != nil {
		return Project{}, err
	}
	p, _ := projectTx(tx, remote)
	if p.OrgID != orgID {
		return Project{}, ErrProjectClaimed
	}
	if err := orgEvent(tx, orgID, "claim_project", principalOfID(account), remote, ""); err != nil {
		return Project{}, err
	}
	return p, nil
}

// EnsureProject ist der implizite Weg: ein Schreibzugriff auf eine unbekannte
// Remote ordnet sie der Standard-Organisation des Schreibenden zu. Der Aufrufer
// prüft vorher mit ProjectByRemote, ob das Projekt schon bekannt ist, damit der
// häufige Fall keinen Schreibpfad braucht. ErrNoOrg heißt: das Konto hat keine
// Organisation, das Projekt bleibt unbeansprucht; *ProjectUnclaimedError:
// mehrere Organisationen ohne Standard.
func (s *Store) EnsureProject(accountPrincipal, remote string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal, remote}, func(d *Store, p []any) (Project, error) {
			return d.EnsureProject(p[0].(string), p[1].(string))
		})
	}
	remote = scope.NormalizeRemote(remote)
	if remote == "" || len(remote) > maxRemoteLen {
		return Project{}, nil
	}
	return claimInTx(s.db, accountPrincipal, remote, "")
}

// ClaimProject ist der ausdrückliche Weg (`ctx project claim`).
func (s *Store) ClaimProject(accountPrincipal, remote, orgRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal, remote, orgRef}, func(d *Store, p []any) (Project, error) {
			return d.ClaimProject(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Project{}, err
	}
	return claimInTx(s.db, accountPrincipal, remote, orgRef)
}

func claimInTx(db *sql.DB, accountPrincipal, remote, orgRef string) (Project, error) {
	acct, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return Project{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	var orgID int64
	if orgRef != "" {
		o, err := orgByRefTx(tx, orgRef)
		if err != nil {
			return Project{}, err
		}
		orgID = o.ID
	}
	pr, err := claimProjectTx(tx, acct, remote, orgID)
	if err != nil {
		return Project{}, err
	}
	return pr, tx.Commit()
}

// MoveProject hängt ein Projekt in eine andere Organisation. Das verlangt
// Owner-Rechte in der alten und in der neuen. Keine Tabelle mit project-Text
// ändert sich; nur die Zuordnung.
func (s *Store) MoveProject(actorPrincipal, remote, toOrgRef string) (Project, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, remote, toOrgRef}, func(d *Store, p []any) (Project, error) {
			return d.MoveProject(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Project{}, err
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
	cur, ok := projectTx(tx, remote)
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	to, err := orgByRefTx(tx, toOrgRef)
	if err != nil {
		return Project{}, err
	}
	if orgRoleTx(tx, cur.OrgID, actor) != OrgOwner || orgRoleTx(tx, to.ID, actor) != OrgOwner {
		return Project{}, ErrNotOrgOwner
	}
	if cur.OrgID == to.ID {
		return cur, nil
	}
	if _, err := tx.Exec(`UPDATE projects SET org_id=? WHERE remote=?`, to.ID, cur.Remote); err != nil {
		return Project{}, err
	}
	for _, id := range []int64{cur.OrgID, to.ID} {
		if err := orgEvent(tx, id, "move_project", principalOfID(actor), cur.Remote, cur.Org+" -> "+to.Slug); err != nil {
			return Project{}, err
		}
	}
	moved, _ := projectTx(tx, cur.Remote)
	return moved, tx.Commit()
}

// ListProjects liefert die Projekte der Organisationen des Kontos. orgID 0
// heißt: aller seiner Organisationen. Sichtbarkeit innerhalb eines Projekts
// regelt dieser Aufruf nicht.
func (s *Store) ListProjects(accountPrincipal string, orgID int64) ([]Project, error) {
	if s.reader != nil {
		return s.reader.ListProjects(accountPrincipal, orgID)
	}
	acct, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT p.remote, p.org_id, o.slug, p.name, p.created_at FROM projects p
		JOIN orgs o ON o.id = p.org_id JOIN org_members m ON m.org_id = p.org_id AND m.account_id = ?
		WHERE (? = 0 OR p.org_id = ?) ORDER BY o.slug, p.remote LIMIT ?`, acct, orgID, orgID, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Remote, &p.OrgID, &p.Org, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- invitations

func normalizeInviteEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil
	}
	at := strings.LastIndex(email, "@")
	if len(email) > maxEmailLen || at < 1 || at == len(email)-1 || strings.ContainsAny(email, " \t\r\n<>,;") {
		return "", fmt.Errorf("%w: not an email address", ErrInvalidInput)
	}
	return email, nil
}

// CreateInvitation stellt eine Einladung in die Organisation aus (nur Owner)
// und gibt den Code genau einmal zurück. Gespeichert wird nur sein Hash. Eine
// gesetzte E-Mail bindet die Einladung: eingelöst werden kann sie dann nur mit
// einer vom IdP verifizierten, gleichen Adresse. Ohne E-Mail genügt der Code.
func (s *Store) CreateInvitation(actorPrincipal string, orgID int64, email, role string, ttl time.Duration) (string, Invitation, error) {
	type result struct {
		code string
		inv  Invitation
	}
	if s.writer != nil {
		r, err := queueValue(s, []any{actorPrincipal, orgID, email, role, ttl}, func(d *Store, p []any) (result, error) {
			code, inv, err := d.CreateInvitation(p[0].(string), p[1].(int64), p[2].(string), p[3].(string), p[4].(time.Duration))
			return result{code, inv}, err
		})
		return r.code, r.inv, err
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return "", Invitation{}, err
	}
	email, err = normalizeInviteEmail(email)
	if err != nil {
		return "", Invitation{}, err
	}
	if role == "" {
		role = OrgMember
	}
	if role != OrgOwner && role != OrgMember {
		return "", Invitation{}, fmt.Errorf("%w: role must be owner or member", ErrInvalidInput)
	}
	if ttl <= 0 {
		ttl = DefaultInvitationTTL
	}
	if ttl > MaxInvitationTTL {
		return "", Invitation{}, fmt.Errorf("%w: invitations live at most %d days", ErrInvalidInput, int(MaxInvitationTTL/(24*time.Hour)))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", Invitation{}, err
	}
	defer tx.Rollback()
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return "", Invitation{}, ErrNotOrgOwner
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM invitations WHERE org_id=? AND accepted_at='' AND revoked_at='' AND expires_at>?`,
		orgID, now()).Scan(&pending); err != nil {
		return "", Invitation{}, err
	}
	if pending >= maxPendingInvitations {
		return "", Invitation{}, ErrTooManyInvites
	}
	code, hash, err := newCode()
	if err != nil {
		return "", Invitation{}, err
	}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	res, err := tx.Exec(`INSERT INTO invitations(org_id, role, email, code_hash, invited_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
		orgID, role, email, hash, actor, now(), expires)
	if err != nil {
		return "", Invitation{}, err
	}
	id, _ := res.LastInsertId()
	if err := orgEvent(tx, orgID, "invite", principalOfID(actor), email, role); err != nil {
		return "", Invitation{}, err
	}
	inv, err := invitationTx(tx, id)
	if err != nil {
		return "", Invitation{}, err
	}
	return code, inv, tx.Commit()
}

const invitationSelect = `SELECT i.id, i.org_id, i.role, i.email, p.name, i.created_at, i.expires_at, i.accepted_at, i.revoked_at
	FROM invitations i JOIN persons p ON p.id = i.invited_by`

func scanInvitation(r rowScanner) (Invitation, error) {
	var inv Invitation
	var accepted, revoked string
	if err := r.Scan(&inv.ID, &inv.OrgID, &inv.Role, &inv.Email, &inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &accepted, &revoked); err != nil {
		return Invitation{}, err
	}
	switch {
	case accepted != "":
		inv.Status = "accepted"
	case revoked != "":
		inv.Status = "revoked"
	case inv.ExpiresAt <= now():
		inv.Status = "expired"
	default:
		inv.Status = "pending"
	}
	return inv, nil
}

func invitationTx(q rowQuerier, id int64) (Invitation, error) {
	return scanInvitation(q.QueryRow(invitationSelect+` WHERE i.id=?`, id))
}

// ListInvitations zeigt die Einladungen einer Organisation (nur Owner), neueste
// zuerst, ohne Codes.
func (s *Store) ListInvitations(actorPrincipal string, orgID int64) ([]Invitation, error) {
	if s.reader != nil {
		return s.reader.ListInvitations(actorPrincipal, orgID)
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return nil, err
	}
	if orgRoleTx(s.db, orgID, actor) != OrgOwner {
		return nil, ErrNotOrgOwner
	}
	rows, err := s.db.Query(invitationSelect+` WHERE i.org_id=? ORDER BY i.id DESC LIMIT 200`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvitation zieht eine noch offene Einladung zurück (nur Owner der
// Organisation, zu der sie gehört).
func (s *Store) RevokeInvitation(actorPrincipal string, orgID, id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, id}, func(d *Store, p []any) error {
			return d.RevokeInvitation(p[0].(string), p[1].(int64), p[2].(int64))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	if orgRoleTx(s.db, orgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	res, err := s.db.Exec(`UPDATE invitations SET revoked_at=? WHERE id=? AND org_id=? AND accepted_at='' AND revoked_at=''`, now(), id, orgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCodeInvalid
	}
	return orgEvent(s.db, orgID, "revoke_invite", principalOfID(actor), strconv.FormatInt(id, 10), "")
}

func invitationExists(q rowQuerier, code string) bool {
	var one int
	return q.QueryRow(`SELECT 1 FROM invitations WHERE code_hash=?`, hashToken(code)).Scan(&one) == nil
}

// acceptInvitationTx löst eine Einladung für ein existierendes Konto ein. Alle
// Prüfungen laufen vor dem Verbrauch, ein Fehlschlag verbraucht sie nicht.
// verifiedEmail muss vom Aufrufer verifiziert sein (IdP-Claim email_verified)
// oder leer; eine an eine Adresse gebundene Einladung verlangt Gleichheit.
// Gültigkeit, Zustand und Einlösbarkeit melden einheitlich ErrCodeInvalid.
func acceptInvitationTx(tx execQueryer, code string, account int64, verifiedEmail string) (Org, error) {
	var id, orgID, inviter int64
	var role, email, expires, accepted, revoked string
	err := tx.QueryRow(`SELECT id, org_id, role, email, invited_by, expires_at, accepted_at, revoked_at FROM invitations WHERE code_hash=?`,
		hashToken(code)).Scan(&id, &orgID, &role, &email, &inviter, &expires, &accepted, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Org{}, ErrCodeInvalid
	}
	if err != nil {
		return Org{}, err
	}
	if accepted != "" || revoked != "" || expires <= now() {
		return Org{}, ErrCodeInvalid
	}
	// Wer eingeladen hat, muss noch Owner sein: ein entzogenes Recht entwertet
	// offene Einladungen.
	if orgRoleTx(tx, orgID, inviter) != OrgOwner {
		return Org{}, ErrCodeInvalid
	}
	if email != "" && !(verifiedEmail != "" && strings.EqualFold(email, strings.TrimSpace(verifiedEmail))) {
		return Org{}, ErrInvitationEmail
	}
	if orgRoleTx(tx, orgID, account) != "" {
		return Org{}, ErrAlreadyMember
	}
	res, err := tx.Exec(`UPDATE invitations SET accepted_by=?, accepted_at=? WHERE id=? AND accepted_at='' AND revoked_at=''`, account, now(), id)
	if err != nil {
		return Org{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Org{}, ErrCodeInvalid
	}
	if _, err := tx.Exec(`INSERT INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,?,?)`, orgID, account, role, now()); err != nil {
		return Org{}, err
	}
	if _, err := tx.Exec(`UPDATE persons SET default_org_id=? WHERE id=? AND default_org_id=0`, orgID, account); err != nil {
		return Org{}, err
	}
	if err := orgEvent(tx, orgID, "join", principalOfID(account), principalOfID(account), "invitation "+strconv.FormatInt(id, 10)+" as "+role); err != nil {
		return Org{}, err
	}
	return orgByIDTx(tx, orgID)
}

// AcceptInvitation nimmt ein angemeldetes Konto in die Organisation auf. Der
// Abgleich mit einer gebundenen E-Mail nutzt die E-Mail des Kontos, die nur aus
// einem verifizierten IdP-Claim oder vom Betreiber stammt.
func (s *Store) AcceptInvitation(accountPrincipal, code string) (Org, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal, code}, func(d *Store, p []any) (Org, error) {
			return d.AcceptInvitation(p[0].(string), p[1].(string))
		})
	}
	acct, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return Org{}, err
	}
	if s.attemptLimiter().blocked(accountPrincipal) {
		return Org{}, ErrTooManyAttempts
	}
	org, err := s.acceptInvitation(acct, code)
	s.attemptLimiter().note(accountPrincipal, err)
	return org, err
}

func (s *Store) acceptInvitation(acct int64, code string) (Org, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback()
	a, err := accountState(tx, acct)
	if err != nil {
		return Org{}, err
	}
	if a.State != "active" {
		return Org{}, ErrAccountDisabled
	}
	o, err := acceptInvitationTx(tx, code, acct, a.Email)
	if err != nil {
		return Org{}, err
	}
	return o, tx.Commit()
}

// inviteName macht aus dem Anzeigenamen einen freien Kontonamen.
func inviteName(tx rowQuerier, wanted string) (string, error) {
	base := strings.TrimSpace(wanted)
	if base == "" {
		base = "user"
	}
	if len(base) > 64 {
		base = base[:64]
	}
	name := base
	for i := 2; i < 1000; i++ {
		var one int
		err := tx.QueryRow(`SELECT 1 FROM persons WHERE name=?`, name).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return "", fmt.Errorf("no free account name for %q", wanted)
}

// createInvitedAccountTx legt das Konto zu einer Einladung an und löst sie ein;
// scheitert die Einlösung, bleibt auch das Konto aus (Rollback des Aufrufers).
func createInvitedAccountTx(tx execQueryer, name, email, code string) (int64, error) {
	if !invitationExists(tx, code) {
		return 0, ErrCodeInvalid
	}
	name, err := inviteName(tx, name)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO persons(name, token_hash, created_at, email) VALUES(?,?,?,?)`, name, "", now(), strings.TrimSpace(email))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := acceptInvitationTx(tx, code, id, email); err != nil {
		return 0, err
	}
	return id, nil
}

// InviteLocal legt für eine Einladung ein Konto ohne IdP an (lokaler Modus:
// Code plus gewählter Name). Eine an eine E-Mail gebundene Einladung geht hier
// nicht, weil ohne IdP nichts die Adresse verifiziert. Es gibt keine Sperre
// nach Fehlversuchen: ohne Absender-Schlüssel würde ein gemeinsamer Zähler jedem
// Unangemeldeten erlauben, die Einlösung für alle zu sperren, und ein
// 256-Bit-Code lässt sich nicht erraten.
func (s *Store) InviteLocal(code, name string) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{code, name}, func(d *Store, p []any) (Account, error) {
			return d.InviteLocal(p[0].(string), p[1].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	id, err := createInvitedAccountTx(tx, name, "", code)
	if err != nil {
		return Account{}, err
	}
	a, err := accountState(tx, id)
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit()
}

// ---------------------------------------------------------------- attempts

// attemptLimiter zählt falsche Einladungs-Codes je Schlüssel (Identität oder
// Konto). Der Speicher ist begrenzt: bei Überlauf fliegen abgelaufene und dann
// die ältesten Schlüssel raus. Die Codes haben 256 Bit; die Sperre schützt vor
// Aufwand und Lärm, nicht vor Raten.
type attemptLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[string][]time.Time
}

const (
	maxInviteFailures   = 5
	inviteFailureWindow = 10 * time.Minute
	maxLimiterKeys      = 1000
)

// limiters hält einen Zähler je Datenbank. Der Schreibpfad arbeitet mit
// frischen Store-Kopien (direct); ein Feld am Store ginge dabei verloren,
// der *sql.DB bleibt derselbe.
var limiters sync.Map

func (s *Store) attemptLimiter() *attemptLimiter {
	if l, ok := limiters.Load(s.db); ok {
		return l.(*attemptLimiter)
	}
	l, _ := limiters.LoadOrStore(s.db, &attemptLimiter{now: time.Now, failures: map[string][]time.Time{}})
	return l.(*attemptLimiter)
}

func (l *attemptLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-inviteFailureWindow)
	l.failures[key] = trimBefore(l.failures[key], cutoff)
	if len(l.failures[key]) == 0 {
		delete(l.failures, key)
		return false
	}
	return len(l.failures[key]) >= maxInviteFailures
}

// note zählt einen Fehlschlag, der auf einen falschen Code hindeutet.
func (l *attemptLimiter) note(key string, err error) {
	if !errors.Is(err, ErrCodeInvalid) && !errors.Is(err, ErrInvitationEmail) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.now()
	if _, ok := l.failures[key]; !ok && len(l.failures) >= maxLimiterKeys {
		cutoff := at.Add(-inviteFailureWindow)
		var oldest string
		var oldestAt time.Time
		for k, v := range l.failures {
			v = trimBefore(v, cutoff)
			if len(v) == 0 {
				delete(l.failures, k)
				continue
			}
			if oldest == "" || v[len(v)-1].Before(oldestAt) {
				oldest, oldestAt = k, v[len(v)-1]
			}
		}
		if len(l.failures) >= maxLimiterKeys {
			delete(l.failures, oldest)
		}
	}
	l.failures[key] = append(l.failures[key], at)
}
