package store

import "database/sql"

// ideaWorkshopSchema (REQ-611): Texte der Idee mit Fassungen, offene
// Entscheidungen mit Varianten und Stimmen, Bild-Anhänge, Projekt-Policy. Alles
// neue Tabellen, additiv; ideas bekommt nur zwei Spalten (phase, hint_shown).
//
// idea_texts: Rohfassung, Kurzfassung und Konzept je als eigene, nur anfügende
// Reihe von Fassungen (idea_id, part, revision). Nichts wird überschrieben; die
// Rohfassung steht Byte für Byte da, das Konzept als JSON mit den festen
// Abschnitten. Eigene Tabelle statt Dokumenten: ein Dokument hat eigene Rechte,
// einen eigenen Spiegel und eine Suche, und die Rohfassung eines Meetings soll
// genau die Rechte der Idee haben und sonst nirgends auftauchen.
//
// open_decisions: eine Frage an einer Idee oder einem Issue (subject_kind,
// subject_id). way sagt, wer entscheiden darf (human oder agent) und bleibt beim
// Vertagen und Fallenlassen erhalten; state ist der Stand (open, decided,
// deferred, dropped). Die „Art der Schließung“ der Oberfläche ist way, solange
// offen oder entschieden, sonst state. Die Tabelle trägt das Projekt selbst, damit die
// Zugriffsprüfung ohne Join auskommt; der Gegenstand wird beim Schreiben auf
// dasselbe Projekt geprüft.
//
// decision_variants und decision_votes: Varianten A, B, C mit Text, Pro und
// Contra; je Person und Art (Mensch, Agent) eine Stimme, die letzte zählt.
//
// idea_approvals: der Freigabe-Marker. Er entsteht nur, wenn ein Mensch (Websitzung)
// eine Idee freigibt, und trägt wer, wann, die Fassung des Konzepts und die
// Anforderungen zu diesem Zeitpunkt (JSON). Issues aus der Idee verlangen den
// aktiven Marker, nicht bloß die Phase; „umgesetzt“ rechnet gegen den Schnappschuss
// der Anforderungen, den ein Agent nicht umschreiben kann. Zurück aus der Freigabe
// wird der Marker widerrufen (revoked_at), nie gelöscht.
//
// attachments: Bild-Anhänge als Blob in der Datenbank (höchstens 5 MB, geprüft),
// gebunden an Idee oder Issue (subject) und an Idee, Antwort oder Variante
// (owner). Rechte folgen dem Gegenstand.
const ideaWorkshopSchema = `
CREATE TABLE IF NOT EXISTS idea_texts(
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  part TEXT NOT NULL CHECK(part IN ('raw','summary','concept')),
  revision INTEGER NOT NULL CHECK(revision >= 1),
  body TEXT NOT NULL,
  digest TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  by_agent INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY(idea_id, part, revision));
CREATE TABLE IF NOT EXISTS open_decisions(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  subject_kind TEXT NOT NULL CHECK(subject_kind IN ('idea','request')),
  subject_id INTEGER NOT NULL,
  question TEXT NOT NULL CHECK(trim(question) != ''),
  detail TEXT NOT NULL DEFAULT '',
  way TEXT NOT NULL CHECK(way IN ('human','agent')),
  way_why TEXT NOT NULL DEFAULT '',
  aspects TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','decided','deferred','dropped')),
  answer TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  trigger_text TEXT NOT NULL DEFAULT '',
  chosen_variant_id INTEGER,
  knowledge_id INTEGER,
  decided_by TEXT NOT NULL DEFAULT '',
  decided_by_agent INTEGER NOT NULL DEFAULT 0,
  decided_at TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS open_decisions_subject ON open_decisions(subject_kind, subject_id);
CREATE INDEX IF NOT EXISTS open_decisions_project_state ON open_decisions(project, state, way);
CREATE TABLE IF NOT EXISTS decision_variants(
  id INTEGER PRIMARY KEY,
  decision_id INTEGER NOT NULL REFERENCES open_decisions(id) ON DELETE RESTRICT,
  label TEXT NOT NULL,
  title TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  pro TEXT NOT NULL DEFAULT '',
  contra TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(decision_id, label));
CREATE TABLE IF NOT EXISTS decision_votes(
  decision_id INTEGER NOT NULL REFERENCES open_decisions(id) ON DELETE RESTRICT,
  voter_key TEXT NOT NULL,
  voter TEXT NOT NULL,
  by_agent INTEGER NOT NULL DEFAULT 0,
  variant_id INTEGER NOT NULL REFERENCES decision_variants(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  PRIMARY KEY(decision_id, voter_key));
CREATE TABLE IF NOT EXISTS decision_events(
  id INTEGER PRIMARY KEY,
  decision_id INTEGER NOT NULL REFERENCES open_decisions(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL, person TEXT NOT NULL DEFAULT '', by_agent INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS decision_events_decision ON decision_events(decision_id);
CREATE TABLE IF NOT EXISTS idea_approvals(
  id INTEGER PRIMARY KEY,
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  concept_revision INTEGER NOT NULL,
  requirements TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0,
  approved_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idea_approvals_idea ON idea_approvals(idea_id);
CREATE TABLE IF NOT EXISTS decision_policy(
  project TEXT PRIMARY KEY CHECK(project != ''),
  mode TEXT NOT NULL CHECK(mode IN ('rule','human')),
  set_by TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS attachments(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  subject_kind TEXT NOT NULL CHECK(subject_kind IN ('idea','request')),
  subject_id INTEGER NOT NULL,
  owner_kind TEXT NOT NULL CHECK(owner_kind IN ('idea','reply','variant')),
  owner_ref TEXT NOT NULL DEFAULT '',
  mime TEXT NOT NULL CHECK(mime IN ('image/png','image/jpeg','image/webp')),
  bytes INTEGER NOT NULL CHECK(bytes > 0 AND bytes <= 5242880),
  width INTEGER NOT NULL, height INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  alt TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  data BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS attachments_subject ON attachments(subject_kind, subject_id);
`

// ensureIdeaWorkshop legt die Tabellen und die zwei Spalten an, beliebig oft
// aufrufbar. Er läuft nach ensureLabelsAndIdeas, denn ideas muss da sein.
func ensureIdeaWorkshop(db *sql.DB) error {
	if _, err := db.Exec(ideaWorkshopSchema); err != nil {
		return err
	}
	if _, err := db.Exec(ideaCoverageSchema); err != nil {
		return err
	}
	if err := ensureColumn(db, "ideas", "phase", `TEXT NOT NULL DEFAULT 'raw'`); err != nil {
		return err
	}
	return ensureColumn(db, "ideas", "hint_shown", `INTEGER NOT NULL DEFAULT 0`)
}
