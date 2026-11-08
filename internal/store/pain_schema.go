package store

import (
	"database/sql"
	"strings"
)

const painAndTasksSchema = `
CREATE TABLE IF NOT EXISTS pains(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  title TEXT NOT NULL CHECK(trim(title) != ''),
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','mitigated','resolved','obsolete')),
  remaining TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK(state != 'mitigated' OR trim(remaining) != ''));
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
