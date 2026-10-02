package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// Autorität einer Nachricht für ihren Empfänger (Spec 7.1, 7.4, 7.5).
const (
	AuthorityDirective = "directive"
	AuthorityRequest   = "request"
)

// MessageAuthority sind die abgeleiteten, nie gespeicherten Angaben zu einer
// Nachricht aus Sicht eines Empfängers. Sie werden beim Lesen aus den aktuellen
// Rollen berechnet: eine Herabstufung gilt sofort für alles, was noch nicht
// gelesen wurde.
type MessageAuthority struct {
	SenderRole    string
	RecipientRole string
	Authority     string
}

// AuthorityFor ist die einzige Regel: eine Anweisung ist es nur, wenn der Rang
// des Absenders über dem des Empfängers liegt UND der Absender serverseitig
// verifiziert ist. Gleicher Rang, fehlende Rolle (Rang 0) und unverifizierte
// Absender sind Bitten.
func AuthorityFor(senderRank, recipientRank int, verified bool) string {
	if verified && senderRank > 0 && senderRank > recipientRank {
		return AuthorityDirective
	}
	return AuthorityRequest
}

// humanAuthorityKey ist der org_state-Schlüssel der Migrationsmarke: die
// höchste Nachrichten-ID zum Zeitpunkt, an dem dieser Code zum ersten Mal auf
// der Datenbank lief. Vor Paket 6 wurde jeder Beitrag ohne Agenten-ID als human
// gespeichert, auch Bearer- und CLI-Posts. Nur Nachrichten NACH der Marke sind
// als human verifiziert; ältere human-Posts zählen als unverifiziert.
const humanAuthorityKey = "human_authority_after"

// ensureHumanAuthorityMarker setzt die Marke genau einmal. Eine neue
// Datenbank bekommt 0: alles darin entstand unter der neuen Regel.
func ensureHumanAuthorityMarker(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO org_state(key, value)
		SELECT ?, CAST(COALESCE(MAX(id), 0) AS TEXT) FROM coord_messages`, humanAuthorityKey)
	return err
}

// authorityCtx bewertet viele Nachrichten mit konstant vielen Abfragen: das
// Projekt und die Rolle des Empfängers und die Migrationsmarke werden einmal
// gelesen, die Rolle eines Absenders einmal je (Art, Konto, Absender).
type authorityCtx struct {
	q       rowQuerier
	project string
	// recipient ist der Agent, für den bewertet wird; leer bei reiner
	// Absenderanzeige im Browser.
	recipient     string
	recipientRole RoleInfo
	// recipientHasRole: das KONTO des Empfängers hat selbst eine Rolle im
	// Projekt. Die Untergrenze guest für Konten ohne Rolle reicht nicht, um
	// angewiesen zu werden.
	recipientHasRole bool
	humanAfter       int64
	senders          map[string]senderInfo
}

type senderInfo struct {
	role     RoleInfo
	verified bool
}

func loadHumanAfter(q rowQuerier) int64 {
	var v string
	if q.QueryRow(`SELECT value FROM org_state WHERE key=?`, humanAuthorityKey).Scan(&v) != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// agentProjectTx ist das aktuelle Projekt eines Agenten: seine aktive
// Mitgliedschaft in einem Projektraum, nicht der Raum der Erstanmeldung (ein
// Agent kann zuerst im Maschinenraum angemeldet worden sein). Ein Agent ist in
// höchstens einem Projekt (ErrCoordAgentScopeChanged). Ohne Projektraum: leer.
func agentProjectTx(q rowQuerier, externalID string) string {
	var roomKey string
	if q.QueryRow(`SELECT m.room_key FROM coord_room_memberships m
		JOIN coord_rooms r ON r.room_key=m.room_key
		WHERE m.principal_id=? AND m.left_at='' AND r.kind=?
		ORDER BY m.joined_at, m.room_key LIMIT 1`, externalID, RoomProject).Scan(&roomKey) != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(roomKey, "project:"))
}

func newAgentAuthorityCtx(q rowQuerier, recipient string) *authorityCtx {
	c := &authorityCtx{q: q, recipient: recipient, humanAfter: loadHumanAfter(q), senders: map[string]senderInfo{}}
	c.project = agentProjectTx(q, recipient)
	if c.project == "" {
		return c
	}
	c.recipientRole = effectiveAgentRoleTx(q, c.project, recipient)
	if _, account, ok := agentAccountTx(q, recipient); ok && account != 0 {
		c.recipientHasRole = projectRoleTx(q, c.project, account).Role != ""
	}
	return c
}

func newProjectAuthorityCtx(q rowQuerier, project string) *authorityCtx {
	return &authorityCtx{q: q, project: strings.TrimSpace(project), humanAfter: loadHumanAfter(q), senders: map[string]senderInfo{}}
}

// sender liest die Rolle des Absenders im Projekt und sagt, ob sie
// verifiziert ist. Alles stammt aus Feldern, die der Server beim Speichern
// gesetzt hat (author_kind, author_principal_id, sender_external_id), nie aus
// Body oder Client-Angaben.
//
//   - human: das Konto aus author_principal_id; verifiziert nur für
//     Nachrichten nach der Migrationsmarke (der Server vergibt human erst seit
//     Paket 6 nur für interaktive Browser-Sitzungen). Unverifiziert heißt: keine
//     Rolle, Bitte.
//   - agent: nur ein angemeldeter Agent, der dem Konto in author_principal_id
//     gehört, mit seiner effektiven Rolle. Ein Beitrag ohne Agentenidentität
//     (Bearer-Token ohne agent_external_id) ist unverifiziert und ohne Rolle.
//   - alles andere (system, unbekannt): keine Rolle.
func (c *authorityCtx) sender(m CoordMessage) senderInfo {
	if c.project == "" {
		return senderInfo{}
	}
	human := m.AuthorKind == AuthorHuman
	if human && m.ID <= c.humanAfter {
		return senderInfo{}
	}
	key := m.AuthorKind + "|" + m.AuthorPrincipalID + "|" + m.SenderExternalID
	if human {
		key = m.AuthorKind + "|" + m.AuthorPrincipalID
	}
	if got, ok := c.senders[key]; ok {
		return got
	}
	var out senderInfo
	switch m.AuthorKind {
	case AuthorHuman:
		if id, err := parsePersonPrincipalID(m.AuthorPrincipalID); err == nil {
			out = senderInfo{role: projectRoleTx(c.q, c.project, id), verified: true}
		}
	case AuthorAgent:
		var principal string
		if c.q.QueryRow(`SELECT principal_id FROM coord_agents WHERE external_id=?`, m.SenderExternalID).Scan(&principal) == nil &&
			principal != "" && principal == m.AuthorPrincipalID {
			out = senderInfo{role: effectiveAgentRoleTx(c.q, c.project, m.SenderExternalID), verified: true}
		}
	}
	c.senders[key] = out
	return out
}

// evaluate bewertet eine Nachricht für den Empfänger. Ohne Projekt
// (Maschinenraum, unbekannter Agent) gibt es keine Rollen und immer eine Bitte.
func (c *authorityCtx) evaluate(m CoordMessage) MessageAuthority {
	if c.project == "" {
		return MessageAuthority{Authority: AuthorityRequest}
	}
	sender := c.sender(m)
	authority := AuthorityFor(RoleRank(sender.role.Role), RoleRank(c.recipientRole.Role), sender.verified)
	if !c.recipientHasRole {
		authority = AuthorityRequest
	}
	return MessageAuthority{SenderRole: sender.role.Role, RecipientRole: c.recipientRole.Role, Authority: authority}
}

// MessageAuthority berechnet live, welche Autorität m für den angemeldeten
// Agenten recipientAgent hat.
func (s *Store) MessageAuthority(m CoordMessage, recipientAgent string) MessageAuthority {
	if s.reader != nil {
		return s.reader.MessageAuthority(m, recipientAgent)
	}
	return newAgentAuthorityCtx(s.db, strings.TrimSpace(recipientAgent)).evaluate(m)
}

// SenderRolesInProject liefert zu jeder Nachricht die Rolle des Absenders im
// Projekt, für die Anzeige im Browser; leer ohne Projekt, Rolle oder
// Verifizierung. Absender werden einmal abgefragt, nicht je Nachricht.
func (s *Store) SenderRolesInProject(project string, msgs []CoordMessage) []string {
	if s.reader != nil {
		return s.reader.SenderRolesInProject(project, msgs)
	}
	out := make([]string, len(msgs))
	if strings.TrimSpace(project) == "" {
		return out
	}
	c := newProjectAuthorityCtx(s.db, project)
	for i, m := range msgs {
		out[i] = c.sender(m).role.Role
	}
	return out
}

// CanEndStanding sagt, ob der Handelnde die Vorgabe beenden darf; die Oberfläche
// zeigt "Beenden" nur dann. Dieselbe Regel wie EndStanding.
func (a CoordAccess) CanEndStanding(roomKey, messageID string) bool {
	if a.Store == nil {
		return false
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	return canEndStandingTx(reader.db, a.Principal.ID, a.AgentExternalID, roomKey, messageID)
}

// canEndStandingTx: eine Vorgabe beendet die Autorin selbst oder wer im Projekt
// mindestens ihren Rang hat; Owner (auch implizite) und Instanz-Admins immer.
// Räume ohne Projekt oder Projekte ohne Besitzer kennen keine Rollen und bleiben
// bei der Raumzugehörigkeit. Eine Vorgabe, die es nicht gibt, ist nicht
// verboten, sondern "nicht gefunden" beim Beenden.
func canEndStandingTx(q rowQuerier, principal, agent, roomKey, messageID string) bool {
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return true
	}
	var m CoordMessage
	if q.QueryRow(`SELECT id, author_kind, author_principal_id, sender_external_id FROM coord_messages WHERE id=? AND destination_kind='room' AND destination_id=?`,
		id, roomKey).Scan(&m.ID, &m.AuthorKind, &m.AuthorPrincipalID, &m.SenderExternalID) != nil {
		return true
	}
	if agent != "" {
		if m.SenderExternalID == agent {
			return true
		}
	} else if m.AuthorKind == AuthorHuman && m.AuthorPrincipalID == principal {
		return true
	}
	project, ok := strings.CutPrefix(roomKey, "project:")
	if !ok {
		return true
	}
	var claimed int
	if q.QueryRow(`SELECT COUNT(*) FROM projects WHERE remote=?`, project).Scan(&claimed) != nil || claimed == 0 {
		return true
	}
	account, hasAccount := int64(0), false
	if agent != "" {
		_, account, hasAccount = agentAccountTx(q, agent)
	} else if n, err := parsePersonPrincipalID(principal); err == nil {
		account, hasAccount = n, true
	}
	if !hasAccount || account == 0 {
		return false
	}
	var admin int
	if q.QueryRow(`SELECT is_admin FROM persons WHERE id=?`, account).Scan(&admin) == nil && admin == 1 {
		return true
	}
	var actor RoleInfo
	if agent != "" {
		actor = effectiveAgentRoleTx(q, project, agent)
	} else {
		actor = projectRoleTx(q, project, account)
	}
	author := newProjectAuthorityCtx(q, project).sender(m)
	return RoleRank(actor.Role) > 0 && RoleRank(actor.Role) >= RoleRank(author.role.Role)
}
