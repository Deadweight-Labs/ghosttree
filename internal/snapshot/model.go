// Package snapshot defines the versioned context-snapshot contract shared by
// storage and transports.
package snapshot

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	SchemaVersion              uint32 = 3
	ExportVersion              uint32 = 2
	WorktreeFingerprintVersion uint32 = 1
)

type Digest [32]byte

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

func (d Digest) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Digest) UnmarshalText(text []byte) error {
	if len(text) != hex.EncodedLen(len(d)) {
		return fmt.Errorf("snapshot digest must be 64 lowercase hexadecimal characters")
	}
	for _, b := range text {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return fmt.Errorf("snapshot digest must be 64 lowercase hexadecimal characters")
		}
	}
	_, err := hex.Decode(d[:], text)
	return err
}

type GitProvenance struct {
	ObjectFormat               string  `json:"git_object_format"`
	Commit                     string  `json:"git_commit"`
	Ref                        *string `json:"git_ref"`
	Branch                     *string `json:"git_branch"`
	Dirty                      bool    `json:"git_dirty"`
	WorktreeFingerprintVersion *uint32 `json:"git_worktree_fingerprint_version"`
	WorktreeFingerprint        *Digest `json:"git_worktree_fingerprint"`
	AllowDirtyUsed             bool    `json:"allow_dirty_used"`
	MetadataSource             string  `json:"git_metadata_source"`
}

type DigestHead struct {
	Project       string        `json:"project"`
	Name          string        `json:"name"`
	SchemaVersion uint32        `json:"schema_version"`
	Git           GitProvenance `json:"git"`
	Message       *string       `json:"message"`
	ActorID       string        `json:"actor_id"`
	ActorLabel    *string       `json:"actor_label"`
	SessionRef    *string       `json:"session_ref"`
	CreatedAt     string        `json:"created_at"`
}

type Head struct {
	ID                            int64            `json:"id"`
	Project                       string           `json:"project"`
	Name                          string           `json:"name"`
	SchemaVersion                 uint32           `json:"schema_version"`
	State                         string           `json:"state"`
	ContentDigest                 Digest           `json:"content_digest"`
	GitObjectFormat               string           `json:"git_object_format"`
	GitCommit                     string           `json:"git_commit"`
	GitRef                        *string          `json:"git_ref"`
	GitBranch                     *string          `json:"git_branch"`
	GitDirty                      bool             `json:"git_dirty"`
	GitWorktreeFingerprintVersion *uint32          `json:"git_worktree_fingerprint_version"`
	GitWorktreeFingerprint        *Digest          `json:"git_worktree_fingerprint"`
	AllowDirtyUsed                bool             `json:"allow_dirty_used"`
	GitMetadataSource             string           `json:"git_metadata_source"`
	Message                       *string          `json:"message"`
	ActorID                       string           `json:"actor_id"`
	ActorLabel                    *string          `json:"actor_label"`
	SessionRef                    *string          `json:"session_ref"`
	CreatedAt                     string           `json:"created_at"`
	EntryCount                    int64            `json:"entry_count"`
	PayloadBytesTotal             int64            `json:"payload_bytes_total"`
	Counts                        map[string]int64 `json:"counts"`
}

// ExportHeadV2 is deliberately closed rather than embedding Head. Any field
// change requires a new export version.
type ExportHeadV2 struct {
	Project                       string  `json:"project"`
	Name                          string  `json:"name"`
	SchemaVersion                 uint32  `json:"schema_version"`
	ContentDigest                 Digest  `json:"content_digest"`
	GitObjectFormat               string  `json:"git_object_format"`
	GitCommit                     string  `json:"git_commit"`
	GitRef                        *string `json:"git_ref"`
	GitBranch                     *string `json:"git_branch"`
	GitDirty                      bool    `json:"git_dirty"`
	GitWorktreeFingerprintVersion *uint32 `json:"git_worktree_fingerprint_version"`
	GitWorktreeFingerprint        *Digest `json:"git_worktree_fingerprint"`
	AllowDirtyUsed                bool    `json:"allow_dirty_used"`
	GitMetadataSource             string  `json:"git_metadata_source"`
	Message                       *string `json:"message"`
	ActorID                       string  `json:"actor_id"`
	ActorLabel                    *string `json:"actor_label"`
	SessionRef                    *string `json:"session_ref"`
	CreatedAt                     string  `json:"created_at"`
	EntryCount                    int64   `json:"entry_count"`
	PayloadBytesTotal             int64   `json:"payload_bytes_total"`
}

type Entry struct {
	Domain        string          `json:"domain"`
	Key           string          `json:"key"`
	Payload       json.RawMessage `json:"payload"`
	PayloadDigest Digest          `json:"payload_digest"`
	PayloadSize   int64           `json:"payload_size"`
}

type EntrySummary struct {
	Domain        string `json:"domain"`
	Key           string `json:"key"`
	PayloadDigest Digest `json:"payload_digest"`
	PayloadSize   int64  `json:"payload_size"`
}

type CreateInput struct {
	Project    string         `json:"project"`
	Name       string         `json:"name"`
	Git        GitProvenance  `json:"git"`
	Message    *string        `json:"message"`
	ActorID    string         `json:"actor_id"`
	ActorLabel *string        `json:"actor_label"`
	SessionRef *string        `json:"session_ref"`
	GitRecheck *GitProvenance `json:"git_recheck,omitempty"`
}

type CreateResult struct {
	Snapshot Head      `json:"snapshot"`
	Created  bool      `json:"created"`
	Warnings []Warning `json:"warnings"`
}

type ListFilter struct {
	Project string
	Cursor  string
	Limit   int
}

type SnapshotPage struct {
	Snapshots  []Head `json:"snapshots"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type EntryFilter struct {
	Domain string
	Key    string
	Cursor string
	Limit  int
}

type EntryPage struct {
	Entries    []EntrySummary `json:"entries,omitempty"`
	Exact      *Entry         `json:"entry,omitempty"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RuleError struct {
	Code               string         `json:"code"`
	Message            string         `json:"message,omitempty"`
	Resolution         string         `json:"resolution,omitempty"`
	Details            map[string]any `json:"details,omitempty"`
	Retryable          bool           `json:"retryable"`
	ExistingDigest     string         `json:"existing_digest,omitempty"`
	RequestedDigest    string         `json:"requested_digest,omitempty"`
	ExistingGitCommit  string         `json:"existing_git_commit"`
	RequestedGitCommit string         `json:"requested_git_commit"`
}

func (e *RuleError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type Limits struct {
	MaxEntryPayloadBytes    int64
	MaxEntriesPerSnapshot   int64
	MaxSnapshotPayloadBytes int64
	MaxCanonicalHeadBytes   int64
	MaxSnapshotLogicalBytes int64
	MaxSnapshotsPerProject  int64
	MaxProjectLogicalBytes  int64
	MaxSnapshotsPerStore    int64
	MaxStoreLogicalBytes    int64
	MaxMessageBytes         int64
	MaxActorIDBytes         int64
	MaxActorLabelBytes      int64
	MaxSessionRefBytes      int64
	MaxGitRefBytes          int64
	MaxGitBranchBytes       int64
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntryPayloadBytes:    4 << 20,
		MaxEntriesPerSnapshot:   20_000,
		MaxSnapshotPayloadBytes: 128 << 20,
		MaxCanonicalHeadBytes:   32 << 10,
		MaxSnapshotLogicalBytes: 160 << 20,
		MaxSnapshotsPerProject:  1_000,
		MaxProjectLogicalBytes:  8 << 30,
		MaxSnapshotsPerStore:    10_000,
		MaxStoreLogicalBytes:    64 << 30,
		MaxMessageBytes:         4_096,
		MaxActorIDBytes:         512,
		MaxActorLabelBytes:      512,
		MaxSessionRefBytes:      512,
		MaxGitRefBytes:          1_024,
		MaxGitBranchBytes:       1_024,
	}
}
