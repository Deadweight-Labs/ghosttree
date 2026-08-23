// Package store is ghosttree's SQLite persistence layer.
package store

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS persons(
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  token_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS machines(
  hostname TEXT PRIMARY KEY, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS knowledge(
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan','instruction','request')),
  title TEXT NOT NULL, body TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  confidence TEXT NOT NULL DEFAULT 'trusted' CHECK(confidence IN ('quarantined','staged','trusted','verified')),
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated','superseded','archived')),
  origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
  superseded_by INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS instruction_activation_path(
  knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE,
  pattern TEXT NOT NULL,
  PRIMARY KEY(knowledge_id, pattern));
CREATE TABLE IF NOT EXISTS instruction_activation_task(
  knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE,
  task TEXT NOT NULL CHECK(task IN ('code','review','test','deploy','security','docs')),
  PRIMARY KEY(knowledge_id, task));
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(title, body, content='knowledge', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS knowledge_ai AFTER INSERT ON knowledge BEGIN
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER IF NOT EXISTS knowledge_au AFTER UPDATE ON knowledge BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TABLE IF NOT EXISTS knowledge_evidence(
  id INTEGER PRIMARY KEY,
  knowledge_id INTEGER NOT NULL REFERENCES knowledge(id),
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  chunk_seq INTEGER NOT NULL,
  quote TEXT NOT NULL DEFAULT '',
  UNIQUE(knowledge_id, session_id, chunk_seq));
CREATE TABLE IF NOT EXISTS request_resolution(
  knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id),
  state TEXT NOT NULL CHECK(state IN ('open','done','dropped')),
  evidence_kind TEXT NOT NULL DEFAULT '',
  evidence_ref TEXT NOT NULL DEFAULT '',
  by_person TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS migration_runs(
  id INTEGER PRIMARY KEY, project TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','complete')),
  created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS migration_artifacts(
  run_id INTEGER NOT NULL REFERENCES migration_runs(id),
  path TEXT NOT NULL, digest TEXT NOT NULL,
  PRIMARY KEY(run_id, path));
CREATE TABLE IF NOT EXISTS migration_evidence(
  knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id),
  run_id INTEGER NOT NULL REFERENCES migration_runs(id),
  source TEXT NOT NULL, digest TEXT NOT NULL, item_key TEXT NOT NULL UNIQUE,
  quote TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS sessions(
  id INTEGER PRIMARY KEY,
  harness TEXT NOT NULL, external_id TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
  UNIQUE(harness, external_id));
CREATE TABLE IF NOT EXISTS session_chunks(
  id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  seq INTEGER NOT NULL, role TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '', raw TEXT NOT NULL,
  UNIQUE(session_id, seq));
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(text, content='session_chunks', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON session_chunks BEGIN
  INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Single writer: serialize access instead of hitting SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	// busy_timeout covers the second process case (ctx person add against a
	// running server) that WAL alone does not.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the connection for schema inspection at startup.
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return time.Now().UTC().Format(time.RFC3339) }
