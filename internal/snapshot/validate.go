package snapshot

import (
	"strings"
	"time"
	"unicode/utf8"
)

var snapshotDomains = [...]string{"document", "ghost", "ghost-review", "knowledge", "request"}

func supportedSchemaVersion(schemaVersion uint32) bool {
	return schemaVersion == SchemaVersion
}

func NewCounts(schemaVersion uint32) (map[string]int64, error) {
	if !supportedSchemaVersion(schemaVersion) {
		return nil, &RuleError{Code: "unsupported_snapshot_schema"}
	}
	counts := make(map[string]int64, len(snapshotDomains))
	for _, domain := range snapshotDomains {
		counts[domain] = 0
	}
	return counts, nil
}

func ValidateHead(head Head, counts map[string]int64) error {
	if !supportedSchemaVersion(head.SchemaVersion) {
		return &RuleError{Code: "unsupported_snapshot_schema"}
	}
	if head.Project == "" || head.ActorID == "" || head.State != "sealed" || !validSnapshotName(head.Name) || !utf8.ValidString(head.Project+head.ActorID) {
		return &RuleError{Code: "snapshot_integrity_error"}
	}
	created, err := time.Parse(time.RFC3339Nano, head.CreatedAt)
	if err != nil || created.Location() != time.UTC || !strings.HasSuffix(head.CreatedAt, "Z") || head.EntryCount < 0 || head.PayloadBytesTotal < 0 || !validGitProvenance(head) {
		return &RuleError{Code: "snapshot_integrity_error"}
	}
	if len(counts) != len(snapshotDomains) {
		return &RuleError{Code: "snapshot_integrity_error"}
	}
	var total int64
	for _, domain := range snapshotDomains {
		count, ok := counts[domain]
		if !ok || count < 0 {
			return &RuleError{Code: "snapshot_integrity_error"}
		}
		total += count
	}
	if total != head.EntryCount {
		return &RuleError{Code: "snapshot_integrity_error"}
	}
	return nil
}

func validSnapshotName(name string) bool {
	if len(name) < 1 || len(name) > 128 {
		return false
	}
	for i, b := range []byte(name) {
		alnum := b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
		if !alnum && (i == 0 || b != '.' && b != '_' && b != '+' && b != '-') {
			return false
		}
	}
	return true
}

func validGitProvenance(head Head) bool {
	length := 0
	switch head.GitObjectFormat {
	case "sha1":
		length = 40
	case "sha256":
		length = 64
	default:
		return false
	}
	if len(head.GitCommit) != length {
		return false
	}
	for _, b := range []byte(head.GitCommit) {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	if head.GitMetadataSource != "server-verified" && head.GitMetadataSource != "client-reported" {
		return false
	}
	if !head.GitDirty {
		return head.GitWorktreeFingerprintVersion == nil && head.GitWorktreeFingerprint == nil && !head.AllowDirtyUsed
	}
	return head.GitWorktreeFingerprintVersion != nil && *head.GitWorktreeFingerprintVersion == WorktreeFingerprintVersion && head.GitWorktreeFingerprint != nil
}
