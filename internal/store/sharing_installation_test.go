package store

import (
	"errors"
	"testing"
)

func TestSourceKeepsItsInstallation(t *testing.T) {
	st, o := prefixFixture(t)
	src, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, 4242, []string{"github.com/deadweight-labs/a"})
	if err != nil || src.InstallationID != 4242 || src.Repos != 1 {
		t.Fatalf("connect: %+v %v", src, err)
	}
	// Ein erneuter Nachweis ohne Installation (OAuth) löscht sie nicht.
	if src, err = st.ConnectSource("person:1", o.ID, "github", prefixLogin, nil); err != nil || src.InstallationID != 4242 {
		t.Fatalf("reconnect without installation: %+v %v", src, err)
	}
	list, _ := st.ListSources("person:1", o.ID)
	if len(list) != 1 || list[0].InstallationID != 4242 {
		t.Fatalf("list: %+v", list)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, -1, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative id: %v", err)
	}
}

func TestOneInstallationBelongsToOneSource(t *testing.T) {
	st, o := prefixFixture(t)
	other := mustOrg(t, st, "person:1", "Beta", "beta")
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", other.ID, "github", "other-org", 7, nil); !errors.Is(err, ErrSourceTaken) {
		t.Fatalf("same installation for a second source: %v", err)
	}
}

func TestInstallationMigrationIsAdditiveAndRepeatable(t *testing.T) {
	st, o := prefixFixture(t)
	if _, err := st.ConnectSource("person:1", o.ID, "github", prefixLogin, nil); err != nil {
		t.Fatal(err)
	}
	if err := migrateSourceInstallation(st.db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	list, _ := st.ListSources("person:1", o.ID)
	if len(list) != 1 || list[0].InstallationID != 0 {
		t.Fatalf("an old source keeps 0: %+v", list)
	}
}

func TestSyncInstallationReposCreatesProjectsUnderTheClaimedPrefix(t *testing.T) {
	st, o := prefixFixture(t)
	src, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, 99, []string{"github.com/deadweight-labs/a"})
	if err != nil {
		t.Fatal(err)
	}
	// Ohne Präfix-Regel bleibt es bei der Liste der Quelle.
	n, err := st.SyncInstallationRepos(99, []string{"github.com/deadweight-labs/new"}, nil)
	if err != nil || n != 0 {
		t.Fatalf("unclaimed prefix: %d %v", n, err)
	}
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/new"); known {
		t.Fatal("no prefix, no project")
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); err != nil {
		t.Fatal(err)
	}
	n, err = st.SyncInstallationRepos(99, []string{"github.com/Deadweight-Labs/fresh", "github.com/deadweight-labs/new", "github.com/someone-else/stranger", "bogus"}, nil)
	if err != nil || n != 2 {
		t.Fatalf("created: %d %v", n, err)
	}
	p, known := st.ProjectByRemote("github.com/deadweight-labs/fresh")
	if !known || p.OrgID != o.ID || !st.ProjectShared(p.Remote) {
		t.Fatalf("project: %+v %v", p, known)
	}
	if _, known := st.ProjectByRemote("github.com/someone-else/stranger"); known {
		t.Fatal("a repo of another account is ignored")
	}
	if n, _ := st.SyncInstallationRepos(99, []string{"github.com/deadweight-labs/fresh"}, nil); n != 0 {
		t.Fatalf("the same repo twice creates nothing: %d", n)
	}
	repos, _ := st.ListSourceRepos("person:1", o.ID, src.ID)
	if len(repos) != 3 {
		t.Fatalf("source list: %+v", repos)
	}
	// Entfernen räumt die Liste auf, das Projekt bleibt.
	if _, err := st.SyncInstallationRepos(99, nil, []string{"github.com/deadweight-labs/fresh"}); err != nil {
		t.Fatal(err)
	}
	repos, _ = st.ListSourceRepos("person:1", o.ID, src.ID)
	if len(repos) != 2 {
		t.Fatalf("after removal: %+v", repos)
	}
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/fresh"); !known {
		t.Fatal("removing a repo from the installation keeps the project")
	}
}

func TestSyncInstallationReposIgnoresUnknownInstallations(t *testing.T) {
	st, _ := prefixFixture(t)
	if n, err := st.SyncInstallationRepos(12345, []string{"github.com/deadweight-labs/x"}, nil); err != nil || n != 0 {
		t.Fatalf("unknown installation: %d %v", n, err)
	}
	if n, err := st.SyncInstallationRepos(0, []string{"github.com/deadweight-labs/x"}, nil); err != nil || n != 0 {
		t.Fatalf("zero installation: %d %v", n, err)
	}
}
