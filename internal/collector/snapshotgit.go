package collector

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

var releaseSnapshotName = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$`)

func IsReleaseSnapshotName(name string) bool { return releaseSnapshotName.MatchString(name) }

func ResolveSnapshotGit(repoRoot, name string, allowDirty bool) (snapshot.GitProvenance, error) {
	var result snapshot.GitProvenance
	objectFormat, err := gitOut(repoRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return result, err
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return result, fmt.Errorf("unsupported git object format %q", objectFormat)
	}
	commit, err := gitOut(repoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return result, err
	}
	commit = strings.ToLower(commit)

	var ref, branch *string
	if symbolic, symbolicErr := gitOut(repoRoot, "symbolic-ref", "-q", "HEAD"); symbolicErr == nil {
		ref = stringPointer(symbolic)
		branchName := strings.TrimPrefix(symbolic, "refs/heads/")
		branch = stringPointer(branchName)
	}

	isRelease := IsReleaseSnapshotName(name)
	if isRelease {
		tagRef := "refs/tags/" + name
		tagCommit, tagErr := gitOut(repoRoot, "rev-parse", "--verify", tagRef+"^{commit}")
		if tagErr != nil || !strings.EqualFold(tagCommit, commit) {
			return result, &snapshot.RuleError{Code: "snapshot_tag_mismatch"}
		}
		ref = stringPointer(tagRef)
	}

	dirty, err := snapshotGitDirty(repoRoot)
	if err != nil {
		return result, err
	}
	if isRelease && dirty && !allowDirty {
		return result, &snapshot.RuleError{Code: "snapshot_dirty_worktree"}
	}

	result = snapshot.GitProvenance{
		ObjectFormat:   objectFormat,
		Commit:         commit,
		Ref:            ref,
		Branch:         branch,
		Dirty:          dirty,
		AllowDirtyUsed: isRelease && dirty && allowDirty,
		MetadataSource: "server-verified",
	}
	if dirty {
		manifest, manifestErr := snapshotWorktreeManifest(repoRoot)
		if manifestErr != nil {
			return snapshot.GitProvenance{}, manifestErr
		}
		version := snapshot.WorktreeFingerprintVersion
		digest := snapshot.Digest(sha256.Sum256(manifest))
		result.WorktreeFingerprintVersion = &version
		result.WorktreeFingerprint = &digest
	}
	return result, nil
}

func RecheckSnapshotGit(repoRoot, name string, expected snapshot.GitProvenance) error {
	current, err := ResolveSnapshotGit(repoRoot, name, true)
	if err != nil || !sameSnapshotGit(current, expected) {
		return &snapshot.RuleError{Code: "snapshot_git_changed", Retryable: true}
	}
	return nil
}

func sameSnapshotGit(a, b snapshot.GitProvenance) bool {
	return a.ObjectFormat == b.ObjectFormat &&
		a.Commit == b.Commit &&
		equalStringPointers(a.Ref, b.Ref) &&
		equalStringPointers(a.Branch, b.Branch) &&
		a.Dirty == b.Dirty &&
		equalUint32Pointers(a.WorktreeFingerprintVersion, b.WorktreeFingerprintVersion) &&
		equalDigestPointers(a.WorktreeFingerprint, b.WorktreeFingerprint) &&
		a.AllowDirtyUsed == b.AllowDirtyUsed &&
		a.MetadataSource == b.MetadataSource
}

func snapshotGitDirty(repoRoot string) (bool, error) {
	raw, err := gitBytes(repoRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return false, err
	}
	for pos := 0; pos < len(raw); {
		end := bytes.IndexByte(raw[pos:], 0)
		if end < 0 {
			return false, errors.New("unterminated git status record")
		}
		end += pos
		record := raw[pos:end]
		pos = end + 1
		if len(record) < 4 {
			return false, errors.New("short git status record")
		}
		paths := []string{string(record[3:])}
		if record[0] == 'R' || record[0] == 'C' || record[1] == 'R' || record[1] == 'C' {
			end = bytes.IndexByte(raw[pos:], 0)
			if end < 0 {
				return false, errors.New("unterminated git rename record")
			}
			end += pos
			paths = append(paths, string(raw[pos:end]))
			pos = end + 1
		}
		for _, path := range paths {
			if !ghost.IsFingerprintGeneratedPath(path) {
				return true, nil
			}
		}
	}
	drafts, err := ignoredDocumentWorktreePaths(repoRoot)
	if err != nil {
		return false, err
	}
	return len(drafts) != 0, nil
}

func snapshotWorktreeManifest(repoRoot string) ([]byte, error) {
	var manifest bytes.Buffer
	manifest.WriteString("ghosttree-worktree-fingerprint\x00")
	if err := binary.Write(&manifest, binary.BigEndian, snapshot.WorktreeFingerprintVersion); err != nil {
		return nil, err
	}

	indexRaw, err := gitBytes(repoRoot, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	indexRecords, paths, submodules, err := parseSnapshotIndex(indexRaw)
	if err != nil {
		return nil, err
	}
	for _, record := range indexRecords {
		if !ghost.IsFingerprintGeneratedPath(record.path) {
			writeManifestRecord(&manifest, "index", record.path, record.data)
		}
	}

	untrackedRaw, err := gitBytes(repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range splitNullPaths(untrackedRaw) {
		if !ghost.IsFingerprintGeneratedPath(path) {
			paths[path] = struct{}{}
		}
	}
	drafts, err := ignoredDocumentWorktreePaths(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, path := range drafts {
		paths[path] = struct{}{}
	}

	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		if !ghost.IsFingerprintGeneratedPath(path) {
			orderedPaths = append(orderedPaths, path)
		}
	}
	sort.Slice(orderedPaths, func(i, j int) bool { return orderedPaths[i] < orderedPaths[j] })
	for _, path := range orderedPaths {
		data, err := worktreePathRecord(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		writeManifestRecord(&manifest, "worktree", path, data)
	}

	orderedSubmodules := make([]string, 0, len(submodules))
	for path := range submodules {
		if !ghost.IsFingerprintGeneratedPath(path) {
			orderedSubmodules = append(orderedSubmodules, path)
		}
	}
	sort.Slice(orderedSubmodules, func(i, j int) bool { return orderedSubmodules[i] < orderedSubmodules[j] })
	for _, path := range orderedSubmodules {
		data := []byte("unavailable")
		subRoot := filepath.Join(repoRoot, filepath.FromSlash(path))
		if head, headErr := gitOut(subRoot, "rev-parse", "--verify", "HEAD^{commit}"); headErr == nil {
			nested, nestedErr := snapshotWorktreeManifest(subRoot)
			if nestedErr != nil {
				return nil, nestedErr
			}
			var framed bytes.Buffer
			writeLengthBytes(&framed, []byte(strings.ToLower(head)))
			writeLengthBytes(&framed, nested)
			data = framed.Bytes()
		}
		writeManifestRecord(&manifest, "submodule", path, data)
	}
	return manifest.Bytes(), nil
}

func ignoredDocumentWorktreePaths(repoRoot string) ([]string, error) {
	raw, err := gitBytes(repoRoot, "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--", ".ghosttree/edit", ".ghosttree/edit.tmp")
	if err != nil {
		return nil, err
	}
	return splitNullPaths(raw), nil
}

type snapshotIndexRecord struct {
	path string
	data []byte
}

func parseSnapshotIndex(raw []byte) ([]snapshotIndexRecord, map[string]struct{}, map[string]struct{}, error) {
	var records []snapshotIndexRecord
	paths := make(map[string]struct{})
	submodules := make(map[string]struct{})
	for _, rawRecord := range bytes.Split(raw, []byte{0}) {
		if len(rawRecord) == 0 {
			continue
		}
		tab := bytes.IndexByte(rawRecord, '\t')
		if tab < 0 {
			return nil, nil, nil, errors.New("invalid git index record")
		}
		metadata, pathBytes := rawRecord[:tab], rawRecord[tab+1:]
		parts := bytes.Fields(metadata)
		if len(parts) != 3 {
			return nil, nil, nil, errors.New("invalid git index metadata")
		}
		path := string(pathBytes)
		data := append([]byte(nil), metadata...)
		records = append(records, snapshotIndexRecord{path: path, data: data})
		paths[path] = struct{}{}
		if string(parts[0]) == "160000" && string(parts[2]) == "0" {
			submodules[path] = struct{}{}
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].path != records[j].path {
			return records[i].path < records[j].path
		}
		return bytes.Compare(records[i].data, records[j].data) < 0
	})
	return records, paths, submodules, nil
}

func worktreePathRecord(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte("missing"), nil
	}
	if err != nil {
		return nil, err
	}
	var data bytes.Buffer
	if err := binary.Write(&data, binary.BigEndian, uint32(info.Mode())); err != nil {
		return nil, err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		writeLengthBytes(&data, []byte("symlink"))
		writeLengthBytes(&data, []byte(target))
	case info.Mode().IsRegular():
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		writeLengthBytes(&data, []byte("file"))
		writeLengthBytes(&data, hash.Sum(nil))
	default:
		writeLengthBytes(&data, []byte(info.Mode().Type().String()))
	}
	return data.Bytes(), nil
}

func splitNullPaths(raw []byte) []string {
	var paths []string
	for _, path := range bytes.Split(raw, []byte{0}) {
		if len(path) != 0 {
			paths = append(paths, string(path))
		}
	}
	return paths
}

func writeManifestRecord(dst *bytes.Buffer, kind, path string, data []byte) {
	writeLengthBytes(dst, []byte(kind))
	writeLengthBytes(dst, []byte(path))
	writeLengthBytes(dst, data)
}

func writeLengthBytes(dst *bytes.Buffer, data []byte) {
	_ = binary.Write(dst, binary.BigEndian, uint64(len(data)))
	_, _ = dst.Write(data)
}

func stringPointer(value string) *string { return &value }

func equalStringPointers(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func equalUint32Pointers(a, b *uint32) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func equalDigestPointers(a, b *snapshot.Digest) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
