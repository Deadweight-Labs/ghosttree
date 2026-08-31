package snapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

const ExportMediaType = "application/vnd.ghosttree.context-snapshot+json;version=2"

type ExportFilter struct {
	Domain string  `json:"domain"`
	Key    *string `json:"key"`
}

type Verification struct {
	SnapshotName string
	EntryCount   int64
	Full         bool
	Digest       *Digest
}

type exportEnvelopeV2 struct {
	Counts        map[string]int64 `json:"counts"`
	Entries       []Entry          `json:"entries"`
	ExportVersion uint32           `json:"export_version"`
	Filter        *ExportFilter    `json:"filter"`
	Snapshot      ExportHeadV2     `json:"snapshot"`
}

func WriteExport(dst io.Writer, head Head, counts map[string]int64, entries []Entry, filter *ExportFilter) error {
	if err := ValidateHead(head, counts); err != nil {
		return err
	}
	if err := validateExportFilter(filter, entries); err != nil {
		return err
	}
	ordered := append([]Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Domain != ordered[j].Domain {
			return ordered[i].Domain < ordered[j].Domain
		}
		return ordered[i].Key < ordered[j].Key
	})
	if err := verifyEntries(head, counts, ordered, filter == nil); err != nil {
		return err
	}

	envelope := exportEnvelopeV2{
		Counts: copyCounts(counts), Entries: ordered, ExportVersion: ExportVersion,
		Filter: filter, Snapshot: exportHeadV2(head),
	}
	raw, err := MarshalCanonical(envelope)
	if err != nil {
		return integrityError(err)
	}
	raw = append(raw, '\n')
	_, err = dst.Write(raw)
	return err
}

func VerifyExport(src io.Reader) (Verification, error) {
	raw, err := io.ReadAll(src)
	if err != nil {
		return Verification{}, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || (len(raw) > 1 && raw[len(raw)-2] == '\n') {
		return Verification{}, integrityError(fmt.Errorf("export must end with exactly one LF"))
	}
	canonical := raw[:len(raw)-1]
	if err := ValidateCanonical(canonical); err != nil {
		return Verification{}, integrityError(err)
	}
	var envelope exportEnvelopeV2
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Verification{}, integrityError(err)
	}
	if envelope.ExportVersion != ExportVersion {
		return Verification{}, &RuleError{Code: "unsupported_snapshot_schema"}
	}
	head := headFromExportV2(envelope.Snapshot)
	if err := ValidateHead(head, envelope.Counts); err != nil {
		return Verification{}, err
	}
	if err := validateExportFilter(envelope.Filter, envelope.Entries); err != nil {
		return Verification{}, err
	}
	if err := verifyEntries(head, envelope.Counts, envelope.Entries, envelope.Filter == nil); err != nil {
		return Verification{}, err
	}
	verification := Verification{SnapshotName: head.Name, EntryCount: int64(len(envelope.Entries)), Full: envelope.Filter == nil}
	if verification.Full {
		digest := head.ContentDigest
		verification.Digest = &digest
	}
	return verification, nil
}

func validateExportFilter(filter *ExportFilter, entries []Entry) error {
	if filter == nil {
		return nil
	}
	if filter.Domain == "" || (filter.Key != nil && *filter.Key == "") {
		return &RuleError{Code: "snapshot_invalid_filter"}
	}
	for _, entry := range entries {
		if entry.Domain != filter.Domain || (filter.Key != nil && entry.Key != *filter.Key) {
			return &RuleError{Code: "snapshot_invalid_filter"}
		}
	}
	if filter.Key != nil && len(entries) != 1 {
		return &RuleError{Code: "snapshot_invalid_filter"}
	}
	return nil
}

func verifyEntries(head Head, counts map[string]int64, entries []Entry, full bool) error {
	summaries := make([]EntrySummary, len(entries))
	derivedCounts := make(map[string]int64)
	var total int64
	previousDomain, previousKey := "", ""
	for i, entry := range entries {
		if entry.Domain == "" || entry.Key == "" || (i > 0 && (entry.Domain < previousDomain || (entry.Domain == previousDomain && entry.Key <= previousKey))) {
			return integrityError(fmt.Errorf("entries are not uniquely ordered"))
		}
		previousDomain, previousKey = entry.Domain, entry.Key
		if !json.Valid(entry.Payload) {
			return integrityError(fmt.Errorf("invalid payload JSON"))
		}
		if supportedSchemaVersion(head.SchemaVersion) {
			if err := ValidateCanonical(entry.Payload); err != nil {
				return integrityError(fmt.Errorf("%s/%s: %w", entry.Domain, entry.Key, err))
			}
		}
		if int64(len(entry.Payload)) != entry.PayloadSize || EntryDigest(entry.Payload) != entry.PayloadDigest {
			return integrityError(fmt.Errorf("payload size or digest mismatch"))
		}
		total += entry.PayloadSize
		derivedCounts[entry.Domain]++
		summaries[i] = EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize}
	}
	if !full {
		return nil
	}
	if head.EntryCount != int64(len(entries)) || head.PayloadBytesTotal != total {
		return integrityError(fmt.Errorf("snapshot aggregate mismatch"))
	}
	digest, err := ContentDigest(DigestHeadFromHead(head), summaries)
	if err != nil || digest != head.ContentDigest {
		return integrityError(fmt.Errorf("snapshot aggregate mismatch"))
	}
	for domain, count := range counts {
		if count != derivedCounts[domain] {
			return integrityError(fmt.Errorf("count mismatch for %s", domain))
		}
		delete(derivedCounts, domain)
	}
	if len(derivedCounts) != 0 {
		return integrityError(fmt.Errorf("counts omit domains"))
	}
	return nil
}

func exportHeadV2(head Head) ExportHeadV2 {
	return ExportHeadV2{
		Project: head.Project, Name: head.Name, SchemaVersion: head.SchemaVersion, ContentDigest: head.ContentDigest,
		GitObjectFormat: head.GitObjectFormat, GitCommit: head.GitCommit, GitRef: head.GitRef, GitBranch: head.GitBranch,
		GitDirty: head.GitDirty, GitWorktreeFingerprintVersion: head.GitWorktreeFingerprintVersion,
		GitWorktreeFingerprint: head.GitWorktreeFingerprint, AllowDirtyUsed: head.AllowDirtyUsed,
		GitMetadataSource: head.GitMetadataSource, Message: head.Message, ActorID: head.ActorID, ActorLabel: head.ActorLabel,
		SessionRef: head.SessionRef, CreatedAt: head.CreatedAt, EntryCount: head.EntryCount, PayloadBytesTotal: head.PayloadBytesTotal,
	}
}

func headFromExportV2(head ExportHeadV2) Head {
	return Head{
		Project: head.Project, Name: head.Name, SchemaVersion: head.SchemaVersion, State: "sealed", ContentDigest: head.ContentDigest,
		GitObjectFormat: head.GitObjectFormat, GitCommit: head.GitCommit, GitRef: head.GitRef, GitBranch: head.GitBranch,
		GitDirty: head.GitDirty, GitWorktreeFingerprintVersion: head.GitWorktreeFingerprintVersion,
		GitWorktreeFingerprint: head.GitWorktreeFingerprint, AllowDirtyUsed: head.AllowDirtyUsed,
		GitMetadataSource: head.GitMetadataSource, Message: head.Message, ActorID: head.ActorID, ActorLabel: head.ActorLabel,
		SessionRef: head.SessionRef, CreatedAt: head.CreatedAt, EntryCount: head.EntryCount, PayloadBytesTotal: head.PayloadBytesTotal,
	}
}

func copyCounts(counts map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(counts))
	for domain, count := range counts {
		out[domain] = count
	}
	return out
}

func integrityError(cause error) error {
	return &RuleError{Code: "snapshot_integrity_error", Message: cause.Error(), Retryable: false}
}
