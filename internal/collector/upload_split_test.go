package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type statusErr struct{ code int }

func (e statusErr) Error() string   { return "http status" }
func (e statusErr) HTTPStatus() int { return e.code }

// limitUp lehnt einen Stapel ab, dessen Wire-Größe über limit liegt, wie der
// Server mit 413.
type limitUp struct {
	fakeUp
	limit int
	sizes []int
}

func (l *limitUp) AppendChunks(id int64, cs []store.Chunk) error {
	b, _ := json.Marshal(map[string]any{"chunks": cs})
	l.sizes = append(l.sizes, len(b))
	if len(b) > l.limit {
		return statusErr{413}
	}
	return l.fakeUp.AppendChunks(id, cs)
}

func writeLines(t *testing.T, fp string, n int, filler string) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"type":"user","cwd":"/tmp","sessionId":"s","message":{"role":"user","content":"` + filler + `"}}` + "\n")
	}
	if err := os.WriteFile(fp, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBatchSizeCountsTheSerializedBodyIncludingText(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	// Every character is escaped twice on the wire (HTML escaping and the
	// quote inside raw), and the text repeats the content once more.
	writeLines(t, fp, 30, strings.Repeat("<", 1<<20))
	old := uploadBatchBytes
	uploadBatchBytes = 8 << 20
	oldLine := serverLineLimit
	serverLineLimit = 10 << 20
	defer func() { uploadBatchBytes, serverLineLimit = old, oldLine }()
	up := &limitUp{limit: 10 << 20}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	for _, n := range up.sizes {
		if n > uploadBatchBytes+(8<<20) {
			t.Errorf("request of %d bytes is far above the %d budget", n, uploadBatchBytes)
		}
	}
	if got := len(up.chunks[1]); got != 30 {
		t.Errorf("uploaded %d of 30 lines", got)
	}
}

func TestRefusedBatchIsHalvedInsteadOfRetriedForever(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	writeLines(t, fp, 16, strings.Repeat("a", 100<<10))
	up := &limitUp{limit: 600 << 10}
	st := newTestState(dir)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatalf("a refused batch must be split, got %v", err)
	}
	if got := len(up.chunks[1]); got != 16 {
		t.Fatalf("uploaded %d of 16 lines", got)
	}
	for i, c := range up.chunks[1] {
		if c.Seq != i {
			t.Fatalf("line %d has seq %d", i, c.Seq)
		}
	}
	if f := st.Files[fp]; f == nil || f.Seq != 16 {
		t.Errorf("state did not advance: %+v", f)
	}
}

func TestSingleLineTooLargeForTheServerIsReplacedByAMarker(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	writeLines(t, fp, 1, strings.Repeat("a", 300<<10))
	old := serverLineLimit
	serverLineLimit = 100 << 10
	defer func() { serverLineLimit = old }()
	up := &limitUp{limit: 100 << 10}
	st := newTestState(dir)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	got := up.chunks[1]
	if len(got) != 1 || got[0].Seq != 0 || !strings.Contains(got[0].Raw, "omitted") {
		t.Fatalf("chunks = %+v", got)
	}
	if st.Files[fp].Offset == 0 {
		t.Error("offset did not advance past the oversized line")
	}
}

// A 413 for a line that is within the server's own limit comes from somewhere
// else (a proxy limit): replacing the line would lose it for good, so the file
// stops and says so.
func TestRefusalBelowTheServerLimitPausesTheFileInsteadOfReplacingTheLine(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	writeLines(t, fp, 1, strings.Repeat("a", 300<<10))
	up := &limitUp{limit: 100 << 10} // refuses, although far below serverLineLimit
	st := newTestState(dir)
	err := SyncFile(fp, "claude-code", up, st, "m")
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("want a loud pause error, got %v", err)
	}
	if len(up.chunks[1]) != 0 {
		t.Errorf("the line was replaced: %+v", up.chunks[1])
	}
	if f := st.Files[fp]; f != nil && f.Offset != 0 {
		t.Errorf("offset advanced to %d", f.Offset)
	}
}

type refUp struct {
	fakeUp
	refs []store.SessionRef
}

func (r *refUp) UpsertSessionRef(s store.Session) (store.SessionRef, error) {
	return store.SessionRef{PublicID: "abcdefghijkm"}, nil
}

func (r *refUp) AppendChunksRef(ref store.SessionRef, cs []store.Chunk) error {
	r.refs = append(r.refs, ref)
	return nil
}

func TestGuestCollectorKeepsTheAddressInsteadOfANumber(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	writeLines(t, fp, 2, "hi")
	up := &refUp{}
	st := newTestState(dir)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.refs) != 1 || up.refs[0].PublicID != "abcdefghijkm" || up.refs[0].ID != 0 {
		t.Errorf("refs = %+v", up.refs)
	}
	if f := st.Files[fp]; f.PublicID != "abcdefghijkm" || f.SessionID != 0 {
		t.Errorf("state = %+v", f)
	}
}

// The server limit applies to the whole body, so a line just below it still
// does not fit once the {"chunks":[...]} envelope is added: it must get the
// marker instead of an endless 413.
func TestLineJustBelowTheServerLimitCountsTheEnvelope(t *testing.T) {
	old := serverLineLimit
	serverLineLimit = 100 << 10
	defer func() { serverLineLimit = old }()
	c := store.Chunk{Seq: 0, Role: "user", Raw: ""}
	c.Raw = strings.Repeat("a", serverLineLimit-wireSize(c)-envelopeBytes/2)
	if w := wireSize(c); w > serverLineLimit || w+envelopeBytes <= serverLineLimit {
		t.Fatalf("setup: wire size %d, limit %d", w, serverLineLimit)
	}
	up := &limitUp{limit: serverLineLimit}
	if err := uploadSplit(up, store.SessionRef{ID: 1}, []store.Chunk{c}); err != nil {
		t.Fatalf("a line that cannot fit must be replaced by a marker, got %v", err)
	}
	got := up.chunks[1]
	if len(got) != 1 || got[0].Seq != 0 || !strings.Contains(got[0].Raw, "omitted") {
		t.Fatalf("chunks = %+v", got)
	}
}
