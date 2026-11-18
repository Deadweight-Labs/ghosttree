package store

import (
	"errors"
	"strings"
	"time"
)

// Grenzen für Einladungsmails je Tag. Sie stehen in der Datenbank, damit ein
// Neustart sie nicht leert: der Server schickt sonst über seine Adresse und seinen
// Ruf Mails für jeden, der ein Konto hat.
const (
	maxInviteMailsPerAccount = 20
	maxInviteMailsPerOrg     = 50
	maxInviteMailsPerAddress = 3
	inviteMailWindow         = 24 * time.Hour
)

// ErrInviteMailLimit: für diese Adresse, dieses Konto oder diese Organisation sind
// heute genug Einladungsmails gegangen. Die Einladung selbst bleibt gültig; nur die
// Mail unterbleibt.
var ErrInviteMailLimit = errors.New("too many invitation mails")

const inviteMailSchema = `
CREATE TABLE IF NOT EXISTS invite_mail_log(
  id INTEGER PRIMARY KEY,
  sender TEXT NOT NULL, org_id INTEGER NOT NULL, target_hash TEXT NOT NULL, sent_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS invite_mail_log_sent ON invite_mail_log(sent_at);
`

// ReserveInviteMail zählt eine Einladungsmail an to gegen die Grenzen und merkt sie
// vor. Ist eine Grenze erreicht, ist es ErrInviteMailLimit und nichts wird gemerkt.
// Die Adresse liegt nur als Hash in der Tabelle.
func (s *Store) ReserveInviteMail(sender string, orgID int64, to string) error {
	if s.writer != nil {
		return queueWrite(s, []any{sender, orgID, to}, func(d *Store, p []any) error {
			return d.ReserveInviteMail(p[0].(string), p[1].(int64), p[2].(string))
		})
	}
	target := hashToken("invite-mail\x00" + mailboxKey(to))
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	since := time.Now().UTC().Add(-inviteMailWindow).Format(time.RFC3339)
	// Old rows are of no use to anyone.
	if _, err := tx.Exec(`DELETE FROM invite_mail_log WHERE sent_at<?`, since); err != nil {
		return err
	}
	for _, c := range []struct {
		column string
		arg    any
		limit  int
	}{
		{"sender", sender, maxInviteMailsPerAccount},
		{"org_id", orgID, maxInviteMailsPerOrg},
		{"target_hash", target, maxInviteMailsPerAddress},
	} {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM invite_mail_log WHERE `+c.column+`=?`, c.arg).Scan(&n); err != nil {
			return err
		}
		if n >= c.limit {
			return ErrInviteMailLimit
		}
	}
	if _, err := tx.Exec(`INSERT INTO invite_mail_log(sender, org_id, target_hash, sent_at) VALUES(?,?,?,?)`,
		sender, orgID, target, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// mailboxKey is what two addresses that reach the same mailbox share: lower case,
// no +tag in the local part, no quotes, no dot at the end of the domain. The limit
// per address counts by it, so a +1, +2, +3 does not make three mailboxes of one.
func mailboxKey(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndex(addr, "@")
	if at < 1 {
		return addr
	}
	local, domain := addr[:at], strings.TrimRight(addr[at+1:], ".")
	local = strings.Trim(local, `"`)
	if plus := strings.Index(local, "+"); plus > 0 {
		local = local[:plus]
	}
	return local + "@" + domain
}
