package postgresbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Backend struct {
	pool       *pgxpool.Pool
	admin      *pgx.Conn
	database   string
	maxConns   int
	version    string
	settings   map[string]string
	mu         sync.RWMutex
	sessions   map[string]int64
	documents  map[string]int64
	migrations map[string]int64
	closeOnce  sync.Once
	closeErr   error
}

func OpenEphemeral(ctx context.Context, adminURL, runID string, maxConns int) (storebench.Backend, error) {
	if adminURL == "" {
		return nil, fmt.Errorf("postgres admin URL is required")
	}
	if maxConns <= 0 {
		return nil, fmt.Errorf("max connections must be positive")
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return nil, err
	}
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		return nil, err
	}
	database := benchmarkDatabaseName(runID)
	if _, err := admin.Exec(ctx, `CREATE DATABASE "`+database+`"`); err != nil {
		_ = admin.Close(ctx)
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		_ = dropDatabase(ctx, admin, database)
		_ = admin.Close(ctx)
		return nil, err
	}
	poolConfig.ConnConfig.Database = database
	poolConfig.MaxConns = int32(maxConns)
	poolConfig.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		_ = dropDatabase(ctx, admin, database)
		_ = admin.Close(ctx)
		return nil, err
	}
	b := &Backend{pool: pool, admin: admin, database: database, maxConns: maxConns,
		sessions: map[string]int64{}, documents: map[string]int64{}, migrations: map[string]int64{}}
	if err := pool.Ping(ctx); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := b.createSchema(ctx); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := b.loadMetadata(ctx); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := validateDurabilitySettings(b.settings); err != nil {
		_ = b.Close()
		return nil, err
	}
	return b, nil
}

func benchmarkDatabaseName(runID string) string {
	var clean strings.Builder
	for _, r := range strings.ToLower(runID) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			clean.WriteRune(r)
		} else if clean.Len() > 0 && !strings.HasSuffix(clean.String(), "_") {
			clean.WriteByte('_')
		}
		if clean.Len() >= 24 {
			break
		}
	}
	if clean.Len() == 0 {
		clean.WriteString("run")
	}
	return fmt.Sprintf("ghosttree_bench_%s_%d", strings.Trim(clean.String(), "_"), time.Now().UnixNano())
}

func (b *Backend) createSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE sessions (id BIGSERIAL PRIMARY KEY, harness TEXT NOT NULL, external_id TEXT NOT NULL, project TEXT NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), UNIQUE(harness,external_id))`,
		`CREATE TABLE session_chunks (session_id BIGINT NOT NULL REFERENCES sessions(id), seq INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, raw TEXT NOT NULL, search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple',text)) STORED, PRIMARY KEY(session_id,seq))`,
		`CREATE INDEX session_chunks_search ON session_chunks USING GIN(search)`,
		`CREATE TABLE ghost_files (id BIGSERIAL PRIMARY KEY, project TEXT NOT NULL, path TEXT NOT NULL, kind TEXT NOT NULL, description TEXT NOT NULL, content_sha TEXT NOT NULL, line_count INTEGER NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple',path || ' ' || description)) STORED, UNIQUE(project,path))`,
		`CREATE INDEX ghost_files_tree ON ghost_files(project,path)`,
		`CREATE INDEX ghost_files_search ON ghost_files USING GIN(search)`,
		`CREATE TABLE ghost_file_history (id BIGSERIAL PRIMARY KEY, ghost_id BIGINT NOT NULL, project TEXT NOT NULL, path TEXT NOT NULL, kind TEXT NOT NULL, description TEXT NOT NULL, content_sha TEXT NOT NULL, line_count INTEGER NOT NULL, archived_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`,
		`CREATE TABLE documents (id BIGSERIAL PRIMARY KEY, project TEXT NOT NULL, slug TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, head_revision INTEGER NOT NULL, status TEXT NOT NULL, person TEXT NOT NULL, UNIQUE(project,slug))`,
		`CREATE TABLE document_revisions (document_id BIGINT NOT NULL REFERENCES documents(id), revision INTEGER NOT NULL, body TEXT NOT NULL, digest TEXT NOT NULL, message TEXT NOT NULL, person TEXT NOT NULL, PRIMARY KEY(document_id,revision))`,
		`CREATE TABLE migration_runs (id BIGSERIAL PRIMARY KEY, project TEXT NOT NULL, state TEXT NOT NULL, signature TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), UNIQUE(project,state,signature))`,
		`CREATE TABLE migration_artifacts (run_id BIGINT NOT NULL REFERENCES migration_runs(id), path TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(run_id,path))`,
	}
	for _, statement := range statements {
		if _, err := b.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) loadMetadata(ctx context.Context) error {
	b.settings = map[string]string{}
	if err := b.pool.QueryRow(ctx, `SHOW server_version`).Scan(&b.version); err != nil {
		return err
	}
	for _, setting := range []string{"fsync", "synchronous_commit", "full_page_writes"} {
		var value string
		if err := b.pool.QueryRow(ctx, `SELECT current_setting($1)`, setting).Scan(&value); err != nil {
			return err
		}
		b.settings[setting] = value
	}
	return nil
}

func validateDurabilitySettings(settings map[string]string) error {
	for _, name := range []string{"fsync", "synchronous_commit", "full_page_writes"} {
		if settings[name] != "on" {
			return fmt.Errorf("unsafe PostgreSQL durability setting %s=%q, require on", name, settings[name])
		}
	}
	return nil
}

func (b *Backend) Name() string { return "postgresql" }

func (b *Backend) Execute(ctx context.Context, operation storebench.Operation) error {
	switch payload := operation.Payload.(type) {
	case storebench.SessionUpsertPayload:
		var id int64
		err := b.pool.QueryRow(ctx, `INSERT INTO sessions(harness,external_id,project) VALUES('storebench',$1,$2)
			ON CONFLICT(harness,external_id) DO UPDATE SET project=EXCLUDED.project,last_seen_at=clock_timestamp() RETURNING id`,
			payload.ExternalID, payload.Project).Scan(&id)
		if err == nil {
			b.setID(b.sessions, payload.LogicalID, id)
		}
		return err
	case storebench.ChunksAppendPayload:
		id, err := b.id(b.sessions, payload.Session)
		if err != nil {
			return err
		}
		tx, err := b.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, chunk := range payload.Chunks {
			if _, err := tx.Exec(ctx, `INSERT INTO session_chunks(session_id,seq,role,text,raw) VALUES($1,$2,$3,$4,$5) ON CONFLICT(session_id,seq) DO NOTHING`,
				id, chunk.Seq, chunk.Role, chunk.Text, chunk.Raw); err != nil {
				return err
			}
			var role, text, raw string
			if err := tx.QueryRow(ctx, `SELECT role,text,raw FROM session_chunks WHERE session_id=$1 AND seq=$2`, id, chunk.Seq).Scan(&role, &text, &raw); err != nil {
				return err
			}
			if role != chunk.Role || text != chunk.Text || raw != chunk.Raw {
				return fmt.Errorf("session %s chunk %d conflicts with existing payload", payload.Session, chunk.Seq)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET last_seen_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	case storebench.GhostPutPayload:
		tx, err := b.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`, payload.Project, payload.Path); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ghost_file_history(ghost_id,project,path,kind,description,content_sha,line_count)
			SELECT id,project,path,kind,description,content_sha,line_count FROM ghost_files WHERE project=$1 AND path=$2`, payload.Project, payload.Path); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ghost_files(project,path,kind,description,content_sha,line_count) VALUES($1,$2,'file',$3,$4,$5)
			ON CONFLICT(project,path) DO UPDATE SET description=EXCLUDED.description,content_sha=EXCLUDED.content_sha,line_count=EXCLUDED.line_count,updated_at=clock_timestamp()`,
			payload.Project, payload.Path, payload.Description, payload.ContentSHA, payload.LineCount)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	case storebench.GhostReadPayload:
		var description, contentSHA string
		var lineCount int
		if err := b.pool.QueryRow(ctx, `SELECT description,content_sha,line_count FROM ghost_files WHERE project=$1 AND path=$2`, payload.Project, payload.Path).
			Scan(&description, &contentSHA, &lineCount); err != nil {
			return err
		}
		if digest(description) != payload.DescriptionDigest || contentSHA != payload.ContentSHA || lineCount != payload.LineCount {
			return fmt.Errorf("ghost %s read result mismatch", payload.Path)
		}
		return nil
	case storebench.GhostTreePayload:
		rows, err := b.pool.Query(ctx, `SELECT path FROM ghost_files WHERE project=$1 AND (path=$2 OR path LIKE $2 || '/%') ORDER BY path`, payload.Project, payload.Prefix)
		if err != nil {
			return err
		}
		paths := []string{}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return err
			}
			paths = append(paths, path)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(paths) != len(payload.ExpectedPaths) {
			return fmt.Errorf("ghost tree returned %d paths, want %d", len(paths), len(payload.ExpectedPaths))
		}
		for i := range paths {
			if paths[i] != payload.ExpectedPaths[i] {
				return fmt.Errorf("ghost tree path %d = %q, want %q", i, paths[i], payload.ExpectedPaths[i])
			}
		}
		return nil
	case storebench.DocumentCreatePayload:
		return b.createDocument(ctx, payload)
	case storebench.DocumentPushPayload:
		return b.pushDocument(ctx, payload)
	case storebench.DocumentReadPayload:
		id, err := b.id(b.documents, payload.Document)
		if err != nil {
			return err
		}
		var revision int
		var body, value string
		if err := b.pool.QueryRow(ctx, `SELECT revision,body,digest FROM document_revisions WHERE document_id=$1 AND revision=$2`, id, payload.Revision).
			Scan(&revision, &body, &value); err != nil {
			return err
		}
		if revision != payload.Revision || value != payload.Digest || digest(body) != payload.Digest {
			return fmt.Errorf("document %s revision %d read result mismatch", payload.Document, payload.Revision)
		}
		return nil
	case storebench.MigrationBeginPayload:
		return b.beginMigration(ctx, payload)
	case storebench.MigrationReadPayload:
		got, err := b.migrationArtifactsForProject(ctx, payload.Project)
		if err != nil {
			return err
		}
		return compareArtifacts(payload.Project, got, payload.Artifacts)
	case storebench.SessionReadPayload:
		id, err := b.id(b.sessions, payload.Session)
		if err != nil {
			return err
		}
		rows, err := b.pool.Query(ctx, `SELECT seq,role,text,raw FROM session_chunks WHERE session_id=$1 ORDER BY seq`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			if index >= len(payload.Chunks) {
				return fmt.Errorf("session %s returned extra chunks", payload.Session)
			}
			var seq int
			var role, text, raw string
			if err := rows.Scan(&seq, &role, &text, &raw); err != nil {
				return err
			}
			want := payload.Chunks[index]
			if seq != want.Seq || role != want.Role || text != want.Text || raw != want.Raw {
				return fmt.Errorf("session %s chunk %d read result mismatch", payload.Session, index)
			}
			index++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if index != len(payload.Chunks) {
			return fmt.Errorf("session %s returned %d chunks, want %d", payload.Session, index, len(payload.Chunks))
		}
		return nil
	default:
		return fmt.Errorf("unsupported %s payload %T", operation.Kind, operation.Payload)
	}
}

func (b *Backend) createDocument(ctx context.Context, payload storebench.DocumentCreatePayload) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO documents(project,slug,kind,title,head_revision,status,person) VALUES($1,$2,'spec',$3,1,'active','storebench')
		ON CONFLICT(project,slug) DO NOTHING RETURNING id`, payload.Project, payload.Slug, payload.Title).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var title, kind, status, person string
		if err := tx.QueryRow(ctx, `SELECT id,title,kind,status,person FROM documents WHERE project=$1 AND slug=$2`, payload.Project, payload.Slug).
			Scan(&id, &title, &kind, &status, &person); err != nil {
			return err
		}
		if title != payload.Title || kind != "spec" || status != "active" || person != "storebench" {
			return fmt.Errorf("document %s conflicts with existing metadata", payload.LogicalID)
		}
	} else if err != nil {
		return err
	}
	value := digest(payload.Body)
	if _, err := tx.Exec(ctx, `INSERT INTO document_revisions(document_id,revision,body,digest,message,person) VALUES($1,1,$2,$3,'benchmark create','storebench') ON CONFLICT(document_id,revision) DO NOTHING`, id, payload.Body, value); err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT digest FROM document_revisions WHERE document_id=$1 AND revision=1`, id).Scan(&stored); err != nil {
		return err
	}
	if stored != value {
		return fmt.Errorf("document %s create conflicts with existing digest", payload.LogicalID)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	b.setID(b.documents, payload.LogicalID, id)
	return nil
}

func (b *Backend) pushDocument(ctx context.Context, payload storebench.DocumentPushPayload) error {
	id, err := b.id(b.documents, payload.Document)
	if err != nil {
		return err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	revision := payload.Base + 1
	value := digest(payload.Body)
	if _, err := tx.Exec(ctx, `INSERT INTO document_revisions(document_id,revision,body,digest,message,person) VALUES($1,$2,$3,$4,'benchmark revision','storebench') ON CONFLICT(document_id,revision) DO NOTHING`,
		id, revision, payload.Body, value); err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT digest FROM document_revisions WHERE document_id=$1 AND revision=$2`, id, revision).Scan(&stored); err != nil {
		return err
	}
	if stored != value {
		return fmt.Errorf("document %s revision %d conflicts with existing digest", payload.Document, revision)
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET head_revision=GREATEST(head_revision,$2) WHERE id=$1`, id, revision); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (b *Backend) beginMigration(ctx context.Context, payload storebench.MigrationBeginPayload) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO migration_runs(project,state,signature) VALUES($1,'pending',$2)
		ON CONFLICT(project,state,signature) DO UPDATE SET project=EXCLUDED.project RETURNING id`, payload.Project, artifactSignature(payload.Artifacts)).Scan(&id); err != nil {
		return err
	}
	for _, artifact := range payload.Artifacts {
		if _, err := tx.Exec(ctx, `INSERT INTO migration_artifacts(run_id,path,digest) VALUES($1,$2,$3) ON CONFLICT(run_id,path) DO UPDATE SET digest=EXCLUDED.digest`, id, artifact.Path, artifact.Digest); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	b.setID(b.migrations, payload.LogicalID, id)
	return nil
}

func (b *Backend) migrationArtifactsForProject(ctx context.Context, project string) (map[string]string, error) {
	rows, err := b.pool.Query(ctx, `SELECT a.path,a.digest FROM migration_artifacts a JOIN migration_runs r ON r.id=a.run_id WHERE r.project=$1`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var path, value string
		if err := rows.Scan(&path, &value); err != nil {
			return nil, err
		}
		result[path] = value
	}
	return result, rows.Err()
}

func (b *Backend) Verify(ctx context.Context, expected storebench.Expected) error {
	for logical, want := range expected.Sessions {
		id, err := b.id(b.sessions, logical)
		if err != nil {
			return err
		}
		var harness, externalID, project string
		if err := b.pool.QueryRow(ctx, `SELECT harness,external_id,project FROM sessions WHERE id=$1`, id).Scan(&harness, &externalID, &project); err != nil {
			return err
		}
		if harness != want.Harness || externalID != want.ExternalID || project != want.Project {
			return fmt.Errorf("session %s identity mismatch", logical)
		}
		rows, err := b.pool.Query(ctx, `SELECT seq,role,text,raw FROM session_chunks WHERE session_id=$1 ORDER BY seq`, id)
		if err != nil {
			return err
		}
		seen := 0
		for rows.Next() {
			var seq int
			var role, text, raw string
			if err := rows.Scan(&seq, &role, &text, &raw); err != nil {
				rows.Close()
				return err
			}
			expectedChunk, ok := want.Chunks[seq]
			if !ok || role != expectedChunk.Role || text != expectedChunk.Text || raw != expectedChunk.Raw {
				rows.Close()
				return fmt.Errorf("session %s chunk %d mismatch", logical, seq)
			}
			seen++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if seen != len(want.Chunks) {
			return fmt.Errorf("session %s chunk count got %d want %d", logical, seen, len(want.Chunks))
		}
	}
	for logical, want := range expected.Ghosts {
		var description, contentSHA string
		var lineCount int
		if err := b.pool.QueryRow(ctx, `SELECT description,content_sha,line_count FROM ghost_files WHERE project=$1 AND path=$2`, want.Project, want.Path).
			Scan(&description, &contentSHA, &lineCount); err != nil {
			return fmt.Errorf("ghost %s: %w", logical, err)
		}
		if len(description) != want.DescriptionBytes || digest(description) != want.DescriptionDigest || contentSHA != want.ContentSHA || lineCount != want.LineCount {
			return fmt.Errorf("ghost %s content mismatch", logical)
		}
	}
	for logical, want := range expected.Documents {
		id, err := b.id(b.documents, logical)
		if err != nil {
			return err
		}
		var project, slug, status string
		var head int
		if err := b.pool.QueryRow(ctx, `SELECT project,slug,head_revision,status FROM documents WHERE id=$1`, id).Scan(&project, &slug, &head, &status); err != nil {
			return err
		}
		if project != want.Project || slug != want.Slug || head != len(want.RevisionDigests) || status != want.Status {
			return fmt.Errorf("document %s head mismatch", logical)
		}
		for index, wantDigest := range want.RevisionDigests {
			var body, value string
			if err := b.pool.QueryRow(ctx, `SELECT body,digest FROM document_revisions WHERE document_id=$1 AND revision=$2`, id, index+1).Scan(&body, &value); err != nil {
				return err
			}
			if value != wantDigest || digest(body) != wantDigest {
				return fmt.Errorf("document %s revision %d mismatch", logical, index+1)
			}
		}
	}
	for logical, want := range expected.Migrations {
		id, err := b.id(b.migrations, logical)
		if err != nil {
			return err
		}
		var state string
		if err := b.pool.QueryRow(ctx, `SELECT state FROM migration_runs WHERE id=$1`, id).Scan(&state); err != nil {
			return err
		}
		if state != want.State {
			return fmt.Errorf("migration %s state got %s want %s", logical, state, want.State)
		}
		rows, err := b.pool.Query(ctx, `SELECT path,digest FROM migration_artifacts WHERE run_id=$1`, id)
		if err != nil {
			return err
		}
		got := map[string]string{}
		for rows.Next() {
			var path, value string
			if err := rows.Scan(&path, &value); err != nil {
				rows.Close()
				return err
			}
			got[path] = value
		}
		rows.Close()
		if err := compareArtifacts(logical, got, want.Artifacts); err != nil {
			return err
		}
	}
	counts := []struct {
		table string
		want  int
	}{{"sessions", len(expected.Sessions)}, {"ghost_files", len(expected.Ghosts)}, {"documents", len(expected.Documents)}, {"migration_runs", len(expected.Migrations)}}
	for _, check := range counts {
		var got int
		if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM `+check.table).Scan(&got); err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("%s count got %d want %d", check.table, got, check.want)
		}
	}
	return b.verifyNoOrphans(ctx)
}

func (b *Backend) verifyNoOrphans(ctx context.Context) error {
	queries := []string{
		`SELECT count(*) FROM session_chunks c LEFT JOIN sessions s ON s.id=c.session_id WHERE s.id IS NULL`,
		`SELECT count(*) FROM document_revisions r LEFT JOIN documents d ON d.id=r.document_id WHERE d.id IS NULL`,
		`SELECT count(*) FROM migration_artifacts a LEFT JOIN migration_runs r ON r.id=a.run_id WHERE r.id IS NULL`,
	}
	for _, query := range queries {
		var count int
		if err := b.pool.QueryRow(ctx, query).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("postgres foreign key verification found %d orphan rows", count)
		}
	}
	return nil
}

func (b *Backend) Stats() storebench.BackendStats {
	stats := b.pool.Stat()
	var databaseBytes int64
	_ = b.pool.QueryRow(context.Background(), `SELECT pg_database_size(current_database())`).Scan(&databaseBytes)
	settings := make(map[string]string, len(b.settings))
	for key, value := range b.settings {
		settings[key] = value
	}
	return storebench.BackendStats{Engine: "postgresql", EngineVersion: b.version, Settings: settings,
		MaxOpenConnections: b.maxConns, WaitCount: stats.EmptyAcquireCount(),
		WaitDurationNS: stats.EmptyAcquireWaitTime().Nanoseconds(), DatabaseBytes: databaseBytes}
}

func (b *Backend) Close() error {
	b.closeOnce.Do(func() {
		b.pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		b.closeErr = errors.Join(dropDatabase(ctx, b.admin, b.database), b.admin.Close(ctx))
	})
	return b.closeErr
}

func dropDatabase(ctx context.Context, admin *pgx.Conn, database string) error {
	if !strings.HasPrefix(database, "ghosttree_bench_") {
		return fmt.Errorf("refusing to drop non-benchmark database %q", database)
	}
	_, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS "`+database+`"`)
	return err
}

func (b *Backend) setID(ids map[string]int64, logical string, id int64) {
	b.mu.Lock()
	ids[logical] = id
	b.mu.Unlock()
}

func (b *Backend) id(ids map[string]int64, logical string) (int64, error) {
	b.mu.RLock()
	id, ok := ids[logical]
	b.mu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("logical id %q has not been created", logical)
	}
	return id, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func artifactSignature(artifacts []storebench.Artifact) string {
	values := append([]storebench.Artifact(nil), artifacts...)
	sort.Slice(values, func(i, j int) bool { return values[i].Path < values[j].Path })
	hash := sha256.New()
	for _, artifact := range values {
		_, _ = hash.Write([]byte(artifact.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(artifact.Digest))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func compareArtifacts(logical string, got map[string]string, want []storebench.Artifact) error {
	if len(got) != len(want) {
		return fmt.Errorf("migration %s artifacts: got %d want %d", logical, len(got), len(want))
	}
	for _, artifact := range want {
		if got[artifact.Path] != artifact.Digest {
			return fmt.Errorf("migration %s artifact %s mismatch", logical, artifact.Path)
		}
	}
	return nil
}
