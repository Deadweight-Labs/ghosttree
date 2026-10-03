package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Einladungslinks (/join/<code>) vergeben nur eine Projektrolle: member oder
// guest. Owner und Lead vergibt ein Mensch im Browser, nie ein Link.
const (
	DefaultGuestInvitationTTL = 3 * 24 * time.Hour
)

// ErrGuestLinkNeedsEnforcement: Gast-Links gibt es nur, wenn die
// Sichtbarkeit durchgesetzt wird (GHOSTTREE_ENFORCE_ACCESS=1).
var ErrGuestLinkNeedsEnforcement = errors.New("guest links need access enforcement (GHOSTTREE_ENFORCE_ACCESS=1); without it a guest would see more than the page promises")

// InvitePreview ist alles, was ein Inhaber des Codes vor der Anmeldung sieht.
// Bewusst klein: keine Ids, keine Zähler, keine Namen anderer Personen; der Name
// der einladenden Person ohne E-Mail-Domain.
type InvitePreview struct {
	Inviter   string // Anzeigename der einladenden Person
	Org       string
	Project   string
	Role      string
	ExpiresAt string
}

// ensureInvitationProjectRole ergänzt auf einer alten Datenbank die
// Projektrolle einer Einladung. Die Spalte project_id gibt es schon.
func ensureInvitationProjectRole(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(invitations)`)
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
		if name == "project_role" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE invitations ADD COLUMN project_role TEXT NOT NULL DEFAULT '' CHECK(project_role IN ('','member','guest'))`)
	return err
}

// CreateProjectInvitation stellt eine Einladung für genau ein Projekt aus (nur
// Owner der Organisation, zu der das Projekt gehört) und gibt den Code genau
// einmal zurück. Die Rolle ist member oder guest. Ohne ttl gelten 7 Tage, für
// Gäste 3. Es gibt keine E-Mail-Bindung: ein Link soll weitergegeben werden
// können, die kurze Frist und der Widerruf begrenzen das.
func (s *Store) CreateProjectInvitation(actorPrincipal string, orgID int64, remote, projectRole string, ttl time.Duration) (string, Invitation, error) {
	type result struct {
		code string
		inv  Invitation
	}
	if s.writer != nil {
		r, err := queueValue(s, []any{actorPrincipal, orgID, remote, projectRole, ttl}, func(d *Store, p []any) (result, error) {
			code, inv, err := d.CreateProjectInvitation(p[0].(string), p[1].(int64), p[2].(string), p[3].(string), p[4].(time.Duration))
			return result{code, inv}, err
		})
		return r.code, r.inv, err
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return "", Invitation{}, err
	}
	if projectRole != RoleMember && projectRole != RoleGuest {
		return "", Invitation{}, fmt.Errorf("%w: a link grants member or guest, nothing else", ErrInvalidInput)
	}
	if ttl <= 0 {
		ttl = DefaultInvitationTTL
		if projectRole == RoleGuest {
			ttl = DefaultGuestInvitationTTL
		}
	}
	if ttl > MaxInvitationTTL {
		return "", Invitation{}, fmt.Errorf("%w: invitations live at most %d days", ErrInvalidInput, int(MaxInvitationTTL/(24*time.Hour)))
	}
	// Ein Gast sieht nur, was die Durchsetzung ihm lässt. Ohne sie erlaubt die
	// Matrix im Log-Modus alles, und der Link würde mehr gewähren, als die
	// Seite verspricht.
	if projectRole == RoleGuest && !s.AccessEnforced() {
		return "", Invitation{}, ErrGuestLinkNeedsEnforcement
	}
	remote, err = normalizeRemote(remote)
	if err != nil {
		return "", Invitation{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", Invitation{}, err
	}
	defer tx.Rollback()
	if orgRoleTx(tx, orgID, actor) != OrgOwner {
		return "", Invitation{}, ErrNotOrgOwner
	}
	var projectID, projectOrg int64
	if tx.QueryRow(`SELECT id, org_id FROM projects WHERE remote=?`, remote).Scan(&projectID, &projectOrg) != nil || projectOrg != orgID {
		return "", Invitation{}, ErrProjectNotFound
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
	res, err := tx.Exec(`INSERT INTO invitations(org_id, project_id, project_role, role, email, code_hash, invited_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		orgID, projectID, projectRole, OrgMember, "", hash, actor, now(), expires)
	if err != nil {
		return "", Invitation{}, err
	}
	id, _ := res.LastInsertId()
	if err := orgEvent(tx, orgID, "invite", principalOfID(actor), "", projectRole+" of "+remote); err != nil {
		return "", Invitation{}, err
	}
	inv, err := invitationTx(tx, id)
	if err != nil {
		return "", Invitation{}, err
	}
	return code, inv, tx.Commit()
}

// OpenProjectInvitation sagt, ob ein Code eine noch offene Projekt-Einladung
// ist, mit genau der Gültigkeit der Vorschau (enforced wie dort). Für jeden anderen Code (auch einen unbekannten, abgelaufenen oder
// verbrauchten) ist die Antwort dieselbe, damit niemand an ihr Codes
// unterscheidet.
func (s *Store) OpenProjectInvitation(code string, enforced bool) bool {
	if s.reader != nil {
		return s.reader.OpenProjectInvitation(code, enforced)
	}
	_, err := s.PreviewInvitation(code, enforced)
	return err == nil
}

// PreviewInvitation liest (enforced: ob die Sichtbarkeit durchgesetzt wird; ohne sie gilt ein Gast-Link nicht) eine Einladung, ohne etwas zu verbrauchen oder zu
// schreiben. Jeder Grund, aus dem sie nicht (mehr) einlösbar ist, ergibt
// dieselbe Antwort ErrCodeInvalid und läuft über genau eine Abfrage: nicht
// vorhanden, abgelaufen, verbraucht, widerrufen, Einlader nicht mehr Owner,
// Projekt verschoben oder gelöscht, Gast-Link ohne durchgesetzte Sichtbarkeit, und eine Einladung, die kein
// Link-Rollenpaar (Projekt plus member/guest) trägt. Für einen Fremden ist
// all das nicht unterscheidbar (Pitfall #2447).
func (s *Store) PreviewInvitation(code string, enforced bool) (InvitePreview, error) {
	if s.reader != nil {
		return s.reader.PreviewInvitation(code, enforced)
	}
	var p InvitePreview
	err := s.db.QueryRow(`SELECT ip.name, o.name, COALESCE(NULLIF(pr.name,''), pr.remote), i.project_role, i.expires_at
		FROM invitations i
		JOIN persons ip ON ip.id = i.invited_by
		JOIN orgs o ON o.id = i.org_id
		JOIN org_members m ON m.org_id = i.org_id AND m.account_id = i.invited_by AND m.role = ?
		JOIN projects pr ON pr.id = i.project_id AND pr.org_id = i.org_id
		WHERE i.code_hash = ? AND i.accepted_at = '' AND i.revoked_at = '' AND i.expires_at > ?
		  AND i.email = '' AND i.project_role IN (?, ?) AND (i.project_role <> ? OR ?)`,
		OrgOwner, hashToken(code), now(), RoleMember, RoleGuest, RoleGuest, enforced).Scan(&p.Inviter, &p.Org, &p.Project, &p.Role, &p.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InvitePreview{}, ErrCodeInvalid
	}
	p.Inviter = inviterDisplay(p.Inviter)
	return p, err
}

// PreviewOrgInvitation liest eine offene Einladung in eine Organisation (mit
// oder ohne Projekt, mit oder ohne E-Mail-Bindung), ohne etwas zu verbrauchen.
// Sie zeigt nur, was die Einlösung auch gelten ließe: die einladende Person ist
// noch Owner, ein Projekt liegt noch in der Organisation und trägt member oder
// guest (guest nur bei durchgesetzter Sichtbarkeit), und eine an eine E-Mail
// gebundene Einladung zeigt sich nur dort, wo ein Identitätsanbieter die Adresse
// bestätigen kann (viaIdP). Jeder andere Fall ergibt ErrCodeInvalid, für einen
// Fremden nicht von "unbekannt" zu unterscheiden. Gezeigt werden nur Name der
// einladenden Person (siehe inviterDisplay), Organisation, Projekt (falls eins)
// und Ablauf.
func (s *Store) PreviewOrgInvitation(code string, enforced, viaIdP bool) (InvitePreview, error) {
	if s.reader != nil {
		return s.reader.PreviewOrgInvitation(code, enforced, viaIdP)
	}
	var p InvitePreview
	err := s.db.QueryRow(`SELECT ip.name, o.name, COALESCE((SELECT COALESCE(NULLIF(pr.name,''), pr.remote) FROM projects pr WHERE pr.id = i.project_id), ''), i.project_role, i.expires_at
		FROM invitations i
		JOIN persons ip ON ip.id = i.invited_by
		JOIN orgs o ON o.id = i.org_id
		JOIN org_members m ON m.org_id = i.org_id AND m.account_id = i.invited_by AND m.role = ?
		WHERE i.code_hash = ? AND i.accepted_at = '' AND i.revoked_at = '' AND i.expires_at > ?
		  AND (i.project_id = 0 OR EXISTS (SELECT 1 FROM projects pr WHERE pr.id = i.project_id AND pr.org_id = i.org_id
		       AND i.project_role IN (?, ?) AND (i.project_role <> ? OR ?)))
		  AND (i.email = '' OR ?)`,
		OrgOwner, hashToken(code), now(), RoleMember, RoleGuest, RoleGuest, enforced, viaIdP).Scan(&p.Inviter, &p.Org, &p.Project, &p.Role, &p.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InvitePreview{}, ErrCodeInvalid
	}
	p.Inviter = inviterDisplay(p.Inviter)
	return p, err
}

// inviterDisplay: Ein Anzeigename kann eine E-Mail-Adresse sein (OIDC
// preferred_username). Vor der Anmeldung sieht ein Fremder den Namen, also nur
// der lokale Teil, nie die Adresse.
func inviterDisplay(name string) string {
	if at := strings.Index(name, "@"); at > 0 {
		return name[:at]
	}
	if strings.HasPrefix(name, "@") {
		return strings.TrimLeft(name, "@")
	}
	return name
}
