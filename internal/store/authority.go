package store

import "strings"

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

// agentProjectTx ist das Projekt eines Agenten: die Remote seines
// Anmelderaums. Ein Agent im Maschinenraum hat keins.
func agentProjectTx(q rowQuerier, externalID string) string {
	var roomKey string
	if q.QueryRow(`SELECT room_key FROM coord_agents WHERE external_id=?`, externalID).Scan(&roomKey) != nil {
		return ""
	}
	project, ok := strings.CutPrefix(roomKey, "project:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(project)
}

// senderRoleTx liest die Rolle des Absenders im Projekt und sagt, ob sie
// verifiziert ist. Alles stammt aus Feldern, die der Server beim Speichern
// gesetzt hat (author_kind, author_principal_id, sender_external_id), nie aus
// Body oder Client-Angaben.
//
//   - human: das Konto aus author_principal_id; verifiziert, weil der Server
//     human nur für interaktive Browser-Sitzungen vergibt.
//   - agent: nur ein angemeldeter Agent, der dem Konto in author_principal_id
//     gehört, mit seiner effektiven Rolle. Ein Beitrag ohne Agentenidentität
//     (Bearer-Token ohne agent_external_id) ist unverifiziert und ohne Rolle.
//   - alles andere (system, unbekannt): keine Rolle.
func senderRoleTx(q rowQuerier, project string, m CoordMessage) (RoleInfo, bool) {
	switch m.AuthorKind {
	case AuthorHuman:
		id, err := parsePersonPrincipalID(m.AuthorPrincipalID)
		if err != nil {
			return RoleInfo{}, false
		}
		return projectRoleTx(q, project, id), true
	case AuthorAgent:
		var principal string
		if q.QueryRow(`SELECT principal_id FROM coord_agents WHERE external_id=?`, m.SenderExternalID).Scan(&principal) != nil ||
			principal == "" || principal != m.AuthorPrincipalID {
			return RoleInfo{}, false
		}
		return effectiveAgentRoleTx(q, project, m.SenderExternalID), true
	}
	return RoleInfo{}, false
}

// authorityForAgentTx bewertet eine Nachricht für einen Agenten als Empfänger,
// im Projekt des Empfängers, gleich in welchem Raum sie liegt. Ohne Projekt
// (Maschinenraum) gibt es keine Rollen und immer eine Bitte.
func authorityForAgentTx(q rowQuerier, m CoordMessage, recipientAgent string) MessageAuthority {
	project := agentProjectTx(q, recipientAgent)
	if project == "" {
		return MessageAuthority{Authority: AuthorityRequest}
	}
	recipient := effectiveAgentRoleTx(q, project, recipientAgent)
	sender, verified := senderRoleTx(q, project, m)
	return MessageAuthority{
		SenderRole:    sender.Role,
		RecipientRole: recipient.Role,
		Authority:     AuthorityFor(RoleRank(sender.Role), RoleRank(recipient.Role), verified),
	}
}

// MessageAuthority berechnet live, welche Autorität m für den angemeldeten
// Agenten recipientAgent hat.
func (s *Store) MessageAuthority(m CoordMessage, recipientAgent string) MessageAuthority {
	if s.reader != nil {
		return s.reader.MessageAuthority(m, recipientAgent)
	}
	return authorityForAgentTx(s.db, m, strings.TrimSpace(recipientAgent))
}

// SenderRoleInProject ist die Rolle des Absenders von m im Projekt, für die
// Anzeige im Browser. Leer ohne Projekt oder Rolle.
func (s *Store) SenderRoleInProject(project string, m CoordMessage) string {
	if s.reader != nil {
		return s.reader.SenderRoleInProject(project, m)
	}
	if strings.TrimSpace(project) == "" {
		return ""
	}
	role, _ := senderRoleTx(s.db, strings.TrimSpace(project), m)
	return role.Role
}
