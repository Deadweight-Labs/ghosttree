package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Ein älterer Client schickt jede Zeile, auch Kopien und Ballast. Der Server
// legt sie nicht ab, schlankt den Rest, und Ansicht und Index vertragen die
// Lücken in der Folge.
func TestAppendChunksSlimsLinesOfOlderClients(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) *Store{
		"direct": openTest,
		"writer": func(t *testing.T) *Store {
			s, err := Open(filepath.Join(t.TempDir(), "ghosttree.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "slim-" + name})
			if err != nil {
				t.Fatal(err)
			}
			prompt := `{"type":"user","message":{"role":"user","content":"rename the flag"},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`
			chunks := []Chunk{
				{Seq: 0, Role: "user", Text: "rename the flag", Raw: prompt},
				{Seq: 1, Raw: `{"type":"api-request-blob","message":{"role":"user","content":"rename the flag"},"timestamp":"2026-11-01T10:00:01.000Z"}`},
				{Seq: 2, Role: "assistant", Text: "renamed", Raw: `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"easy","signature":"c2ln"},{"type":"text","text":"renamed"}]},"uuid":"a1","timestamp":"2026-11-01T10:00:02.000Z"}`},
				{Seq: 3, Raw: `{"type":"attachment","attachment":{"type":"hook_success","content":"{}"},"timestamp":"2026-11-01T10:00:03.000Z"}`},
			}
			original := append([]Chunk(nil), chunks...)
			if err := s.AppendChunks(id, chunks); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(chunks, original) {
				t.Fatal("AppendChunks changed the caller's chunks")
			}
			got, err := s.ReadSession(id, 0, 10)
			if err != nil || len(got) != 2 {
				t.Fatalf("stored %d chunks, err %v", len(got), err)
			}
			if got[0].Raw != prompt || got[1].Seq != 2 || strings.Contains(got[1].Raw, "signature") || !strings.Contains(got[1].Raw, `"thinking":"easy"`) {
				t.Errorf("stored %+v", got)
			}
			// Nur Weggelassenes: kein Fehler, nichts gespeichert.
			if err := s.AppendChunks(id, chunks[3:]); err != nil {
				t.Fatal(err)
			}
			w, err := s.SessionWindow(id, 0, -1, 1)
			if err != nil || len(w.Chunks) != 1 || w.Total != 2 || !w.Later || w.LaterFrom != 1 {
				t.Fatalf("window %+v err %v", w, err)
			}
			next, err := s.SessionWindow(id, w.LaterFrom, -1, 1)
			if err != nil || len(next.Chunks) != 1 || next.Chunks[0].Seq != 2 || next.Later || !next.Earlier {
				t.Errorf("next window over the gap %+v err %v", next, err)
			}
			var indexed int
			_ = s.db.QueryRow(`SELECT count(*) FROM chunk_index WHERE session_id=?`, id).Scan(&indexed)
			if indexed != 2 {
				t.Errorf("indexed %d chunks", indexed)
			}
		})
	}
}

// longKeyOutput ist eine Werkzeugausgabe, in der ein privater Schlüssel über
// die Schnittstelle der Kürzung reicht: BEGIN im behaltenen Anfang, END weit
// dahinter. Würde vor dem Schwärzen gekürzt, fände die Schwärzung kein END
// mehr und der Anfang des Schlüssels bliebe stehen.
func longKeyOutput() (string, string) {
	body := "MIIEowIBAAKCAQEAfakekeymaterial"
	var b strings.Builder
	b.WriteString(strings.Repeat("log line before the key\n", 200))
	b.WriteString("-----BEGIN RSA " + "PRIVATE KEY-----\n")
	for b.Len() < 40_000 {
		b.WriteString(body + "\n")
	}
	b.WriteString("-----END RSA " + "PRIVATE KEY-----\n")
	b.WriteString(strings.Repeat("log line after the key\n", 200))
	return b.String(), body
}

func TestAppendChunksRedactsBeforeCutting(t *testing.T) {
	s := openTest(t)
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "key-cut"})
	if err != nil {
		t.Fatal(err)
	}
	out, body := longKeyOutput()
	enc, _ := json.Marshal(out)
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + string(enc) + `}]},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`
	if err := s.AppendChunks(id, []Chunk{{Seq: 0, Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadSession(id, 0, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("chunks %d err %v", len(got), err)
	}
	if strings.Contains(got[0].Raw, body) {
		t.Error("part of the private key was stored")
	}
	if !strings.Contains(got[0].Raw, "log line before the key") {
		t.Error("output around the key was lost")
	}
}
