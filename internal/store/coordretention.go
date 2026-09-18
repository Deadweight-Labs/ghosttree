package store

import (
	"fmt"
	"time"
)

// Aufbewahrungsfristen als Startwerte für Tests, nicht als gemessene Optima.
// Spec §7 nennt sieben Tage für ausführliche Aktivität und dreißig für
// gewöhnlichen Chatverkehr und markiert beides ausdrücklich als Vorschlag.
const (
	ChatRetention     = 30 * 24 * time.Hour
	ActivityRetention = 7 * 24 * time.Hour
)

// RetentionResult sagt, was weg ist und was BEWUSST geblieben ist. Die zweite
// Zahl ist die wichtigere: eine Aufräumung, die nur meldet, wie viel sie
// gelöscht hat, verbirgt genau den Fall, in dem sie zu viel gelöscht hätte.
type RetentionResult struct {
	MessagesDeleted int `json:"messages_deleted"`
	MessagesHeld    int `json:"messages_held"`
	ActivityDeleted int `json:"activity_deleted"`
}

// ApplyCoordRetention räumt abgelaufenen Chatverkehr und alte Aktivität weg.
//
// DIE AUSNAHME IST DER EIGENTLICHE INHALT DIESER FUNKTION. Spec §6: eine
// Nachricht, die Grundlage eines dauerhaften Ergebnisses ist, darf nicht
// verschwinden. Vier Dinge halten deshalb fest:
//
//   - Beiträge, die in einen Thread übernommen wurden. Die Kopie in
//     thread_sources überlebt ohnehin, aber das Original mit zu löschen
//     hieße, den Verlauf drumherum zu verlieren, in dem es stand.
//   - Nachrichten, an denen eine noch GELTENDE menschliche Vorgabe hängt.
//     §11: "Chat-Retention entfernt keine noch aktive menschliche
//     Einschränkung."
//   - Nachrichten mit Objektbezügen. Wer auf REQ-350 zeigt, ist Provenienz
//     und nicht Geplauder.
//   - Quellen adressierter Attention. Auch ein abgeschlossenes Item bleibt
//     nachvollziehbar und hält seine unveränderliche Quellnachricht fest.
//
// Was NICHT hier steht: eine Löschung von Threads oder Wissen. Beides hat
// seinen eigenen Lebenszyklus, und §7 sagt ausdrücklich, dass Chat-Retention
// ihn nicht beeinflussen darf.
func (s *Store) ApplyCoordRetention(before, activityBefore string) (RetentionResult, error) {
	if before == "" || activityBefore == "" {
		return RetentionResult{}, fmt.Errorf("retention needs both cut-off times")
	}
	if s.writer != nil {
		return queueValue(s, []any{before, activityBefore}, func(d *Store, p []any) (RetentionResult, error) {
			return d.ApplyCoordRetention(p[0].(string), p[1].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return RetentionResult{}, err
	}
	defer tx.Rollback()

	const held = `(
		EXISTS(SELECT 1 FROM thread_sources ts
			WHERE ts.source_kind='coord_message' AND ts.source_id=CAST(coord_messages.id AS TEXT))
		OR EXISTS(SELECT 1 FROM coord_standing cs
			WHERE cs.message_id=CAST(coord_messages.id AS TEXT) AND cs.ended_at IS NULL)
		OR EXISTS(SELECT 1 FROM coord_message_refs r WHERE r.message_id=coord_messages.id)
		OR EXISTS(SELECT 1 FROM coord_attention attention WHERE attention.message_id=coord_messages.id)
	)`

	var out RetentionResult
	if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_messages
		WHERE created_at < ? AND `+held, before).Scan(&out.MessagesHeld); err != nil {
		return RetentionResult{}, err
	}
	res, err := tx.Exec(`DELETE FROM coord_messages WHERE created_at < ? AND NOT `+held, before)
	if err != nil {
		return RetentionResult{}, err
	}
	if n, err := res.RowsAffected(); err == nil {
		out.MessagesDeleted = int(n)
	}

	res, err = tx.Exec(`DELETE FROM path_activity WHERE at < ?`, activityBefore)
	if err != nil {
		return RetentionResult{}, err
	}
	if n, err := res.RowsAffected(); err == nil {
		out.ActivityDeleted = int(n)
	}
	return out, tx.Commit()
}

// RetentionCutoff bildet den Stichtag aus einer Frist.
func RetentionCutoff(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(time.RFC3339)
}
