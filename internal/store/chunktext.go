package store

import (
	"database/sql"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
)

// session_chunks.text ist seit IDEA-25 abgeleitet statt gespeichert. Gelesen
// wird nach einer Regel: Steht Text in der Zeile, gilt er; ist er leer, wird er
// aus raw abgeleitet (internal/chunktext). Das neue Binary verhält sich damit
// vor der Migration wie das alte, und nach ctx db slim-text liest jede Zeile
// abgeleitet.
//
// Die Spalte bleibt im Schema und wird bei -session-text=derived leer
// geschrieben; text_chars hält die Länge des Textes, den der Server sonst
// gespeichert hätte, für ctx cost.

// Die beiden Arten, wie der Server den Text eines Chunks schreibt.
const (
	SessionTextStored  = "stored"
	SessionTextDerived = "derived"
)

// ParseSessionText prüft den Wert des Schalters -session-text.
func ParseSessionText(mode string) (string, error) {
	switch mode {
	case SessionTextStored, SessionTextDerived:
		return mode, nil
	}
	return "", fmt.Errorf("session text must be %q or %q, got %q", SessionTextStored, SessionTextDerived, mode)
}

// setSessionText wählt, wie der Store den Text neuer Chunks schreibt. Es gilt
// für die Wege, die über AppendChunks und AppendChunkBatches laufen, und ist
// vor dem Bedienen von Anfragen zu setzen (OpenRuntimeWithText).
func (s *Store) setSessionText(mode string) error {
	mode, err := ParseSessionText(mode)
	if err != nil {
		return err
	}
	s.textDerived = mode == SessionTextDerived
	return nil
}

// ensureChunkText legt text_chars an. ALTER TABLE ADD COLUMN ist eine
// Änderung der Metadaten, auch bei einer großen Tabelle.
func ensureChunkText(db *sql.DB) error {
	return addColumnIfMissing(db, "session_chunks", "text_chars", "INTEGER NOT NULL DEFAULT 0")
}

// sessionHarness ist der Harness einer Session; für eine unbekannte Nummer
// leer, was den Parser für Claude Code wählt (wie der Sammler).
func (s *Store) sessionHarness(id int64) string {
	var h string
	_ = s.db.QueryRowContext(s.ctx(), `SELECT harness FROM sessions WHERE id = ?`, id).Scan(&h)
	return h
}

// chunkTextOf ist der Text eines Chunks nach der Leseregel: der gespeicherte,
// sonst der aus der entpackten Zeile abgeleitete.
func chunkTextOf(harness, stored, raw string) string {
	if stored != "" {
		return stored
	}
	_, text := chunktext.Derive(harness, raw)
	return text
}

// chunkTextTx liest den Text eines Chunks innerhalb einer Transaktion nach der
// Leseregel. Die beiden Zitatprüfungen des Destillers brauchen genau den Text,
// den ReadSession dem Destiller gegeben hat.
func (s *Store) chunkTextTx(tx *sql.Tx, sessionID int64, seq int) (string, error) {
	var stored, harness string
	var raw rawcodec.Value
	if err := tx.QueryRow(`SELECT c.text, c.raw, se.harness FROM session_chunks c
		JOIN sessions se ON se.id = c.session_id
		WHERE c.session_id = ? AND c.seq = ?`, sessionID, seq).Scan(&stored, &raw, &harness); err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	line, err := s.rawState().line(s.ctx(), tx, raw)
	if err != nil {
		return "", fmt.Errorf("session %d chunk %d: %w", sessionID, seq, err)
	}
	return chunkTextOf(harness, "", line), nil
}
