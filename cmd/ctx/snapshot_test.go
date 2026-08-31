package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestSnapshotCommandUsageAndFilterValidation(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := cmdSnapshot(nil, &out, &diagnostics); code != 2 || !strings.Contains(out.String(), "ctx snapshot create") {
		t.Fatalf("usage exit=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := cmdSnapshot([]string{"export", "mark", "--key", "k"}, &out, &diagnostics); code != 2 || !strings.Contains(out.String(), "--key requires --domain") {
		t.Fatalf("key exit=%d out=%q", code, out.String())
	}
}

func TestSnapshotCommandExportIsStdoutPureAndRejectsSymlinkOutput(t *testing.T) {
	repo := snapshotCLIRepo(t)
	head, entry := snapshotCLIHead()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/entries") && r.URL.Query().Get("key") != "":
			json.NewEncoder(w).Encode(snapshot.EntryPage{Exact: &entry})
		case strings.HasSuffix(r.URL.Path, "/entries"):
			json.NewEncoder(w).Encode(snapshot.EntryPage{Entries: []snapshot.EntrySummary{{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize}}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"snapshot": head, "counts": head.Counts})
		}
	}))
	t.Cleanup(srv.Close)
	snapshotCLIConfig(t, srv.URL)

	var out, diagnostics bytes.Buffer
	if code := cmdSnapshot([]string{"export", head.Name, repo}, &out, &diagnostics); code != 0 {
		t.Fatalf("export exit=%d out=%q diagnostics=%q", code, out.String(), diagnostics.String())
	}
	if !json.Valid(bytes.TrimSuffix(out.Bytes(), []byte{'\n'})) || diagnostics.Len() != 0 {
		t.Fatalf("stdout=%q diagnostics=%q", out.String(), diagnostics.String())
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "export.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdSnapshot([]string{"export", head.Name, "-o", link, repo}, &out, &diagnostics); code == 0 {
		t.Fatal("symlink output accepted")
	}
	if body, _ := os.ReadFile(target); string(body) != "keep" {
		t.Fatalf("symlink target changed: %q", body)
	}
}

func snapshotCLIRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main", repo}, {"-C", repo, "config", "user.name", "Test"}, {"-C", repo, "config", "user.email", "test@example.test"}, {"-C", repo, "remote", "add", "origin", "https://github.com/deadweight-labs/ghosttree.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", repo, "add", "README.md"}, {"-C", repo, "commit", "-m", "initial"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

func snapshotCLIConfig(t *testing.T, server string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := config.Save(config.Config{ServerURL: server, Token: "token"}); err != nil {
		t.Fatal(err)
	}
}

func snapshotCLIHead() (snapshot.Head, snapshot.Entry) {
	payload := json.RawMessage(`{"value":1}`)
	entry := snapshot.Entry{Domain: "knowledge", Key: "k", Payload: payload, PayloadDigest: snapshot.EntryDigest(payload), PayloadSize: int64(len(payload))}
	summary := snapshot.EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize}
	counts, _ := snapshot.NewCounts(snapshot.SchemaVersion)
	counts["knowledge"] = 1
	head := snapshot.Head{Project: "github.com/deadweight-labs/ghosttree", Name: "mark", SchemaVersion: snapshot.SchemaVersion, State: "sealed", GitObjectFormat: "sha1", GitCommit: strings.Repeat("a", 40), GitMetadataSource: "server-verified", ActorID: "person:1", CreatedAt: "2026-08-30T00:00:00Z", EntryCount: 1, PayloadBytesTotal: entry.PayloadSize, Counts: counts}
	head.ContentDigest, _ = snapshot.ContentDigest(snapshot.DigestHeadFromHead(head), []snapshot.EntrySummary{summary})
	return head, entry
}
