package store

import "database/sql"

// embeddingsSchema holds the vectors of every kind except knowledge, which has
// tables of its own (knowledge_embeddings and the tables next to it) from the
// first version of hybrid search. Both are made on start, with CREATE TABLE IF
// NOT EXISTS, so no schema upgrade is needed; without a configured embedding
// server they stay empty and are never read.
//
// One table for the other kinds, told apart by kind, because a kind is one entry
// in embedKinds and a table is a migration: the ghost descriptions, requests,
// pains and documents are different objects with the same question (is the vector
// of this text still current?), and sessions can be added without touching the
// schema. object_id is the id of the object in its own table (ghost_files.id,
// requests.id, ...). There is no foreign key: the row may outlive an object that
// is gone or archived, and the search reads a vector only through the object.
// The job removes what is left over (PurgeEmbeddings).
//
// The columns mean what they mean in knowledge_embeddings: model and
// prefix_version belong to every vector, text_hash is the SHA-256 of the text
// that was sent, vec is dim int8 values with one scale, read_at is the time the
// job took the text from the object.
const embeddingsSchema = `
CREATE TABLE IF NOT EXISTS embeddings(
  kind TEXT NOT NULL, object_id INTEGER NOT NULL,
  model TEXT NOT NULL, prefix_version TEXT NOT NULL, dim INTEGER NOT NULL,
  text_hash TEXT NOT NULL, scale REAL NOT NULL, vec BLOB NOT NULL,
  read_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  PRIMARY KEY(kind, object_id)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS embedding_failures(
  kind TEXT NOT NULL, object_id INTEGER NOT NULL,
  model TEXT NOT NULL, prefix_version TEXT NOT NULL, text_hash TEXT NOT NULL,
  attempts INTEGER NOT NULL, permanent INTEGER NOT NULL DEFAULT 0,
  retry_at TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', failed_at TEXT NOT NULL,
  PRIMARY KEY(kind, object_id)) WITHOUT ROWID;
`

// sessionEmbeddingsSchema holds the vectors of the sessions, one per text (a
// prompt, the final answer of a turn), keyed by the chunk the text is taken from:
// session_chunks.id is one integer for the pair of session and sequence, and the
// session of a vector is the session of its chunk. Unlike the table above these
// reference their parent, with ON DELETE CASCADE: a chunk that is deleted takes
// its vector and its failure along, and so does a session whose chunks are
// deleted first (session_chunks does not cascade from sessions), without the job
// having to find them. The job still removes what a database without foreign
// keys left behind (PurgeEmbeddings), and the texts that are no longer picked.
//
// session_embedding_sync says which sessions were looked at and found complete:
// every text they have is embedded, or is too short to count, or was refused by
// the server for good. The job leaves such a session alone until it gets a chunk
// (sessions.last_seen_at reaches read_at) or the mark was made under another
// model, text format or version of the rules that make the texts
// (sessionTextVersion); read_at is the time taken before the session was read,
// like the read_at of a vector. A session that waits for something (its last
// answer has not stood long enough, a text is parked for a time) is not recorded,
// so it stays due and is looked at again.
//
// The columns of the first two are those of knowledge_embeddings.
const sessionEmbeddingsSchema = `
CREATE TABLE IF NOT EXISTS session_embeddings(
  chunk_id INTEGER NOT NULL PRIMARY KEY REFERENCES session_chunks(id) ON DELETE CASCADE,
  model TEXT NOT NULL, prefix_version TEXT NOT NULL, dim INTEGER NOT NULL,
  text_hash TEXT NOT NULL, scale REAL NOT NULL, vec BLOB NOT NULL,
  read_at TEXT NOT NULL, updated_at TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_embedding_failures(
  chunk_id INTEGER NOT NULL PRIMARY KEY REFERENCES session_chunks(id) ON DELETE CASCADE,
  model TEXT NOT NULL, prefix_version TEXT NOT NULL, text_hash TEXT NOT NULL,
  attempts INTEGER NOT NULL, permanent INTEGER NOT NULL DEFAULT 0,
  retry_at TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', failed_at TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_embedding_sync(
  session_id INTEGER NOT NULL PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  read_at TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL DEFAULT '',
  prefix_version TEXT NOT NULL DEFAULT '') WITHOUT ROWID;
`

// sessionSyncColumns are the columns of session_embedding_sync that a first
// version of the table did not have; a table that was made without them gets them,
// with values that no current model and version has, so that its sessions are
// looked at once more.
var sessionSyncColumns = []struct{ name, ddl string }{
	{"version", `INTEGER NOT NULL DEFAULT 0`},
	{"model", `TEXT NOT NULL DEFAULT ''`},
	{"prefix_version", `TEXT NOT NULL DEFAULT ''`},
}

// documentsFTSSchema is the full-text index of the documents, which had none:
// title, slug and the whole text of the head revision, keyed by the document id.
// The triggers keep it in step. A revision becomes the head when it is inserted
// (CreateDocument and PushRevision move head_revision first, then insert), a
// rename or a new title moves the row, and a document that is deleted takes its
// row along. Archived documents stay in the index and are left out by the search.
const documentsFTSSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(title, slug, body);
CREATE TRIGGER IF NOT EXISTS documents_fts_revision AFTER INSERT ON document_revisions BEGIN
  DELETE FROM documents_fts WHERE rowid = new.document_id
    AND EXISTS (SELECT 1 FROM documents d WHERE d.id = new.document_id AND d.head_revision = new.revision);
  INSERT INTO documents_fts(rowid, title, slug, body)
    SELECT d.id, d.title, d.slug, new.body FROM documents d
    WHERE d.id = new.document_id AND d.head_revision = new.revision;
END;
CREATE TRIGGER IF NOT EXISTS documents_fts_meta AFTER UPDATE OF title, slug ON documents BEGIN
  DELETE FROM documents_fts WHERE rowid = new.id;
  INSERT INTO documents_fts(rowid, title, slug, body)
    SELECT new.id, new.title, new.slug, r.body FROM document_revisions r
    WHERE r.document_id = new.id AND r.revision = new.head_revision;
END;
CREATE TRIGGER IF NOT EXISTS documents_fts_delete AFTER DELETE ON documents BEGIN
  DELETE FROM documents_fts WHERE rowid = old.id;
END;
`

// ensureEmbeddings creates the tables of the vectors and the full-text index of
// the documents, and indexes the documents that are not in it yet: all of them on
// the first start with this version, none later. Idempotent.
func ensureEmbeddings(db *sql.DB) error {
	if _, err := db.Exec(embeddingsSchema); err != nil {
		return err
	}
	if _, err := db.Exec(sessionEmbeddingsSchema); err != nil {
		return err
	}
	for _, c := range sessionSyncColumns {
		if err := ensureColumn(db, "session_embedding_sync", c.name, c.ddl); err != nil {
			return err
		}
	}
	if _, err := db.Exec(documentsFTSSchema); err != nil {
		return err
	}
	// documents_fts_docsize has one small row per indexed document, so the check
	// does not read the texts.
	_, err := db.Exec(`INSERT INTO documents_fts(rowid, title, slug, body)
		SELECT d.id, d.title, d.slug, r.body FROM documents d
		JOIN document_revisions r ON r.document_id = d.id AND r.revision = d.head_revision
		WHERE d.id NOT IN (SELECT id FROM documents_fts_docsize)`)
	return err
}
