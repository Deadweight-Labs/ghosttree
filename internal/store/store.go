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
-- One primary at a time, not one per session ever. A session that works a
-- backlog has several main tasks in sequence, and the state column is what
-- makes finishing one mean anything.
CREATE UNIQUE INDEX IF NOT EXISTS request_work_one_active_primary
  ON request_work(session_id) WHERE role='primary' AND state='active';
DROP INDEX IF EXISTS request_work_one_primary;
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
  person TEXT NOT NULL DEFAULT '', confirmed_by TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  last_modified_by TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT '', hit_count INTEGER NOT NULL DEFAULT 0,
  search_hits INTEGER NOT NULL DEFAULT 0,
  -- When the entry was seen, as opposed to when it was written down. The
  -- distiller works a backlog: a finding from a June session is filed today,
  -- and created_at alone makes every one of them look like today's news.
  observed_at TEXT NOT NULL DEFAULT '',
  -- Womit ein behobener Fehler abgesichert ist, nicht nur was kaputt war. Ein
  -- Pitfall hilft, solange ein Agent ihn liest; ein Test hilft immer. Vier
  -- Zustände, weil der leere einer davon ist: '' hat niemand beurteilt,
  -- 'covered' nennt den Test, 'uncovered' ist die belegte Lücke, und
  -- 'not_applicable' ist die Entscheidung, dass hier nichts zu testen war.
  -- Ohne den vierten sähe eine bewusste Entscheidung aus wie eine offene
  -- Aufgabe — und das trifft die Mehrheit der Einträge.
  regression_state TEXT NOT NULL DEFAULT '',
  regression_test TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS instruction_activation_path(
  knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE,
  pattern TEXT NOT NULL,
  PRIMARY KEY(knowledge_id, pattern));
-- The task gate is gone: see internal/activation. Dropped here so a database
-- that has it does not keep a table nothing reads.
DROP TABLE IF EXISTS instruction_activation_task;
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(title, body, content='knowledge', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS knowledge_ai AFTER INSERT ON knowledge BEGIN
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER IF NOT EXISTS knowledge_au AFTER UPDATE ON knowledge BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TABLE IF NOT EXISTS request_sightings(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  chunk_seq INTEGER NOT NULL,
  quote TEXT NOT NULL DEFAULT '',
  UNIQUE(request_id, session_id, chunk_seq));
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
  document_id INTEGER REFERENCES documents(id), revision INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER NOT NULL REFERENCES migration_runs(id),
  source TEXT NOT NULL, digest TEXT NOT NULL, item_key TEXT NOT NULL UNIQUE,
  quote TEXT NOT NULL DEFAULT '',
  CHECK((knowledge_id IS NOT NULL) + (request_id IS NOT NULL) + (document_id IS NOT NULL) = 1),
  UNIQUE(knowledge_id), UNIQUE(request_id), UNIQUE(document_id,revision));
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
-- prompt_version is part of the identity, not a note beside it. Keyed on the
-- transcript alone, a session was retired against whichever prompt happened to
-- reach it first, and no later improvement could ever touch it again.
CREATE TABLE IF NOT EXISTS session_distillations(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  digest TEXT NOT NULL,
  prompt_version TEXT NOT NULL DEFAULT '',
  item_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY(session_id,digest,prompt_version));
-- model is recorded per batch, not read from configuration at report time: a
-- price change must not retroactively restate what an earlier batch cost.
CREATE TABLE IF NOT EXISTS distill_batches(
  id INTEGER PRIMARY KEY,
  provider_batch_id TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL CHECK(state IN ('open','collected','failed')),
  model TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
-- prompt_version is recorded here as well as on the distillation, because
-- releasing a session for reprocessing deletes the distillation row — and with
-- it the only record of which prompt that spend belonged to.
CREATE TABLE IF NOT EXISTS distill_batch_items(
  batch_id INTEGER NOT NULL REFERENCES distill_batches(id) ON DELETE CASCADE,
  custom_id TEXT NOT NULL,
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  digest TEXT NOT NULL,
  prompt_version TEXT NOT NULL DEFAULT '',
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(batch_id, custom_id));
CREATE INDEX IF NOT EXISTS distill_batch_items_session ON distill_batch_items(session_id);
-- Ghost-Dateien: eine Beschreibung je Pfad. Eigene Tabelle statt eines Typs in
-- knowledge, weil ein neuer Typ dort in sechs bestehenden Lesepfaden wieder
-- ausgeschlossen werden müsste und Vergessen nicht knallt, sondern still
-- Dateibeschreibungen in den Bootstrap kippt.
CREATE TABLE IF NOT EXISTS ghost_files(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  -- Repo-relativ und normalisiert; die Wurzel ist der leere String.
  path TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('file','dir')),
  description TEXT NOT NULL,
  -- Bei kind='file' der SHA-256 des Inhalts, bei kind='dir' der SHA-256 über
  -- die sortierte Liste der direkten Kinder. Ein Verzeichnis hat keinen Inhalt,
  -- es hat Kinder — sein Zweck ändert sich nicht, weil eine Funktion darin
  -- umgeschrieben wurde.
  content_sha TEXT NOT NULL DEFAULT '',
  -- git hash-object, nur bei kind='file'. Trägt den Diff gegen die beschriebene
  -- Fassung und die Erkennung von Umbenennungen.
  git_blob TEXT NOT NULL DEFAULT '',
  line_count INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '',
  session_ref TEXT NOT NULL DEFAULT '',
  described_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(project, path));
CREATE INDEX IF NOT EXISTS ghost_files_blob ON ghost_files(project, git_blob);
CREATE VIRTUAL TABLE IF NOT EXISTS ghost_files_fts
  USING fts5(path, description, content='ghost_files', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS ghost_files_ai AFTER INSERT ON ghost_files BEGIN
  INSERT INTO ghost_files_fts(rowid,path,description) VALUES(new.id,new.path,new.description);
END;
CREATE TRIGGER IF NOT EXISTS ghost_files_au AFTER UPDATE ON ghost_files BEGIN
  INSERT INTO ghost_files_fts(ghost_files_fts,rowid,path,description) VALUES('delete',old.id,old.path,old.description);
  INSERT INTO ghost_files_fts(rowid,path,description) VALUES(new.id,new.path,new.description);
END;
CREATE TRIGGER IF NOT EXISTS ghost_files_ad AFTER DELETE ON ghost_files BEGIN
  INSERT INTO ghost_files_fts(ghost_files_fts,rowid,path,description) VALUES('delete',old.id,old.path,old.description);
END;
-- Jede Fassung, die von einer neueren abgelöst wurde. Die Datei selbst hat ihre
-- Historie in git; diese Tabelle ist die Historie der BESCHREIBUNG, und die
-- steht nirgendwo sonst.
--
-- Ursprünglich war ausdrücklich keine vorgesehen, mit der Begründung, eine alte
-- Beschreibung eines dreimal umgeschriebenen Codes sei schlimmer als keine. Das
-- gilt weiter für die AUSLIEFERUNG — ausgeliefert wird nur die aktuelle
-- Fassung. Es gilt nicht für die Aufbewahrung: ein Beschreiben ist ein Upsert
-- ohne Rückfrage, und es gab zwei Wege, auf denen eine gute Beschreibung still
-- verschwand (der Hook forderte beim zweiten Ändern eine neue an, obwohl es
-- eine gab; eine Dateikopie hängte die Beschreibung des Originals auf sich um).
-- Ohne Aufbewahrung ist beides unwiederbringlich.
--
-- Kein Fremdschlüssel auf ghost_files: der Eintrag überlebt absichtlich, wenn
-- die Datei und ihre Beschreibung längst weg sind. Genau dann ist er wertvoll.
CREATE TABLE IF NOT EXISTS ghost_file_versions(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'file',
  description TEXT NOT NULL,
  -- Der Codestand, den diese Fassung beschrieb. Damit ist später zu sehen,
  -- welche Fassung der Datei jemand vor sich hatte, als er das schrieb.
  content_sha TEXT NOT NULL DEFAULT '',
  git_blob TEXT NOT NULL DEFAULT '',
  line_count INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '',
  session_ref TEXT NOT NULL DEFAULT '',
  -- Wann diese Fassung geschrieben wurde, und wann sie abgelöst wurde.
  described_at TEXT NOT NULL, replaced_at TEXT NOT NULL,
  -- Warum sie nicht mehr gilt: 'ersetzt' (neu beschrieben) oder 'verschoben'
  -- (der Pfad wanderte). Ein Umzug ist keine neue Erkenntnis, aber er soll
  -- nachvollziehbar sein.
  reason TEXT NOT NULL DEFAULT 'ersetzt');
CREATE INDEX IF NOT EXISTS ghost_file_versions_path
  ON ghost_file_versions(project, path, replaced_at DESC);
-- Was in dieser Session schon gesagt wurde: ausgelieferte Beschreibungen UND
-- ausgesprochene Aufforderungen. Auf den Pfad geschlüsselt statt auf die
-- Eintrags-Id, weil eine Aufforderung einen Pfad meint, für den es noch keinen
-- Eintrag gibt. Kein Fremdschlüssel auf sessions: der Hook feuert, bevor der
-- Collector die Session angelegt haben muss.
CREATE TABLE IF NOT EXISTS ghost_deliveries(
  session_key TEXT NOT NULL,
  project TEXT NOT NULL,
  path TEXT NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(session_key, project, path));
-- Angesehen und absichtlich nicht beschrieben. Ohne diesen Zustand kann der
-- Baum "noch nie angesehen" nicht von "angesehen, nichts zu sagen"
-- unterscheiden, und jeder weitere Bestandslauf liest dieselben verworfenen
-- Dateien erneut — womit "wiederaufnehmbar" eine falsche Zusage wäre.
--
-- Eigene Tabelle und nicht eine Spalte auf ghost_files: eine neue Pflichtspalte
-- entsteht auf einer bestehenden Datenbank nicht von selbst und liesse jede
-- Abfrage, die sie nennt, still leer laufen (#432). Neue Tabellen entstehen bei
-- jedem Open(). Muster und Schlüssel wie ghost_deliveries.
CREATE TABLE IF NOT EXISTS ghost_reviews(
  project TEXT NOT NULL,
  path TEXT NOT NULL,
  -- git hash-object der angesehenen Fassung. Die Entscheidung "nichts zu sagen"
  -- galt dieser Fassung; ändert die Datei sich, ist der Pfad wieder Kandidat.
  git_blob TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL,
  PRIMARY KEY(project, path));
CREATE TABLE IF NOT EXISTS knowledge_versions(
  id INTEGER PRIMARY KEY,
  knowledge_id INTEGER NOT NULL,
  type TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '', changed_by TEXT NOT NULL DEFAULT '', changed_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS knowledge_versions_entry
  ON knowledge_versions(knowledge_id, changed_at DESC, id DESC);
INSERT OR IGNORE INTO search_documents(kind,domain_id,title,body,project,branch,machine)
  SELECT 'knowledge',id,title,body,project,branch,machine FROM knowledge;
CREATE TABLE IF NOT EXISTS documents(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  slug TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('spec','plan','investigation','report','other')),
  title TEXT NOT NULL,
  head_revision INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project,slug));
CREATE TABLE IF NOT EXISTS document_revisions(
  id INTEGER PRIMARY KEY,
  document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  body TEXT NOT NULL,
  digest TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(document_id,revision));
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
	if err := ensureKnowledgeConfirmedBy(db); err != nil {
		return nil, err
	}
	if err := ensureKnowledgeLastModifiedBy(db); err != nil {
		return nil, err
	}
	// Ohne diese beiden liefert jede Abfrage, die sie nennt, auf einer
	// bestehenden Datenbank einen Fehler statt eines Ergebnisses — CREATE TABLE
	// IF NOT EXISTS ist dort ein No-op.
	if err := ensureKnowledgeColumn(db, "regression_state"); err != nil {
		return nil, err
	}
	if err := ensureKnowledgeColumn(db, "regression_test"); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// ensureKnowledgeColumn ergänzt eine Textspalte mit leerem Vorgabewert, falls
// sie fehlt. Nur für genau diese Form: eine Spalte mit Vorgabewert lässt sich
// nachträglich anfügen, eine Pflichtspalte ohne nicht.
func ensureKnowledgeColumn(db *sql.DB, name string) error {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var column, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &column, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if column == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE knowledge ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT ''`)
	return err
}

func ensureKnowledgeLastModifiedBy(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "last_modified_by" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE knowledge ADD COLUMN last_modified_by TEXT NOT NULL DEFAULT ''`)
	return err
}

func ensureKnowledgeConfirmedBy(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "confirmed_by" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE knowledge ADD COLUMN confirmed_by TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the connection for schema inspection at startup.
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return time.Now().UTC().Format(time.RFC3339) }
