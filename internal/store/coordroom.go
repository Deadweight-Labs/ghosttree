package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrCoordHandoffTargetUnbound = errors.New("coordination handoff target is not bound to a principal")

// Raumarten. Projekt- und Maschinenraum entstehen aus der Arbeitsumgebung;
// Direktnachrichten und Gruppen werden ausdrücklich eröffnet.
const (
	RoomProject = "project"
	RoomMachine = "machine"
	RoomDirect  = "direct"
	RoomGroup   = "group"
)

// RoomKeyForDirect bildet den Schlüssel einer Direktnachricht aus den
// sortierten Teilnehmern. Sortiert, damit A→B und B→A derselbe Raum sind —
// sonst führen zwei Agenten zwei getrennte Hälften desselben Gesprächs und
// wundern sich, warum die Antwort fehlt.
//
// Der Schlüssel ist gehasht, weil Session-Referenzen lang sind und ein
// Raumschlüssel in Indizes und Cursorn steht. Die Teilnehmer stehen ohnehin
// in coord_room_members; der Schlüssel muss sie nicht lesbar tragen.
func RoomKeyForDirect(members []string) string {
	return "direct:" + memberDigest(members)
}

// RoomKeyForGroup bildet die Schlüssel bestehender Gruppen aus der Zeit vor
// opaken Gruppen-IDs nach. Neue Gruppen werden mit CreateCoordGroup eröffnet;
// dieser Adapter bleibt, damit vorhandene Verläufe adressierbar bleiben.
func RoomKeyForGroup(members []string) string {
	return "group:" + memberDigest(members)
}

func memberDigest(members []string) string {
	clean := normalizeMembers(members)
	sum := sha256.Sum256([]byte(strings.Join(clean, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func normalizeMembers(members []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// CoordRoom beschreibt einen Raum und seine Art. Projekt- und Maschinenräume
// brauchen keine Mitgliederliste: dort ist berechtigt, wer im Projekt
// beziehungsweise auf der Maschine arbeitet. Direkt und Gruppe haben eine,
// und genau die ist die Zugriffsgrenze.
type CoordRoom struct {
	Key       string   `json:"room_key"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label,omitempty"`
	Members   []string `json:"members,omitempty"`
	Actor     string   `json:"actor,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
}

// EnsureCoordRoom legt einen Raum mit vorgegebenem Schlüssel an. Es bleibt als
// kompatibler Store-Adapter für Direktnachrichten und bestehende Gruppen;
// öffentliche Transporte erzeugen neue Gruppen ausschließlich serverseitig.
func (s *Store) EnsureCoordRoom(r CoordRoom) error {
	switch r.Kind {
	case RoomProject, RoomMachine, RoomDirect, RoomGroup:
	default:
		return fmt.Errorf("unknown room kind %q", r.Kind)
	}
	members := normalizeMembers(r.Members)
	if (r.Kind == RoomDirect || r.Kind == RoomGroup) && len(members) < 2 {
		// Ein Zweiergespräch mit einem Teilnehmer ist ein Notizzettel. Der
		// Fehler fällt sonst erst auf, wenn niemand antwortet.
		return fmt.Errorf("a direct or group room needs at least two members")
	}
	if r.Kind == RoomDirect && len(members) != 2 {
		return fmt.Errorf("a direct room needs exactly two members")
	}
	if r.Kind == RoomDirect && r.Key != RoomKeyForDirect(members) {
		return fmt.Errorf("direct room key is not canonical for its members")
	}
	if s.writer != nil {
		return queueWrite(s, []any{r}, func(d *Store, p []any) error {
			return d.EnsureCoordRoom(p[0].(CoordRoom))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now()
	var existingKind, existingLabel string
	err = tx.QueryRow(`SELECT kind,label FROM coord_rooms WHERE room_key=?`, r.Key).Scan(&existingKind, &existingLabel)
	switch {
	case err == nil:
		if existingKind != r.Kind {
			return fmt.Errorf("room kind mismatch: existing %s, requested %s", existingKind, r.Kind)
		}
		if r.Kind == RoomDirect || r.Kind == RoomGroup {
			existing, err := activeRoomMembersTx(tx, r.Key)
			if r.Kind == RoomDirect {
				existing, err = legacyRoomMembersTx(tx, r.Key)
			}
			if err != nil {
				return err
			}
			if strings.Join(existing, "\x00") != strings.Join(members, "\x00") {
				return fmt.Errorf("existing private room membership is immutable through ensure")
			}
		}
		if r.Kind == RoomGroup {
			if existingLabel != r.Label {
				managers, err := activeManagerCount(tx, r.Key)
				if err != nil {
					return err
				}
				if managers == 0 {
					return ErrLegacyGroupReadOnly
				}
				return fmt.Errorf("%w: update groups through manager-authorized operations", ErrInvalidGroup)
			}
			return tx.Commit()
		}
		_, err = tx.Exec(`UPDATE coord_rooms SET label=? WHERE room_key=?`, r.Label, r.Key)
		if err != nil {
			return err
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at) VALUES(?,?,?,?)`, r.Key, r.Kind, r.Label, at); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_room_members(room_key,member_external_id,joined_at)
			VALUES(?,?,?)`, r.Key, m, at); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
			VALUES(?,?,?,'',0) ON CONFLICT DO NOTHING`, r.Key, m, membershipTime()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func legacyRoomMembersTx(tx *sql.Tx, roomKey string) ([]string, error) {
	rows, err := tx.Query(`SELECT member_external_id FROM coord_room_members WHERE room_key=? ORDER BY member_external_id`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func activeRoomMembersTx(tx *sql.Tx, roomKey string) ([]string, error) {
	rows, err := tx.Query(`SELECT principal_id FROM coord_room_memberships WHERE room_key=? AND left_at='' ORDER BY principal_id`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

// CoordRoomMembers liefert die Teilnehmer. Leer heißt bei Projekt- und
// Maschinenräumen "offen für alle Berechtigten" und bei Direkt- und
// Gruppenräumen "es gibt den Raum nicht".
func (s *Store) CoordRoomMembers(roomKey string) ([]string, error) {
	if s.reader != nil {
		return s.reader.CoordRoomMembers(roomKey)
	}
	rows, err := s.db.Query(`SELECT principal_id FROM coord_room_memberships
		WHERE room_key=? AND left_at='' ORDER BY principal_id`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MayReadCoordRoom ist die Zugriffsgrenze, und sie hat ihren eigenen Platz,
// weil Spec §9 sie ausdrücklich verlangt: "Kanal-Membership aus
// Repository-Erkennung ist Komfort, keine Mandantentrennung."
//
// Für Projekt- und Maschinenräume entscheidet der Perimeter — wer im Projekt
// arbeitet, liest den Projektraum. Für Direkt- und Gruppenräume entscheidet
// die Mitgliedschaft, und ein Unbeteiligter bekommt nichts: kein Verlauf,
// keine Suchtreffer, keine Zusammenfassung.
//
// Ein unbekannter Raumschlüssel ist NICHT lesbar. Das ist die sichere
// Richtung: sonst genügte ein erfundener Schlüssel, um an der Prüfung
// vorbeizukommen.
func (s *Store) MayReadCoordRoom(roomKey, agentExternalID string) (bool, error) {
	if s.reader != nil {
		return s.reader.MayReadCoordRoom(roomKey, agentExternalID)
	}
	switch {
	case strings.HasPrefix(roomKey, "project:"), strings.HasPrefix(roomKey, "machine:"):
		return true, nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
		WHERE room_key=? AND principal_id=? AND left_at=''`, roomKey, agentExternalID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// CoordRoomsFor listet die Direkt- und Gruppenräume eines Teilnehmers.
// Projekt- und Maschinenräume stehen bewusst nicht drin: die ergeben sich
// aus der Umgebung und müssen nicht gemerkt werden.
func (s *Store) CoordRoomsFor(agentExternalID string) ([]CoordRoom, error) {
	if s.reader != nil {
		return s.reader.CoordRoomsFor(agentExternalID)
	}
	rows, err := s.db.Query(`SELECT r.room_key,r.kind,r.label,r.created_at
		FROM coord_rooms r JOIN coord_room_memberships m ON m.room_key=r.room_key
		WHERE m.principal_id=? AND m.left_at='' AND r.kind IN ('direct','group') ORDER BY r.created_at DESC`, agentExternalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoordRoom
	for rows.Next() {
		var r CoordRoom
		if err := rows.Scan(&r.Key, &r.Kind, &r.Label, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.CoordRoomMembers(out[i].Key)
		if err != nil {
			return nil, err
		}
		out[i].Members = members
	}
	return out, nil
}

// CoordAgentOwner nennt die stabile Principal-ID der Person, der eine
// angemeldete Session gehört. Ein leerer Wert bei registered=true ist ein
// ungeclaimter Legacy-Datensatz und gewährt keine Berechtigung.
//
// Das ist der fehlende Schritt zwischen Authentifizierung und Autorisierung:
// ein Token weist eine PERSON aus, ein Raum gehört SESSIONS. Ohne diese
// Zuordnung könnte jeder Tokeninhaber eine fremde Session-Referenz angeben
// und damit deren private Räume lesen — ein Tokeninhaber ist nicht dasselbe
// wie der Teilnehmer.
//
// Dass mehrere Sessions derselben Person einander nicht abschotten, ist
// dagegen bewusst so: Spec §9 hält fest, dass Prozesse unter demselben
// Systemnutzer ohne weitere Isolation keine belastbare Sicherheitsgrenze
// sind. Die Grenze, die hier gezogen wird, verläuft zwischen PERSONEN.
func (s *Store) CoordAgentOwner(externalID string) (string, bool, error) {
	if s.reader != nil {
		return s.reader.CoordAgentOwner(externalID)
	}
	var principalID string
	err := s.db.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`,
		externalID).Scan(&principalID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return principalID, true, nil
}

// HandoffCoordRooms überträgt ausgewählte private Räume von einer Session auf
// eine andere. Das ist der ausdrückliche Handoff aus v1 §5 — und der Grund,
// warum es ihn braucht, steht im selben Absatz: eine neue Session erbt die
// Inbox einer beendeten NICHT automatisch.
//
// Automatisches Erben wäre die naheliegende Bequemlichkeit und der falsche
// Weg: eine Session-Referenz ist keine Person, und wer morgen unter neuer
// Referenz startet, hat keinen Anspruch auf die vertraulichen Gespräche von
// gestern. Wer sie braucht, benennt sie.
//
// Übertragen heißt HINZUFÜGEN, nicht Verschieben: die alte Session bleibt
// Teilnehmerin ihres eigenen Verlaufs. Ihn ihr zu nehmen, würde die Historie
// unlesbar machen, in der sie geschrieben hat.
func (s *Store) HandoffCoordRooms(from, to string, roomKeys []string) (int, error) {
	if from == "" || to == "" {
		return 0, fmt.Errorf("handoff needs both a source and a target session")
	}
	if from == to {
		return 0, fmt.Errorf("a session cannot hand off to itself")
	}
	if s.writer != nil {
		return queueValue(s, []any{from, to, roomKeys}, func(d *Store, p []any) (int, error) {
			return d.HandoffCoordRooms(p[0].(string), p[1].(string), p[2].([]string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var targetBound int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_agents WHERE external_id=? AND principal_id<>''`, to).Scan(&targetBound); err != nil {
		return 0, err
	}
	if targetBound == 0 {
		return 0, ErrCoordHandoffTargetUnbound
	}
	moved := 0
	for _, key := range roomKeys {
		// Nur was der Übergebende selbst lesen darf. Sonst wäre der Handoff
		// ein Weg, sich Zugang zu einem fremden Raum zu verschaffen, indem
		// man ihn einfach nennt.
		var member int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
			WHERE room_key=? AND principal_id=? AND left_at=''`, key, from).Scan(&member); err != nil {
			return 0, err
		}
		if member == 0 {
			continue
		}
		kind, err := coordRoomKindTx(tx, key)
		if err != nil {
			return 0, err
		}
		if kind == RoomGroup {
			managers, err := activeManagerCount(tx, key)
			if err != nil {
				return 0, err
			}
			if managers == 0 {
				return 0, ErrLegacyGroupReadOnly
			}
			manager, err := activeManager(tx, key, from)
			if err != nil {
				return 0, err
			}
			if !manager {
				return 0, fmt.Errorf("%w: group handoff requires a manager", ErrInvalidGroup)
			}
		}
		joinedAt := membershipTime()
		result, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
			VALUES(?,?,?,'',0) ON CONFLICT DO NOTHING`, key, to, joinedAt)
		if err != nil {
			return 0, err
		}
		added, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		moved += int(added)
		if added > 0 && kind == RoomGroup {
			if err := appendMembershipEvent(tx, key, to, "join", from, joinedAt); err != nil {
				return 0, err
			}
		}
	}
	return moved, tx.Commit()
}
