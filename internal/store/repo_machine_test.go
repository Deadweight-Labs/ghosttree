package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

// REQ-615, decision #4349: an entry can carry a project AND a machine
// (knowledge about running a repository on a host). With a project, the project
// role alone decides who reads, changes and is handed it; the machine is only
// where it applies. Machine knowledge WITHOUT a project stays private to the
// owner of the machine and is shared only through the machine share.

const (
	rmMachine = "buildhost-box"
	rmMia     = int64(3)
	rmGus     = int64(5)
)

func rmPrincipals() map[string]Principal {
	return map[string]Principal{
		"robin": {ID: "person:1", Label: "robin"},
		"lena":  {ID: "person:2", Label: "lena"},
		"mia":   {ID: "person:3", Label: "mia"},
		"rex":   {ID: "person:4", Label: "rex"},
		"gus":   {ID: "person:5", Label: "gus"},
		"nora":  {ID: "person:6", Label: "nora"},
	}
}

func rmInsert(t *testing.T, st *Store, title, confidence string, author int64, person string, ax scope.Axes) Knowledge {
	t.Helper()
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "text of " + title, Person: person, AuthorID: author, Confidence: confidence, Scope: ax})
	if err != nil {
		t.Fatalf("insert %q: %v", title, err)
	}
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// accessFixture: robin owner, lena lead, mia member, rex member with review,
// gus guest, nora without a role. nora owns the machine: that must not open the
// project entry for her.
func rmFixture(t *testing.T) *Store {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := st.ClaimMachine(rmMachine, "person:6"); err != nil {
		t.Fatal(err)
	}
	return st
}

// The project role decides, in both directions: members read without owning the
// machine, the owner of the machine without a role reads nothing.
func TestProjectEntryWithAMachineFollowsTheProjectRole(t *testing.T) {
	st := rmFixture(t)
	both := scope.Axes{Project: roleProject, Machine: rmMachine}
	trusted := rmInsert(t, st, "deploy on buildhost", "trusted", rmMia, "mia", both)
	staged := rmInsert(t, st, "draft on buildhost", "staged", rmMia, "mia", both)
	who := rmPrincipals()
	type row struct {
		viewer  string
		seeT    bool // sees the trusted entry
		seeS    bool // sees the staged entry
		deliver bool // is handed the trusted entry
		edit    error
	}
	for _, c := range []row{
		{"robin", true, true, true, nil},
		{"lena", true, true, true, nil},
		{"mia", true, true, true, nil},
		{"rex", true, true, true, ErrAccessForbidden},
		// A guest reads released entries only, as for any project entry.
		{"gus", true, false, true, ErrAccessForbidden},
		// nora owns the machine and has no role: for her the entry does not exist.
		{"nora", false, false, false, ErrAccessNotFound},
	} {
		pa := st.Access(who[c.viewer])
		if got := pa.CanSeeKnowledge(trusted); got != c.seeT {
			t.Errorf("%s sees the trusted project entry with a machine: %v, want %v", c.viewer, got, c.seeT)
		}
		if got := pa.CanSeeKnowledge(staged); got != c.seeS {
			t.Errorf("%s sees the staged project entry with a machine: %v, want %v", c.viewer, got, c.seeS)
		}
		if got := pa.CanSeeKnowledge(trusted) && pa.CanDeliverKnowledge(trusted); got != c.deliver {
			t.Errorf("%s is handed the project entry with a machine: %v, want %v", c.viewer, got, c.deliver)
		}
		if err := st.Access(who[c.viewer]).CheckKnowledge(trusted, ActEdit); !errors.Is(err, c.edit) && !(c.edit == nil && err == nil) {
			t.Errorf("%s edits it: %v, want %v", c.viewer, err, c.edit)
		}
	}
	// Writing: the project role, never the machine.
	for viewer, wantErr := range map[string]error{"mia": nil, "rex": nil, "lena": nil, "gus": ErrAccessForbidden, "nora": ErrAccessNotFound} {
		err := st.Access(who[viewer]).CheckKnowledgeCreate(Knowledge{Type: "note", Title: "x", Scope: both, Confidence: "trusted"})
		if !errors.Is(err, wantErr) && !(wantErr == nil && err == nil) {
			t.Errorf("%s files a project entry with a machine: %v, want %v", viewer, err, wantErr)
		}
	}
	// Decide: the same row on its own, for the record of the rule.
	if d := st.Access(who["nora"]).Decide(roleProject, ResKnowledge, ActRead, Object{Machine: rmMachine, Confidence: "trusted"}); d.Allowed {
		t.Error("the owner of the machine reads a project entry without a role")
	}
	if d := st.Access(who["mia"]).Decide(roleProject, ResKnowledge, ActRead, Object{Machine: rmMachine, Confidence: "trusted"}); !d.Allowed {
		t.Errorf("a member is refused a project entry that names a machine she does not own: %+v", d)
	}
}

// Knowledge without a project stays private: only the owner of the machine, and
// nobody else with a project role, however high.
func TestMachineOnlyKnowledgeStaysPrivateToTheOwner(t *testing.T) {
	st := rmFixture(t)
	only := rmInsert(t, st, "fail2ban rules of the host", "trusted", 6, "nora", scope.Axes{Machine: rmMachine})
	who := rmPrincipals()
	for viewer, want := range map[string]bool{"nora": true, "robin": false, "lena": false, "mia": false, "rex": false, "gus": false} {
		if got := st.Access(who[viewer]).CanSeeKnowledge(only); got != want {
			t.Errorf("%s sees machine-only knowledge: %v, want %v", viewer, got, want)
		}
	}
	// A project role does not change it: mia is a project member of roleProject,
	// but the entry has no project.
	if st.Access(who["mia"]).CanSeeKnowledge(only) {
		t.Error("a project member reads machine-only knowledge")
	}
	// The author's text is delivered only while the author owns the machine.
	if !st.Access(who["nora"]).CanDeliverKnowledge(only) {
		t.Error("the owner's own machine knowledge is not delivered")
	}
	foreign := rmInsert(t, st, "planted on the machine", "trusted", rmMia, "mia", scope.Axes{Machine: rmMachine})
	if st.Access(who["nora"]).CanDeliverKnowledge(foreign) {
		t.Error("text of another account on the owner's machine is delivered (#2581)")
	}
}

// The delivery lock of the project holds for an entry with a machine: the author
// must be a writer in the project.
func TestProjectEntryWithAMachineIsNotDeliveredFromANonWriter(t *testing.T) {
	st := rmFixture(t)
	both := scope.Axes{Project: roleProject, Machine: rmMachine}
	fromGuest := rmInsert(t, st, "text of a guest", "trusted", rmGus, "gus", both)
	fromMember := rmInsert(t, st, "text of a member", "trusted", rmMia, "mia", both)
	pa := st.Access(rmPrincipals()["rex"])
	if !pa.CanSeeKnowledge(fromGuest) {
		t.Error("the entry of a guest is not readable for members")
	}
	if pa.CanDeliverKnowledge(fromGuest) {
		t.Error("the text of a guest is delivered")
	}
	if !pa.CanDeliverKnowledge(fromMember) {
		t.Error("the text of a member is not delivered")
	}
	// A writer who is demoted loses delivery for what he wrote before.
	if err := st.SetProjectRole("person:1", roleProject, "person:3", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if st.Access(rmPrincipals()["rex"]).CanDeliverKnowledge(fromMember) {
		t.Error("the text of a demoted author is still delivered")
	}
	// A reviewer takes it over by verifying it (the rule of every project entry).
	verified := fromMember
	verified.Confidence, verified.ConfirmedBy = "verified", "rex"
	if !st.Access(rmPrincipals()["rex"]).CanDeliverKnowledge(verified) {
		t.Error("a verified entry confirmed by a writer is not delivered")
	}
}

// A session reads what belongs to its project on any machine, and what is bound
// to its own machine without a project. A session on the machine WITHOUT the
// project does not read the project entry (the leak in the other direction).
func TestProjectEntryWithAMachineIsReadByEverySessionOfTheProject(t *testing.T) {
	st := rmFixture(t)
	both := scope.Axes{Project: roleProject, Machine: rmMachine}
	rmInsert(t, st, "bound to buildhost", "trusted", rmMia, "mia", both)
	rmInsert(t, st, "only the host", "trusted", 6, "nora", scope.Axes{Machine: rmMachine})
	rmInsert(t, st, "plain project", "trusted", rmMia, "mia", scope.Axes{Project: roleProject})
	rmInsert(t, st, "other project", "trusted", rmMia, "mia", scope.Axes{Project: "github.com/dw/q", Machine: rmMachine})
	titles := func(ks []Knowledge) map[string]bool {
		out := map[string]bool{}
		for _, k := range ks {
			out[k.Title] = true
		}
		return out
	}
	for name, c := range map[string]struct {
		ax   scope.Axes
		want map[string]bool
	}{
		"project, on another machine":    {scope.Axes{Project: roleProject, Machine: "laptop"}, map[string]bool{"bound to buildhost": true, "plain project": true}},
		"project, on the machine":        {scope.Axes{Project: roleProject, Machine: rmMachine}, map[string]bool{"bound to buildhost": true, "plain project": true, "only the host": true}},
		"project, no machine named":      {scope.Axes{Project: roleProject}, map[string]bool{"bound to buildhost": true, "plain project": true}},
		"the machine without a project":  {scope.Axes{Machine: rmMachine}, map[string]bool{"only the host": true}},
		"another project on the machine": {scope.Axes{Project: "github.com/dw/q", Machine: rmMachine}, map[string]bool{"other project": true, "only the host": true}},
	} {
		got := titles(mustKnowledgeFor(t, st, c.ax))
		if len(got) != len(c.want) {
			t.Errorf("%s: read %v, want %v", name, got, c.want)
		}
		for title := range c.want {
			if !got[title] {
				t.Errorf("%s: %q is missing (read %v)", name, title, got)
			}
		}
	}
	// Search: the union a session reads, with the access filter of the viewer.
	for name, c := range map[string]struct {
		viewer string
		ax     scope.Axes
		found  bool
	}{
		"member, project session on another machine": {"mia", scope.Axes{Project: roleProject, Machine: "laptop"}, true},
		"stranger who owns the machine":              {"nora", scope.Axes{Project: roleProject, Machine: rmMachine}, false},
		"session on the machine without the project": {"mia", scope.Axes{Machine: rmMachine}, false},
	} {
		pa := st.Access(rmPrincipals()[c.viewer])
		hits, err := st.SearchKnowledgeForContext("buildhost", c.ax, 20, pa.Viewer())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, k := range hits {
			if k.Title == "bound to buildhost" && pa.CanSeeKnowledge(k) {
				found = true
			}
		}
		if found != c.found {
			t.Errorf("search, %s: found %v, want %v", name, found, c.found)
		}
	}
	// An explicit filter by machine (context_search machine=...) finds the entry;
	// the viewer's access filter still decides.
	hits, err := st.SearchKnowledge("buildhost", scope.Axes{Machine: rmMachine}, 20, st.Access(rmPrincipals()["nora"]).Viewer())
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	pa := st.Access(rmPrincipals()["nora"])
	for _, k := range hits {
		if pa.CanSeeKnowledge(k) {
			seen = append(seen, k.Title)
		}
	}
	for _, title := range seen {
		if title == "bound to buildhost" {
			t.Errorf("the machine filter hands a project entry to a person without a role: %v", seen)
		}
	}
}

func mustKnowledgeFor(t *testing.T, st *Store, ax scope.Axes) []Knowledge {
	t.Helper()
	ks, err := st.KnowledgeForContext(ax)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// The counters of the home page and the knowledge window count a project entry
// with a machine like every project entry, by the role of the viewer.
func TestKnowledgeWindowCountsProjectEntriesWithAMachine(t *testing.T) {
	st := rmFixture(t)
	rmInsert(t, st, "bound to buildhost", "trusted", rmMia, "mia", scope.Axes{Project: roleProject, Machine: rmMachine})
	rmInsert(t, st, "only the host", "trusted", 6, "nora", scope.Axes{Machine: rmMachine})
	count := func(who string) []string {
		pa := st.Access(rmPrincipals()[who])
		ks, err := st.KnowledgeWindow(pa.RestrictKnowledgeWindow(KnowledgeWindow{Limit: 50}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, k := range ks {
			if pa.CanSeeKnowledge(k) {
				out = append(out, k.Title)
			}
		}
		return out
	}
	if got := count("mia"); len(got) != 1 || got[0] != "bound to buildhost" {
		t.Errorf("a member counts %v", got)
	}
	if got := count("nora"); len(got) != 0 {
		t.Errorf("the owner of the machine without a role counts %v", got)
	}
}

// An entry with a project is seen through the project; the machine share has no
// say. Writing it together with a share is refused, sharing it afterwards says
// "not shareable", and a share of the machine to an organization opens nothing.
func TestProjectEntryWithAMachineIsNotSharedThroughTheMachine(t *testing.T) {
	f := newShareFixture(t)
	st := f.st
	const project = "github.com/dw/app"
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:3", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	f.share(t, "build-1", ShareRead)
	both := scope.Axes{Project: project, Machine: "build-1"}
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "x", Body: "b", Person: "robin", AuthorID: 1, Scope: both, ShareOrgIDs: []int64{f.org.ID}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("project plus machine written together with a share: %v", err)
	}
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "deploy app on build-1", Body: "b", Person: "robin", AuthorID: 1, Scope: both})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ShareEntry(webRobin, EntryKnowledge, id, "dw"); !errors.Is(err, ErrEntryNotShareable) {
		t.Errorf("sharing an entry with a project through the machine: %v", err)
	}
	if orgs := st.EntryShareOrgs(webRobin, EntryKnowledge, id); len(orgs) != 0 {
		t.Errorf("shared with %v", orgs)
	}
	// mia has the project role: reads it. lena is a member of the org that the
	// machine is shared with, but has no role in the project: she does not.
	// The owner of the machine (robin) reads it as owner of the project org.
	for who, want := range map[string]bool{"mia": true, "lena": false, "gus": false} {
		p := map[string]Principal{"mia": webMia, "lena": webLena, "gus": webGus}[who]
		if got := f.sees(t, p, id); got != want {
			t.Errorf("%s reads the project entry with the machine: %v, want %v", who, got, want)
		}
	}
	// Machine-only knowledge is still shared through the machine share.
	only := f.entry(t, "build-1 users", 1, "robin", "build-1")
	if err := st.ShareEntry(webRobin, EntryKnowledge, only, "dw"); err != nil {
		t.Fatal(err)
	}
	if !f.sees(t, webLena, only) {
		t.Error("shared machine-only knowledge is not read by a member of the organization")
	}
	if f.sees(t, webGus, only) {
		t.Error("shared machine-only knowledge is read by a stranger")
	}
}

// The machine page lists the entries that name the machine, those with a project
// through the project role, those without as owner or through the share.
func TestMachineEntriesListOnlyWhatTheViewerMayRead(t *testing.T) {
	f := newShareFixture(t)
	st := f.st
	const project = "github.com/dw/app"
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:3", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	f.share(t, "build-1", ShareRead)
	proj, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "deploy app on build-1", Body: "b", Person: "robin", AuthorID: 1, Scope: scope.Axes{Project: project, Machine: "build-1"}})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "draft", Body: "b", Person: "robin", AuthorID: 1, Confidence: "staged", Scope: scope.Axes{Machine: "build-1"}})
	if err != nil {
		t.Fatal(err)
	}
	private := f.entry(t, "private host fact", 1, "robin", "build-1")
	shared := f.entry(t, "shared host fact", 1, "robin", "build-1")
	if err := st.ShareEntry(webRobin, EntryKnowledge, shared, "dw"); err != nil {
		t.Fatal(err)
	}
	other := f.entry(t, "another host", 1, "robin", "other-host")
	_ = staged
	_ = other
	ids := func(p Principal) map[int64]bool {
		es, err := st.Access(p).MachineEntries("build-1")
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, e := range es {
			if e.Kind == EntryKnowledge {
				out[e.ID] = true
			}
		}
		return out
	}
	// robin owns the machine: sees his own, the project entry as owner of the
	// project org, and his draft.
	if got := ids(webRobin); !got[proj] || !got[private] || !got[shared] || got[other] {
		t.Errorf("the owner's machine page: %v", got)
	}
	// mia: the project role gives her the project entry; the share gives her the
	// shared host fact; the private one stays out.
	if got := ids(webMia); !got[proj] || !got[shared] || got[private] {
		t.Errorf("mia's machine page: %v", got)
	}
	// lena: a member of the organization without a role in the project: the shared
	// fact only, never the project entry.
	if got := ids(webLena); got[proj] || !got[shared] || got[private] {
		t.Errorf("lena's machine page: %v", got)
	}
	if got := ids(webGus); len(got) != 0 {
		t.Errorf("a stranger's machine page: %v", got)
	}
}

// A runbook for a repository on a host (project and machine) is read through the
// project role; one without a project stays with the owner of the machine.
func TestRunbookWithProjectAndMachineFollowsTheProjectRole(t *testing.T) {
	st := rmFixture(t)
	both, err := st.WriteRunbook(RunbookWrite{Project: roleProject, Machine: rmMachine, Name: "deploy", Spec: rbSpec("deploy on buildhost"), Person: "mia", AuthorID: rmMia})
	if err != nil {
		t.Fatal(err)
	}
	only, err := st.WriteRunbook(RunbookWrite{Machine: rmMachine, Name: "host-check", Spec: rbSpec("check the host"), Person: "nora", AuthorID: 6})
	if err != nil {
		t.Fatal(err)
	}
	refOf := func(id int64) RunbookRef {
		ref, err := st.RunbookRefByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	who := rmPrincipals()
	for viewer, c := range map[string]struct{ both, only bool }{
		"mia": {true, false}, "rex": {true, false}, "lena": {true, false}, "robin": {true, false},
		"gus": {false, false}, "nora": {false, true},
	} {
		pa := st.Access(who[viewer])
		if got := pa.CanSeeRunbook(refOf(both.ID)); got != c.both {
			t.Errorf("%s reads the project runbook for a machine: %v, want %v", viewer, got, c.both)
		}
		if got := pa.CanSeeRunbook(refOf(only.ID)); got != c.only {
			t.Errorf("%s reads the machine-only runbook: %v, want %v", viewer, got, c.only)
		}
	}
	// Creating follows the role too: nora owns the machine and may not file one
	// for the project; a member may, without owning the machine.
	if err := st.Access(who["nora"]).CheckRunbookCreate(roleProject, rmMachine); err == nil {
		t.Error("the owner of the machine without a role files a project runbook")
	}
	if err := st.Access(who["mia"]).CheckRunbookCreate(roleProject, rmMachine); err != nil {
		t.Errorf("a member files a project runbook for a machine she does not own: %v", err)
	}
	// Sharing through the machine is for machine-only runbooks.
	if machine, _, err := st.entryMachine(EntryRunbook, both.ID); err != nil || machine != "" {
		t.Errorf("a runbook with a project counts as shareable through the machine: %q %v", machine, err)
	}
	if machine, _, err := st.entryMachine(EntryRunbook, only.ID); err != nil || machine != rmMachine {
		t.Errorf("a machine-only runbook is not shareable: %q %v", machine, err)
	}
}

// A snapshot is a copy of the project's knowledge: an entry with a machine is
// part of it, machine-only knowledge never is.
func TestSnapshotCaptureKeepsProjectEntriesWithAMachine(t *testing.T) {
	s, err := Open(t.TempDir() + "/capture.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const day = "'2026-01-01T00:00:00Z'"
	if _, err := s.DB().Exec(`INSERT INTO persons(id,name,token_hash,created_at) VALUES(7,'Robin','x',` + day + `);
		INSERT INTO knowledge(id,type,title,body,project,branch,machine,confidence,status,origin,person,created_at,updated_at) VALUES
		(1,'note','bound to a host','x','p','','host','trusted','active','human','Robin',` + day + `,` + day + `),
		(2,'note','only the host','x','','','host','trusted','active','human','Robin',` + day + `,` + day + `)`); err != nil {
		t.Fatal(err)
	}
	conn, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := captureContextEntries(context.Background(), conn, "p", snapshot.SchemaVersion, snapshot.DefaultLimits())
	conn.Close() // the database has one connection: the next query needs it
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the snapshot holds %d entries, want the one with a project", len(entries))
	}
	// Reading an old snapshot hides machine-only knowledge but not project
	// knowledge that names a machine.
	for payload, hidden := range map[string]bool{
		`{"project":"p","machine":"host"}`: false,
		`{"project":"","machine":"host"}`:  true,
		`{"project":"p","machine":""}`:     false,
	} {
		var n int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS payload, 'knowledge' AS domain) WHERE 1=1`+snapshotEntryVisible, payload).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 0) != hidden {
			t.Errorf("payload %s: hidden=%v, want %v", payload, n == 0, hidden)
		}
	}
}
