package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func archiveCLISetup(t *testing.T) (*store.Store, string) {
	t.Helper()
	repo := newRepo(t)
	t.Chdir(repo)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("operator")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	withConfig(t, srv.URL)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: token, Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}
	return st, repo
}

func TestGhostArchiveCLIPreviewConfirmationHistoryAndDoctor(t *testing.T) {
	st, _ := archiveCLISetup(t)
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "gone.go", Description: "preserved words"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDoctor([]string{"--only", "tree"}, &out); code != 1 || !strings.Contains(out.String(), "ctx ghost archive") {
		t.Fatalf("doctor before: %d %s", code, &out)
	}
	out.Reset()
	if code := cmdGhost([]string{"archive", "gone.go"}, &out); code != 0 || !strings.Contains(out.String(), "Vorschau") {
		t.Fatalf("preview: %d %s", code, &out)
	}
	if _, err := st.GhostFileByPath("github.com/test/repo", "gone.go"); err != nil {
		t.Fatal("preview mutated", err)
	}
	out.Reset()
	if code := cmdGhost([]string{"archive", "gone.go", "--reason", "confirmed removed", "--confirm-deleted"}, &out); code != 0 {
		t.Fatalf("archive: %d %s", code, &out)
	}
	out.Reset()
	if code := cmdDoctor([]string{"--only", "tree"}, &out); code != 0 {
		t.Fatalf("doctor after: %d %s", code, &out)
	}
	out.Reset()
	if code := cmdGhost([]string{"history", "gone.go", "--voll"}, &out); code != 0 || !strings.Contains(out.String(), "preserved words") {
		t.Fatalf("history: %d %s", code, &out)
	}
	out.Reset()
	if code := cmdGhost([]string{"archive", "--confirm-deleted", "--reason", "confirmed removed", "gone.go"}, &out); code != 0 {
		t.Fatalf("repeat: %d %s", code, &out)
	}
	hist, _ := st.GhostFileHistory("github.com/test/repo", "gone.go", 0)
	if len(hist) != 1 {
		t.Fatalf("retry history: %+v", hist)
	}
}

func TestGhostArchiveCLIRejectsLivePathsAndDetectableMoves(t *testing.T) {
	st, repo := archiveCLISetup(t)
	for _, path := range []string{"internal", "internal/store/store.go", "untracked.go"} {
		if path == "untracked.go" {
			if err := os.WriteFile(filepath.Join(repo, path), []byte("live"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: path, Description: "live"}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if code := cmdGhost([]string{"archive", "--confirm-deleted", "--reason", "mistake", path}, &out); code == 0 {
			t.Fatalf("live path archived: %q %s", path, &out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "moved.go"), []byte("unique moved contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", "moved.go").Run(); err != nil {
		t.Fatal(err)
	}
	_, blob, _, err := ghost.HashFile(filepath.Join(repo, "moved.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "old.go", Description: "move me", GitBlob: blob}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdGhost([]string{"archive", "old.go", "--confirm-deleted", "--reason", "mistake"}, &out); code == 0 || !strings.Contains(out.String(), "ctx mirror") {
		t.Fatalf("detectable move: %d %s", code, &out)
	}
	if _, err := st.GhostFileByPath("github.com/test/repo", "old.go"); err != nil {
		t.Fatal("move candidate lost", err)
	}
	if err := os.Remove(filepath.Join(repo, "moved.go")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdGhost([]string{"archive", "old.go", "--confirm-deleted", "--reason", "mistake"}, &out); code == 0 {
		t.Fatalf("incomplete move scan allowed archive: %s", &out)
	}
	if _, err := st.GhostFileByPath("github.com/test/repo", "old.go"); err != nil {
		t.Fatal("incomplete scan lost source", err)
	}
}

func TestGhostArchiveCLIRejectsBroadRequestsBeforeAnyWrite(t *testing.T) {
	st, _ := archiveCLISetup(t)
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "gone.go", Description: "keep"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"archive", "--confirm-deleted", "--reason", "gone"},
		{"archive", "gone.go", "--confirm-deleted"},
		{"archive", ".", "--confirm-deleted", "--reason", "gone"},
		{"archive", "gone.go", "gone.go", "--confirm-deleted", "--reason", "gone"},
		{"archive", "gone.go", "internal/store/store.go", "--confirm-deleted", "--reason", "gone"},
		{"archive", "--all", "--confirm-deleted", "--reason", "gone"},
	} {
		var out bytes.Buffer
		if code := cmdGhost(args, &out); code == 0 {
			t.Fatalf("broad request accepted: %v %s", args, &out)
		}
		if _, err := st.GhostFileByPath("github.com/test/repo", "gone.go"); err != nil {
			t.Fatalf("request changed active description: %v %v", args, err)
		}
	}
}

func TestGhostArchiveCLIEmptyInventoryAndSymlinkCannotConfirmAbsence(t *testing.T) {
	st, repo := archiveCLISetup(t)
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "link/gone.go", Description: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdGhost([]string{"archive", "link/gone.go", "--confirm-deleted", "--reason", "gone"}, &out); code == 0 || !strings.Contains(out.String(), "Symlink") {
		t.Fatalf("symlink path: %d %s", code, &out)
	}
	if err := exec.Command("git", "-C", repo, "rm", "--cached", "-r", "internal").Run(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdGhost([]string{"archive", "link/gone.go", "--confirm-deleted", "--reason", "gone"}, &out); code == 0 || !strings.Contains(out.String(), "leere Git") {
		t.Fatalf("empty inventory: %d %s", code, &out)
	}
}

func TestArchiveRejectsUnmergedUniqueMove(t *testing.T) {
	st, repo := archiveCLISetup(t)
	dst := filepath.Join(repo, "moved.go")
	if err := os.WriteFile(dst, []byte("unique moved words"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", "moved.go").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	_, blob, _, err := ghost.HashFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "update-index", "--index-info")
	cmd.Stdin = strings.NewReader("0 0000000000000000000000000000000000000000\tmoved.go\n100644 " + blob + " 1\tmoved.go\n100644 " + blob + " 2\tmoved.go\n100644 " + blob + " 3\tmoved.go\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "old.go", Description: "preserve move", GitBlob: blob}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := cmdGhost([]string{"archive", "old.go", "--reason", "deleted", "--confirm-deleted"}, &out)
	if code == 0 {
		t.Fatalf("archive accepted despite unique existing move destination: %s", &out)
	}
}

func TestDoctorEmptyInventoryCannotLabelAllDescriptionsOrphans(t *testing.T) {
	st, repo := archiveCLISetup(t)
	if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: "old.go", Description: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "rm", "--cached", "-r", "internal").Run(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDoctor([]string{"--only", "tree"}, &out); code != 0 || !strings.Contains(out.String(), "UNVERIFIED") || strings.Contains(out.String(), "Beschreibungen ohne Datei") {
		t.Fatalf("empty inventory: %d %s", code, &out)
	}
}
