package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedStripImagesDB legt eine Datenbank an, in der Bilder noch im Rohtext
// stehen, wie vor dem Platzhalter gespeichert. Es gibt vier Zeilen mit Bild.
func seedStripImagesDB(t *testing.T) (path string, rows map[int64]string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "strip"})
	if err != nil {
		t.Fatal(err)
	}
	pixels := make([]byte, 50_000)
	rand.New(rand.NewSource(5)).Read(pixels)
	b64 := base64.StdEncoding.EncodeToString(pixels)
	lines := []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"base64 is how images travel"}]}}`,
		`{"type":"response_item","payload":{"output":[{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `"}]}}`,
		`{"toolUseResult":{"file":{"base64":"` + b64 + `","type":"image/jpeg"}}}`,
		`{"result":{"content":[{"type":"image","data":"` + b64 + `","mimeType":"image/png"}]}}`,
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
	}
	for i, l := range lines {
		if _, err := s.DB().Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, i, "", "", l); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, readRaw(t, path)
}

func readRaw(t *testing.T, path string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := db.Query(`SELECT id, raw FROM session_chunks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[int64]string{}
	for r.Next() {
		var id int64
		var raw string
		if err := r.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		out[id] = raw
	}
	return out
}

func TestStripImagesDryRunWritesNothing(t *testing.T) {
	path, before := seedStripImagesDB(t)
	var out bytes.Buffer
	if code := cmdStripImages([]string{"--db", path, "--dry-run"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	for _, want := range []string{"dry run", "chunks with embedded files would replace: 4", "of 6 chunks"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "VACUUM") {
		t.Errorf("a dry run points to VACUUM:\n%s", out.String())
	}
	if !reflect.DeepEqual(readRaw(t, path), before) {
		t.Fatal("a dry run changed the database")
	}
}

func TestStripImagesReplacesAndSecondRunChangesNothing(t *testing.T) {
	path, before := seedStripImagesDB(t)
	var out bytes.Buffer
	if code := cmdStripImages([]string{"--db", path, "--batch", "2", "--sleep", "0s"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	for _, want := range []string{"replaced: 4", "bytes saved:", "VACUUM"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	after := readRaw(t, path)
	changed := 0
	for id, raw := range before {
		want, saved := blobstrip.Strip(raw)
		if after[id] != want {
			t.Errorf("chunk %d: got %.120s", id, after[id])
		}
		if saved > 0 {
			changed++
			if len(after[id]) > 1000 || !strings.Contains(after[id], "[ghosttree: ") {
				t.Errorf("chunk %d not replaced: %.120s", id, after[id])
			}
		}
	}
	if changed != 4 {
		t.Fatalf("%d chunks changed, want 4", changed)
	}

	out.Reset()
	if code := cmdStripImages([]string{"--db", path}, &out); code != 0 {
		t.Fatalf("second run: code %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "replaced: 0") || !strings.Contains(out.String(), "bytes saved: 0 ") || strings.Contains(out.String(), "VACUUM") {
		t.Errorf("second run output:\n%s", out.String())
	}
	if !reflect.DeepEqual(readRaw(t, path), after) {
		t.Fatal("the second run changed rows")
	}
}

func TestStripImagesRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"no db":         {nil, 2},
		"bad batch":     {[]string{"--db", filepath.Join(dir, "x.db"), "--batch", "0"}, 2},
		"stray operand": {[]string{"--db", filepath.Join(dir, "x.db"), "now"}, 2},
		"missing file":  {[]string{"--db", filepath.Join(dir, "x.db")}, 1},
	} {
		var out bytes.Buffer
		if code := cmdStripImages(c.args, &out); code != c.code {
			t.Errorf("%s: code %d, want %d: %s", name, code, c.code, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.db")); err == nil {
		t.Error("a missing database was created")
	}
}
