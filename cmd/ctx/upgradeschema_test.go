package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestUpgradeSchemaMovesLegacyRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB().Exec(`PRAGMA writable_schema=ON;
		UPDATE sqlite_master SET sql=replace(sql,'''instruction''','''instruction'',''request''') WHERE type='table' AND name='knowledge';
		PRAGMA writable_schema=OFF;`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB().Exec(`
		INSERT INTO knowledge(type,title,body,confidence,status,origin,superseded_by,created_at,updated_at)
		VALUES('request','move me','body','trusted','active','agent',0,'x','x');
		INSERT INTO request_resolution(knowledge_id,state,at) VALUES(1,'open','x');`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out bytes.Buffer
	if code := cmdUpgradeSchema([]string{"--db", path}, &out); code != 0 {
		t.Fatalf("code = %d, output = %s", code, out.String())
	}
	if !strings.Contains(out.String(), "request domain") {
		t.Fatalf("output = %q", out.String())
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.RequestByID(1); err != nil {
		t.Fatal(err)
	}
}
