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
CREATE TABLE IF NOT EXISTS requests(
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL CHECK(type IN ('feature','change','bug','investigation')),
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','done','dropped')),
  priority TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
  person TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS requests_idempotency
  ON requests(idempotency_key) WHERE idempotency_key != '';
CREATE TABLE IF NOT EXISTS request_criteria(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  number INTEGER NOT NULL,
  description TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','met','waived')),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(request_id, number));
CREATE TABLE IF NOT EXISTS request_evidence(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  criterion_id INTEGER REFERENCES request_criteria(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('commit','test','file','decision','session','url')),
  ref TEXT NOT NULL, person TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS request_relations(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  other_request_id INTEGER REFERENCES requests(id) ON DELETE RESTRICT,
  knowledge_id INTEGER REFERENCES knowledge(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('parent','related','blocks','duplicates','supersedes','knowledge','external')),
  external_ref TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS request_work(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  role TEXT NOT NULL CHECK(role IN ('primary','related')),
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','paused','completed','abandoned')),
  started_at TEXT NOT NULL, ended_at TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '',
  UNIQUE(request_id, session_id, role));
CREATE UNIQUE INDEX IF NOT EXISTS request_work_one_primary
  ON request_work(session_id) WHERE role='primary';
CREATE TABLE IF NOT EXISTS request_activity(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL, person TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS search_documents(
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('knowledge','request')),
  domain_id INTEGER NOT NULL,
  title TEXT NOT NULL, body TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  UNIQUE(kind, domain_id));
CREATE VIRTUAL TABLE IF NOT EXISTS search_documents_fts USING fts5(title, body, content='search_documents', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS search_documents_ai AFTER INSERT ON search_documents BEGIN
  INSERT INTO search_documents_fts(rowid,title,body) VALUES(new.id,new.title,new.body);
END;
CREATE TRIGGER IF NOT EXISTS search_documents_au AFTER UPDATE ON search_documents BEGIN
  INSERT INTO search_documents_fts(search_documents_fts,rowid,title,body) VALUES('delete',old.id,old.title,old.body);
  INSERT INTO search_documents_fts(rowid,title,body) VALUES(new.id,new.title,new.body);
END;
CREATE TABLE IF NOT EXISTS knowledge(
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan','instruction')),
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
  id INTEGER PRIMARY KEY,
  knowledge_id INTEGER REFERENCES knowledge(id), request_id INTEGER REFERENCES requests(id),
  run_id INTEGER NOT NULL REFERENCES migration_runs(id),
  source TEXT NOT NULL, digest TEXT NOT NULL, item_key TEXT NOT NULL UNIQUE,
  quote TEXT NOT NULL DEFAULT '',
  CHECK((knowledge_id IS NOT NULL) != (request_id IS NOT NULL)),
  UNIQUE(knowledge_id), UNIQUE(request_id));
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
CREATE TABLE IF NOT EXISTS session_distillations(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  digest TEXT NOT NULL,
  item_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY(session_id,digest));
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
