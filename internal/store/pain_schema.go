package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// painsTableDDL is the definition of the pains table under a name; the rebuild
// of an older database creates the new table under another name first.
func painsTableDDL(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + `(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  title TEXT NOT NULL CHECK(trim(title) != ''),
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','mitigated','accepted','resolved','obsolete')),
  remaining TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  accepted_reason TEXT NOT NULL DEFAULT '',
  CHECK(state != 'mitigated' OR trim(remaining) != ''),
  CHECK(state != 'accepted' OR trim(accepted_reason) != ''))`
}

var painAndTasksSchema = `
` + painsTableDDL("pains") + `;
CREATE INDEX IF NOT EXISTS pains_project_state ON pains(project, state);
CREATE TABLE IF NOT EXISTS pain_authors(
  pain_id INTEGER PRIMARY KEY REFERENCES pains(id) ON DELETE RESTRICT, account_id INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS pain_events(
  id INTEGER PRIMARY KEY,
  pain_id INTEGER NOT NULL REFERENCES pains(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('created','state','corrected','link_added','link_removed')),
  from_state TEXT NOT NULL DEFAULT '', to_state TEXT NOT NULL DEFAULT '',
  remaining TEXT NOT NULL DEFAULT '',
  evidence_kind TEXT NOT NULL DEFAULT '' CHECK(evidence_kind IN ('','commit','test','file','decision','session','url','request')),
  evidence_ref TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '', account_id INTEGER NOT NULL DEFAULT 0, session_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS pain_events_pain ON pain_events(pain_id, id);
CREATE TRIGGER IF NOT EXISTS pain_events_no_update BEFORE UPDATE ON pain_events
  BEGIN SELECT RAISE(ABORT,'pain_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS pain_events_no_delete BEFORE DELETE ON pain_events
  BEGIN SELECT RAISE(ABORT,'pain_events is append-only'); END;
CREATE TABLE IF NOT EXISTS entity_links(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  src_kind TEXT NOT NULL CHECK(src_kind IN ('pain')),
  src_id INTEGER NOT NULL REFERENCES pains(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('motivates')),
  dst_kind TEXT NOT NULL CHECK(dst_kind IN ('request','knowledge','external','idea')),
  dst_id INTEGER NOT NULL DEFAULT 0,
  dst_ref TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  CHECK((dst_kind='external' AND dst_ref != '' AND dst_id=0) OR (dst_kind!='external' AND dst_id>0 AND dst_ref='')),
  UNIQUE(src_kind, src_id, kind, dst_kind, dst_id, dst_ref));
CREATE INDEX IF NOT EXISTS entity_links_dst ON entity_links(dst_kind, dst_id);
CREATE TABLE IF NOT EXISTS human_tasks(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  work_id INTEGER REFERENCES request_work(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('secret','file','input','action')),
  title TEXT NOT NULL CHECK(trim(title) != ''),
  description TEXT NOT NULL DEFAULT '',
  location TEXT NOT NULL DEFAULT '',
  check_hint TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','done','confirmed','dropped')),
  assignee_account INTEGER NOT NULL DEFAULT 0,
  created_by_account INTEGER NOT NULL DEFAULT 0, created_by_person TEXT NOT NULL DEFAULT '', created_session_id INTEGER NOT NULL DEFAULT 0,
  done_by_account INTEGER NOT NULL DEFAULT 0, done_by_person TEXT NOT NULL DEFAULT '',
  done_note TEXT NOT NULL DEFAULT '', done_via TEXT NOT NULL DEFAULT '' CHECK(done_via IN ('','web','token')), done_at TEXT NOT NULL DEFAULT '',
  state_note TEXT NOT NULL DEFAULT '',
  confirmed_session_id INTEGER NOT NULL DEFAULT 0, confirmed_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK(kind NOT IN ('secret','file') OR trim(location) != ''));
CREATE INDEX IF NOT EXISTS human_tasks_project_state ON human_tasks(project, state);
CREATE INDEX IF NOT EXISTS human_tasks_request ON human_tasks(request_id);
`

// ensurePainAndTasks creates the tables of pains and human tasks. Idempotent;
// no existing table is touched.
func ensurePainAndTasks(db *sql.DB) error {
	if _, err := db.Exec(painAndTasksSchema); err != nil {
		return err
	}
	if err := allowPainAccepted(db); err != nil {
		return err
	}
	if err := allowIdeaLinkTarget(db); err != nil {
		return err
	}
	// steps (REQ-603): the walk-through of a task as JSON, additive.
	return addColumnIfMissing(db, "human_tasks", "steps", "TEXT NOT NULL DEFAULT ''")
}

// allowIdeaLinkTarget erweitert die CHECK-Bedingung von entity_links um das Ziel
// idea (REQ-606: ein Pain motiviert eine Idee). SQLite ändert eine CHECK-Bedingung
// nicht; die Tabelle wird neu gebaut, die Zeilen und Nummern bleiben. Eine
// Datenbank, die das Ziel schon kennt, bleibt unberührt, der Aufruf ist
// wiederholbar.
func allowIdeaLinkTarget(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='entity_links'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "'idea'") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE entity_links_new(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  src_kind TEXT NOT NULL CHECK(src_kind IN ('pain')),
  src_id INTEGER NOT NULL REFERENCES pains(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('motivates')),
  dst_kind TEXT NOT NULL CHECK(dst_kind IN ('request','knowledge','external','idea')),
  dst_id INTEGER NOT NULL DEFAULT 0,
  dst_ref TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  CHECK((dst_kind='external' AND dst_ref != '' AND dst_id=0) OR (dst_kind!='external' AND dst_id>0 AND dst_ref='')),
  UNIQUE(src_kind, src_id, kind, dst_kind, dst_id, dst_ref))`,
		`INSERT INTO entity_links_new(id,project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,account_id,created_at)
  SELECT id,project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,account_id,created_at FROM entity_links`,
		`DROP TABLE entity_links`,
		`ALTER TABLE entity_links_new RENAME TO entity_links`,
		`CREATE INDEX IF NOT EXISTS entity_links_dst ON entity_links(dst_kind, dst_id)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// allowPainAccepted (REQ-616) lets a pain be accepted: the state accepted and its
// reason column. SQLite does not change a CHECK condition, so the table is
// rebuilt once when its definition does not know the state yet; rows, numbers
// and everything that points at them stay. A database that knows the state is
// left alone, the call can be repeated.
//
// The tables pain_events, pain_authors and entity_links refer to pains with ON
// DELETE RESTRICT, and the DROP of the old table would refuse while they hold
// rows. Foreign keys are therefore switched off for the rebuild, on one
// connection of its own (the switch is a no-op inside a transaction and per
// connection), and checked again before the commit.
func allowPainAccepted(db *sql.DB) error {
	known := func(q interface {
		QueryRow(string, ...any) *sql.Row
	}) (bool, error) {
		var ddl string
		if err := q.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='pains'`).Scan(&ddl); err != nil {
			return false, err
		}
		return strings.Contains(ddl, "'accepted'"), nil
	}
	if ok, err := known(db); err != nil || ok {
		return err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A second process may have done the rebuild in the meantime.
	if ok, err := known(tx); err != nil || ok {
		return err
	}
	for _, q := range []string{
		painsTableDDL("pains_new"),
		`INSERT INTO pains_new(id,project,title,body,state,remaining,person,session_ref,created_at,updated_at)
  SELECT id,project,title,body,state,remaining,person,session_ref,created_at,updated_at FROM pains`,
		`DROP TABLE pains`,
		`ALTER TABLE pains_new RENAME TO pains`,
		`CREATE INDEX IF NOT EXISTS pains_project_state ON pains(project, state)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// Only the tables that refer to pains: a check of the whole database would
	// read every table of a large instance on startup, and an unrelated old
	// violation elsewhere would keep the server from starting.
	for _, table := range []string{"pain_authors", "pain_events", "entity_links"} {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return err
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return fmt.Errorf("pains rebuild: a row of %s refers to a pain that is gone", table)
		}
	}
	return tx.Commit()
}
