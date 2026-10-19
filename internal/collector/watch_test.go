package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeUp struct {
	sessions []store.Session
	chunks   map[int64][]store.Chunk
	fail     bool
}

func (f *fakeUp) UpsertSession(s store.Session) (int64, error) {
	if f.fail {
		return 0, os.ErrDeadlineExceeded
	}
	f.sessions = append(f.sessions, s)
	return int64(len(f.sessions)), nil
}

func (f *fakeUp) AppendChunks(id int64, cs []store.Chunk) error {
	if f.fail {
		return os.ErrDeadlineExceeded
	}
	if f.chunks == nil {
		f.chunks = map[int64][]store.Chunk{}
	}
	f.chunks[id] = append(f.chunks[id], cs...)
	return nil
}

func newTestState(dir string) *State {
	return &State{path: filepath.Join(dir, "state.json"), Files: map[string]*fileState{}}
}

func TestSyncFileIncremental(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "abc.jsonl")
	githubToken := "ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	line := `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"abc","message":{"role":"user","content":"secret is ` + githubToken + `"}}`
	os.WriteFile(fp, []byte(line+"\n"), 0o644)
	st := newTestState(dir)
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, st, "workstation-a"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 1 || up.sessions[0].ExternalID != "abc" {
		t.Fatalf("sessions = %+v", up.sessions)
	}
	got := up.chunks[1]
	if len(got) != 1 || got[0].Seq != 0 {
		t.Fatalf("chunks = %+v", got)
	}
	if want := "[REDACTED:github]"; !strings.Contains(got[0].Text, want) || !strings.Contains(got[0].Raw, want) {
		t.Errorf("redaction missing in %+v", got[0])
	}
	// Append a second line; only it must be uploaded.
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","cwd":"/tmp","gitBranch":"","sessionId":"abc","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n")
	f.Close()
	SyncFile(fp, "claude-code", up, st, "workstation-a")
	if len(up.chunks[1]) != 2 || up.chunks[1][1].Seq != 1 {
		t.Fatalf("incremental chunks = %+v", up.chunks[1])
	}
}

func TestSyncFileKeepsOffsetOnFailure(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "abc.jsonl")
	os.WriteFile(fp, []byte(`{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"abc","message":{"role":"user","content":"hello"}}`+"\n"), 0o644)
	st := newTestState(dir)
	up := &fakeUp{fail: true}
	if err := SyncFile(fp, "claude-code", up, st, "workstation-a"); err == nil {
		t.Fatal("want error when upload fails")
	}
	up.fail = false
	SyncFile(fp, "claude-code", up, st, "workstation-a")
	if len(up.chunks) == 0 {
		t.Fatal("retry after failure must upload the pending line")
	}
}

func TestSyncFileIgnoresPartialLine(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "abc.jsonl")
	full := `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"abc","message":{"role":"user","content":"one"}}` + "\n"
	os.WriteFile(fp, []byte(full+`{"type":"user","cwd":"/tmp"`), 0o644)
	st := newTestState(dir)
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, st, "workstation-a"); err != nil {
		t.Fatal(err)
	}
	if len(up.chunks[1]) != 1 {
		t.Fatalf("partial trailing line must wait, got %+v", up.chunks[1])
	}
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`,"sessionId":"abc","message":{"role":"user","content":"two"}}` + "\n")
	f.Close()
	SyncFile(fp, "claude-code", up, st, "workstation-a")
	if len(up.chunks[1]) != 2 || up.chunks[1][1].Text != "two" {
		t.Fatalf("completed line must upload, got %+v", up.chunks[1])
	}
}

func TestStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	st, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Files["/x.jsonl"] = &fileState{Offset: 12, SessionID: 3, Seq: 4}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if f := again.Files["/x.jsonl"]; f == nil || f.Offset != 12 || f.Seq != 4 {
		t.Errorf("reloaded state = %+v", again.Files)
	}
}

type sizeUp struct {
	fakeUp
	batches []int
}

func (s *sizeUp) AppendChunks(id int64, cs []store.Chunk) error {
	n := 0
	for _, c := range cs {
		n += len(c.Raw)
	}
	s.batches = append(s.batches, n)
	return s.fakeUp.AppendChunks(id, cs)
}

func TestSyncFileSplitsUploadsBySize(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.jsonl")
	filler := strings.Repeat("a", 1<<20)
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(`{"type":"user","cwd":"/tmp","sessionId":"big","message":{"role":"user","content":"` + filler + `"}}` + "\n")
	}
	if err := os.WriteFile(fp, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	up := &sizeUp{}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.batches) < 2 {
		t.Fatalf("40 MiB went out in %d request(s)", len(up.batches))
	}
	for _, n := range up.batches {
		if n > uploadBatchBytes+(2<<20) {
			t.Errorf("batch of %d bytes exceeds the limit", n)
		}
	}
}
