package storebench

import "time"

type Kind string

const (
	SessionUpsert  Kind = "session_upsert"
	ChunksAppend   Kind = "chunks_append"
	GhostPut       Kind = "ghost_put"
	GhostRead      Kind = "ghost_read"
	GhostTree      Kind = "ghost_tree"
	DocumentCreate Kind = "document_create"
	DocumentPush   Kind = "document_push"
	DocumentRead   Kind = "document_read"
	MigrationBegin Kind = "migration_begin"
	MigrationRead  Kind = "migration_read"
	SessionRead    Kind = "session_read"
)

type Operation struct {
	ID           string        `json:"id"`
	Kind         Kind          `json:"kind"`
	DependsOn    []string      `json:"depends_on,omitempty"`
	At           time.Duration `json:"at_ns"`
	PayloadBytes int64         `json:"payload_bytes"`
	Payload      any           `json:"payload"`
}

type Workload struct {
	Seed       uint64      `json:"seed"`
	Scale      Scale       `json:"scale"`
	Operations []Operation `json:"operations"`
}

type Scale struct {
	Projects           int `json:"projects"`
	Sessions           int `json:"sessions_per_project"`
	ChunksPerSession   int `json:"chunks_per_session"`
	ChunkBodyBytes     int `json:"chunk_body_bytes"`
	GhostFiles         int `json:"ghost_files_per_project"`
	GhostBodyBytes     int `json:"ghost_body_bytes"`
	Documents          int `json:"documents_per_project"`
	DocumentRevisions  int `json:"document_revisions"`
	DocumentBodyBytes  int `json:"document_body_bytes"`
	MigrationArtifacts int `json:"migration_artifacts_per_project"`
	ReadEvery          int `json:"read_every"`
}

type SessionUpsertPayload struct {
	LogicalID  string `json:"logical_id"`
	ExternalID string `json:"external_id"`
	Project    string `json:"project"`
}

type ChunkPayload struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
	Raw  string `json:"raw"`
}

type ChunksAppendPayload struct {
	Session string         `json:"session"`
	Chunks  []ChunkPayload `json:"chunks"`
}

type GhostPutPayload struct {
	Project     string `json:"project"`
	Path        string `json:"path"`
	Description string `json:"description"`
	ContentSHA  string `json:"content_sha"`
	LineCount   int    `json:"line_count"`
}

type GhostReadPayload struct {
	Project           string `json:"project"`
	Path              string `json:"path"`
	DescriptionDigest string `json:"description_digest"`
	ContentSHA        string `json:"content_sha"`
	LineCount         int    `json:"line_count"`
}

type GhostTreePayload struct {
	Project       string   `json:"project"`
	Prefix        string   `json:"prefix"`
	ExpectedPaths []string `json:"expected_paths"`
}

type DocumentCreatePayload struct {
	LogicalID string `json:"logical_id"`
	Project   string `json:"project"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

type DocumentPushPayload struct {
	Document string `json:"document"`
	Base     int    `json:"base"`
	Body     string `json:"body"`
}

type DocumentReadPayload struct {
	Document string `json:"document"`
	Revision int    `json:"revision"`
	Digest   string `json:"digest"`
}

type Artifact struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type MigrationBeginPayload struct {
	LogicalID string     `json:"logical_id"`
	Project   string     `json:"project"`
	Artifacts []Artifact `json:"artifacts"`
}

type MigrationReadPayload struct {
	Project   string     `json:"project"`
	Artifacts []Artifact `json:"artifacts"`
}

type SessionReadPayload struct {
	Session string         `json:"session"`
	Chunks  []ChunkPayload `json:"chunks"`
}

type Expected struct {
	Sessions   map[string]ExpectedSession   `json:"sessions"`
	Ghosts     map[string]ExpectedGhost     `json:"ghosts"`
	Documents  map[string]ExpectedDocument  `json:"documents"`
	Migrations map[string]ExpectedMigration `json:"migrations"`
}

type ExpectedSession struct {
	ExternalID string               `json:"external_id"`
	Project    string               `json:"project"`
	Chunks     map[int]ChunkPayload `json:"chunks"`
}

type ExpectedGhost struct {
	Project           string `json:"project"`
	Path              string `json:"path"`
	DescriptionDigest string `json:"description_digest"`
	DescriptionBytes  int    `json:"description_bytes"`
}

type ExpectedDocument struct {
	Project         string   `json:"project"`
	Slug            string   `json:"slug"`
	RevisionDigests []string `json:"revision_digests"`
}

type ExpectedMigration struct {
	Project   string     `json:"project"`
	Artifacts []Artifact `json:"artifacts"`
}
