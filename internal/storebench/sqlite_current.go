package storebench

import (
	"context"
	"fmt"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type currentSQLite struct {
	store      *store.Store
	mu         sync.RWMutex
	sessions   map[string]int64
	documents  map[string]int64
	migrations map[string]int64
}

func OpenCurrentSQLite(path string) (Backend, error) {
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	return &currentSQLite{store: st, sessions: map[string]int64{}, documents: map[string]int64{}, migrations: map[string]int64{}}, nil
}

func (b *currentSQLite) Name() string { return "sqlite_current" }

func (b *currentSQLite) Execute(ctx context.Context, operation Operation) error {
	return b.executeOn(ctx, b.store, operation)
}

func (b *currentSQLite) executeOn(ctx context.Context, st *store.Store, operation Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch payload := operation.Payload.(type) {
	case SessionUpsertPayload:
		id, err := st.UpsertSession(store.Session{Harness: "storebench", ExternalID: payload.ExternalID,
			Scope: scope.Axes{Project: payload.Project}})
		if err == nil {
			b.setID(b.sessions, payload.LogicalID, id)
		}
		return err
	case ChunksAppendPayload:
		id, err := b.id(b.sessions, payload.Session)
		if err != nil {
			return err
		}
		chunks := make([]store.Chunk, len(payload.Chunks))
		for i, chunk := range payload.Chunks {
			chunks[i] = store.Chunk{Seq: chunk.Seq, Role: chunk.Role, Text: chunk.Text, Raw: chunk.Raw}
		}
		return st.AppendChunks(id, chunks)
	case GhostPutPayload:
		_, err := st.PutGhostFile(store.GhostFile{Project: payload.Project, Path: payload.Path, Kind: "file",
			Description: payload.Description, ContentSHA: payload.ContentSHA, LineCount: payload.LineCount})
		return err
	case GhostReadPayload:
		ghost, err := st.GhostFileByPath(payload.Project, payload.Path)
		if err != nil {
			return err
		}
		if digest(ghost.Description) != payload.DescriptionDigest || ghost.ContentSHA != payload.ContentSHA || ghost.LineCount != payload.LineCount {
			return fmt.Errorf("ghost %s read result mismatch", payload.Path)
		}
		return nil
	case GhostTreePayload:
		ghosts, err := st.GhostFilesUnder(payload.Project, payload.Prefix)
		if err != nil {
			return err
		}
		if len(ghosts) != len(payload.ExpectedPaths) {
			return fmt.Errorf("ghost tree returned %d paths, want %d", len(ghosts), len(payload.ExpectedPaths))
		}
		for i, ghost := range ghosts {
			if ghost.Path != payload.ExpectedPaths[i] {
				return fmt.Errorf("ghost tree path %d = %q, want %q", i, ghost.Path, payload.ExpectedPaths[i])
			}
		}
		return nil
	case DocumentCreatePayload:
		document, err := st.CreateDocument(store.Document{Project: payload.Project, Slug: payload.Slug,
			Kind: "spec", Title: payload.Title, Person: "storebench"}, payload.Body, "benchmark create")
		if err == nil {
			b.setID(b.documents, payload.LogicalID, document.ID)
		}
		return err
	case DocumentPushPayload:
		id, err := b.id(b.documents, payload.Document)
		if err != nil {
			return err
		}
		_, err = st.PushRevision(id, payload.Base, payload.Body, "benchmark revision", "storebench")
		return err
	case DocumentReadPayload:
		id, err := b.id(b.documents, payload.Document)
		if err != nil {
			return err
		}
		revision, err := st.DocumentRevision(id, payload.Revision)
		if err != nil {
			return err
		}
		if revision.Revision != payload.Revision || revision.Digest != payload.Digest || digest(revision.Body) != payload.Digest {
			return fmt.Errorf("document %s revision %d read result mismatch", payload.Document, payload.Revision)
		}
		return nil
	case MigrationBeginPayload:
		artifacts := make(map[string]string, len(payload.Artifacts))
		for _, artifact := range payload.Artifacts {
			artifacts[artifact.Path] = artifact.Digest
		}
		id, err := st.BeginMigration(payload.Project, artifacts)
		if err == nil {
			b.setID(b.migrations, payload.LogicalID, id)
		}
		return err
	case MigrationReadPayload:
		rows, err := st.DB().QueryContext(ctx, `SELECT a.path,a.digest FROM migration_artifacts a JOIN migration_runs r ON r.id=a.run_id WHERE r.project=?`, payload.Project)
		if err != nil {
			return err
		}
		got := map[string]string{}
		for rows.Next() {
			var path, value string
			if err := rows.Scan(&path, &value); err != nil {
				_ = rows.Close()
				return err
			}
			got[path] = value
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return compareArtifacts(payload.Project, got, payload.Artifacts)
	case SessionReadPayload:
		id, err := b.id(b.sessions, payload.Session)
		if err != nil {
			return err
		}
		chunks, err := st.ReadSession(id, 0, len(payload.Chunks)+1)
		if err != nil {
			return err
		}
		if len(chunks) != len(payload.Chunks) {
			return fmt.Errorf("session %s returned %d chunks, want %d", payload.Session, len(chunks), len(payload.Chunks))
		}
		for i, chunk := range chunks {
			want := payload.Chunks[i]
			if chunk.Seq != want.Seq || chunk.Role != want.Role || chunk.Text != want.Text || chunk.Raw != want.Raw {
				return fmt.Errorf("session %s chunk %d read result mismatch", payload.Session, i)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported %s payload %T", operation.Kind, operation.Payload)
	}
}

func (b *currentSQLite) Verify(ctx context.Context, expected Expected) error {
	return verifySQLite(ctx, b.store, b.sessions, b.documents, b.migrations, expected)
}

func (b *currentSQLite) Stats() BackendStats {
	stats := b.store.RuntimeStats()
	return BackendStats{Engine: "sqlite", EngineVersion: sqliteVersion(b.store), Settings: sqliteSettings(b.store),
		MaxOpenConnections: stats.DB.MaxOpenConnections, WaitCount: stats.DB.WaitCount,
		WaitDurationNS: stats.DB.WaitDuration.Nanoseconds(), DatabaseBytes: stats.DatabaseBytes,
		WALBytes: stats.WALBytes, SHMBytes: stats.SHMBytes}
}

func sqliteSettings(st *store.Store) map[string]string {
	settings := map[string]string{}
	for _, name := range []string{"journal_mode", "synchronous", "busy_timeout"} {
		var value string
		if err := st.DB().QueryRow(`PRAGMA ` + name).Scan(&value); err == nil {
			settings[name] = value
		}
	}
	return settings
}

func sqliteVersion(st *store.Store) string {
	var version string
	if err := st.DB().QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		return "unknown"
	}
	return version
}

func (b *currentSQLite) Close() error { return b.store.Close() }

func (b *currentSQLite) setID(ids map[string]int64, logical string, id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids[logical] = id
}

func (b *currentSQLite) id(ids map[string]int64, logical string) (int64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	id, ok := ids[logical]
	if !ok {
		return 0, fmt.Errorf("logical id %q has not been created", logical)
	}
	return id, nil
}
