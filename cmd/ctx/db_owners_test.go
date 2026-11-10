package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestDBMigrateOwnersDryRunApplyAndBackup(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "g.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrg("person:1", "Default", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, store.SourceGitHub, "acme", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", "github.com/robin-gh/tool", "default"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	plan := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(plan, []byte(`{"org_name": "Acme Labs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDBMigrateOwners([]string{"--db", db, "--dry-run", "--plan", plan}, &out); code != 0 || !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), "default -> acme") {
		t.Fatalf("dry run %d: %s", code, out.String())
	}
	out.Reset()
	if code := cmdDBMigrateOwners([]string{"--db", db, "--apply"}, &out); code != 2 {
		t.Fatalf("apply without a backup decision: %d", code)
	}
	if code := cmdDBMigrateOwners([]string{"--db", db, "--dry-run", "--plan", writeFile(t, dir, "bad.json", `{"unknown": 1}`)}, &out); code != 2 {
		t.Fatalf("unknown plan field accepted: %d", code)
	}
	backup := filepath.Join(dir, "before.db")
	out.Reset()
	if code := cmdDBMigrateOwners([]string{"--db", db, "--apply", "--backup", backup, "--plan", plan}, &out); code != 0 || !strings.Contains(out.String(), "applied") {
		t.Fatalf("apply %d: %s", code, out.String())
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	st, _ = store.Open(db)
	defer st.Close()
	if p, _ := st.ProjectByRemote("github.com/robin-gh/tool"); p.OrgKind != store.OrgKindUser {
		t.Fatalf("not moved: %+v", p)
	}
	if o, _ := st.OrgByRef("acme"); o.Name != "Acme Labs" {
		t.Fatalf("org = %+v", o)
	}
	// The backup is the state before.
	old, _ := store.Open(backup)
	defer old.Close()
	if p, _ := old.ProjectByRemote("github.com/robin-gh/tool"); p.Org != "default" {
		t.Fatalf("backup = %+v", p)
	}
}

func writeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Review point 6: the dry run reads the database and changes nothing in it,
// not even the schema steps of this version.
func TestDBMigrateOwnersDryRunLeavesTheDatabaseAsItWas(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "g.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrg("person:1", "Default", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, store.SourceGitHub, "acme", 5, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// As from before this version: a schema step (the display backfill) is due.
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM org_state WHERE key='owner_display_source_backfill'; UPDATE orgs SET display_source=''`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	var out bytes.Buffer
	if code := cmdDBMigrateOwners([]string{"--db", db, "--dry-run", "--scratch", dir}, &out); code != 0 {
		t.Fatalf("dry run %d: %s", code, out.String())
	}
	raw, _ = sql.Open("sqlite", db)
	defer raw.Close()
	var marks int
	var display string
	_ = raw.QueryRow(`SELECT COUNT(*) FROM org_state WHERE key='owner_display_source_backfill'`).Scan(&marks)
	_ = raw.QueryRow(`SELECT display_source FROM orgs WHERE slug='default'`).Scan(&display)
	if marks != 0 || display != "" {
		t.Fatalf("the dry run changed the database: marks %d display %q", marks, display)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "migrate-owners-dry-*")); len(left) != 0 {
		t.Fatalf("the copy stayed: %v", left)
	}
}
