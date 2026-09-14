package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

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

// RoomKeyForGroup ist dasselbe für eine kleine Runde. Eine Gruppe mit
// denselben Teilnehmern ist derselbe Raum: wer dreimal dieselbe Abstimmung
// eröffnet, bekommt dreimal denselben Verlauf statt drei halber.
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
	CreatedAt string   `json:"created_at,omitempty"`
}

// EnsureCoordRoom legt einen Raum mit seinen Teilnehmern an. Idempotent:
// derselbe Aufruf zweimal ergibt denselben Raum mit demselben Verlauf.
func (s *Store) EnsureCoordRoom(r CoordRoom) error {
	switch r.Kind {
	case RoomProject, RoomMachine, RoomDirect, RoomGroup:
	default:
		return fmt.Errorf("unknown room kind %q", r.Kind)
	}
	if (r.Kind == RoomDirect || r.Kind == RoomGroup) && len(normalizeMembers(r.Members)) < 2 {
		// Ein Zweiergespräch mit einem Teilnehmer ist ein Notizzettel. Der
		// Fehler fällt sonst erst auf, wenn niemand antwortet.
		return fmt.Errorf("a direct or group room needs at least two members")
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
	if _, err := tx.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at)
		VALUES(?,?,?,?) ON CONFLICT(room_key) DO UPDATE SET label=excluded.label`,
		r.Key, r.Kind, r.Label, at); err != nil {
		return err
	}
	for _, m := range normalizeMembers(r.Members) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_room_members(room_key,member_external_id,joined_at)
			VALUES(?,?,?)`, r.Key, m, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CoordRoomMembers liefert die Teilnehmer. Leer heißt bei Projekt- und
// Maschinenräumen "offen für alle Berechtigten" und bei Direkt- und
// Gruppenräumen "es gibt den Raum nicht".
func (s *Store) CoordRoomMembers(roomKey string) ([]string, error) {
	if s.reader != nil {
		return s.reader.CoordRoomMembers(roomKey)
	}
	rows, err := s.db.Query(`SELECT member_external_id FROM coord_room_members
		WHERE room_key=? ORDER BY member_external_id`, roomKey)
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
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coord_room_members
		WHERE room_key=? AND member_external_id=?`, roomKey, agentExternalID).Scan(&count); err != nil {
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
		FROM coord_rooms r JOIN coord_room_members m ON m.room_key=r.room_key
		WHERE m.member_external_id=? ORDER BY r.created_at DESC`, agentExternalID)
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
