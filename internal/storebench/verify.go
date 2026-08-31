package storebench

import (
	"context"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func verifySQLite(ctx context.Context, st *store.Store, sessions, documents, migrations map[string]int64, expected Expected) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for logical, want := range expected.Sessions {
		id, ok := sessions[logical]
		if !ok {
			return fmt.Errorf("session %s: logical id missing", logical)
		}
		got, err := st.SessionByID(id)
		if err != nil {
			return fmt.Errorf("session %s: %w", logical, err)
		}
		if got.ExternalID != want.ExternalID || got.Scope.Project != want.Project {
			return fmt.Errorf("session %s: identity mismatch", logical)
		}
		chunks, err := st.ReadSession(id, 0, len(want.Chunks)+1)
		if err != nil {
			return fmt.Errorf("session %s chunks: %w", logical, err)
		}
		if len(chunks) != len(want.Chunks) {
			return fmt.Errorf("session %s chunks: got %d want %d", logical, len(chunks), len(want.Chunks))
		}
		for _, chunk := range chunks {
			expectedChunk, ok := want.Chunks[chunk.Seq]
			if !ok || chunk.Role != expectedChunk.Role || chunk.Text != expectedChunk.Text || chunk.Raw != expectedChunk.Raw {
				return fmt.Errorf("session %s chunk %d mismatch", logical, chunk.Seq)
			}
		}
	}
	for logical, want := range expected.Ghosts {
		got, err := st.GhostFileByPath(want.Project, want.Path)
		if err != nil {
			return fmt.Errorf("ghost %s: %w", logical, err)
		}
		if len(got.Description) != want.DescriptionBytes {
			return fmt.Errorf("ghost %s: bytes got %d want %d", logical, len(got.Description), want.DescriptionBytes)
		}
		if gotDigest := digest(got.Description); gotDigest != want.DescriptionDigest {
			return fmt.Errorf("ghost %s: digest got %s want %s", logical, gotDigest, want.DescriptionDigest)
		}
	}
	for logical, want := range expected.Documents {
		id, ok := documents[logical]
		if !ok {
			return fmt.Errorf("document %s: logical id missing", logical)
		}
		got, err := st.DocumentByID(id)
		if err != nil {
			return fmt.Errorf("document %s: %w", logical, err)
		}
		if got.Project != want.Project || got.Slug != want.Slug || got.HeadRevision != len(want.RevisionDigests) {
			return fmt.Errorf("document %s: head mismatch", logical)
		}
		for index, wantDigest := range want.RevisionDigests {
			revision, err := st.DocumentRevision(id, index+1)
			if err != nil {
				return fmt.Errorf("document %s revision %d: %w", logical, index+1, err)
			}
			if revision.Digest != wantDigest {
				return fmt.Errorf("document %s revision %d: digest got %s want %s", logical, index+1, revision.Digest, wantDigest)
			}
		}
	}
	for logical, want := range expected.Migrations {
		runID, ok := migrations[logical]
		if !ok {
			return fmt.Errorf("migration %s: logical id missing", logical)
		}
		got, err := migrationArtifacts(ctx, st, runID)
		if err != nil {
			return fmt.Errorf("migration %s: %w", logical, err)
		}
		if err := compareArtifacts(logical, got, want.Artifacts); err != nil {
			return err
		}
	}
	return verifyTableCounts(ctx, st, expected)
}

func migrationArtifacts(ctx context.Context, st *store.Store, runID int64) (map[string]string, error) {
	rows, err := st.DB().QueryContext(ctx, `SELECT path,digest FROM migration_artifacts WHERE run_id=?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, value string
		if err := rows.Scan(&path, &value); err != nil {
			return nil, err
		}
		out[path] = value
	}
	return out, rows.Err()
}

func compareArtifacts(logical string, got map[string]string, want []Artifact) error {
	if len(got) != len(want) {
		return fmt.Errorf("migration %s artifacts: got %d want %d", logical, len(got), len(want))
	}
	for _, artifact := range want {
		if got[artifact.Path] != artifact.Digest {
			return fmt.Errorf("migration %s artifact %s digest got %s want %s", logical, artifact.Path, got[artifact.Path], artifact.Digest)
		}
	}
	return nil
}

func verifyTableCounts(ctx context.Context, st *store.Store, expected Expected) error {
	checks := []struct {
		table string
		want  int
	}{
		{"sessions", len(expected.Sessions)},
		{"ghost_files", len(expected.Ghosts)},
		{"documents", len(expected.Documents)},
		{"migration_runs", len(expected.Migrations)},
	}
	for _, check := range checks {
		var got int
		if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+check.table).Scan(&got); err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("%s count got %d want %d", check.table, got, check.want)
		}
	}
	return nil
}
