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
		_, err := st.GhostFileByPath(payload.Project, payload.Path)
		return err
	case GhostTreePayload:
		_, err := st.GhostFilesUnder(payload.Project, payload.Prefix)
		return err
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
		_, err = st.DocumentRevision(id, payload.Revision)
		return err
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
		return rows.Close()
	case SessionReadPayload:
		id, err := b.id(b.sessions, payload.Session)
		if err != nil {
			return err
		}
		_, err = st.SessionByID(id)
		return err
	default:
		return fmt.Errorf("unsupported %s payload %T", operation.Kind, operation.Payload)
	}
}

func (b *currentSQLite) Verify(ctx context.Context, expected Expected) error {
	return verifySQLite(ctx, b.store, b.sessions, b.documents, b.migrations, expected)
}

func (b *currentSQLite) Stats() BackendStats {
	stats := b.store.RuntimeStats()
	return BackendStats{MaxOpenConnections: stats.DB.MaxOpenConnections, WaitCount: stats.DB.WaitCount,
		WaitDurationNS: stats.DB.WaitDuration.Nanoseconds(), DatabaseBytes: stats.DatabaseBytes,
		WALBytes: stats.WALBytes, SHMBytes: stats.SHMBytes}
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
