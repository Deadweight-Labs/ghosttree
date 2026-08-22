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
  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan')),
  title TEXT NOT NULL, body TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  confidence TEXT NOT NULL DEFAULT 'observation' CHECK(confidence IN ('observation','verified')),
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated')),
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(title, body, content='knowledge', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS knowledge_ai AFTER INSERT ON knowledge BEGIN
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER IF NOT EXISTS knowledge_au AFTER UPDATE ON knowledge BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
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

func now() string { return time.Now().UTC().Format(time.RFC3339) }
