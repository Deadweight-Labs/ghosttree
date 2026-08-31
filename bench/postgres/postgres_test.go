package postgresbench

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
	"github.com/jackc/pgx/v5"
)

func TestOpenEphemeralExecutesAndVerifiesSmallWorkload(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	backend, err := OpenEphemeral(ctx, adminURL, "integration", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	workload, expected := storebench.Generate(254, storebench.Scale{
		Projects: 1, Sessions: 4, ChunksPerSession: 3, GhostFiles: 8,
		GhostBodyBytes: 128, Documents: 2, DocumentRevisions: 2,
		DocumentBodyBytes: 256, MigrationArtifacts: 4, ReadEvery: 2,
	})
	report, err := storebench.Run(ctx, backend, workload, expected,
		storebench.RunConfig{Concurrency: 4, ArrivalMultiplier: 10, RunID: "postgres-integration"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors != 0 || report.VerificationError != "" || report.BackendStats.Engine != "postgresql" {
		t.Fatalf("report = %+v", report)
	}
	for key, want := range map[string]string{"fsync": "on", "synchronous_commit": "on", "full_page_writes": "on"} {
		if got := report.BackendStats.Settings[key]; got != want {
			t.Fatalf("setting %s = %q, want %q", key, got, want)
		}
	}
}

func TestWriteRetriesRemainIdempotent(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	backend, err := OpenEphemeral(ctx, adminURL, "idempotency", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	workload, expected := storebench.Generate(256, storebench.Scale{Projects: 1, Sessions: 2,
		ChunksPerSession: 3, GhostFiles: 2, GhostBodyBytes: 32, Documents: 1,
		DocumentRevisions: 2, DocumentBodyBytes: 64, MigrationArtifacts: 3})
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range workload.Operations {
		if readKind(operation.Kind) {
			continue
		}
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatalf("retry %s: %v", operation.Kind, err)
		}
	}
	if err := backend.Verify(ctx, expected); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDropsOnlyEphemeralBenchmarkDatabase(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	opened, err := OpenEphemeral(ctx, adminURL, "cleanup", 2)
	if err != nil {
		t.Fatal(err)
	}
	database := opened.(*Backend).database
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname=$1`, database).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ephemeral database %s remains", database)
	}
}

func TestValidateDurabilitySettingsRejectsUnsafeValues(t *testing.T) {
	for _, setting := range []string{"fsync", "synchronous_commit", "full_page_writes"} {
		values := map[string]string{"fsync": "on", "synchronous_commit": "on", "full_page_writes": "on"}
		values[setting] = "off"
		if err := validateDurabilitySettings(values); err == nil || !strings.Contains(err.Error(), setting) {
			t.Fatalf("settings %v produced %v, want %s error", values, err, setting)
		}
	}
}

func TestChunkConflictWithDifferentPayloadIsRejected(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	backend, err := OpenEphemeral(ctx, adminURL, "chunk-conflict", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if err := backend.Execute(ctx, storebench.Operation{Kind: storebench.SessionUpsert,
		Payload: storebench.SessionUpsertPayload{LogicalID: "session", ExternalID: "external", Project: "project"}}); err != nil {
		t.Fatal(err)
	}
	first := storebench.Operation{Kind: storebench.ChunksAppend, Payload: storebench.ChunksAppendPayload{Session: "session",
		Chunks: []storebench.ChunkPayload{{Seq: 0, Role: "user", Text: "first", Raw: "first"}}}}
	if err := backend.Execute(ctx, first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Payload = storebench.ChunksAppendPayload{Session: "session",
		Chunks: []storebench.ChunkPayload{{Seq: 0, Role: "user", Text: "different", Raw: "different"}}}
	if err := backend.Execute(ctx, conflict); err == nil {
		t.Fatal("different chunk payload under the same sequence was acknowledged")
	}
}

func TestGhostReplacementArchivesPreviousValueAndSearchIndexExists(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	opened, err := OpenEphemeral(ctx, adminURL, "ghost-history", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	backend := opened.(*Backend)
	operation := storebench.Operation{Kind: storebench.GhostPut, Payload: storebench.GhostPutPayload{
		Project: "project", Path: "file.go", Description: "first searchable description", ContentSHA: "one", LineCount: 1}}
	if err := backend.Execute(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.Payload = storebench.GhostPutPayload{Project: "project", Path: "file.go",
		Description: "second searchable description", ContentSHA: "two", LineCount: 2}
	if err := backend.Execute(ctx, operation); err != nil {
		t.Fatal(err)
	}
	var history int
	var ghostSearchIndex, chunkSearchIndex string
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM ghost_file_history`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT to_regclass('public.ghost_files_search')::text`).Scan(&ghostSearchIndex); err != nil {
		t.Fatal(err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT to_regclass('public.session_chunks_search')::text`).Scan(&chunkSearchIndex); err != nil {
		t.Fatal(err)
	}
	if history != 1 || ghostSearchIndex != "ghost_files_search" || chunkSearchIndex != "session_chunks_search" {
		t.Fatalf("history=%d ghost search=%q chunk search=%q", history, ghostSearchIndex, chunkSearchIndex)
	}
}

func TestDocumentCreateConflictWithDifferentTitleIsRejected(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	backend, err := OpenEphemeral(ctx, adminURL, "document-conflict", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	operation := storebench.Operation{Kind: storebench.DocumentCreate, Payload: storebench.DocumentCreatePayload{
		LogicalID: "document", Project: "project", Slug: "slug", Title: "first", Body: "same body"}}
	if err := backend.Execute(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.Payload = storebench.DocumentCreatePayload{LogicalID: "document", Project: "project", Slug: "slug",
		Title: "different", Body: "same body"}
	if err := backend.Execute(ctx, operation); err == nil {
		t.Fatal("different document title under the same key was acknowledged")
	}
}

func TestConcurrentGhostReplacementsPreserveCompleteHistoryChain(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	opened, err := OpenEphemeral(ctx, adminURL, "ghost-concurrency", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	backend := opened.(*Backend)
	if _, err := backend.pool.Exec(ctx, `CREATE FUNCTION slow_ghost_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.03); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.pool.Exec(ctx, `CREATE TRIGGER slow_ghost BEFORE UPDATE ON ghost_files FOR EACH ROW EXECUTE FUNCTION slow_ghost_update()`); err != nil {
		t.Fatal(err)
	}
	put := func(description string) error {
		return backend.Execute(ctx, storebench.Operation{Kind: storebench.GhostPut, Payload: storebench.GhostPutPayload{
			Project: "project", Path: "same.go", Description: description, ContentSHA: description, LineCount: 1}})
	}
	if err := put("initial"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsFound := make(chan error, 8)
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errorsFound <- put(fmt.Sprintf("replacement-%d", n))
		}()
	}
	close(start)
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := backend.pool.Query(ctx, `SELECT description FROM ghost_file_history UNION ALL SELECT description FROM ghost_files WHERE project='project' AND path='same.go'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var description string
		if err := rows.Scan(&description); err != nil {
			t.Fatal(err)
		}
		seen[description] = struct{}{}
	}
	if len(seen) != 9 {
		t.Fatalf("history chain has %d distinct versions, want 9: %v", len(seen), seen)
	}
}

func readKind(kind storebench.Kind) bool {
	switch kind {
	case storebench.GhostRead, storebench.GhostTree, storebench.DocumentRead, storebench.MigrationRead, storebench.SessionRead:
		return true
	default:
		return false
	}
}

func TestVerifierRejectsCorruptSessionChunk(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	ctx := context.Background()
	opened, err := OpenEphemeral(ctx, adminURL, "corrupt-chunk", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	backend := opened.(*Backend)
	workload, expected := storebench.Generate(255, storebench.Scale{Projects: 1, Sessions: 1, ChunksPerSession: 2})
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE session_chunks SET text='corrupt'`); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(ctx, expected); err == nil {
		t.Fatal("verifier accepted corrupt session chunk")
	}
}
