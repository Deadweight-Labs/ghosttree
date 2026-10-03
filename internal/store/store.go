// Package store is ghosttree's SQLite persistence layer.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db            *sql.DB
	path          string
	snapshotFault func(string) error
	writer        *runtimeWriter
	bookkeeper    *runtimeWriter
	reader        *Store
	deviceOnce    sync.Once
	device        *DeviceFlows
	joinOnce      sync.Once
	join          *JoinSessions
	closeOnce     sync.Once
	closeErr      error
	acfg          *accessConfig
	accessOnce    sync.Once
}

type OpenOptions struct {
	MaxOpenConns int
}

type RuntimeStats struct {
	Writer                            WriterStats
	Reader                            sql.DBStats
	DB                                sql.DBStats
	DatabaseBytes, WALBytes, SHMBytes int64
}

const defaultFileMaxOpenConns = 1

const schema = `
CREATE TABLE IF NOT EXISTS persons(
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  token_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_identities(
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  issuer TEXT NOT NULL, subject TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY(issuer, subject));
CREATE TABLE IF NOT EXISTS account_codes(
  code_hash TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('bootstrap','claim','login')),
  account_id INTEGER NOT NULL DEFAULT 0, expires_at TEXT NOT NULL,
  used_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS api_tokens(
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  token_hash TEXT NOT NULL UNIQUE, label TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK(kind IN ('cli','legacy','device')),
  machine TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, last_used_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '', revoked_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS account_migrations(
  version INTEGER PRIMARY KEY, migrated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS orgs(
  id INTEGER PRIMARY KEY, slug TEXT UNIQUE NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS org_members(
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  role TEXT NOT NULL CHECK(role IN ('owner','member')), joined_at TEXT NOT NULL,
  PRIMARY KEY(org_id, account_id));
CREATE INDEX IF NOT EXISTS org_members_account ON org_members(account_id);
CREATE TABLE IF NOT EXISTS projects(
  id INTEGER PRIMARY KEY, remote TEXT UNIQUE NOT NULL,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS projects_org ON projects(org_id);
CREATE TABLE IF NOT EXISTS invitations(
  id INTEGER PRIMARY KEY,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  project_id INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL CHECK(role IN ('owner','member')),
  email TEXT NOT NULL DEFAULT '', code_hash TEXT NOT NULL UNIQUE,
  invited_by INTEGER NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL,
  accepted_by INTEGER NOT NULL DEFAULT 0, accepted_at TEXT NOT NULL DEFAULT '',
  revoked_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS invitations_org ON invitations(org_id, id DESC);
CREATE TABLE IF NOT EXISTS org_events(
  id INTEGER PRIMARY KEY, org_id INTEGER NOT NULL, action TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS org_state(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS project_members(
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  role TEXT NOT NULL CHECK(role IN ('owner','lead','member','guest')),
  can_review INTEGER NOT NULL DEFAULT 0 CHECK(can_review IN (0,1)),
  granted_by INTEGER NOT NULL DEFAULT 0, granted_at TEXT NOT NULL,
  PRIMARY KEY(project_id, account_id));
CREATE INDEX IF NOT EXISTS project_members_account ON project_members(account_id);
CREATE TABLE IF NOT EXISTS role_events(
  id INTEGER PRIMARY KEY, scope TEXT NOT NULL, subject TEXT NOT NULL,
  old_role TEXT NOT NULL DEFAULT '', new_role TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '', via TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS role_events_no_update BEFORE UPDATE ON role_events
  BEGIN SELECT RAISE(ABORT, 'role_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS role_events_no_delete BEFORE DELETE ON role_events
  BEGIN SELECT RAISE(ABORT, 'role_events is append-only'); END;
CREATE TABLE IF NOT EXISTS context_snapshot_access(
  person_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  project TEXT NOT NULL,
  can_read INTEGER NOT NULL CHECK(can_read IN (0,1)),
  can_create INTEGER NOT NULL CHECK(can_create IN (0,1)),
  can_release_bind INTEGER NOT NULL CHECK(can_release_bind IN (0,1)),
  PRIMARY KEY(person_id, project));
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
  kind TEXT NOT NULL, person TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
  session_id INTEGER NOT NULL DEFAULT 0);
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
CREATE TABLE IF NOT EXISTS knowledge_groups(
  id INTEGER PRIMARY KEY, project TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  volatility TEXT NOT NULL DEFAULT 'unrated'
    CHECK(volatility IN ('unrated','volatile','slow','timeless')),
  volatility_by TEXT NOT NULL DEFAULT '', volatility_at TEXT NOT NULL DEFAULT '',
  -- Groups are never deleted, so their events stay attributable: a merged
  -- group points at the survivor, a group without members is dissolved.
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','merged','dissolved')),
  merged_into INTEGER REFERENCES knowledge_groups(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL,
  CHECK((state='merged') = (merged_into IS NOT NULL)));
CREATE TABLE IF NOT EXISTS knowledge_relations(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  from_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE RESTRICT,
  to_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('supersedes','sibling')),
  state TEXT NOT NULL CHECK(state IN ('proposed','active','rejected','revoked')),
  origin TEXT NOT NULL CHECK(origin IN ('human','agent','distiller','migrated')),
  group_id INTEGER REFERENCES knowledge_groups(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL, created_by_account INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  decided_by TEXT NOT NULL DEFAULT '', decided_by_account INTEGER NOT NULL DEFAULT 0, decided_at TEXT NOT NULL DEFAULT '',
  CHECK(from_id <> to_id),
  CHECK(kind <> 'sibling' OR from_id < to_id));
CREATE UNIQUE INDEX IF NOT EXISTS knowledge_relations_live
  ON knowledge_relations(kind, from_id, to_id) WHERE state IN ('proposed','active');
CREATE INDEX IF NOT EXISTS knowledge_relations_from ON knowledge_relations(from_id);
CREATE INDEX IF NOT EXISTS knowledge_relations_to ON knowledge_relations(to_id);
CREATE INDEX IF NOT EXISTS knowledge_relations_group ON knowledge_relations(group_id);
CREATE TABLE IF NOT EXISTS knowledge_relation_events(
  id INTEGER PRIMARY KEY, project TEXT NOT NULL DEFAULT '',
  relation_id INTEGER, group_id INTEGER,
  action TEXT NOT NULL,
  actor TEXT NOT NULL, actor_account_id INTEGER NOT NULL DEFAULT 0, actor_role TEXT NOT NULL DEFAULT '',
  via TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS knowledge_relation_events_rel ON knowledge_relation_events(relation_id);
CREATE INDEX IF NOT EXISTS knowledge_relation_events_group ON knowledge_relation_events(group_id);
CREATE TRIGGER IF NOT EXISTS knowledge_relation_events_no_update BEFORE UPDATE ON knowledge_relation_events
  BEGIN SELECT RAISE(ABORT, 'knowledge_relation_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS knowledge_relation_events_no_delete BEFORE DELETE ON knowledge_relation_events
  BEGIN SELECT RAISE(ABORT, 'knowledge_relation_events is append-only'); END;
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
CREATE TABLE IF NOT EXISTS ghost_archive_receipts(
  project TEXT NOT NULL,
  path TEXT NOT NULL,
  token TEXT NOT NULL,
  person TEXT NOT NULL,
  reason TEXT NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(project,path,token));
CREATE TABLE IF NOT EXISTS ghost_path_revisions(
  project TEXT NOT NULL,
  path TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK(revision>0),
  PRIMARY KEY(project,path));
INSERT OR IGNORE INTO ghost_path_revisions(project,path,revision)
  SELECT project,path,1 FROM ghost_files;
CREATE TRIGGER IF NOT EXISTS ghost_path_revision_insert AFTER INSERT ON ghost_files BEGIN
  INSERT INTO ghost_path_revisions(project,path,revision) VALUES(new.project,new.path,1)
    ON CONFLICT(project,path) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER IF NOT EXISTS ghost_path_revision_delete AFTER DELETE ON ghost_files BEGIN
  INSERT INTO ghost_path_revisions(project,path,revision) VALUES(old.project,old.path,1)
    ON CONFLICT(project,path) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER IF NOT EXISTS ghost_path_revision_update AFTER UPDATE ON ghost_files BEGIN
  INSERT INTO ghost_path_revisions(project,path,revision) VALUES(old.project,old.path,1)
    ON CONFLICT(project,path) DO UPDATE SET revision=revision+1;
  INSERT INTO ghost_path_revisions(project,path,revision)
    SELECT new.project,new.path,1 WHERE new.project!=old.project OR new.path!=old.path
    ON CONFLICT(project,path) DO UPDATE SET revision=revision+1;
END;
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
CREATE TABLE IF NOT EXISTS coord_agents(
  id INTEGER PRIMARY KEY,
  external_id TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL,
  room_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  person TEXT,
  principal_id TEXT NOT NULL DEFAULT '',
  cwd TEXT,
  branch TEXT,
  worktree TEXT,
  parent_external_id TEXT,
  capabilities TEXT,
  registered_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS coord_agents_room ON coord_agents(room_key,last_seen_at);
CREATE TABLE IF NOT EXISTS coord_messages(
  id INTEGER PRIMARY KEY,
  destination_kind TEXT NOT NULL CHECK(destination_kind IN ('room','discussion')),
  destination_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  sender_external_id TEXT NOT NULL,
  author_principal_id TEXT NOT NULL DEFAULT '',
  author_kind TEXT NOT NULL CHECK(author_kind IN ('agent','human','system')),
  parent_external_id TEXT,
  client_id TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'message',
  intent TEXT NOT NULL DEFAULT '',
  priority TEXT NOT NULL DEFAULT 'normal',
  body TEXT NOT NULL,
  reply_to INTEGER,
  origin_event_id TEXT,
  causation_id TEXT,
  expires_at TEXT,
  observed_at_client TEXT,
  created_at TEXT NOT NULL,
  UNIQUE(sender_external_id,client_id),
  UNIQUE(destination_kind,destination_id,sequence));
CREATE INDEX IF NOT EXISTS coord_messages_destination
  ON coord_messages(destination_kind,destination_id,id);
CREATE INDEX IF NOT EXISTS coord_messages_destination_sequence
  ON coord_messages(destination_kind,destination_id,sequence);
CREATE UNIQUE INDEX IF NOT EXISTS coord_messages_origin
  ON coord_messages(origin_event_id) WHERE origin_event_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS coord_destination_sequences(
  destination_kind TEXT NOT NULL,
  destination_id TEXT NOT NULL,
  last_sequence INTEGER NOT NULL CHECK(last_sequence>=0),
  PRIMARY KEY(destination_kind,destination_id));
INSERT INTO coord_destination_sequences(destination_kind,destination_id,last_sequence)
  SELECT destination_kind,destination_id,MAX(sequence) FROM coord_messages
  GROUP BY destination_kind,destination_id
  ON CONFLICT(destination_kind,destination_id) DO UPDATE SET
    last_sequence=MAX(last_sequence,excluded.last_sequence);
CREATE TABLE IF NOT EXISTS threads(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  title TEXT NOT NULL,
  question TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','resolved','deferred')),
  archived INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '',
  author_principal_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  resolved_at TEXT);
CREATE INDEX IF NOT EXISTS threads_project ON threads(project,archived,updated_at);
CREATE TABLE IF NOT EXISTS thread_homes(
  thread_id INTEGER PRIMARY KEY REFERENCES threads(id) ON DELETE RESTRICT,
  room_key TEXT NOT NULL REFERENCES coord_rooms(room_key) ON DELETE RESTRICT,
  anchor_message_id INTEGER UNIQUE REFERENCES coord_messages(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS thread_homes_room ON thread_homes(room_key,created_at,thread_id);
CREATE TABLE IF NOT EXISTS thread_links(
  thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  object_kind TEXT NOT NULL,
  object_id TEXT NOT NULL,
  object_revision TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(thread_id,object_kind,object_id,object_revision));
CREATE INDEX IF NOT EXISTS thread_links_object ON thread_links(object_kind,object_id);
CREATE TABLE IF NOT EXISTS thread_sources(
  thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  source_kind TEXT NOT NULL,
  source_id TEXT NOT NULL,
  room_key TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  author_kind TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  original_at TEXT NOT NULL DEFAULT '',
  copied_at TEXT NOT NULL,
  PRIMARY KEY(thread_id,source_kind,source_id));
CREATE TABLE IF NOT EXISTS thread_visibility(
  thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  member_external_id TEXT NOT NULL,
  PRIMARY KEY(thread_id,member_external_id));
CREATE TABLE IF NOT EXISTS thread_summaries(
  thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  body TEXT NOT NULL,
  open_questions TEXT NOT NULL DEFAULT '',
  covers_through_sequence INTEGER NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(thread_id,revision));
CREATE TABLE IF NOT EXISTS thread_outcomes(
  thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'proposed' CHECK(state IN ('proposed','accepted','rejected')),
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  decided_at TEXT,
  PRIMARY KEY(thread_id,kind,ref_id));
CREATE TABLE IF NOT EXISTS path_activity(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL DEFAULT '',
  session_external_id TEXT NOT NULL,
  checkout TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL,
  path TEXT NOT NULL,
  writes INTEGER NOT NULL DEFAULT 0,
  quality TEXT NOT NULL CHECK(quality IN ('intent','reported_success','observed_change','unattributed')),
  at TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  UNIQUE(account_id,session_external_id,tool,path,quality,at));
CREATE INDEX IF NOT EXISTS path_activity_path ON path_activity(project,path,at);
CREATE INDEX IF NOT EXISTS path_activity_session ON path_activity(session_external_id,at);
CREATE TABLE IF NOT EXISTS coord_standing(
  room_key TEXT NOT NULL,
  message_id TEXT NOT NULL,
  person TEXT NOT NULL,
  body TEXT NOT NULL,
  targets TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  ended_at TEXT,
  ended_by TEXT,
  PRIMARY KEY(room_key,message_id));
CREATE TABLE IF NOT EXISTS coord_rooms(
  room_key TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('project','machine','direct','group')),
  label TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS coord_room_members(
  room_key TEXT NOT NULL REFERENCES coord_rooms(room_key) ON DELETE CASCADE,
  member_external_id TEXT NOT NULL,
  joined_at TEXT NOT NULL,
  PRIMARY KEY(room_key,member_external_id));
CREATE TABLE IF NOT EXISTS coord_room_memberships(
  room_key TEXT NOT NULL REFERENCES coord_rooms(room_key) ON DELETE RESTRICT,
  principal_id TEXT NOT NULL,
  joined_at TEXT NOT NULL,
  left_at TEXT NOT NULL DEFAULT '',
  is_manager INTEGER NOT NULL DEFAULT 0 CHECK(is_manager IN (0,1)),
  PRIMARY KEY(room_key,principal_id,joined_at));
CREATE UNIQUE INDEX IF NOT EXISTS coord_room_memberships_one_active
  ON coord_room_memberships(room_key,principal_id) WHERE left_at='';
CREATE INDEX IF NOT EXISTS coord_room_memberships_active
  ON coord_room_memberships(principal_id,room_key) WHERE left_at='';
CREATE TABLE IF NOT EXISTS coord_room_membership_migrations(
  version INTEGER PRIMARY KEY,
  migrated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS coord_room_membership_events(
  id INTEGER PRIMARY KEY,
  room_key TEXT NOT NULL REFERENCES coord_rooms(room_key) ON DELETE RESTRICT,
  principal_id TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL CHECK(action IN ('group_create','join','leave','manager_grant','manager_revoke')),
  actor_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS coord_room_membership_events_no_update
  BEFORE UPDATE ON coord_room_membership_events BEGIN SELECT RAISE(ABORT,'membership events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS coord_room_membership_events_no_delete
  BEFORE DELETE ON coord_room_membership_events BEGIN SELECT RAISE(ABORT,'membership events are append-only'); END;
CREATE TABLE IF NOT EXISTS coord_deliveries(
  message_id INTEGER NOT NULL,
  recipient_external_id TEXT NOT NULL,
  state TEXT NOT NULL,
  rank INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(message_id,recipient_external_id));
CREATE TABLE IF NOT EXISTS coord_cursors(
  agent_external_id TEXT NOT NULL,
  destination_kind TEXT NOT NULL,
  destination_id TEXT NOT NULL,
  last_message_id INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(agent_external_id,destination_kind,destination_id));
CREATE TABLE IF NOT EXISTS coord_message_mentions(
  message_id INTEGER NOT NULL REFERENCES coord_messages(id) ON DELETE CASCADE,
  mentioned_external_id TEXT NOT NULL,
  PRIMARY KEY(message_id,mentioned_external_id));
CREATE TABLE IF NOT EXISTS coord_message_raw_mentions(
  message_id INTEGER NOT NULL REFERENCES coord_messages(id) ON DELETE CASCADE,
  mentioned_external_id TEXT NOT NULL,
  PRIMARY KEY(message_id,mentioned_external_id));
CREATE TABLE IF NOT EXISTS coord_message_sender_roles(
  message_id INTEGER PRIMARY KEY REFERENCES coord_messages(id) ON DELETE CASCADE,
  role TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS coord_message_mentions_recipient
  ON coord_message_mentions(mentioned_external_id,message_id);
CREATE TABLE IF NOT EXISTS coord_attention(
  id INTEGER PRIMARY KEY,
  recipient_principal_id TEXT NOT NULL,
  message_id INTEGER NOT NULL REFERENCES coord_messages(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL CHECK(reason IN ('question','approval','blocker','handoff')),
  state TEXT NOT NULL CHECK(state IN ('open','resolved','dismissed','expired')),
  created_at TEXT NOT NULL,
  resolved_at TEXT NOT NULL DEFAULT '',
  UNIQUE(recipient_principal_id,message_id,reason));
CREATE INDEX IF NOT EXISTS coord_attention_recipient
  ON coord_attention(recipient_principal_id,state,id);
INSERT OR IGNORE INTO coord_attention(recipient_principal_id,message_id,reason,state,created_at)
  SELECT mm.mentioned_external_id,m.id,m.intent,'open',m.created_at
  FROM coord_messages m JOIN coord_message_mentions mm ON mm.message_id=m.id
  WHERE m.intent IN ('question','approval','blocker','handoff');
CREATE TABLE IF NOT EXISTS coord_read_state(
  principal_id TEXT NOT NULL,
  destination_kind TEXT NOT NULL CHECK(destination_kind IN ('room','discussion')),
  destination_id TEXT NOT NULL,
  read_through_sequence INTEGER NOT NULL DEFAULT 0 CHECK(read_through_sequence>=0),
  manual_unread_from_sequence INTEGER CHECK(manual_unread_from_sequence>0),
  updated_at TEXT NOT NULL,
  PRIMARY KEY(principal_id,destination_kind,destination_id));
CREATE TABLE IF NOT EXISTS coord_message_refs(
  message_id INTEGER NOT NULL REFERENCES coord_messages(id) ON DELETE CASCADE,
  ref_kind TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  ref_revision TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(message_id,ref_kind,ref_id,ref_revision));
CREATE TABLE IF NOT EXISTS coord_events(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  object_kind TEXT NOT NULL,
  object_id TEXT NOT NULL,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS coord_events_created ON coord_events(created_at);
DROP TRIGGER IF EXISTS coord_events_bound;
DROP TRIGGER IF EXISTS coord_events_age;
CREATE TRIGGER coord_events_age AFTER INSERT ON coord_events BEGIN
  DELETE FROM coord_events
  WHERE created_at<strftime('%Y-%m-%dT%H:%M:%fZ','now','-10 minutes')
     OR sequence<=NEW.sequence-100000;
END;
CREATE TRIGGER IF NOT EXISTS coord_messages_event AFTER INSERT ON coord_messages BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('message',NEW.destination_kind,NEW.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_rooms_insert_event AFTER INSERT ON coord_rooms BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('membership','room',NEW.room_key,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_rooms_update_event AFTER UPDATE OF label ON coord_rooms
WHEN OLD.label IS NOT NEW.label BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('membership','room',NEW.room_key,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_membership_event AFTER INSERT ON coord_room_membership_events BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('membership','room',NEW.room_key,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_membership_visibility_event AFTER INSERT ON coord_room_membership_events
WHEN NEW.action='leave' BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('visibility','principal',NEW.principal_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_read_insert_event AFTER INSERT ON coord_read_state BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('read',NEW.destination_kind,NEW.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_read_update_event AFTER UPDATE ON coord_read_state
WHEN OLD.read_through_sequence IS NOT NEW.read_through_sequence
  OR OLD.manual_unread_from_sequence IS NOT NEW.manual_unread_from_sequence BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('read',NEW.destination_kind,NEW.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_attention_insert_event AFTER INSERT ON coord_attention BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('attention','attention',CAST(NEW.id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_attention_update_event AFTER UPDATE ON coord_attention
WHEN OLD.state IS NOT NEW.state OR OLD.reason IS NOT NEW.reason
  OR OLD.resolved_at IS NOT NEW.resolved_at BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('attention','attention',CAST(NEW.id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_standing_insert_event AFTER INSERT ON coord_standing BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('standing','room',NEW.room_key,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_standing_update_event AFTER UPDATE ON coord_standing
WHEN OLD.body IS NOT NEW.body OR OLD.targets IS NOT NEW.targets
  OR OLD.ended_at IS NOT NEW.ended_at OR OLD.ended_by IS NOT NEW.ended_by BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('standing','room',NEW.room_key,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_thread_insert_event AFTER INSERT ON threads BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('thread','thread',CAST(NEW.id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_thread_update_event AFTER UPDATE ON threads BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('thread','thread',CAST(NEW.id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_thread_home_insert_event AFTER INSERT ON thread_homes BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('thread','thread',CAST(NEW.thread_id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_thread_link_insert_event AFTER INSERT ON thread_links BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('thread','thread',CAST(NEW.thread_id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_thread_link_delete_event AFTER DELETE ON thread_links BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  VALUES('thread','thread',CAST(OLD.thread_id AS TEXT),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER IF NOT EXISTS coord_delivery_insert_event AFTER INSERT ON coord_deliveries BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  SELECT 'delivery',destination_kind,destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
  FROM coord_messages WHERE id=NEW.message_id;
END;
CREATE TRIGGER IF NOT EXISTS coord_delivery_update_event AFTER UPDATE ON coord_deliveries
WHEN OLD.state IS NOT NEW.state OR OLD.rank IS NOT NEW.rank BEGIN
  INSERT INTO coord_events(kind,object_kind,object_id,created_at)
  SELECT 'delivery',destination_kind,destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
  FROM coord_messages WHERE id=NEW.message_id;
END;
`

func Open(path string) (*Store, error) {
	if sqliteFilePath(path) == "" {
		return OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	}
	return OpenRuntime(path, DefaultWriterConfig())
}

func OpenReadOnly(path string, maxOpenConns int) (*Store, error) {
	if maxOpenConns <= 0 {
		return nil, fmt.Errorf("max open connections must be positive")
	}
	dbPath := sqliteFilePath(path)
	if dbPath == "" {
		return nil, fmt.Errorf("read-only store requires a file-backed database")
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database path %q is not a regular file", dbPath)
	}
	// Ein relativer Pfad würde als URI "file://./x.db" mit der Autorität "."
	// gelesen; absolut hat die URI keine.
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: absPath}).String() +
		"?mode=ro&_pragma=foreign_keys(1)&_pragma=recursive_triggers(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func OpenWithOptions(path string, options OpenOptions) (*Store, error) {
	if options.MaxOpenConns <= 0 {
		return nil, fmt.Errorf("max open connections must be positive")
	}
	dbPath := sqliteFilePath(path)
	if dbPath == "" {
		options.MaxOpenConns = 1
	}
	if dbPath != "" {
		if err := prepareDatabaseFiles(dbPath); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", storeSQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if dbPath != "" {
		var journal string
		if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
			_ = db.Close()
			return nil, err
		}
		if strings.ToLower(journal) != "wal" {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite journal mode = %q, want wal", journal)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateAccounts(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateCoordRoomMemberships(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureCoordAgentPrincipalID(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensurePathActivityAccount(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureCoordAgentPresence(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureThreadHomes(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureCoordWaitCycles(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureActivitySession(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureCoordAgentRole(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureHumanAuthorityMarker(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureAgentControl(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateOwnership(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateOrgs(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSessionIndex(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureCanonicalSessionProjects(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureInvitationProjectRole(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureThreadAuthorPrincipalID(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureKnowledgeConfirmedBy(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureKnowledgeLastModifiedBy(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Ohne diese beiden liefert jede Abfrage, die sie nennt, auf einer
	// bestehenden Datenbank einen Fehler statt eines Ergebnisses — CREATE TABLE
	// IF NOT EXISTS ist dort ein No-op.
	if err := ensureKnowledgeColumn(db, "regression_state"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureKnowledgeColumn(db, "regression_test"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := EnsureContextSnapshotSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if report, err := BackfillKnowledgeRelations(db); err != nil {
		_ = db.Close()
		return nil, err
	} else if report.Unclean() {
		log.Printf("knowledge relations backfill: created=%d skipped_unusable=%d skipped_cycle=%d superseded_without_parent=%d",
			report.Created, report.SkippedUnusable, report.SkippedCycle, report.SupersededWithoutParent)
	}
	db.SetMaxOpenConns(options.MaxOpenConns)
	db.SetMaxIdleConns(options.MaxOpenConns)
	st := &Store{db: db, path: dbPath}
	if err := st.startIndexBackfill(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

func storeSQLiteDSN(path string) string {
	if path == ":memory:" {
		path = "file::memory:"
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	if strings.HasSuffix(path, "?") || strings.HasSuffix(path, "&") {
		separator = ""
	}
	return path + separator + "_pragma=foreign_keys(1)&_pragma=recursive_triggers(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"
}

func sqliteFilePath(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return ""
	}
	path, rawQuery, _ := strings.Cut(path, "?")
	if strings.HasPrefix(path, "file:") {
		query, err := url.ParseQuery(rawQuery)
		if err == nil && strings.EqualFold(query.Get("mode"), "memory") {
			return ""
		}
	}
	return strings.TrimPrefix(path, "file:")
}

func prepareDatabaseFiles(path string) error {
	if path == ":memory:" {
		return nil
	}
	if err := ensurePrivateDatabaseFile(path); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := tightenExistingDatabaseFile(path + suffix); err != nil {
			return err
		}
	}
	return nil
}

func ensurePrivateDatabaseFile(path string) error {
	for attempt := 0; attempt < 3; attempt++ {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("database path %q is not a regular file", path)
			}
			return chmodRegularFile(path, info, "database path")
		}
		if !os.IsNotExist(err) {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	return fmt.Errorf("database path %q changed while opening", path)
}

func tightenExistingDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database sidecar %q is not a regular file", path)
	}
	return chmodRegularFile(path, info, "database sidecar")
}

func chmodRegularFile(path string, expected os.FileInfo, kind string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return fmt.Errorf("%s %q changed while opening", kind, path)
	}
	return f.Chmod(0o600)
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

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.writer != nil {
			s.writer.close()
		}
		if s.reader != nil {
			s.closeErr = s.reader.Close()
		}
		if err := s.db.Close(); s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

func (s *Store) RuntimeStats() RuntimeStats {
	stats := RuntimeStats{DB: s.db.Stats()}
	if s.writer != nil {
		stats.Writer = s.writer.stats()
	}
	if s.reader != nil {
		stats.Reader = s.reader.db.Stats()
	}
	if s.path == "" {
		return stats
	}
	stats.DatabaseBytes = fileBytes(s.path)
	stats.WALBytes = fileBytes(s.path + "-wal")
	stats.SHMBytes = fileBytes(s.path + "-shm")
	return stats
}

func fileBytes(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// DB exposes the connection for schema inspection at startup.
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return time.Now().UTC().Format(time.RFC3339) }
