package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// prodLike builds the structure the production database had before REQ-623,
// in small: one org "default" holding everything, its GitHub source with a
// shared prefix, personal and foreign repositories, named projects, throwaway
// remotes, a test organization, two service-like accounts that are members,
// an unclaimed remote a member worked in, and legacy sessions without account.
func prodLike(t *testing.T) *Store {
	t.Helper()
	st := orgStore(t, "robin", "e2e-host", "e2e-friend", "gardener", "ops-runner", "philipp")
	def := mustOrg(t, st, "person:1", "Default", "default")
	join := func(org Org, who string) {
		code, _, err := st.CreateInvitation("person:1", org.ID, "", OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range []string{"person:4", "person:5", "person:6"} {
		join(def, who)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", def.ID, SourceGitHub, "acme", 179, []string{"github.com/acme/app", "github.com/acme/site"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", def.ID, "github.com/acme/*", VisibilityShared); err != nil {
		t.Fatal(err)
	}
	// Projects as the old default-org rule created them.
	for _, r := range []string{"github.com/acme/app", "github.com/acme/site", "github.com/robin-gh/tool", "github.com/robin-gh/notes",
		"github.com/lab/proto", "github.com/jellyfin/swiftfin", "ai/opt/assistant", "ai//opt/assistant"} {
		if _, err := st.db.Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, r, def.ID, now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []string{"github.com/acme/app", "github.com/acme/site"} {
		mustShare(t, st, "person:1", r, true)
	}
	if _, err := st.CreateNamedProject("person:1", "infra", "default"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", "default/infra", "person:5", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", "github.com/acme/app", "person:4", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	// Throwaway remotes that test runs left behind.
	// A room join of a test made this one (PAIN-67), and nobody ever worked in it.
	for _, r := range []string{"/home/u/.cache/rules-live-1/origin", "benchmark.invalid/x", "github.com/example-nobody/unknown-repo-xyz"} {
		if _, err := st.db.Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, r, def.ID, now()); err != nil {
			t.Fatal(err)
		}
	}
	// A test organization with its own project.
	e2e := mustOrg(t, st, "person:2", "E2E", "e2e-test")
	join2, _, _ := st.CreateInvitation("person:2", e2e.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:3", join2); err != nil {
		t.Fatal(err)
	}
	// From before the reserved test owners: a claim made it then.
	if _, err := st.db.Exec(`INSERT INTO projects(remote, org_id, created_at) VALUES(?,?,?)`, "github.com/e2e-collab/guestbook", e2e.ID, now()); err != nil {
		t.Fatal(err)
	}
	// Sessions: philipp first in an unclaimed remote, legacy ones without an account.
	sess := func(ext, project string, acct int64, at string) {
		if _, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: ext, Scope: scope.Axes{Project: project}, StartedAt: at, AccountID: acct}); err != nil {
			t.Fatal(err)
		}
	}
	sess("s1", "github.com/philipp-org/fleet", 6, "2026-10-01T10:00:00Z")
	// An unclaimed remote two people used, the later one with an earlier time.
	sess("s5", "github.com/shared-org/tool", 1, "2026-10-02T10:00:00Z")
	sess("s6", "github.com/shared-org/tool", 6, "2020-01-01T10:00:00Z")
	sess("s2", "github.com/robin-gh/tool", 0, "2026-09-01T10:00:00Z")
	sess("s3", "github.com/jellyfin/swiftfin", 0, "2026-09-02T10:00:00Z")
	// Philipp's one later session in a repository robin used long before.
	sess("s4", "github.com/robin-gh/tool", 6, "2026-10-05T10:00:00Z")
	// Sessions from before accounts carry none.
	if _, err := st.db.Exec(`UPDATE sessions SET account_id=0 WHERE external_id IN ('s2','s3')`); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimMachine("old-laptop", "person:2"); err != nil {
		t.Fatal(err)
	}
	return st
}

func prodPlan() OwnerMigrationPlan {
	return OwnerMigrationPlan{
		OrgName:         "Acme Labs",
		Services:        map[string]string{"gardener": "", "ops-runner": "acme"},
		ArchiveOrgs:     []string{"e2e-test"},
		Archive:         []string{"github.com/example-nobody"},
		ArchiveMachines: []string{"old-laptop"},
		DisableAccounts: []string{"e2e-host", "e2e-friend"},
		Merge:           map[string]string{"ai//opt/assistant": "ai/opt/assistant"},
		// Philipp wrote once in robin's repository: the plan decides it.
		To: map[string]string{"github.com/robin-gh": "robin"},
	}
}

// Security review 6: authors of every kind count, a remote several people
// wrote to stays where it is unless the plan assigns it, and sessions without
// an account go to the instance owner, never to the plan's default owner.
func TestMigrateOwnersBlocksContestedRemotesAndStampsTheInstanceOwner(t *testing.T) {
	st := prodLike(t)
	// Philipp wrote knowledge (no session) in the repository only robin's
	// legacy sessions name.
	if _, err := st.db.Exec(`INSERT INTO knowledge(type, title, body, project, person, created_at, updated_at) VALUES('note','t','b','github.com/jellyfin/swiftfin','philipp',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	plan := prodPlan()
	plan.To = nil
	// The default owner is not the instance owner; what robin alone used
	// stays in the organization, so nobody loses anything.
	plan.DefaultOwner = "philipp"
	plan.Keep = []string{"ai", "github.com/robin-gh/notes", "default/infra"}
	// An entry whose author could not be told apart (recorded without owner)
	// makes its remote contested as well.
	res, err := st.db.Exec(`INSERT INTO knowledge(type, title, body, project, person, created_at, updated_at) VALUES('note','t','b','github.com/lab/proto','sam',?,?)`, now(), now())
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := res.LastInsertId()
	if _, err := st.db.Exec(`INSERT OR REPLACE INTO knowledge_authors(knowledge_id, account_id) VALUES(?,0)`, kid); err != nil {
		t.Fatal(err)
	}
	rep, err := st.MigrateOwners(plan, true)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Unexpected)
	}
	for _, r := range []string{"github.com/robin-gh/tool", "github.com/jellyfin/swiftfin", "github.com/shared-org/tool", "github.com/lab/proto"} {
		if !slices.Contains(rep.Contested, r) {
			t.Errorf("%s not contested: %v", r, rep.Contested)
		}
	}
	for _, r := range []string{"github.com/robin-gh/tool", "github.com/jellyfin/swiftfin"} {
		if p, _ := st.ProjectByRemote(r); p.Org != "acme" {
			t.Errorf("%s moved to %q although contested", r, p.Org)
		}
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE external_id IN ('s2','s3') AND account_id=1`).Scan(&n)
	if n != 2 {
		t.Fatalf("legacy sessions stamped to the instance owner: %d of 2", n)
	}
}

func TestMigrateOwnersDryRunChangesNothingAndReportsEverything(t *testing.T) {
	st := prodLike(t)
	rep, err := st.MigrateOwners(prodPlan(), false)
	if err != nil {
		t.Fatalf("%v: %v", err, rep.Unexpected)
	}
	if rep.Applied || rep.Org != "default" || rep.OrgMoved != "default -> acme" || rep.Stayed != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Moved["robin"]; len(got) != 7 || !slices.Contains(got, "robin/infra") {
		t.Fatalf("moved to robin = %v", got)
	}
	if got := rep.Claimed["philipp"]; len(got) != 1 || got[0] != "github.com/philipp-org/fleet" {
		t.Fatalf("claimed = %v", rep.Claimed)
	}
	// A remote several people used is not decided by a time the client sent.
	if len(rep.Contested) != 1 || rep.Contested[0] != "github.com/shared-org/tool" {
		t.Fatalf("contested = %v", rep.Contested)
	}
	if len(rep.Archived) != 4 || rep.SessionsStamped != 2 || len(rep.Services) != 2 || len(rep.Disabled) != 2 || len(rep.ArchivedMachines) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	// Losses are the services' only; gains are own namespaces only.
	if _, ok := rep.Lost["gardener"]; !ok {
		t.Errorf("the gardener loses nothing: %v", rep.Lost)
	}
	for who := range rep.Lost {
		if who != "gardener" && who != "ops-runner" {
			t.Errorf("%s loses %v", who, rep.Lost[who])
		}
	}
	// Nothing changed.
	if p, _ := st.ProjectByRemote("github.com/robin-gh/tool"); p.Org != "default" {
		t.Fatalf("dry run moved a project: %+v", p)
	}
	if o, _ := st.OrgByRef("default"); o.Slug != "default" {
		t.Fatal("dry run moved the slug")
	}
}

func TestMigrateOwnersApplyIsWhatTheDryRunSaidAndIdempotent(t *testing.T) {
	st := prodLike(t)
	rep, err := st.MigrateOwners(prodPlan(), true)
	if err != nil || !rep.Applied {
		t.Fatalf("%v %v", err, rep.Unexpected)
	}
	org, _ := st.OrgByRef("acme")
	if org.Name != "Acme Labs" || org.DisplaySource != DisplayUser || rep.DisplaySource != DisplayUser {
		t.Fatalf("org = %+v, report display %q", org, rep.DisplaySource)
	}
	if cur, ok := st.OrgSlugTarget("default"); !ok || cur != "acme" {
		t.Fatal("no redirect from default")
	}
	for remote, want := range map[string]string{
		"github.com/acme/app": "acme", "github.com/robin-gh/tool": "robin", "github.com/lab/proto": "robin",
		"github.com/jellyfin/swiftfin": "robin", "robin/infra": "robin", "github.com/philipp-org/fleet": "philipp", "ai/opt/assistant": "robin",
	} {
		if p, ok := st.ProjectByRemote(remote); !ok || p.Org != want {
			t.Errorf("%s belongs to %+v, want %s", remote, p, want)
		}
	}
	for _, r := range []string{"/home/u/.cache/rules-live-1/origin", "benchmark.invalid/x", "github.com/e2e-collab/guestbook", "ai//opt/assistant", "github.com/example-nobody/unknown-repo-xyz"} {
		if p, _ := st.ProjectByRemote(r); p.ArchivedAt == "" {
			t.Errorf("%s not archived", r)
		}
	}
	// Rights after: philipp keeps the shared project, sees none of robin's;
	// ops-runner keeps its explicit role in infra and nothing else.
	for who, want := range map[string]map[string]string{
		"person:6": {"github.com/acme/app": RoleMember, "github.com/robin-gh/tool": "", "github.com/philipp-org/fleet": RoleOwner},
		"person:5": {"robin/infra": RoleMember, "github.com/acme/site": ""},
		"person:4": {"github.com/acme/app": RoleMember, "github.com/acme/site": ""},
		"person:1": {"robin/infra": RoleOwner, "github.com/acme/site": RoleOwner, "github.com/philipp-org/fleet": ""},
	} {
		for remote, role := range want {
			if got := st.ProjectRole(remote, who).Role; got != role {
				t.Errorf("%s in %s = %q, want %q", who, remote, got, role)
			}
		}
	}
	if st.AccountKind("person:4") != AccountKindService {
		t.Error("gardener is no service")
	}
	var unstamped int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id=0`).Scan(&unstamped)
	if unstamped != 0 {
		t.Errorf("%d sessions without account", unstamped)
	}
	// A plan that opts in lets name and address follow GitHub from now on.
	follow := prodPlan()
	follow.FollowGitHub = true
	if dry, err := st.MigrateOwners(follow, false); err != nil || dry.DisplaySource != DisplayGitHub {
		t.Fatalf("follow_github dry run: %q %v", dry.DisplaySource, err)
	}
	if org, _ := st.OrgByRef("acme"); org.DisplaySource != DisplayUser {
		t.Fatalf("the dry run changed the display source: %q", org.DisplaySource)
	}
	// Twice: nothing more to do, no deviation.
	again, err := st.MigrateOwners(prodPlan(), true)
	if err != nil || len(again.Moved) != 0 || len(again.Archived) != 0 || again.OrgMoved != "" || len(again.Unexpected) != 0 ||
		len(again.Merged) != 0 || len(again.Renamed) != 0 {
		t.Fatalf("second run: %+v %v", again, err)
	}
}

// A step that would take a person's access away is refused, and nothing of
// the migration stays.
func TestMigrateOwnersRefusesToHidePeoplesProjects(t *testing.T) {
	st := prodLike(t)
	// A project outside the prefix, shared with the whole org: philipp sees it
	// today, and moving it to robin's namespace would hide it from him.
	mustShare(t, st, "person:1", "github.com/lab/proto", true)
	rep, err := st.MigrateOwners(prodPlan(), true)
	if !errors.Is(err, ErrMigrationDeviates) || rep.Applied || len(rep.Unexpected) == 0 {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	if p, _ := st.ProjectByRemote("github.com/lab/proto"); p.Org != "default" {
		t.Fatal("a refused migration moved a project")
	}
	if _, ok := st.UserNamespace("person:1"); ok {
		t.Fatal("a refused migration left a namespace")
	}
	// Keeping it in the org is the way out.
	plan := prodPlan()
	plan.Keep = []string{"github.com/lab"}
	if _, err := st.MigrateOwners(plan, true); err != nil {
		t.Fatalf("with keep: %v", err)
	}
}

// Security review 11: the plan names projects by their ids before the
// migration, a rename of a project the move renamed follows it, and a second
// run with the same plan does nothing.
func TestMigrateOwnersRenamesFollowTheMoveAndRepeat(t *testing.T) {
	st := prodLike(t)
	plan := prodPlan()
	plan.Rename = map[string]string{"default/infra": "robin/infrastructure"}
	rep, err := st.MigrateOwners(plan, true)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Unexpected)
	}
	if _, ok := st.ProjectByRemote("robin/infrastructure"); !ok {
		t.Fatalf("not renamed: %v", rep.Renamed)
	}
	again, err := st.MigrateOwners(plan, true)
	if err != nil || len(again.Renamed) != 0 || len(again.Merged) != 0 || len(again.Unexpected) != 0 {
		t.Fatalf("second run: renamed %v merged %v unexpected %v err %v", again.Renamed, again.Merged, again.Unexpected, err)
	}
}

// Robin's decision E5: the plan's "to" gives remotes to the organization (also
// by the address it gets in the migration); they are shared with its
// members, and who gains them through that is no deviation.
func TestMigrateOwnersAssignsRemotesToTheOrganization(t *testing.T) {
	st := prodLike(t)
	plan := prodPlan()
	plan.To = map[string]string{"github.com/robin-gh": "robin", "github.com/shared-org/tool": "acme", "github.com/jellyfin": "default"}
	rep, err := st.MigrateOwners(plan, true)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Unexpected)
	}
	for _, r := range []string{"github.com/shared-org/tool", "github.com/jellyfin/swiftfin"} {
		p, ok := st.ProjectByRemote(r)
		if !ok || p.Org != "acme" || !p.Shared {
			t.Errorf("%s = %+v %v", r, p, ok)
		}
		if slices.Contains(rep.Contested, r) {
			t.Errorf("%s still contested", r)
		}
	}
	if role := st.ProjectRole("github.com/shared-org/tool", "person:6").Role; role != RoleMember {
		t.Errorf("philipp in the org's repository = %q", role)
	}
	if !slices.Contains(rep.GainedInOrg["philipp"], "github.com/shared-org/tool") || slices.Contains(rep.Gained["philipp"], "github.com/shared-org/tool") {
		t.Errorf("an org gain reported as own namespace: own %v org %v", rep.Gained, rep.GainedInOrg)
	}
	bad := prodPlan()
	bad.To = map[string]string{"github.com/lab": "no-such-owner"}
	if _, err := prodLike(t).MigrateOwners(bad, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unknown owner in to: %v", err)
	}
}
