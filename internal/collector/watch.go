package collector

import (
	"bufio"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/fsnotify/fsnotify"
)

type Uploader interface {
	UpsertSession(s store.Session) (int64, error)
	AppendChunks(id int64, chunks []store.Chunk) error
}

// uploadBatch bounds request size during the initial import of old transcripts.
const uploadBatch = 500

// metaScanLines is how far into a file we look for the session metadata line.
const metaScanLines = 200

func parserFor(harness string) func([]byte) ParsedLine {
	if harness == "codex" {
		return ParseCodexLine
	}
	return ParseClaudeLine
}

// SyncFile uploads everything appended to path since the last confirmed run.
// The offset only advances after the server accepted a batch.
func SyncFile(path, harness string, up Uploader, st *State, machine string) error {
	fs := st.file(path)
	if fs.SessionID == 0 || fs.MetadataVersion < 1 {
		id, err := registerSession(path, harness, up, machine)
		if err != nil {
			return err
		}
		fs.SessionID = id
		fs.MetadataVersion = 1
		if err := st.Save(); err != nil {
			return err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(fs.Offset, io.SeekStart); err != nil {
		return err
	}
	parse := parserFor(harness)
	r := bufio.NewReaderSize(f, 1<<20)
	offset := fs.Offset
	var batch []store.Chunk
	seq := fs.Seq
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := up.AppendChunks(fs.SessionID, batch); err != nil {
			return err
		}
		fs.Offset = offset
		fs.Seq = seq
		batch = batch[:0]
		return st.Save()
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A trailing line without newline is still being written: leave it.
			break
		}
		offset += int64(len(line))
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			// Blank lines still advance the offset once a batch is confirmed.
			continue
		}
		p := parse([]byte(trimmed))
		batch = append(batch, store.Chunk{
			Seq:  seq,
			Role: p.Role,
			Text: redact.Redact(p.Text),
			Raw:  redact.Redact(trimmed),
		})
		seq++
		if len(batch) >= uploadBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func registerSession(path, harness string, up Uploader, machine string) (int64, error) {
	head, err := firstLines(path, metaScanLines)
	if err != nil {
		return 0, err
	}
	var externalID, cwd, project, branch string
	if harness == "codex" {
		externalID, cwd, project, branch = CodexSessionMeta(head)
	} else {
		externalID, cwd, branch = ClaudeSessionMeta(path, head)
	}
	if externalID == "" {
		externalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if project == "" && cwd != "" {
		p, b := GitInfo(cwd)
		project = p
		if branch == "" {
			branch = b
		}
	}
	started := ""
	if fi, err := os.Stat(path); err == nil {
		started = fi.ModTime().UTC().Format(time.RFC3339)
	}
	return up.UpsertSession(store.Session{
		Harness:    harness,
		ExternalID: externalID,
		Scope:      scope.Axes{Project: project, Branch: branch, Machine: machine},
		CWD:        cwd,
		StartedAt:  started,
	})
}

func firstLines(path string, n int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for len(out) < n && sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	// A scan error here only limits metadata detection, never the sync itself.
	return out, nil
}

// Sweep syncs every transcript under the roots once.
func Sweep(roots map[string]string, up Uploader, st *State, machine string) error {
	for root, harness := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			if err := SyncFile(p, harness, up, st, machine); err != nil {
				log.Printf("sync %s: %v", p, err)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			log.Printf("walk %s: %v", root, err)
		}
	}
	return st.Save()
}

// Watch reacts to filesystem events and additionally sweeps every interval,
// because fsnotify alone misses whatever changed while the daemon was down.
func Watch(roots map[string]string, up Uploader, st *State, machine string, interval time.Duration) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	harnessOf := func(p string) string {
		for root, h := range roots {
			if strings.HasPrefix(p, root) {
				return h
			}
		}
		return "claude-code"
	}
	addTree := func(root string) {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				w.Add(p)
			}
			return nil
		})
	}
	for root := range roots {
		addTree(root)
	}
	if err := Sweep(roots, up, st, machine); err != nil {
		log.Printf("initial sweep: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
				addTree(ev.Name)
				continue
			}
			if !strings.HasSuffix(ev.Name, ".jsonl") {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if err := SyncFile(ev.Name, harnessOf(ev.Name), up, st, machine); err != nil {
				log.Printf("sync %s: %v", ev.Name, err)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher: %v", err)
		case <-ticker.C:
			for root := range roots {
				addTree(root)
			}
			if err := Sweep(roots, up, st, machine); err != nil {
				log.Printf("sweep: %v", err)
			}
		}
	}
}

// DefaultRoots maps the known transcript directories to their harness.
func DefaultRoots(home string) map[string]string {
	return map[string]string{
		filepath.Join(home, ".claude", "projects"): "claude-code",
		filepath.Join(home, ".codex", "sessions"):  "codex",
	}
}
