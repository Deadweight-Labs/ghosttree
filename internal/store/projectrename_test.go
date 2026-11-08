package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const (
	renameFrom = "github.com/deadweight-labs/before"
	renameTo   = "github.com/deadweight-labs/after"
)

// renameFixture legt ein Projekt über den Webhook-Pfad an (Präfix-Regel, Quelle
// mit Installation 99) und hängt Einträge in mehrere Tabellen daran.
func renameFixture(t *testing.T) (*Store, Org) {
	t.Helper()
	st, o := prefixFixture(t)
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, 99, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.SyncInstallationRepos(99, []string{renameFrom}, nil); err != nil || n != 1 {
		t.Fatalf("project not created: %d %v", n, err)
	}
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "vorher", Body: "x", Person: "robin", Scope: scope.Axes{Project: renameFrom}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "r", Person: "robin", Scope: scope.Axes{Project: renameFrom}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateThread(Thread{Project: renameFrom, Title: "t", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(GhostFile{Project: renameFrom, Path: "a.go", Description: "d", ContentSHA: "s"}); err != nil {
		t.Fatal(err)
	}
	return st, o
}

func TestRenamedRepoRenamesTheExistingProjectWithEverythingOnIt(t *testing.T) {
	st, o := renameFixture(t)
	before, _ := st.ProjectByRemote(renameFrom)
	var revBefore int
	if err := st.db.QueryRow(`SELECT revision FROM ghost_path_revisions WHERE project=? AND path='a.go'`, renameFrom).Scan(&revBefore); err != nil {
		t.Fatal(err)
	}

	renamed, err := st.RenameInstallationRepo(99, renameFrom, renameTo)
	if err != nil || !renamed {
		t.Fatalf("renamed %v err %v", renamed, err)
	}
	if _, stays := st.ProjectByRemote(renameFrom); stays {
		t.Fatal("the old name must be gone, not left beside the new one")
	}
	after, ok := st.ProjectByRemote(renameTo)
	if !ok || after.ID != before.ID || after.OrgID != o.ID || !st.ProjectShared(renameTo) {
		t.Fatalf("the same project, org and sharing under the new name: before %+v after %+v", before, after)
	}
	tables, err := projectTextTables(st.db)
	if err != nil {
		t.Fatal(err)
	}
	moved := map[string]bool{}
	for _, tbl := range tables {
		var old, now int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE project=?`, renameFrom).Scan(&old); err != nil {
			t.Fatal(err)
		}
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE project=?`, renameTo).Scan(&now); err != nil {
			t.Fatal(err)
		}
		if old != 0 {
			t.Errorf("%s still holds %d rows under the old name", tbl, old)
		}
		if now > 0 {
			moved[tbl] = true
		}
	}
	for _, want := range []string{"knowledge", "requests", "threads", "ghost_files", "search_documents"} {
		if !moved[want] {
			t.Errorf("%s did not follow the project (moved: %v)", want, moved)
		}
	}
	var revAfter int
	if err := st.db.QueryRow(`SELECT revision FROM ghost_path_revisions WHERE project=? AND path='a.go'`, renameTo).Scan(&revAfter); err != nil || revAfter <= revBefore {
		t.Fatalf("the path revision must not fall back: before %d after %d (%v)", revBefore, revAfter, err)
	}
	if n := 0; st.db.QueryRow(`SELECT COUNT(*) FROM ghost_path_revisions WHERE project=?`, renameFrom).Scan(&n) != nil || n != 0 {
		t.Fatalf("revision rows under the old name: %d", n)
	}
	if ks, err := st.KnowledgeForProject(renameTo); err != nil || len(ks) != 1 {
		t.Fatalf("knowledge under the new name: %d %v", len(ks), err)
	}
}

func TestRenamedRepoWithATakenNameChangesNothing(t *testing.T) {
	st, _ := renameFixture(t)
	if n, err := st.SyncInstallationRepos(99, []string{renameTo}, nil); err != nil || n != 1 {
		t.Fatalf("second project: %d %v", n, err)
	}
	renamed, err := st.RenameInstallationRepo(99, renameFrom, renameTo)
	if err != nil || renamed {
		t.Fatalf("a taken name must not merge two projects: renamed %v err %v", renamed, err)
	}
	if _, ok := st.ProjectByRemote(renameFrom); !ok {
		t.Fatal("the old project must stay")
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE project=?`, renameFrom).Scan(&n); err != nil || n != 1 {
		t.Fatalf("knowledge stays under the old name: %d %v", n, err)
	}
}

func TestRenamedRepoLeavesForeignAndUnknownCasesAlone(t *testing.T) {
	st, _ := renameFixture(t)
	for name, c := range map[string]struct {
		inst     int64
		from, to string
	}{
		"unknown installation": {12345, renameFrom, renameTo},
		"zero installation":    {0, renameFrom, renameTo},
		"foreign target":       {99, renameFrom, "github.com/someone-else/after"},
		"foreign source":       {99, "github.com/someone-else/before", renameTo},
		"no such project":      {99, "github.com/deadweight-labs/never-existed", renameTo},
		"same name":            {99, renameFrom, renameFrom},
	} {
		if renamed, err := st.RenameInstallationRepo(c.inst, c.from, c.to); err != nil || renamed {
			t.Errorf("%s: renamed %v err %v", name, renamed, err)
		}
	}
	if _, ok := st.ProjectByRemote(renameFrom); !ok {
		t.Fatal("the project must be untouched")
	}
}

// So läuft der Webhook: erst die Umbenennung, dann die Listenabgleichung mit dem
// neuen Namen als add und dem alten als remove. Der Abgleich darf danach kein
// zweites Projekt anlegen, und die Liste der Quelle führt nur noch den neuen Namen.
func TestListSyncAfterTheRenameCreatesNoSecondProject(t *testing.T) {
	st, o := renameFixture(t)
	if renamed, err := st.RenameInstallationRepo(99, renameFrom, renameTo); err != nil || !renamed {
		t.Fatalf("renamed %v err %v", renamed, err)
	}
	n, err := st.SyncInstallationRepos(99, []string{renameTo}, []string{renameFrom})
	if err != nil || n != 0 {
		t.Fatalf("list sync created %d projects (%v), want 0", n, err)
	}
	var projects int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE org_id=?`, o.ID).Scan(&projects); err != nil || projects != 1 {
		t.Fatalf("projects of the org: %d (%v), want the one renamed project", projects, err)
	}
}
