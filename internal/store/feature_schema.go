package store

import "database/sql"

// featureSchema (REQ-612): Features als lebende Beschreibung. Alles neue Tabellen,
// additiv; keine bestehende Tabelle bekommt eine Spalte.
//
// features ist die Identität: eine Nummer (FEAT-n, instanzweit eindeutig wie
// IDEA-n), das Projekt, der Zustand. Der Name steht hier, damit er je Projekt
// eindeutig sein kann (ein Index auf lower(name)); jede Fassung trägt ihn
// trotzdem mit, denn die Historie soll zeigen, wie das Feature hieß.
//
// feature_versions ist der Inhalt, nur anfügend (Trigger gegen Ändern und
// Löschen): jede Fassung ist ein vollständiger Stand, keine Differenz. Sie trägt
// Anker und Entstehungskontext des Schreibvorgangs wie eine Fassung eines Ablaufs
// (anchor_id, origin_id), dazu idea_id und pr, wenn sie von einer Idee oder in
// einem PR ausgelöst wurde. Die PR-Nummer des Ankers kommt später dazu
// (RecordPullRequest), die Spalte pr ist die ausdrücklich genannte. Issues, die eine
// Fassung ausgelöst haben, stehen in feature_version_requests.
//
// feature_links verbindet ein Feature mit Ideen und Issues: origin (daraus entstand
// es) oder affects (das ändert es: „betrifft FEAT-n“, REQ-611 Kriterium 13). Eine
// Tabelle für beide Richtungen, damit die Idee ihr Feature und das Feature seine
// Ideen aus derselben Zeile liest.
//
// feature_pr_paths sind die Pfade, die ein PR ändert, wie ein Client sie gemeldet
// hat (git diff). Der Server hat das Repo nicht und fragt GitHub nicht; die Pfade
// sind die Grundlage der Checkliste des PRs. feature_pr_checks hält fest, dass
// jemand einen PR gegen ein Feature geprüft und die Beschreibung für unverändert
// richtig befunden hat; ohne das ließe sich der Punkt bei einem Umbau, der kein
// Verhalten ändert, nie abhaken.
//
// feature_deliveries merkt, welche Fassung eines Features eine Session schon als
// Kontext bekommen hat (wie ghost_deliveries): je Session und Feature einmal je
// Fassung, damit ein Bereich nicht bei jedem Dateizugriff dasselbe wiederholt und
// das Budget des Hooks verbraucht.
const featureSchema = `
CREATE TABLE IF NOT EXISTS features(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  name TEXT NOT NULL CHECK(trim(name) != ''),
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','retired')),
  reason TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS features_project_name ON features(project, lower(name));
CREATE INDEX IF NOT EXISTS features_project_state ON features(project, state);
CREATE TABLE IF NOT EXISTS feature_versions(
  id INTEGER PRIMARY KEY,
  feature_id INTEGER NOT NULL REFERENCES features(id) ON DELETE RESTRICT,
  revision INTEGER NOT NULL CHECK(revision >= 1),
  name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  behavior TEXT NOT NULL DEFAULT '',
  limits TEXT NOT NULL DEFAULT '',
  cli TEXT NOT NULL DEFAULT '',
  mcp TEXT NOT NULL DEFAULT '',
  api TEXT NOT NULL DEFAULT '',
  ui TEXT NOT NULL DEFAULT '',
  areas TEXT NOT NULL DEFAULT '[]',
  digest TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  idea_id INTEGER NOT NULL DEFAULT 0,
  pr INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '',
  by_agent INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  anchor_id INTEGER REFERENCES git_anchors(id),
  origin_id INTEGER REFERENCES origin_contexts(id),
  UNIQUE(feature_id, revision));
CREATE INDEX IF NOT EXISTS feature_versions_pr ON feature_versions(pr) WHERE pr > 0;
CREATE INDEX IF NOT EXISTS feature_versions_idea ON feature_versions(idea_id) WHERE idea_id > 0;
CREATE TRIGGER IF NOT EXISTS feature_versions_immutable BEFORE UPDATE ON feature_versions
  BEGIN SELECT RAISE(ABORT,'feature_versions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS feature_versions_no_delete BEFORE DELETE ON feature_versions
  BEGIN SELECT RAISE(ABORT,'feature_versions is append-only'); END;
CREATE TABLE IF NOT EXISTS feature_version_requests(
  version_id INTEGER NOT NULL REFERENCES feature_versions(id) ON DELETE RESTRICT,
  request_id INTEGER NOT NULL,
  PRIMARY KEY(version_id, request_id));
CREATE TABLE IF NOT EXISTS feature_links(
  feature_id INTEGER NOT NULL REFERENCES features(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('idea','request')),
  ref_id INTEGER NOT NULL,
  rel TEXT NOT NULL CHECK(rel IN ('origin','affects')),
  added_by TEXT NOT NULL DEFAULT '',
  added_at TEXT NOT NULL,
  PRIMARY KEY(feature_id, kind, ref_id, rel));
CREATE INDEX IF NOT EXISTS feature_links_ref ON feature_links(kind, ref_id);
CREATE TABLE IF NOT EXISTS feature_pr_paths(
  project TEXT NOT NULL CHECK(project != ''),
  pr INTEGER NOT NULL CHECK(pr > 0),
  path TEXT NOT NULL,
  reported_by TEXT NOT NULL DEFAULT '',
  reported_at TEXT NOT NULL,
  PRIMARY KEY(project, pr, path));
CREATE TABLE IF NOT EXISTS feature_deliveries(
  session_key TEXT NOT NULL,
  feature_id INTEGER NOT NULL,
  revision INTEGER NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(session_key, feature_id, revision));
CREATE INDEX IF NOT EXISTS feature_deliveries_age ON feature_deliveries(at);
CREATE TABLE IF NOT EXISTS feature_pr_checks(
  feature_id INTEGER NOT NULL REFERENCES features(id) ON DELETE RESTRICT,
  pr INTEGER NOT NULL CHECK(pr > 0),
  note TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(feature_id, pr));
`

// ensureFeatures legt die Tabellen an, beliebig oft aufrufbar.
func ensureFeatures(db *sql.DB) error {
	_, err := db.Exec(featureSchema)
	return err
}
