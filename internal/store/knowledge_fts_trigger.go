package store

import (
	"context"
	"database/sql"
	"strings"
)

// The full-text index of the knowledge covers the title and the body, and the
// trigger that keeps it current used to run on every update of a row. Counting a
// use (last_used_at, hit_count, search_hits) is an update, and the start-up
// briefing counts every entry it reads: some thousand updates, each deleting and
// writing the entry's words again. They held the writer for tens of milliseconds
// per briefing, and every other write waited behind them (PAIN-76).
//
// The trigger now fires only when title or body change, which are the only columns
// the index holds. Databases made before have the old trigger; ensureKnowledgeFTSTrigger
// replaces it once. The index itself is not touched.
const knowledgeFTSTriggerSQL = `CREATE TRIGGER knowledge_au AFTER UPDATE OF title, body ON knowledge BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END`

func ensureKnowledgeFTSTrigger(db *sql.DB) error {
	var current string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='knowledge_au'`).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && strings.Contains(current, "UPDATE OF title, body") {
		return nil
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS knowledge_au`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, knowledgeFTSTriggerSQL); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}
