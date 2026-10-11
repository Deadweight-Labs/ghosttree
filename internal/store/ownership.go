package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrMachineTaken: der Maschinenname gehört einem anderen Konto.
	ErrMachineTaken = errors.New("machine name belongs to another account")
	// ErrSessionCollision: (harness, external_id) existiert schon und gehört
	// einem anderen Konto oder einer anderen Maschine.
	ErrSessionCollision = errors.New("session id belongs to another account or machine")
)

// Machine ist ein beanspruchter Maschinenname mit seinem Besitzer.
type Machine struct {
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
	Owner     string `json:"owner"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// migrateOwnership ergänzt machines und sessions um account_id. Beides sind
// reine Metadaten-Änderungen (ADD COLUMN mit konstantem Default) und schreiben
// keine Zeile um, auch auf einer Datenbank mit Millionen Chunks nicht.
//
// account_id = 0 heißt "Altbestand" und gilt als Eigentum des Instanz-Owners
// (kleinste persons.id, siehe instanceOwnerID). sessions wird deshalb NICHT
// per UPDATE umgeschrieben: das wäre ein Vollscan mit Schreiblast auf der
// größten Tabelle, und ein zurückgerolltes Binary legt ohnehin wieder Zeilen
// mit 0 an. Die kleinen Tabellen machines und coord_agents werden dagegen
// ausdrücklich gestempelt, damit ihre Besitzer in der Datenbank stehen. Der
// Schritt ist wiederholbar: er ergänzt nur Zeilen, die noch keinen Besitzer
// haben.
func migrateOwnership(db *sql.DB) error {
	for _, c := range []struct{ table, column string }{
		{"machines", "account_id"}, {"machines", "claimed_by_token"}, {"sessions", "account_id"}, {"sessions", "shared"},
	} {
		if err := addColumnIfMissing(db, c.table, c.column, `INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	owner := instanceOwnerID(db)
	if owner == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE machines SET account_id=? WHERE account_id=0`, owner); err != nil {
		return err
	}
	// Eine Agent-Zeile geht an das Konto, dessen Name ihrem person-Label
	// entspricht, sonst an den Owner.
	if _, err := tx.Exec(`UPDATE coord_agents SET principal_id=COALESCE(
			(SELECT 'person:'||p.id FROM persons p WHERE p.name=coord_agents.person), 'person:'||?)
		WHERE principal_id=''`, owner); err != nil {
		return err
	}
	return tx.Commit()
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// instanceOwnerID ist das Konto, dem Altbestand gehört: die kleinste
// persons.id (bei ghosttree person:1, Robin). 0, wenn es noch kein Konto gibt.
func instanceOwnerID(q rowQuerier) int64 {
	var id sql.NullInt64
	if err := q.QueryRow(`SELECT MIN(id) FROM persons`).Scan(&id); err != nil || !id.Valid {
		return 0
	}
	return id.Int64
}

func canonicalMachine(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// effectiveMachineOwner löst account_id = 0 (Altbestand) zum Instanz-Owner auf.
func effectiveOwner(stored, owner int64) int64 {
	if stored == 0 {
		return owner
	}
	return stored
}

func accountNumericID(principalID string) (int64, bool) {
	id, err := parsePersonPrincipalID(principalID)
	return id, err == nil
}

// ClaimMachine ordnet einen Maschinennamen dem Konto zu. Ein neuer Name wird
// beansprucht, ein eigener bleibt idempotent, ein fremder ergibt
// ErrMachineTaken. Altbestand (account_id 0) gehört dem Instanz-Owner.
func (s *Store) ClaimMachine(name, accountPrincipalID string) error {
	if s.writer != nil {
		return queueWrite(s, []any{name, accountPrincipalID}, func(d *Store, p []any) error {
			return d.ClaimMachine(p[0].(string), p[1].(string))
		})
	}
	name = canonicalMachine(name)
	if name == "" {
		return nil
	}
	account, ok := accountNumericID(accountPrincipalID)
	if !ok {
		return fmt.Errorf("invalid account %q", accountPrincipalID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := claimMachineTx(tx, name, account); err != nil {
		return err
	}
	return tx.Commit()
}

type txExec interface {
	rowQuerier
	Exec(query string, args ...any) (sql.Result, error)
}

// claimMachineTx beansprucht den Namen für das Konto; created ist wahr, wenn
// dieser Aufruf die Zeile neu angelegt hat (der Name war vorher frei).
func claimMachineTx(tx txExec, name string, account int64) (created bool, err error) {
	var stored int64
	var seen string
	at := now()
	err = tx.QueryRow(`SELECT account_id, last_seen FROM machines WHERE hostname=?`, name).Scan(&stored, &seen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err := tx.Exec(`INSERT INTO machines(hostname, first_seen, last_seen, account_id) VALUES(?,?,?,?)`, name, at, at, account)
		if err == nil {
			return true, nil
		}
		if !strings.Contains(err.Error(), "constraint") {
			return false, err
		}
		// Ein anderer Prozess hat den Namen zwischen Lesen und Einfügen
		// beansprucht (kein Writer-Queue-Schutz, etwa beim CLI): wie ein
		// Fremdname behandeln, nicht als Serverfehler.
		if err := tx.QueryRow(`SELECT account_id, last_seen FROM machines WHERE hostname=?`, name).Scan(&stored, &seen); err != nil {
			return false, err
		}
		if effectiveOwner(stored, instanceOwnerID(tx)) != account {
			return false, ErrMachineTaken
		}
		return false, nil
	case err != nil:
		return false, err
	}
	if effectiveOwner(stored, instanceOwnerID(tx)) != account {
		return false, ErrMachineTaken
	}
	// Aktualisieren nur, wenn nötig: der Collector ruft das bei jedem Upload.
	if stored == 0 || seen < time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) {
		_, err := tx.Exec(`UPDATE machines SET account_id=?, last_seen=? WHERE hostname=?`, account, at, name)
		return false, err
	}
	return false, nil
}

// MachineClaimable meldet ErrMachineTaken, wenn der Name einem anderen Konto
// gehört. Ein freier oder eigener Name ist anspruchsfähig.
func (s *Store) MachineClaimable(name, accountPrincipalID string) error {
	if s.reader != nil {
		return s.reader.MachineClaimable(name, accountPrincipalID)
	}
	account, ok := accountNumericID(accountPrincipalID)
	if !ok {
		return fmt.Errorf("invalid account %q", accountPrincipalID)
	}
	var stored int64
	err := s.db.QueryRow(`SELECT account_id FROM machines WHERE hostname=?`, canonicalMachine(name)).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	if effectiveOwner(stored, instanceOwnerID(s.db)) != account {
		return ErrMachineTaken
	}
	return nil
}

// ResolveMachineName liefert den Namen, unter dem das Konto die Maschine
// anmelden kann. Ist wanted frei oder schon eigen, bleibt er. Gehört er einem
// anderen Konto, ergibt sich ErrMachineTaken, es sei denn auto ist gesetzt: dann
// kommt "<wanted>-<Kontoname>", und ist auch der vergeben, ein Zufallssuffix.
// Die Antwort verrät nur, was der Name-vergeben-Fehler ohnehin verriet; die
// Alternative ist frei oder zufällig, nie die nächste freie Nummer.
func (s *Store) ResolveMachineName(accountPrincipalID, wanted string, auto bool) (string, error) {
	if s.reader != nil {
		return s.reader.ResolveMachineName(accountPrincipalID, wanted, auto)
	}
	err := s.MachineClaimable(wanted, accountPrincipalID)
	if err == nil {
		return wanted, nil
	}
	if !errors.Is(err, ErrMachineTaken) || !auto {
		return "", err
	}
	var person string
	if id, ok := accountNumericID(accountPrincipalID); ok {
		_ = s.db.QueryRow(`SELECT name FROM persons WHERE id=?`, id).Scan(&person)
	}
	suffix := machineSuffix(person)
	candidates := []string{}
	if suffix != "" {
		candidates = append(candidates, joinMachineName(wanted, suffix))
	}
	for i := 0; i < 8; i++ {
		r, rerr := randomHex(2)
		if rerr != nil {
			return "", rerr
		}
		candidates = append(candidates, joinMachineName(wanted, strings.Trim(suffix+"-"+r, "-")))
	}
	for _, c := range candidates {
		if err := s.MachineClaimable(c, accountPrincipalID); err == nil {
			return c, nil
		} else if !errors.Is(err, ErrMachineTaken) {
			return "", err
		}
	}
	return "", ErrMachineTaken
}

// machineSuffix macht aus einem Kontonamen ein Namensstück: Kleinbuchstaben und
// Ziffern, höchstens 16 Zeichen.
func machineSuffix(person string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(person) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
		if b.Len() >= 16 {
			break
		}
	}
	return b.String()
}

// joinMachineName hängt suffix an und kürzt den Stamm, damit das Ganze in die
// 64 Zeichen eines Maschinennamens passt.
func joinMachineName(base, suffix string) string {
	if room := 64 - 1 - len(suffix); len(base) > room {
		base = base[:room]
	}
	return base + "-" + suffix
}

// ListMachines liefert alle Maschinen mit Besitzer; accountPrincipalID filtert
// auf ein Konto (?owner=me), leer heißt alle.
func (s *Store) ListMachines(accountPrincipalID string) ([]Machine, error) {
	if s.reader != nil {
		return s.reader.ListMachines(accountPrincipalID)
	}
	owner := instanceOwnerID(s.db)
	names, err := s.accountNames()
	if err != nil {
		return nil, err
	}
	var want int64
	if accountPrincipalID != "" {
		var ok bool
		if want, ok = accountNumericID(accountPrincipalID); !ok {
			return []Machine{}, nil
		}
	}
	rows, err := s.db.Query(`SELECT hostname, account_id, first_seen, last_seen FROM machines ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Machine{}
	for rows.Next() {
		var m Machine
		var stored int64
		if err := rows.Scan(&m.Name, &stored, &m.FirstSeen, &m.LastSeen); err != nil {
			return nil, err
		}
		id := effectiveOwner(stored, owner)
		if want != 0 && id != want {
			continue
		}
		m.AccountID, m.Owner = "person:"+strconv.FormatInt(id, 10), names[id]
		out = append(out, m)
	}
	return out, rows.Err()
}

// accountNames bildet persons.id auf den Kontonamen ab. Die Tabelle ist klein;
// Listen lösen ihre Besitzer damit in einer Abfrage statt je Zeile auf.
func (s *Store) accountNames() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM persons`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (s *Store) fillSessionOwners(sessions []Session) error {
	if len(sessions) == 0 {
		return nil
	}
	names, err := s.accountNames()
	if err != nil {
		return err
	}
	owner := instanceOwnerID(s.db)
	for i := range sessions {
		id := effectiveOwner(sessions[i].AccountID, owner)
		sessions[i].AccountID = id
		sessions[i].Owner = names[id]
	}
	return nil
}

// ReleaseMachine gibt einen Maschinennamen wieder frei (Admin, DB-Zugriff).
// Sessions behalten ihren Maschinennamen; der nächste Claim ist wieder offen.
func (s *Store) ReleaseMachine(name string) error {
	if s.writer != nil {
		return queueWrite(s, []any{name}, func(d *Store, p []any) error { return d.ReleaseMachine(p[0].(string)) })
	}
	res, err := s.db.Exec(`DELETE FROM machines WHERE hostname=?`, canonicalMachine(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("machine %q not found", name)
	}
	return nil
}

// TransferMachine überträgt einen Maschinennamen an ein anderes Konto (Admin,
// DB-Zugriff). Ein noch nicht beanspruchter Name wird dem Konto zugewiesen.
func (s *Store) TransferMachine(name, account string) error {
	if s.writer != nil {
		return queueWrite(s, []any{name, account}, func(d *Store, p []any) error {
			return d.TransferMachine(p[0].(string), p[1].(string))
		})
	}
	a, err := s.AccountByName(account)
	if err != nil {
		return err
	}
	id, ok := accountNumericID(a.ID)
	if !ok {
		return fmt.Errorf("invalid account %q", account)
	}
	at := now()
	_, err = s.db.Exec(`INSERT INTO machines(hostname, first_seen, last_seen, account_id) VALUES(?,?,?,?)
		ON CONFLICT(hostname) DO UPDATE SET account_id=excluded.account_id, claimed_by_token=0`, canonicalMachine(name), at, at, id)
	return err
}
