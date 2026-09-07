package store

import (
	"strings"
	"testing"
)

func TestAuthenticatePrincipalUsesStablePersonID(t *testing.T) {
	s := openTest(t)
	token, err := s.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}

	principal, ok := s.AuthenticatePrincipal(token)
	if !ok {
		t.Fatal("AuthenticatePrincipal rejected a valid token")
	}
	if principal.ID != "person:1" || principal.Label != "alice" {
		t.Fatalf("principal = %#v", principal)
	}
	captured := principal
	if _, err := s.db.Exec(`UPDATE persons SET name = 'alice-renamed' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	current, ok := s.AuthenticatePrincipal(token)
	if !ok || current.ID != captured.ID || current.Label != "alice-renamed" {
		t.Fatalf("principal after rename = %#v, ok=%v", current, ok)
	}
	if captured.Label != "alice" {
		t.Fatalf("captured label changed to %q", captured.Label)
	}
	if label, ok := s.Authenticate(token); !ok || label != "alice-renamed" {
		t.Fatalf("legacy Authenticate = %q, %v", label, ok)
	}
	if _, ok := s.AuthenticatePrincipal("wrong-token"); ok {
		t.Fatal("invalid token authenticated")
	}
}

// TestContextSnapshotAccessDefaultsToReadAndCreate locks the rule that a
// project nobody has written a row for is usable. The previous default denied
// everything, and the effect was that snapshots shipped and could not be used
// at all until somebody edited the production database by hand.
func TestContextSnapshotAccessDefaultsToReadAndCreate(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}

	access, err := s.ContextSnapshotAccess("person:1", "never-configured")
	if err != nil {
		t.Fatal(err)
	}
	if !access.Read || !access.Create {
		t.Fatalf("a project without a row must be usable: %#v", access)
	}
	// Release-Bind wirkt ueber die eigene Arbeit hinaus und bleibt eine
	// ausdrueckliche Vergabe.
	if access.ReleaseBind {
		t.Fatalf("release-bind must never be granted by default: %#v", access)
	}
}

// TestContextSnapshotAccessRevocationStillBites is the other half of the same
// rule: if an explicit row could not take access away, the default would not
// be a default but a hole.
func TestContextSnapshotAccessRevocationStillBites(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetContextSnapshotAccess("alice", "project-a", false, false, false); err != nil {
		t.Fatal(err)
	}

	access, err := s.ContextSnapshotAccess("person:1", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if access != (SnapshotAccess{}) {
		t.Fatalf("an explicit denial must override the default: %#v", access)
	}
}

func TestContextSnapshotAccessStoresCapabilities(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}

	cases := []SnapshotAccess{
		{Read: true},
		{Read: true, Create: true},
		{Read: true, Create: true, ReleaseBind: true},
	}
	for _, want := range cases {
		if err := s.SetContextSnapshotAccess("alice", "project-a", want.Read, want.Create, want.ReleaseBind); err != nil {
			t.Fatalf("SetContextSnapshotAccess(%#v): %v", want, err)
		}
		got, err := s.ContextSnapshotAccess("person:1", "project-a")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("access = %#v, want %#v", got, want)
		}
	}
	// Die Vergabe gilt genau fuer ihr Projekt. Ein anderes Projekt faellt
	// nicht auf die Vergabe zurueck, sondern auf den Default.
	other, err := s.ContextSnapshotAccess("person:1", "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if other != defaultSnapshotAccess() {
		t.Fatalf("another project must fall back to the default, got %#v", other)
	}
}

func TestContextSnapshotAccessValidatesWrites(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		person       string
		project      string
		read, create bool
		release      bool
	}{
		{name: "unknown person", person: "missing", project: "p", read: true},
		{name: "empty project", person: "alice", project: "", read: true},
		{name: "release without read", person: "alice", project: "p", create: true, release: true},
		{name: "release without create", person: "alice", project: "p", read: true, release: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.SetContextSnapshotAccess(tc.person, tc.project, tc.read, tc.create, tc.release); err == nil {
				t.Fatal("write succeeded")
			}
		})
	}
	if _, err := s.ContextSnapshotAccess("not-a-principal", "p"); err == nil {
		t.Fatal("malformed principal ID accepted")
	}
}

func TestContextSnapshotAccessIdenticalWriteIsNoOp(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContextSnapshotAccess("alice", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TEMP TABLE access_updates(n INTEGER NOT NULL);
		INSERT INTO access_updates VALUES(0);
		CREATE TEMP TRIGGER count_access_updates AFTER UPDATE ON context_snapshot_access
		BEGIN UPDATE access_updates SET n=n+1; END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContextSnapshotAccess("alice", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	var updates int
	if err := s.db.QueryRow(`SELECT n FROM access_updates`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("identical write performed %d updates", updates)
	}
}

func TestContextSnapshotAccessCanonicalizesAndCleansAliasRows(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO context_snapshot_access(person_id,project,can_read,can_create,can_release_bind)
		VALUES(1,' HTTPS://GitHub.com/Example/Repo.git ',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContextSnapshotAccess("alice", "git@github.com:Example/Repo.git", true, true, false); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"github.com/example/repo", "https://github.com/Example/Repo.git", " git@github.com:Example/Repo.git "} {
		access, err := s.ContextSnapshotAccess("person:1", project)
		if err != nil || access != (SnapshotAccess{Read: true, Create: true}) {
			t.Fatalf("project %q access=%+v err=%v", project, access, err)
		}
	}
	var count int
	var stored string
	if err := s.db.QueryRow(`SELECT count(*),min(project) FROM context_snapshot_access WHERE person_id=1`).Scan(&count, &stored); err != nil {
		t.Fatal(err)
	}
	if count != 1 || stored != "github.com/example/repo" {
		t.Fatalf("rows=%d stored=%q", count, stored)
	}
}

func TestContextSnapshotAccessRestrictsPersonDeletion(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContextSnapshotAccess("alice", "p", true, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM persons WHERE name='alice'`); err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("delete error = %v, want foreign-key constraint", err)
	}
}
