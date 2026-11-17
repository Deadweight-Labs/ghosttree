package store

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Anmelden per E-Mail (REQ-676, KN-4724).
//
// Eine "Herausforderung" (email_challenges) ist eine Mail, die der Server an
// eine Adresse schickt: ein kurzer Code zum Eintippen und ein Link, beides für
// dieselbe Sache und nur einmal einlösbar (wer den Code nimmt, entwertet den
// Link). Gespeichert werden nur Hashes. Der Code hat nur 40 Bit; gegen Raten
// schützen die Frist (Minuten), höchstens fünf Fehlversuche je Herausforderung
// und die Grenzen der Weboberfläche je Adresse und Absender.
//
// Wozu eine Herausforderung dient, sagt purpose:
//
//	login    Anmelden; die Adresse gehört zu einem Konto (persons.email).
//	confirm  eine Adresse am Konto bestätigen (nach dem Beitritt, im Profil).
//	reset    ein Passwort neu setzen.
//	join     ein Beitritt über eine an die Adresse gebundene Einladung: Konto
//	         und Mitgliedschaft entstehen erst mit der Bestätigung.
//
// persons.email ist immer eine bestätigte Adresse: aus einem verifizierten
// IdP-Claim, vom Betreiber oder durch eine confirm- oder join-Herausforderung.
// Eine Adresse, die noch nicht bestätigt ist, steht nur in der Herausforderung.

const (
	ChallengeLogin   = "login"
	ChallengeConfirm = "confirm"
	ChallengeReset   = "reset"
	ChallengeJoin    = "join"

	// LoginChallengeTTL gilt für Anmelden und Zurücksetzen: wenige Minuten.
	LoginChallengeTTL = 10 * time.Minute
	// ConfirmChallengeTTL gilt für Bestätigen und Beitritt; die Person muss dafür
	// ihr Postfach öffnen und hat keinen Zeitdruck wie bei einer Anmeldung.
	ConfirmChallengeTTL = time.Hour

	maxChallengeAttempts = 5
	challengeCodeAlpha   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // ohne I, O, 0, 1
	challengeCodeLength  = 8
	// LoginLinkMaxTTL begrenzt Login-Links, die ein Gerät für sein Konto ausstellt.
	WebLoginLinkTTL = 120 * time.Second
)

var (
	// ErrEmailTaken: eine andere Person hat diese Adresse schon bestätigt.
	ErrEmailTaken = errors.New("email address belongs to another account")
	// ErrLoginLinkScope: the member has a footing outside this organization, so
	// the owner of this organization may not sign in as them.
	ErrLoginLinkScope = errors.New("this member belongs to more than this organization")
	// ErrNameTaken: der Name ist inzwischen vergeben.
	ErrNameTaken = errors.New("account name is taken")
)

const emailAuthSchema = `
CREATE TABLE IF NOT EXISTS email_challenges(
  id INTEGER PRIMARY KEY,
  purpose TEXT NOT NULL CHECK(purpose IN ('login','confirm','reset','join')),
  email TEXT NOT NULL, account_id INTEGER NOT NULL DEFAULT 0,
  code_hash TEXT NOT NULL, link_hash TEXT NOT NULL UNIQUE,
  payload TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS email_challenges_email ON email_challenges(purpose, email);
CREATE TABLE IF NOT EXISTS account_passwords(
  account_id INTEGER PRIMARY KEY REFERENCES persons(id) ON DELETE RESTRICT,
  hash TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_welcome(
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  org_id INTEGER NOT NULL, created_at TEXT NOT NULL, dismissed_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(account_id, org_id));
CREATE INDEX IF NOT EXISTS persons_email_lower ON persons(lower(email));
`

// ensureEmailAuth legt die Tabellen an und ergänzt account_codes um die
// Bindung eines Login-Links an Maschine und Token. Läuft nach migrateAccounts,
// weil persons.email dort entsteht.
func ensureEmailAuth(db *sql.DB) error {
	if _, err := db.Exec(emailAuthSchema); err != nil {
		return err
	}
	for _, c := range []struct{ name, ddl string }{
		{"token_id", `INTEGER NOT NULL DEFAULT 0`},
		{"machine", `TEXT NOT NULL DEFAULT ''`},
		{"issued_by", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := ensureColumn(db, "account_codes", c.name, c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeEmail macht aus einer getippten Adresse die Form, in der sie
// verglichen wird: klein geschrieben, ohne Leerraum. Eine Zeichenfolge ohne
// Adressgestalt ist ErrInvalidInput.
func NormalizeEmail(email string) (string, error) { return normalizeInviteEmail(email) }

// MaskEmail zeigt eine Adresse so, dass die Person sie erkennt, ein Fremder sie
// aber nicht liest: der erste Buchstabe, drei Sternchen und die Domain.
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return ""
	}
	first := []rune(email[:at])[0]
	return string(first) + "***" + email[at:]
}

// Challenge ist, was der Aufrufer für die Mail braucht. Klartext gibt es nur
// hier, einmal.
type Challenge struct {
	Purpose   string
	Email     string
	Code      string // 8 Zeichen, ohne Trennstrich
	Link      string // Token für den Link
	ExpiresAt time.Time
}

// FormatCode gruppiert den Code zum Lesen: ABCD-EFGH.
func FormatCode(code string) string {
	if len(code) == challengeCodeLength {
		return code[:4] + "-" + code[4:]
	}
	return code
}

// normalizeCode macht aus der Eingabe die gespeicherte Form: Groß, ohne Leerraum
// und Striche.
func normalizeCode(in string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case ' ', '\t', '-', '_', '.':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newChallengeCode() (string, error) {
	raw := make([]byte, challengeCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, challengeCodeLength)
	for i, b := range raw {
		out[i] = challengeCodeAlpha[int(b)%len(challengeCodeAlpha)] // 256 mod 32 = 0: gleichverteilt
	}
	return string(out), nil
}

func challengeCodeHash(purpose, email, code string) string {
	return hashToken(purpose + "\x00" + email + "\x00" + normalizeCode(code))
}

// liveAccountsByEmail zählt die aktiven Personenkonten mit dieser bestätigten
// Adresse. Genau eines darf sich damit anmelden; bei mehr als einem wäre unklar,
// wer gemeint ist (Altbestand), und es geht niemand.
func liveAccountsByEmail(q queryer, email string) (ids []int64, err error) {
	rows, err := q.Query(`SELECT id FROM persons WHERE lower(email)=? AND state='active' AND kind='person'`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func issueChallengeTx(tx execQueryer, purpose, email string, account int64, payload string, ttl time.Duration) (Challenge, error) {
	code, err := newChallengeCode()
	if err != nil {
		return Challenge{}, err
	}
	link, linkHash, err := newToken()
	if err != nil {
		return Challenge{}, err
	}
	// Es gilt immer nur die jüngste Mail; ältere Codes derselben Sache sind tot.
	if _, err := tx.Exec(`UPDATE email_challenges SET used_at=? WHERE purpose=? AND email=? AND account_id=? AND used_at=''`,
		now(), purpose, email, account); err != nil {
		return Challenge{}, err
	}
	// Alte Zeilen sind nutzlos; so wächst die Tabelle nicht.
	if _, err := tx.Exec(`DELETE FROM email_challenges WHERE expires_at<?`, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		return Challenge{}, err
	}
	expires := time.Now().UTC().Add(ttl)
	if _, err := tx.Exec(`INSERT INTO email_challenges(purpose, email, account_id, code_hash, link_hash, payload, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		purpose, email, account, challengeCodeHash(purpose, email, code), linkHash, payload, now(), expires.Format(time.RFC3339)); err != nil {
		return Challenge{}, err
	}
	return Challenge{Purpose: purpose, Email: email, Code: code, Link: link, ExpiresAt: expires}, nil
}

// IssueLoginChallenge stellt eine Anmelde-Mail aus, wenn die Adresse zu genau
// einem aktiven Konto gehört. Der zweite Wert sagt, ob es eine gibt; der
// Aufrufer antwortet in beiden Fällen gleich (keine Auskunft über Konten).
// purpose ist ChallengeLogin oder ChallengeReset.
func (s *Store) IssueLoginChallenge(purpose, email string) (Challenge, bool, error) {
	if purpose != ChallengeLogin && purpose != ChallengeReset {
		return Challenge{}, false, fmt.Errorf("%w: purpose", ErrInvalidInput)
	}
	if s.writer != nil {
		type result struct {
			c  Challenge
			ok bool
		}
		r, err := queueValue(s, []any{purpose, email}, func(d *Store, p []any) (result, error) {
			c, ok, err := d.IssueLoginChallenge(p[0].(string), p[1].(string))
			return result{c, ok}, err
		})
		return r.c, r.ok, err
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return Challenge{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Challenge{}, false, err
	}
	defer tx.Rollback()
	ids, err := liveAccountsByEmail(tx, email)
	if err != nil || len(ids) != 1 {
		return Challenge{}, false, err
	}
	c, err := issueChallengeTx(tx, purpose, email, ids[0], "", LoginChallengeTTL)
	if err != nil {
		return Challenge{}, false, err
	}
	return c, true, tx.Commit()
}

// IssueConfirmChallenge schickt die Bestätigung einer Adresse für ein Konto.
// Die Adresse wird erst mit dem Einlösen zur Adresse des Kontos.
func (s *Store) IssueConfirmChallenge(account, email string) (Challenge, error) {
	if s.writer != nil {
		return queueValue(s, []any{account, email}, func(d *Store, p []any) (Challenge, error) {
			return d.IssueConfirmChallenge(p[0].(string), p[1].(string))
		})
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return Challenge{}, err
	}
	email, err = NormalizeEmail(email)
	if err != nil {
		return Challenge{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Challenge{}, err
	}
	defer tx.Rollback()
	a, err := accountState(tx, id)
	if err != nil {
		return Challenge{}, err
	}
	if a.State != "active" {
		return Challenge{}, ErrAccountDisabled
	}
	// Eine neue Bestätigung ersetzt jede offene des Kontos, auch für eine andere Adresse.
	if _, err := tx.Exec(`UPDATE email_challenges SET used_at=? WHERE purpose='confirm' AND account_id=? AND used_at=''`, now(), id); err != nil {
		return Challenge{}, err
	}
	c, err := issueChallengeTx(tx, ChallengeConfirm, email, id, "", ConfirmChallengeTTL)
	if err != nil {
		return Challenge{}, err
	}
	return c, tx.Commit()
}

// PendingEmail ist die Adresse, deren Bestätigung für das Konto noch offen ist.
func (s *Store) PendingEmail(account string) string {
	if s.reader != nil {
		return s.reader.PendingEmail(account)
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return ""
	}
	var email string
	_ = s.db.QueryRow(`SELECT email FROM email_challenges WHERE purpose='confirm' AND account_id=? AND used_at='' AND expires_at>? ORDER BY id DESC LIMIT 1`,
		id, now()).Scan(&email)
	return email
}

type joinPayload struct {
	Invite string `json:"i"` // Hash des Einladungscodes
	Name   string `json:"n"`
}

// JoinForm sagt, was die Beitrittsseite einer Org-Einladung zeigt und verlangt.
type JoinForm struct {
	Inviter, Org, Project string
	ExpiresAt             string
	// BoundEmail ist die Adresse, an die die Einladung gebunden ist; leer, wenn
	// sie an keine gebunden ist. Nie im Klartext anzeigen, nur maskiert.
	BoundEmail string
}

// PreviewJoin liest eine offene Org-Einladung samt ihrer Bindung, ohne etwas zu
// verbrauchen. mailVerified sagt, ob die Instanz Adressen bestätigen kann (Mail
// oder IdP): sonst ist eine gebundene Einladung nicht einlösbar, und der Aufrufer
// erklärt das (Fehler ErrInvitationEmail mit dem Formular).
func (s *Store) PreviewJoin(code string, enforced, mailVerified bool) (JoinForm, error) {
	if s.reader != nil {
		return s.reader.PreviewJoin(code, enforced, mailVerified)
	}
	p, err := s.PreviewOrgInvitation(code, enforced, true)
	if err != nil {
		return JoinForm{}, err
	}
	f := JoinForm{Inviter: p.Inviter, Org: p.Org, Project: p.Project, ExpiresAt: p.ExpiresAt}
	if err := s.db.QueryRow(`SELECT email FROM invitations WHERE code_hash=?`, hashToken(code)).Scan(&f.BoundEmail); err != nil {
		return JoinForm{}, err
	}
	if f.BoundEmail != "" && !mailVerified {
		return f, ErrInvitationEmail
	}
	return f, nil
}

// BeginJoinByMail führt einen Beitritt über eine Org-Einladung aus, wenn die
// Instanz Mails schickt.
//
// Ist die Einladung nicht an eine Adresse gebunden, entsteht das Konto sofort
// (wie ohne Mail) und die angegebene Adresse wartet auf ihre Bestätigung; das
// Konto kann sich damit erst per Mail anmelden, wenn sie bestätigt ist. Ist sie
// gebunden, entsteht noch nichts: die Mail geht an die gebundene Adresse, und
// erst ihr Einlösen legt Konto und Mitgliedschaft an. Der zurückgegebene
// Account ist im ersten Fall gesetzt (ID nicht leer), im zweiten nicht.
func (s *Store) BeginJoinByMail(code, name, email string) (Account, Challenge, error) {
	if s.writer != nil {
		type result struct {
			a Account
			c Challenge
		}
		r, err := queueValue(s, []any{code, name, email}, func(d *Store, p []any) (result, error) {
			a, c, err := d.BeginJoinByMail(p[0].(string), p[1].(string), p[2].(string))
			return result{a, c}, err
		})
		return r.a, r.c, err
	}
	enforced := s.AccessEnforced()
	if _, err := s.PreviewOrgInvitation(code, enforced, true); err != nil {
		return Account{}, Challenge{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, Challenge{}, err
	}
	defer tx.Rollback()
	var bound string
	if err := tx.QueryRow(`SELECT email FROM invitations WHERE code_hash=?`, hashToken(code)).Scan(&bound); err != nil {
		return Account{}, Challenge{}, ErrCodeInvalid
	}
	if free, _ := inviteNameFreeTx(tx, name); !free {
		return Account{}, Challenge{}, ErrNameTaken
	}
	if bound != "" {
		raw, _ := json.Marshal(joinPayload{Invite: hashToken(code), Name: name})
		c, err := issueChallengeTx(tx, ChallengeJoin, bound, 0, string(raw), ConfirmChallengeTTL)
		if err != nil {
			return Account{}, Challenge{}, err
		}
		return Account{}, c, tx.Commit()
	}
	email, err = NormalizeEmail(email)
	if err != nil {
		return Account{}, Challenge{}, err
	}
	id, err := createInvitedAccountTx(tx, name, "", code, "", enforced)
	if err != nil {
		return Account{}, Challenge{}, err
	}
	c, err := issueChallengeTx(tx, ChallengeConfirm, email, id, "", ConfirmChallengeTTL)
	if err != nil {
		return Account{}, Challenge{}, err
	}
	a, err := accountState(tx, id)
	if err != nil {
		return Account{}, Challenge{}, err
	}
	return a, c, tx.Commit()
}

func inviteNameFreeTx(q queryer, wanted string) (bool, error) {
	base, exact := inviteBaseName(wanted)
	if !exact {
		return false, nil
	}
	taken, err := accountNameTakenByOtherTx(q, base, 0)
	return err == nil && !taken, err
}

// Redemption beschreibt das Einlösen einer Herausforderung: entweder mit Adresse
// und Code oder mit dem Link-Token.
type Redemption struct {
	Purpose string
	Email   string // mit Code
	Code    string
	Link    string // statt Adresse und Code
	// PasswordHash setzt bei ChallengeReset das neue Passwort.
	PasswordHash string
}

type challengeRow struct {
	id      int64
	email   string
	account int64
	payload string
}

// RedeemChallenge löst eine Herausforderung ein und führt in derselben
// Transaktion aus, wozu sie dient. Jeder Grund, aus dem es nicht geht (falsch,
// abgelaufen, benutzt, zu viele Fehlversuche, unbekannte Adresse), ist
// ErrCodeInvalid; die Fehlversuche einer Herausforderung werden auch dann
// gezählt, wenn der Aufrufer die Transaktion sonst nicht behielte.
func (s *Store) RedeemChallenge(in Redemption) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (Account, error) { return d.RedeemChallenge(p[0].(Redemption)) })
	}
	switch in.Purpose {
	case ChallengeLogin, ChallengeConfirm, ChallengeReset, ChallengeJoin:
	default:
		return Account{}, ErrCodeInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	var row challengeRow
	if in.Link != "" {
		err = tx.QueryRow(`SELECT id, email, account_id, payload FROM email_challenges WHERE link_hash=? AND purpose=? AND used_at='' AND expires_at>?`,
			hashToken(in.Link), in.Purpose, now()).Scan(&row.id, &row.email, &row.account, &row.payload)
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrCodeInvalid
		}
		if err != nil {
			return Account{}, err
		}
	} else {
		email, err := NormalizeEmail(in.Email)
		if err != nil || normalizeCode(in.Code) == "" || len(in.Code) > 32 {
			return Account{}, ErrCodeInvalid
		}
		var codeHash string
		var attempts int
		err = tx.QueryRow(`SELECT id, email, account_id, payload, code_hash, attempts FROM email_challenges
			WHERE purpose=? AND email=? AND used_at='' AND expires_at>? ORDER BY id DESC LIMIT 1`,
			in.Purpose, email, now()).Scan(&row.id, &row.email, &row.account, &row.payload, &codeHash, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrCodeInvalid
		}
		if err != nil {
			return Account{}, err
		}
		if attempts >= maxChallengeAttempts {
			return Account{}, ErrCodeInvalid
		}
		if subtle.ConstantTimeCompare([]byte(codeHash), []byte(challengeCodeHash(in.Purpose, email, in.Code))) != 1 {
			if _, err := tx.Exec(`UPDATE email_challenges SET attempts=attempts+1 WHERE id=?`, row.id); err != nil {
				return Account{}, err
			}
			if err := tx.Commit(); err != nil {
				return Account{}, err
			}
			return Account{}, ErrCodeInvalid
		}
	}
	res, err := tx.Exec(`UPDATE email_challenges SET used_at=? WHERE id=? AND used_at=''`, now(), row.id)
	if err != nil {
		return Account{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Account{}, ErrCodeInvalid
	}
	var account int64
	switch in.Purpose {
	case ChallengeLogin, ChallengeConfirm, ChallengeReset:
		account = row.account
		a, err := accountState(tx, account)
		if err != nil {
			return Account{}, ErrCodeInvalid
		}
		if a.State != "active" {
			return Account{}, ErrAccountDisabled
		}
		switch in.Purpose {
		case ChallengeLogin, ChallengeReset:
			// Die Adresse muss noch die des Kontos sein: wurde sie inzwischen
			// geändert, ist die alte Mail wertlos.
			if !strings.EqualFold(a.Email, row.email) {
				return Account{}, ErrCodeInvalid
			}
		case ChallengeConfirm:
			ids, err := liveAccountsByEmail(tx, row.email)
			if err != nil {
				return Account{}, err
			}
			for _, id := range ids {
				if id != account {
					return Account{}, ErrEmailTaken
				}
			}
			if _, err := tx.Exec(`UPDATE persons SET email=? WHERE id=?`, row.email, account); err != nil {
				return Account{}, err
			}
		}
		if in.Purpose == ChallengeReset {
			if in.PasswordHash == "" {
				return Account{}, ErrInvalidInput
			}
			if err := setPasswordTx(tx, account, in.PasswordHash); err != nil {
				return Account{}, err
			}
		}
	case ChallengeJoin:
		var p joinPayload
		if json.Unmarshal([]byte(row.payload), &p) != nil || p.Invite == "" {
			return Account{}, ErrCodeInvalid
		}
		ids, err := liveAccountsByEmail(tx, row.email)
		if err != nil {
			return Account{}, err
		}
		if len(ids) > 0 {
			// Die Adresse hat schon ein Konto: die Person meldet sich damit an und
			// nimmt die Einladung dort an, statt ein zweites Konto zu bekommen.
			return Account{}, ErrEmailTaken
		}
		if free, _ := inviteNameFreeTx(tx, p.Name); !free {
			return Account{}, ErrNameTaken
		}
		if account, err = createInvitedAccountHashTx(tx, p.Name, row.email, p.Invite, "", s.AccessEnforced()); err != nil {
			return Account{}, err
		}
	}
	a, err := accountState(tx, account)
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit()
}

// PeekChallenge sagt, ob ein Link noch etwas einlöst, ohne ihn zu verbrauchen,
// und für welche Adresse. Die Seite, die ein Link öffnet, fragt damit, ob sie
// eine Bestätigung oder einen Hinweis zeigt; das Einlösen geschieht erst per POST
// (Mail-Vorschauen rufen Links ab).
func (s *Store) PeekChallenge(purpose, link string) (email string, ok bool) {
	if s.reader != nil {
		return s.reader.PeekChallenge(purpose, link)
	}
	err := s.db.QueryRow(`SELECT email FROM email_challenges WHERE link_hash=? AND purpose=? AND used_at='' AND expires_at>?`,
		hashToken(link), purpose, now()).Scan(&email)
	return email, err == nil
}

// ---------------------------------------------------------------- Passwort

func setPasswordTx(tx execQueryer, account int64, hash string) error {
	_, err := tx.Exec(`INSERT INTO account_passwords(account_id, hash, updated_at) VALUES(?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at`, account, hash, now())
	return err
}

// SetPassword speichert den Hash des Passworts eines Kontos.
func (s *Store) SetPassword(account, hash string) error {
	if s.writer != nil {
		return queueWrite(s, []any{account, hash}, func(d *Store, p []any) error { return d.SetPassword(p[0].(string), p[1].(string)) })
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrInvalidInput
	}
	if !s.AccountActive(account) {
		return ErrAccountDisabled
	}
	return setPasswordTx(s.db, id, hash)
}

// PasswordHash liefert den gespeicherten Hash eines Kontos, leer ohne Passwort.
func (s *Store) PasswordHash(account string) string {
	if s.reader != nil {
		return s.reader.PasswordHash(account)
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return ""
	}
	var h string
	_ = s.db.QueryRow(`SELECT hash FROM account_passwords WHERE account_id=?`, id).Scan(&h)
	return h
}

// PasswordLogin sucht das Konto zu einer Adresse für die Anmeldung mit Passwort:
// genau ein aktives Konto mit dieser bestätigten Adresse. hash ist leer, wenn es
// kein Passwort hat; ok ist falsch, wenn es das Konto nicht gibt.
func (s *Store) PasswordLogin(email string) (account Account, hash string, ok bool) {
	if s.reader != nil {
		return s.reader.PasswordLogin(email)
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return Account{}, "", false
	}
	ids, err := liveAccountsByEmail(s.db, email)
	if err != nil || len(ids) != 1 {
		return Account{}, "", false
	}
	a, err := scanAccount(s.db.QueryRow(accountSelect+` WHERE id=?`, ids[0]))
	if err != nil {
		return Account{}, "", false
	}
	_ = s.db.QueryRow(`SELECT hash FROM account_passwords WHERE account_id=?`, ids[0]).Scan(&hash)
	return a, hash, true
}

// ---------------------------------------------------------------- Login-Link

// loginLinkScopeTx says whether an owner of org may sign in as target. An owner
// of one organization must not be able to take over an account that has more
// than this organization: membership of another organization, or repositories in
// the person's own namespace. Such a member signs in by mail, or with ctx web.
func loginLinkScopeTx(q queryer, orgID, target int64) error {
	// Deny by default: everything the person holds must belong to this organization.
	// Their own namespace counts only while it is empty.
	count := func(query string, args ...any) (int, error) {
		var n int
		err := q.QueryRow(query, args...).Scan(&n)
		return n, err
	}
	for _, check := range []struct {
		query string
		args  []any
	}{
		// a seat (member, owner or guest) in any other organization
		{`SELECT COUNT(*) FROM org_members m JOIN orgs o ON o.id=m.org_id
			WHERE m.account_id=? AND m.org_id<>? AND NOT (o.kind=? AND o.account_id=m.account_id)`, []any{target, orgID, OrgKindUser}},
		{`SELECT COUNT(*) FROM org_guests WHERE account_id=? AND org_id<>?`, []any{target, orgID}},
		// a role in a project of another organization
		{`SELECT COUNT(*) FROM project_members pm JOIN projects p ON p.id=pm.project_id WHERE pm.account_id=? AND p.org_id<>?`, []any{target, orgID}},
		// repositories in their own namespace
		{`SELECT COUNT(*) FROM projects p JOIN orgs o ON o.id=p.org_id WHERE o.kind=? AND o.account_id=?`, []any{OrgKindUser, target}},
	} {
		n, err := count(check.query, check.args...)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrLoginLinkScope
		}
	}
	return nil
}

// LoginLinkAllowed says whether the member list may offer a login link for the
// member (the same rules as IssueMemberLoginLink, without issuing one).
func (s *Store) LoginLinkAllowed(orgID int64, targetPrincipal string) bool {
	if s.reader != nil {
		return s.reader.LoginLinkAllowed(orgID, targetPrincipal)
	}
	target, err := parsePersonPrincipalID(targetPrincipal)
	if err != nil || orgRoleTx(s.db, orgID, target) != OrgMember {
		return false
	}
	a, err := scanAccount(s.db.QueryRow(accountSelect+` WHERE id=?`, target))
	if err != nil || a.State != "active" || a.Kind == "service" || a.Admin {
		return false
	}
	return loginLinkScopeTx(s.db, orgID, target) == nil
}

// IssueMemberLoginLink gibt einem Owner der Organisation einen Login-Link für ein
// Mitglied, damit sich jemand ohne Mailversand wieder anmelden kann. Nur für ein
// aktives Personenkonto mit der Rolle member: ein Owner bekommt keinen Link für
// einen anderen Owner, und ein Instanzadmin keinen von einem Owner.
func (s *Store) IssueMemberLoginLink(actorPrincipal string, orgID int64, targetPrincipal string) (string, time.Duration, error) {
	if s.writer != nil {
		type result struct {
			code string
			ttl  time.Duration
		}
		r, err := queueValue(s, []any{actorPrincipal, orgID, targetPrincipal}, func(d *Store, p []any) (result, error) {
			c, t, err := d.IssueMemberLoginLink(p[0].(string), p[1].(int64), p[2].(string))
			return result{c, t}, err
		})
		return r.code, r.ttl, err
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return "", 0, err
	}
	target, err := parsePersonPrincipalID(targetPrincipal)
	if err != nil {
		return "", 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return "", 0, ErrNotOrgOwner
	}
	if orgRoleTx(tx, orgID, target) != OrgMember {
		return "", 0, ErrNotOrgMember
	}
	a, err := accountState(tx, target)
	if err != nil {
		return "", 0, err
	}
	if a.State != "active" || a.Kind == "service" || a.Admin || target == actor {
		return "", 0, ErrInvalidInput
	}
	if err := loginLinkScopeTx(tx, orgID, target); err != nil {
		return "", 0, err
	}
	code, err := insertLoginCodeTx(tx, target, LoginLinkTTL, actor, 0, "")
	if err != nil {
		return "", 0, err
	}
	if err := orgEvent(tx, orgID, "login_link", principalOfID(actor), principalOfID(target), ""); err != nil {
		return "", 0, err
	}
	return code, LoginLinkTTL, tx.Commit()
}

// ErrDeviceTokenRequired: only a token that a person approved in the browser
// (kind device) may ask for a web sign-in; a pasted or provisioned token keeps
// its read-only web session.
var ErrDeviceTokenRequired = errors.New("a sign-in link needs a device token")

// IssueWebLoginLink gibt dem Konto eines Geräts einen Login-Link für die
// Weboberfläche (ctx web): kurz gültig, einmal einlösbar, an das Token und die
// Maschine gebunden, die ihn angefordert hat. Wird das Token widerrufen, bevor
// der Link eingelöst ist, taugt er nicht mehr.
func (s *Store) IssueWebLoginLink(account string, tokenID int64, machine string) (string, time.Duration, error) {
	if s.writer != nil {
		type result struct {
			code string
			ttl  time.Duration
		}
		r, err := queueValue(s, []any{account, tokenID, machine}, func(d *Store, p []any) (result, error) {
			c, t, err := d.IssueWebLoginLink(p[0].(string), p[1].(int64), p[2].(string))
			return result{c, t}, err
		})
		return r.code, r.ttl, err
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return "", 0, err
	}
	if tokenID == 0 {
		return "", 0, ErrInvalidInput
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	a, err := accountState(tx, id)
	if err != nil {
		return "", 0, err
	}
	if a.State != "active" || a.Kind == "service" {
		return "", 0, ErrAccountDisabled
	}
	var kind string
	if tx.QueryRow(`SELECT kind FROM api_tokens WHERE id=? AND account_id=?`, tokenID, id).Scan(&kind) != nil || kind != "device" {
		return "", 0, ErrDeviceTokenRequired
	}
	code, err := insertLoginCodeTx(tx, id, WebLoginLinkTTL, 0, tokenID, machine)
	if err != nil {
		return "", 0, err
	}
	return code, WebLoginLinkTTL, tx.Commit()
}

func insertLoginCodeTx(tx execQueryer, account int64, ttl time.Duration, issuer, tokenID int64, machine string) (string, error) {
	code, hash, err := newCode()
	if err != nil {
		return "", err
	}
	// Spent and expired links are of no use to anyone; so the table does not grow.
	if _, err := tx.Exec(`DELETE FROM account_codes WHERE kind='login' AND expires_at<?`, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO account_codes(code_hash, kind, account_id, expires_at, issued_by, token_id, machine) VALUES(?,?,?,?,?,?,?)`,
		hash, CodeLogin, account, time.Now().UTC().Add(ttl).Format(time.RFC3339), issuer, tokenID, machine); err != nil {
		return "", err
	}
	return code, nil
}

// LoginLinkRestricted says whether a login link was made by someone other than
// the host: an owner for a member, or a device (ctx web). A session that starts
// from such a link may not change how the account signs in or hand out
// credentials. A link from the host (ctx account login-link) is the operator's
// own way of onboarding a person and is not restricted. Looking does not use the
// link up.
func (s *Store) LoginLinkRestricted(code string) bool {
	if s.reader != nil {
		return s.reader.LoginLinkRestricted(code)
	}
	var issuer, token int64
	if s.db.QueryRow(`SELECT issued_by, token_id FROM account_codes WHERE code_hash=? AND kind='login'`, hashToken(code)).Scan(&issuer, &token) != nil {
		return false
	}
	return issuer != 0 || token != 0
}

// loginCodeTokenValidTx prüft bei einem an ein Gerät gebundenen Login-Link, ob
// dessen Token noch gilt.
func loginCodeTokenValidTx(tx execQueryer, codeHash string) bool {
	var tokenID int64
	if tx.QueryRow(`SELECT token_id FROM account_codes WHERE code_hash=?`, codeHash).Scan(&tokenID) != nil {
		return false
	}
	if tokenID == 0 {
		return true
	}
	var one int
	return tx.QueryRow(`SELECT 1 FROM api_tokens t JOIN persons p ON p.id=t.account_id WHERE t.id=? AND t.revoked_at='' AND (t.expires_at='' OR t.expires_at>?) AND p.state='active'`,
		tokenID, now()).Scan(&one) == nil
}

// ---------------------------------------------------------------- Willkommen

// WelcomeCard ist die Willkommenskarte eines Kontos für eine Organisation.
type WelcomeCard struct {
	OrgID   int64
	OrgName string
	OrgSlug string
}

// startWelcomeTx legt die Willkommenskarte an: beim ersten Konto einer Instanz
// und bei jedem Beitritt in eine Organisation.
func startWelcomeTx(tx execQueryer, account, orgID int64) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO account_welcome(account_id, org_id, created_at) VALUES(?,?,?)`, account, orgID, now())
	return err
}

// WelcomeCards liefert die offenen Willkommenskarten eines Kontos, neueste zuerst.
// Eine Organisation, aus der das Konto inzwischen ausgetreten ist, zählt nicht.
func (s *Store) WelcomeCards(account string) []WelcomeCard {
	if s.reader != nil {
		return s.reader.WelcomeCards(account)
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT o.id, o.name, o.slug FROM account_welcome w
		JOIN orgs o ON o.id=w.org_id JOIN org_members m ON m.org_id=w.org_id AND m.account_id=w.account_id
		WHERE w.account_id=? AND w.dismissed_at='' ORDER BY w.created_at DESC, o.id DESC LIMIT 3`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []WelcomeCard
	for rows.Next() {
		var c WelcomeCard
		if rows.Scan(&c.OrgID, &c.OrgName, &c.OrgSlug) == nil {
			out = append(out, c)
		}
	}
	return out
}

// DismissWelcome blendet die Karte aus (weggeklickt oder erledigt).
func (s *Store) DismissWelcome(account string, orgID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{account, orgID}, func(d *Store, p []any) error { return d.DismissWelcome(p[0].(string), p[1].(int64)) })
	}
	id, err := parsePersonPrincipalID(account)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE account_welcome SET dismissed_at=? WHERE account_id=? AND org_id=? AND dismissed_at=''`, now(), id, orgID)
	return err
}
