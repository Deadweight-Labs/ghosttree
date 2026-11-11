package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Wege, auf denen eine Maschinenfreigabe geändert wurde (machine_events.via).
const (
	MachineViaWeb = "web"
	// MachineViaCLIDB: Notausgang des Betreibers mit Datenbankzugriff.
	MachineViaCLIDB = "cli-db"
	// MachineViaAPI: ein Agent hat einen Eintrag freigegeben oder zurückgenommen.
	MachineViaAPI = "api"
)

// Rollen einer Maschinenfreigabe. Lesen zeigt die je Eintrag freigegebenen
// Einträge, Schreiben erlaubt den Mitgliedern zusätzlich eigene Einträge.
const (
	ShareRead  = "read"
	ShareWrite = "write"
)

// Arten von Einträgen mit Maschinen-Scope, die sich mit einer Org teilen lassen.
const (
	EntryKnowledge = "knowledge"
	EntryRunbook   = "runbook"
)

var (
	ErrMachineNotFound = errors.New("machine not found")
	// ErrMachineNotManager: der Aufrufer sieht die Maschine, darf sie aber nicht
	// verwalten (Besitzer, bei Org-Maschinen ein Maintainer).
	ErrMachineNotManager = errors.New("only the owner of a machine (for organization machines a maintainer) may manage it")
	// ErrMachineUnproven: die Maschine wurde nie von sich selbst per Geräteanmeldung
	// beansprucht. Ein Name allein beweist nichts (#2581).
	ErrMachineUnproven = errors.New("machine identity is not proven: sign in on the machine with ctx login first")
	// ErrWebSessionRequired: Verwaltung läuft nur aus einer interaktiven Web-Sitzung (#2411).
	ErrWebSessionRequired = errors.New("managing machine sharing requires an interactive web session")
	ErrShareNotFound      = errors.New("machine is not shared with that organization")
	// ErrNoMachineShare: ein Eintrag lässt sich nur in Orgs freigeben, für die
	// ein Mensch die Maschine schon freigegeben hat.
	ErrNoMachineShare = errors.New("the machine is not shared with that organization")
	// ErrEntryNotShareable: der Eintrag hat keinen Maschinen-Scope oder ihn
	// schrieb jemand, der die Maschine nicht (mehr) verantworten darf.
	ErrEntryNotShareable = errors.New("entry cannot be shared: it has no machine scope or its author is not entitled on the machine")
)

const machineSharingSchema = `
CREATE TABLE IF NOT EXISTS machine_shares(
  hostname TEXT NOT NULL,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  role TEXT NOT NULL CHECK(role IN ('read','write')),
  os_user TEXT NOT NULL DEFAULT '', path_prefix TEXT NOT NULL DEFAULT '',
  granted_by INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  PRIMARY KEY(hostname, org_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS machine_shares_org ON machine_shares(org_id);
CREATE TABLE IF NOT EXISTS machine_entry_shares(
  kind TEXT NOT NULL CHECK(kind IN ('knowledge','runbook')),
  entry_id INTEGER NOT NULL,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  hostname TEXT NOT NULL,
  shared_by INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  PRIMARY KEY(kind, entry_id, org_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS machine_entry_shares_machine ON machine_entry_shares(hostname, org_id);
CREATE TABLE IF NOT EXISTS machine_events(
  id INTEGER PRIMARY KEY, hostname TEXT NOT NULL, action TEXT NOT NULL,
  actor INTEGER NOT NULL DEFAULT 0, subject TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '',
  via TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS machine_events_host ON machine_events(hostname, id);
`

// ensureMachineSharing legt die Tabellen für Maschinenfreigaben an und
// ergänzt machines um owner_org_id (0 = die Maschine gehört der Person in
// account_id, sonst der Org). Alles ist additiv: leere Tabellen und eine
// Spalte mit konstantem Default, keine Zeile wird umgeschrieben und keine
// Fremdschlüsselprüfung über die Datenbank läuft.
func ensureMachineSharing(db *sql.DB) error {
	if _, err := db.Exec(machineSharingSchema); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "machines", "owner_org_id", `INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

// MachineShare ist die Freigabe einer Maschine an eine Org. OSUser und
// PathPrefix sind ein Hinweis, wofür die Freigabe gedacht ist; sie werden
// nicht durchgesetzt (DEC-4). Wirksam begrenzt wird durch das Teilen je Eintrag.
type MachineShare struct {
	Org        string `json:"org"`
	OrgID      int64  `json:"org_id"`
	Role       string `json:"role"`
	OSUser     string `json:"os_user,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	GrantedBy  string `json:"granted_by,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// MachineEvent ist ein Eintrag im Protokoll der Freigaben einer Maschine.
type MachineEvent struct {
	Action    string `json:"action"`
	Actor     string `json:"actor,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Via       string `json:"via"`
	CreatedAt string `json:"created_at"`
}

type machineActor struct {
	id       int64
	operator bool
}

type machineRow struct {
	host      string
	account   int64
	org       int64
	firstSeen string
	lastSeen  string
}

func machineRowTx(q rowQuerier, name string) (machineRow, error) {
	m := machineRow{host: canonicalMachine(name)}
	err := q.QueryRow(`SELECT account_id, owner_org_id, first_seen, last_seen FROM machines WHERE hostname=?`, m.host).
		Scan(&m.account, &m.org, &m.firstSeen, &m.lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrMachineNotFound
	}
	if err != nil {
		return m, err
	}
	m.account = effectiveOwner(m.account, instanceOwnerID(q))
	return m, nil
}

// orgMemberRoleTx ist die Rolle in der Org, ohne Gäste: ein Gast einer Org
// gehört nie zu denen, die Maschinen-Wissen sehen.
func orgMemberRoleTx(q rowQuerier, org, account int64) string {
	var role string
	if q.QueryRow(`SELECT om.role FROM org_members om WHERE om.org_id=? AND om.account_id=?
		AND NOT EXISTS(SELECT 1 FROM org_guests g WHERE g.org_id=om.org_id AND g.account_id=om.account_id)`, org, account).Scan(&role) != nil {
		return ""
	}
	return role
}

// canManageTx: Besitzer der Maschine, bei Org-Maschinen ein Maintainer (Owner
// der Org). Der Betreiber (--db) verwaltet alles.
func canManageTx(q rowQuerier, m machineRow, who machineActor) bool {
	if who.operator {
		return true
	}
	if m.org > 0 {
		return orgMemberRoleTx(q, m.org, who.id) == OrgOwner
	}
	return m.account == who.id
}

// machineProvenTx: die Maschine hat sich selbst per Geräteanmeldung beim
// Besitzer (bei Org-Maschinen bei einem Maintainer) ausgewiesen. Ein frei
// gewählter Name, ein Legacy-Token oder ein von Hand angelegter CLI-Token
// reichen nicht.
func machineProvenTx(q rowQuerier, m machineRow) bool {
	var ok bool
	err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM api_tokens WHERE machine=? AND kind='device' AND revoked_at=''
		AND (account_id=? OR (?>0 AND account_id IN (SELECT account_id FROM org_members WHERE org_id=? AND role='owner'))))`,
		m.host, m.account, m.org, m.org).Scan(&ok)
	return err == nil && ok
}

var osUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func cleanShareRestriction(osUser, pathPrefix string) (string, string, error) {
	osUser, pathPrefix = strings.TrimSpace(osUser), strings.TrimSpace(pathPrefix)
	if osUser != "" && !osUserPattern.MatchString(osUser) {
		return "", "", fmt.Errorf("%w: operating user must be a lowercase system user name", ErrInvalidInput)
	}
	if pathPrefix != "" {
		if len(pathPrefix) > 256 || !strings.HasPrefix(pathPrefix, "/") {
			return "", "", fmt.Errorf("%w: path must be absolute and at most 256 characters", ErrInvalidInput)
		}
		for _, r := range pathPrefix {
			if unicode.IsControl(r) {
				return "", "", fmt.Errorf("%w: path must not contain control characters", ErrInvalidInput)
			}
		}
		for _, seg := range strings.Split(pathPrefix, "/") {
			if seg == ".." {
				return "", "", fmt.Errorf("%w: path must not contain ..", ErrInvalidInput)
			}
		}
		if pathPrefix != "/" {
			pathPrefix = strings.TrimRight(pathPrefix, "/")
		}
	}
	return osUser, pathPrefix, nil
}

func machineEventTx(tx txExec, host, action string, actor int64, subject, detail, via string) error {
	_, err := tx.Exec(`INSERT INTO machine_events(hostname, action, actor, subject, detail, via, created_at) VALUES(?,?,?,?,?,?,?)`,
		host, action, actor, subject, detail, via, now())
	return err
}

// deleteMachineSharesTx nimmt jede Freigabe einer Maschine zurück, samt aller
// Eintrags-Freigaben. Wer den Namen später beansprucht, erbt nichts.
func deleteMachineSharesTx(tx txExec, host string) error {
	if _, err := tx.Exec(`DELETE FROM machine_entry_shares WHERE hostname=?`, host); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM machine_shares WHERE hostname=?`, host)
	return err
}

// managerOf lädt die Maschine und prüft, dass der Aufrufer sie verwaltet.
// Wer sie gar nicht sehen darf, bekommt "gibt es nicht", ein Mitglied, das sie
// nur lesen darf, "verboten".
func managerOf(tx txExec, name string, who machineActor) (machineRow, error) {
	m, err := machineRowTx(tx, name)
	if err != nil {
		return m, err
	}
	if canManageTx(tx, m, who) {
		return m, nil
	}
	if m.org > 0 && orgMemberRoleTx(tx, m.org, who.id) != "" {
		return m, ErrMachineNotManager
	}
	var shared bool
	_ = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM machine_shares ms JOIN org_members om ON om.org_id=ms.org_id AND om.account_id=?
		WHERE ms.hostname=?)`, who.id, m.host).Scan(&shared)
	if shared {
		return m, ErrMachineNotManager
	}
	return m, ErrMachineNotFound
}

func (s *Store) memberActor(actor Principal) (machineActor, error) {
	if actor.TokenKind != WebSessionKind {
		return machineActor{}, ErrWebSessionRequired
	}
	id, err := parsePersonPrincipalID(actor.ID)
	if err != nil {
		return machineActor{}, ErrWebSessionRequired
	}
	return machineActor{id: id}, nil
}

// ShareMachine gibt eine Maschine an eine Org frei oder ändert die Freigabe.
// Nur aus einer Web-Sitzung (#2411), nur durch den Besitzer (bei Org-Maschinen
// einen Maintainer), nur für eine nachgewiesene Maschine und nur an eine Org,
// in der der Aufrufer selbst Mitglied (kein Gast) ist.
func (s *Store) ShareMachine(actor Principal, machine, orgRef, role, osUser, pathPrefix string) (MachineShare, error) {
	if s.writer != nil {
		return queueValue(s, []any{actor, machine, orgRef, role, osUser, pathPrefix}, func(d *Store, p []any) (MachineShare, error) {
			return d.ShareMachine(p[0].(Principal), p[1].(string), p[2].(string), p[3].(string), p[4].(string), p[5].(string))
		})
	}
	who, err := s.memberActor(actor)
	if err != nil {
		return MachineShare{}, err
	}
	return s.shareMachine(who, MachineViaWeb, machine, orgRef, role, osUser, pathPrefix)
}

// ShareMachineAsOperator ist der Notausgang mit Datenbankzugriff (ctx machine
// share --db). Er umgeht die Rechte des Besitzers, aber nicht den Nachweis
// der Maschine.
func (s *Store) ShareMachineAsOperator(machine, orgRef, role, osUser, pathPrefix string) (MachineShare, error) {
	if s.writer != nil {
		return queueValue(s, []any{machine, orgRef, role, osUser, pathPrefix}, func(d *Store, p []any) (MachineShare, error) {
			return d.ShareMachineAsOperator(p[0].(string), p[1].(string), p[2].(string), p[3].(string), p[4].(string))
		})
	}
	return s.shareMachine(machineActor{operator: true}, MachineViaCLIDB, machine, orgRef, role, osUser, pathPrefix)
}

func (s *Store) shareMachine(who machineActor, via, machine, orgRef, role, osUser, pathPrefix string) (MachineShare, error) {
	if role != ShareRead && role != ShareWrite {
		return MachineShare{}, fmt.Errorf("%w: role must be %s or %s", ErrInvalidInput, ShareRead, ShareWrite)
	}
	osUser, pathPrefix, err := cleanShareRestriction(osUser, pathPrefix)
	if err != nil {
		return MachineShare{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MachineShare{}, err
	}
	defer tx.Rollback()
	m, err := managerOf(tx, machine, who)
	if err != nil {
		return MachineShare{}, err
	}
	if !machineProvenTx(tx, m) {
		return MachineShare{}, ErrMachineUnproven
	}
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return MachineShare{}, err
	}
	if !who.operator && orgMemberRoleTx(tx, o.ID, who.id) == "" {
		return MachineShare{}, ErrOrgNotFound
	}
	if o.ID == m.org {
		return MachineShare{}, fmt.Errorf("%w: the machine already belongs to that organization", ErrInvalidInput)
	}
	at := now()
	if _, err := tx.Exec(`INSERT INTO machine_shares(hostname, org_id, role, os_user, path_prefix, granted_by, created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(hostname, org_id) DO UPDATE SET role=excluded.role, os_user=excluded.os_user, path_prefix=excluded.path_prefix`,
		m.host, o.ID, role, osUser, pathPrefix, who.id, at); err != nil {
		return MachineShare{}, err
	}
	if err := machineEventTx(tx, m.host, "share", who.id, o.Slug, role, via); err != nil {
		return MachineShare{}, err
	}
	if err := tx.Commit(); err != nil {
		return MachineShare{}, err
	}
	return MachineShare{Org: o.Slug, OrgID: o.ID, Role: role, OSUser: osUser, PathPrefix: pathPrefix, CreatedAt: at}, nil
}

// UnshareMachine nimmt die Freigabe an eine Org zurück, samt allen
// Eintrags-Freigaben dieser Org auf der Maschine. Dasselbe Recht wie beim Anlegen.
func (s *Store) UnshareMachine(actor Principal, machine, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actor, machine, orgRef}, func(d *Store, p []any) error {
			return d.UnshareMachine(p[0].(Principal), p[1].(string), p[2].(string))
		})
	}
	who, err := s.memberActor(actor)
	if err != nil {
		return err
	}
	return s.unshareMachine(who, MachineViaWeb, machine, orgRef)
}

// UnshareMachineAsOperator ist der Notausgang mit Datenbankzugriff.
func (s *Store) UnshareMachineAsOperator(machine, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{machine, orgRef}, func(d *Store, p []any) error {
			return d.UnshareMachineAsOperator(p[0].(string), p[1].(string))
		})
	}
	return s.unshareMachine(machineActor{operator: true}, MachineViaCLIDB, machine, orgRef)
}

func (s *Store) unshareMachine(who machineActor, via, machine, orgRef string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := managerOf(tx, machine, who)
	if err != nil {
		return err
	}
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return ErrShareNotFound
	}
	res, err := tx.Exec(`DELETE FROM machine_shares WHERE hostname=? AND org_id=?`, m.host, o.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShareNotFound
	}
	if _, err := tx.Exec(`DELETE FROM machine_entry_shares WHERE hostname=? AND org_id=?`, m.host, o.ID); err != nil {
		return err
	}
	if err := machineEventTx(tx, m.host, "unshare", who.id, o.Slug, "", via); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMachineOwnerOrg macht eine Maschine zur Maschine einer Org (Build-Server,
// CI-Runner): der Besitzer der Maschine überträgt sie, und er muss selbst
// Maintainer der Org sein. Bisherige Freigaben an diese Org entfallen; Freigaben
// an andere Orgs bleiben.
func (s *Store) SetMachineOwnerOrg(actor Principal, machine, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actor, machine, orgRef}, func(d *Store, p []any) error {
			return d.SetMachineOwnerOrg(p[0].(Principal), p[1].(string), p[2].(string))
		})
	}
	who, err := s.memberActor(actor)
	if err != nil {
		return err
	}
	return s.setMachineOwnerOrg(who, MachineViaWeb, machine, orgRef)
}

// SetMachineOwnerOrgAsOperator ist der Notausgang mit Datenbankzugriff.
func (s *Store) SetMachineOwnerOrgAsOperator(machine, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{machine, orgRef}, func(d *Store, p []any) error {
			return d.SetMachineOwnerOrgAsOperator(p[0].(string), p[1].(string))
		})
	}
	return s.setMachineOwnerOrg(machineActor{operator: true}, MachineViaCLIDB, machine, orgRef)
}

func (s *Store) setMachineOwnerOrg(who machineActor, via, machine, orgRef string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := managerOf(tx, machine, who)
	if err != nil {
		return err
	}
	if m.org > 0 {
		return fmt.Errorf("%w: the machine already belongs to an organization", ErrInvalidInput)
	}
	if !machineProvenTx(tx, m) {
		return ErrMachineUnproven
	}
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return err
	}
	if !who.operator && orgMemberRoleTx(tx, o.ID, who.id) != OrgOwner {
		return ErrOrgNotFound
	}
	if _, err := tx.Exec(`UPDATE machines SET owner_org_id=? WHERE hostname=?`, o.ID, m.host); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM machine_entry_shares WHERE hostname=? AND org_id=?`, m.host, o.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM machine_shares WHERE hostname=? AND org_id=?`, m.host, o.ID); err != nil {
		return err
	}
	if err := machineEventTx(tx, m.host, "owner_org", who.id, o.Slug, "", via); err != nil {
		return err
	}
	return tx.Commit()
}

// ShareEntry gibt einen Eintrag mit Maschinen-Scope (Wissen oder Ablauf) an
// eine Org frei, für die die Maschine schon freigegeben ist. Das darf, wer die
// Maschine verwaltet und den Eintrag sehen darf; sein Autor muss die Maschine
// jetzt verantworten dürfen, sonst ließe sich fremder Text über die Freigabe
// des Besitzers in die Auslieferung heben (#2581).
func (s *Store) ShareEntry(actor Principal, kind string, id int64, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actor, kind, id, orgRef}, func(d *Store, p []any) error {
			return d.ShareEntry(p[0].(Principal), p[1].(string), p[2].(int64), p[3].(string))
		})
	}
	machine, author, err := s.entryMachine(kind, id)
	if err != nil {
		return err
	}
	a := s.Access(actor)
	if !a.valid {
		return ErrAccessNotFound
	}
	if !a.canSeeEntry(kind, id) {
		return ErrAccessNotFound
	}
	if machine == "" {
		return ErrEntryNotShareable
	}
	if a.machineLevel(machine, nil) != levelManage {
		return ErrAccessForbidden
	}
	// Ein an eine Maschine gebundenes Token gibt nur Einträge dieser Maschine frei,
	// nicht die einer anderen Maschine desselben Besitzers.
	if actor.Machine != "" && canonicalMachine(actor.Machine) != canonicalMachine(machine) {
		return ErrAccessForbidden
	}
	if !a.authorEntitled(machine, author, nil) {
		return ErrEntryNotShareable
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return ErrNoMachineShare
	}
	host := canonicalMachine(machine)
	m, err := machineRowTx(tx, host)
	if err != nil {
		return ErrNoMachineShare
	}
	var granted bool
	if m.org == o.ID {
		granted = true
	} else {
		_ = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM machine_shares WHERE hostname=? AND org_id=?)`, host, o.ID).Scan(&granted)
	}
	if !granted {
		return ErrNoMachineShare
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO machine_entry_shares(kind, entry_id, org_id, hostname, shared_by, created_at) VALUES(?,?,?,?,?,?)`,
		kind, id, o.ID, host, a.account, now()); err != nil {
		return err
	}
	if err := machineEventTx(tx, host, "entry_share", a.account, o.Slug, fmt.Sprintf("%s %d", kind, id), MachineViaAPI); err != nil {
		return err
	}
	return tx.Commit()
}

// UnshareEntry nimmt die Freigabe eines Eintrags an eine Org zurück. Das darf,
// wer die Maschine verwaltet, und der Autor des Eintrags.
func (s *Store) UnshareEntry(actor Principal, kind string, id int64, orgRef string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actor, kind, id, orgRef}, func(d *Store, p []any) error {
			return d.UnshareEntry(p[0].(Principal), p[1].(string), p[2].(int64), p[3].(string))
		})
	}
	machine, author, err := s.entryMachine(kind, id)
	if err != nil {
		return err
	}
	a := s.Access(actor)
	if !a.valid || !a.canSeeEntry(kind, id) {
		return ErrAccessNotFound
	}
	if machine == "" {
		return ErrEntryNotShareable
	}
	if a.machineLevel(machine, nil) != levelManage && !(author > 0 && author == a.account) {
		return ErrAccessForbidden
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := orgByRefTx(tx, orgRef)
	if err != nil {
		return ErrShareNotFound
	}
	res, err := tx.Exec(`DELETE FROM machine_entry_shares WHERE kind=? AND entry_id=? AND org_id=?`, kind, id, o.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShareNotFound
	}
	if err := machineEventTx(tx, canonicalMachine(machine), "entry_unshare", a.account, o.Slug, fmt.Sprintf("%s %d", kind, id), MachineViaAPI); err != nil {
		return err
	}
	return tx.Commit()
}

// entryMachine liefert die Maschine und das Autorkonto eines Eintrags.
// Ein unbekannter Eintrag ergibt ErrAccessNotFound.
func (s *Store) entryMachine(kind string, id int64) (machine string, author int64, err error) {
	switch kind {
	case EntryKnowledge:
		k, err := s.KnowledgeByID(id)
		if err != nil {
			return "", 0, ErrAccessNotFound
		}
		author = s.knowledgeAuthorID(id)
		if author == 0 {
			author = instanceOwnerID(s.db)
		}
		if k.Scope.Project != "" {
			// Mit Projekt gilt die Projektrolle; die Maschinenfreigabe ist nur für
			// reines Maschinen-Wissen da.
			return "", author, nil
		}
		return k.Scope.Machine, author, nil
	case EntryRunbook:
		ref, err := s.RunbookRefByID(id)
		if err != nil {
			return "", 0, ErrAccessNotFound
		}
		if ref.Project != "" {
			return "", ref.AuthorID, nil
		}
		return ref.Machine, ref.AuthorID, nil
	}
	return "", 0, fmt.Errorf("%w: unknown entry kind", ErrInvalidInput)
}

// canSeeEntry: der Betrachter darf den Eintrag lesen, so wie jede Liste es prüft.
func (a *ProjectAccess) canSeeEntry(kind string, id int64) bool {
	switch kind {
	case EntryKnowledge:
		k, err := a.st.KnowledgeByID(id)
		return err == nil && a.CanSeeKnowledge(k)
	case EntryRunbook:
		ref, err := a.st.RunbookRefByID(id)
		return err == nil && a.CanSeeRunbook(ref)
	}
	return false
}

// insertEntryShareTx gibt einen frisch geschriebenen Eintrag an Orgs frei,
// für die die Maschine freigegeben ist oder die sie besitzt. Fehlt eine solche
// Freigabe, scheitert das Schreiben des Eintrags ganz: er soll nie versehentlich
// privat bleiben, wo der Schreiber Teilen wollte.
func insertEntryShareTx(tx txExec, kind string, id int64, machine string, orgs []int64, by int64) error {
	host := canonicalMachine(machine)
	m, err := machineRowTx(tx, host)
	if err != nil {
		return ErrNoMachineShare
	}
	manages := canManageTx(tx, m, machineActor{id: by})
	for _, org := range orgs {
		if !manages {
			// Ohne Verwaltungsrecht: nur über eine Org mit Schreib-Freigabe, in der
			// der Schreiber Mitglied ist (kein Gast).
			var write bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM machine_shares WHERE hostname=? AND org_id=? AND role=?)`, host, org, ShareWrite).Scan(&write); err != nil {
				return err
			}
			if !write || orgMemberRoleTx(tx, org, by) == "" || isServiceTx(tx, by) {
				return ErrAccessForbidden
			}
		}
		var granted bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM machine_shares WHERE hostname=? AND org_id=?)
			OR EXISTS(SELECT 1 FROM machines WHERE hostname=? AND owner_org_id=?)`, host, org, host, org).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			return ErrNoMachineShare
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO machine_entry_shares(kind, entry_id, org_id, hostname, shared_by, created_at) VALUES(?,?,?,?,?,?)`,
			kind, id, org, host, by, now()); err != nil {
			return err
		}
	}
	return nil
}

// MachineOwnerOrgID ist die Org, der die Maschine gehört, 0 bei einer Maschine
// einer Person oder einer unbekannten.
func (s *Store) MachineOwnerOrgID(machine string) int64 {
	if s.reader != nil {
		return s.reader.MachineOwnerOrgID(machine)
	}
	var org int64
	_ = s.db.QueryRow(`SELECT owner_org_id FROM machines WHERE hostname=?`, canonicalMachine(machine)).Scan(&org)
	return org
}
