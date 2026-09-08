package agentbench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteTranscriptManifest hashes every raw transcript under rawDir and writes
// the digests to w, newest-format-first, in the layout sha256sum(1) reads.
//
// It exists to keep a promise the campaign makes but cannot keep by publishing:
// the transcripts of a private repository contain its source, its paths and its
// architecture, so they stay closed. What can be published is their digest.
// Anyone who later receives the transcripts — a reviewer, a sceptic, the author
// two months on — can check that they are the ones the numbers came from, and
// nobody has to take that on trust.
//
// The format is deliberately the one `sha256sum -c transcripts.sha256` already
// understands: two spaces between digest and path, paths relative to the run
// directory. A custom format would need a custom checker, and a checker nobody
// runs proves nothing.
func WriteTranscriptManifest(w io.Writer, runDir, rawDir string) (int, error) {
	type entry struct{ path, digest string }
	var entries []entry

	err := filepath.WalkDir(rawDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		sum := sha256.New()
		if _, err := io.Copy(sum, file); err != nil {
			return err
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			rel = path
		}
		entries = append(entries, entry{path: rel, digest: hex.EncodeToString(sum.Sum(nil))})
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	// Sortiert nach Pfad: Zwei Laeufe derselben Kampagne sollen dasselbe
	// Manifest ergeben, sonst sieht eine Neuberechnung wie eine Aenderung aus.
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "%s  %s\n", e.digest, e.path); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}
