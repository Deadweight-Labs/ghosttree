package store

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// accessFixture: robin (person:1) ist Org-Owner und damit implizit Projekt-Owner,
// lena Lead, mia Member, rex Member mit can_review, gus Guest, nora ohne Org.
func accessFixture(t *testing.T) *Store {
	t.Helper()
	st := orgStore(t, "robin", "lena", "mia", "rex", "gus", "nora")
	if _, err := st.db.Exec(`UPDATE persons SET is_admin=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	for _, who := range []string{"person:2", "person:3", "person:4", "person:5"} {
		code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", roleProject); err != nil {
		t.Fatal(err)
	}
	for who, r := range map[string]struct {
		role   string
		review bool
	}{"person:2": {RoleLead, false}, "person:3": {RoleMember, false}, "person:4": {RoleMember, true}, "person:5": {RoleGuest, false}} {
		if err := st.SetProjectRole("person:1", roleProject, who, r.role, r.review, RoleViaAPI); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// Die Matrix aus Spec 8.1, Zeile für Zeile.
func TestMatrixPerRoleResourceAction(t *testing.T) {
	owner := RoleInfo{Role: RoleOwner}
	lead := RoleInfo{Role: RoleLead}
	member := RoleInfo{Role: RoleMember}
	reviewer := RoleInfo{Role: RoleMember, CanReview: true}
	guest := RoleInfo{Role: RoleGuest}
	none := RoleInfo{}
	own, other := Object{Own: true}, Object{}
	type row struct {
		name string
		role RoleInfo
		res  Resource
		act  Action
		obj  Object
		want bool
	}
	trusted, staged := Object{Confidence: "trusted"}, Object{Confidence: "staged"}
	rows := []row{
		// Wissen
		{"owner reads staged", owner, ResKnowledge, ActRead, staged, true},
		{"member reads staged", member, ResKnowledge, ActRead, staged, true},
		{"guest reads trusted", guest, ResKnowledge, ActRead, trusted, true},
		{"guest reads verified", guest, ResKnowledge, ActRead, Object{Confidence: "verified"}, true},
		{"guest does not read staged", guest, ResKnowledge, ActRead, staged, false},
		{"stranger does not read", none, ResKnowledge, ActRead, trusted, false},
		{"member creates", member, ResKnowledge, ActCreate, other, true},
		{"guest does not create", guest, ResKnowledge, ActCreate, other, false},
		{"member edits own", member, ResKnowledge, ActEdit, own, true},
		{"member does not edit foreign", member, ResKnowledge, ActEdit, other, false},
		{"lead edits foreign", lead, ResKnowledge, ActEdit, other, true},
		{"owner edits foreign", owner, ResKnowledge, ActEdit, other, true},
		{"guest does not edit own", guest, ResKnowledge, ActEdit, own, false},
		{"member does not verify", member, ResKnowledge, ActVerify, own, false},
		{"reviewer verifies", reviewer, ResKnowledge, ActVerify, other, true},
		{"lead verifies", lead, ResKnowledge, ActVerify, other, true},
		{"owner verifies", owner, ResKnowledge, ActVerify, other, true},
		{"guest with review flag does not verify", RoleInfo{Role: RoleGuest, CanReview: true}, ResKnowledge, ActVerify, other, false},
		// Aufträge
		{"guest reads requests", guest, ResRequest, ActRead, other, true},
		{"stranger does not read requests", none, ResRequest, ActRead, other, false},
		{"member creates request", member, ResRequest, ActCreate, other, true},
		{"guest does not create request", guest, ResRequest, ActCreate, other, false},
		{"member corrects own", member, ResRequest, ActEdit, own, true},
		{"member does not correct foreign", member, ResRequest, ActEdit, other, false},
		{"lead corrects foreign", lead, ResRequest, ActEdit, other, true},
		{"member works on any", member, ResRequest, ActWork, other, true},
		{"guest does not work", guest, ResRequest, ActWork, other, false},
		// Sessions
		{"member sees metadata", member, ResSessionMeta, ActRead, other, true},
		{"guest does not see metadata", guest, ResSessionMeta, ActRead, other, false},
		{"owner reads any transcript", owner, ResTranscript, ActRead, other, true},
		{"lead reads any transcript", lead, ResTranscript, ActRead, other, true},
		{"member reads own transcript", member, ResTranscript, ActRead, own, true},
		{"member does not read foreign transcript", member, ResTranscript, ActRead, other, false},
		{"member reads shared transcript", member, ResTranscript, ActRead, Object{Shared: true}, true},
		{"guest does not read shared transcript", guest, ResTranscript, ActRead, Object{Shared: true}, false},
		{"only the owner shares", lead, ResTranscript, ActShare, other, false},
		{"owner of the session shares", member, ResTranscript, ActShare, own, true},
		// Koordination, Agenten, Mitglieder, Dokumente
		{"guest reads the project room", guest, ResRoom, ActRead, other, true},
		{"guest writes to the project room", guest, ResRoom, ActCreate, other, true},
		{"stranger does not read the room", none, ResRoom, ActRead, other, false},
		{"member sees the project entry", member, ResProject, ActRead, other, true},
		{"no role does not see the project entry", RoleInfo{}, ResProject, ActRead, other, false},
		{"member sees agents", member, ResAgents, ActRead, other, true},
		{"guest does not see agents", guest, ResAgents, ActRead, other, false},
		{"guest reads members", guest, ResMembers, ActRead, other, true},
		{"stranger does not read members", none, ResMembers, ActRead, other, false},
		{"guest reads documents", guest, ResDocument, ActRead, other, true},
		{"guest does not write documents", guest, ResDocument, ActCreate, other, false},
		{"member writes own document", member, ResDocument, ActEdit, own, true},
		{"member does not write foreign document", member, ResDocument, ActEdit, other, false},
		{"lead writes foreign document", lead, ResDocument, ActEdit, other, true},
		{"guest reads ghosts", guest, ResGhost, ActRead, other, true},
		{"guest does not write ghosts", guest, ResGhost, ActCreate, other, false},
		{"member writes ghosts", member, ResGhost, ActEdit, other, true},
	}
	for _, r := range rows {
		if got := MatrixAllows(r.role, r.res, r.act, r.obj); got != r.want {
			t.Errorf("%s: got %v, want %v", r.name, got, r.want)
		}
	}
}

// AccountRoles ist die Sammelform von ProjectRole und muss mit ihr
// übereinstimmen, auch für den impliziten Org-Owner und Konten ohne Rolle.
func TestAccountRolesMatchesProjectRole(t *testing.T) {
	st := accessFixture(t)
	if _, err := st.EnsureProject("person:1", "github.com/dw/q"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:1", "person:2", "person:3", "person:4", "person:5", "person:6"} {
		roles, _ := st.AccountRoles(who)
		for _, project := range []string{roleProject, "github.com/dw/q", "github.com/none/such"} {
			want := st.ProjectRole(project, who)
			if got := roles[project]; got != want {
				t.Errorf("%s in %s: AccountRoles %+v, ProjectRole %+v", who, project, got, want)
			}
		}
	}
	if _, admin := st.AccountRoles("person:1"); !admin {
		t.Errorf("robin should be an instance admin")
	}
}

func TestProjectAccessLoadsRolesOnceAndNeedsNoQueryPerDecision(t *testing.T) {
	st := accessFixture(t)
	pa := st.Access(Principal{ID: "person:3", Label: "mia"})
	for i := 0; i < 500; i++ {
		pa.Decide(roleProject, ResKnowledge, ActRead, Object{Confidence: "trusted"})
		pa.Decide("github.com/dw/q", ResRequest, ActRead, Object{})
	}
	if n := pa.rolesLoadCount(); n != 1 {
		t.Fatalf("roles loaded %d times for 1000 decisions, want 1", n)
	}
}

func TestDecideStrangerIsHiddenAndMemberIsForbidden(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	stranger := st.Access(Principal{ID: "person:6", Label: "nora"})
	if err := stranger.Check(roleProject, ResKnowledge, ActRead, Object{Confidence: "trusted"}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger read: %v", err)
	}
	guest := st.Access(Principal{ID: "person:5", Label: "gus"})
	if err := guest.Check(roleProject, ResKnowledge, ActCreate, Object{}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest create: %v", err)
	}
	if err := guest.Check(roleProject, ResTranscript, ActRead, Object{}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("guest transcript is hidden, not forbidden: %v", err)
	}
	// Eine unbeanspruchte Remote darf angelegt, aber nicht gelesen werden.
	if err := stranger.Check("github.com/free/repo", ResKnowledge, ActCreate, Object{}); err != nil {
		t.Fatalf("create in unclaimed project: %v", err)
	}
	if err := stranger.Check("github.com/free/repo", ResKnowledge, ActRead, Object{}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("read in unclaimed project: %v", err)
	}
}

func TestMachineAxisKnowledgeIsOnlyForTheMachineOwner(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := st.ClaimMachine("mia-box", "person:3"); err != nil {
		t.Fatal(err)
	}
	obj := Object{Machine: "mia-box", Confidence: "trusted"}
	for who, want := range map[string]bool{"person:3": true, "person:1": false, "person:2": false, "person:4": false, "person:5": false, "person:6": false} {
		got := st.Access(Principal{ID: who}).Decide("", ResKnowledge, ActRead, obj).Allowed
		if got != want {
			t.Errorf("%s reads machine-axis knowledge: %v, want %v", who, got, want)
		}
	}
	// Eine unbekannte Maschine gehört, wie Altbestand, dem Instanz-Owner.
	if !st.Access(Principal{ID: "person:1"}).Decide("", ResKnowledge, ActRead, Object{Machine: "legacy-box"}).Allowed {
		t.Errorf("instance owner must read knowledge of an unclaimed machine")
	}
}

func TestLogModeAllowsButLogsWouldDenyRateLimitedWithoutContent(t *testing.T) {
	st := accessFixture(t)
	var buf bytes.Buffer
	st.SetAccessMode(AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil)), LogEvery: time.Hour})
	stranger := st.Access(Principal{ID: "person:6", Label: "nora"})
	for i := 0; i < 5; i++ {
		if err := stranger.Check(roleProject, ResKnowledge, ActEdit, Object{}); err != nil {
			t.Fatalf("log mode must not refuse: %v", err)
		}
	}
	if !stranger.Allow(roleProject, ResRequest, ActRead, Object{}) {
		t.Fatal("log mode must allow")
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (one per key, rate limited), got %d:\n%s", len(lines), buf.String())
	}
	for _, want := range []string{"access: would deny", "account=person:6", "project=github.com/dw/p", "resource=knowledge", "action=edit", `reason="not a member"`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line lacks %q: %s", want, lines[0])
		}
	}
	if would, denied := st.AccessCounters(); would != 6 || denied != 0 {
		t.Errorf("counters would=%d denied=%d, want 6 and 0", would, denied)
	}
}

func TestSessionShareIsOwnerOnlyAndGrantsMembers(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s-rex", AccountID: 4, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionShared(id, "person:3", true); !errors.Is(err, ErrNotSessionOwner) {
		t.Fatalf("non-owner share: %v", err)
	}
	sess, _ := st.SessionByID(id)
	mia := st.Access(Principal{ID: "person:3", Label: "mia"})
	if mia.CanSeeTranscript(sess) {
		t.Fatal("member reads an unshared foreign transcript")
	}
	if err := st.SetSessionShared(id, "person:4", true); err != nil {
		t.Fatal(err)
	}
	sess, _ = st.SessionByID(id)
	if !sess.Shared || !st.Access(Principal{ID: "person:3", Label: "mia"}).CanSeeTranscript(sess) {
		t.Fatal("member must read a shared transcript")
	}
	if st.Access(Principal{ID: "person:5", Label: "gus"}).CanSeeTranscript(sess) {
		t.Fatal("guest must never read transcripts")
	}
	if !st.Access(Principal{ID: "person:2", Label: "lena"}).CanSeeTranscript(sess) {
		t.Fatal("lead reads transcripts without a share")
	}
}

// Der Projektraum folgt der Projektrolle: ohne Rolle gibt es ihn nicht, der Gast
// liest und schreibt, Peers sieht er nicht. DMs bleiben Sache der Mitgliedschaft.
func TestProjectRoomFollowsProjectRole(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	for id, principal := range map[string]string{"claude:mia": "person:3", "claude:gus": "person:5", "claude:nora": "person:6"} {
		registerRoleAgent(t, st, id, principal, room, "member")
	}
	read := func(agent, principal string) error {
		_, err := st.CoordinationFor(Principal{ID: principal}, agent).Messages(DestinationRoom, room, 0, 10)
		return err
	}
	// Log-Modus: nichts wird verweigert.
	if err := read("claude:nora", "person:6"); err != nil {
		t.Fatalf("log mode refuses: %v", err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := read("claude:mia", "person:3"); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := read("claude:gus", "person:5"); err != nil {
		t.Fatalf("guest reads the room: %v", err)
	}
	if err := read("claude:nora", "person:6"); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("stranger reads the room: %v", err)
	}
	if _, err := st.CoordinationFor(Principal{ID: "person:5"}, "claude:gus").Peers(room, ""); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("guest sees peers: %v", err)
	}
	if _, err := st.CoordinationFor(Principal{ID: "person:3"}, "claude:mia").Peers(room, ""); err != nil {
		t.Fatalf("member peers: %v", err)
	}
}

// Prod-Szenario: ein einziges Konto, Org-Owner des Bestands, sieht und darf alles
// wie heute, auch mit Durchsetzung.
func TestSingleAccountOrgOwnerSeesEverythingEnforced(t *testing.T) {
	path := t.TempDir() + "/prod.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"github.com/dw/a", "github.com/dw/b"} {
		if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "n " + project, Body: "b", Scope: scope.Axes{Project: project}, Person: "robin", Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "machine", Body: "b", Scope: scope.Axes{Machine: "old-box"}, Person: "robin", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "old", Scope: scope.Axes{Project: "github.com/dw/b"}})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path) // das Backfill ordnet alle Projekte der Default-Org zu
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetAccessMode(AccessMode{Enforce: true})
	pa := st.Access(Principal{ID: "person:1", Label: "robin"})
	for _, project := range []string{"github.com/dw/a", "github.com/dw/b"} {
		if role := pa.Role(project); role.Role != RoleOwner {
			t.Fatalf("%s: role %+v", project, role)
		}
		for _, c := range []struct {
			res Resource
			act Action
		}{{ResKnowledge, ActRead}, {ResKnowledge, ActEdit}, {ResKnowledge, ActVerify}, {ResRequest, ActEdit}, {ResDocument, ActEdit}, {ResGhost, ActEdit}, {ResSessionMeta, ActRead}, {ResTranscript, ActRead}, {ResAgents, ActRead}, {ResRoom, ActRead}} {
			if err := pa.Check(project, c.res, c.act, Object{}); err != nil {
				t.Errorf("%s %s %s: %v", project, c.res, c.act, err)
			}
		}
	}
	sess, _ := st.SessionByID(legacy)
	if !pa.CanSeeTranscript(sess) {
		t.Error("legacy session of the instance owner")
	}
	ks, _ := st.KnowledgeForProject("")
	for _, k := range ks {
		if !pa.CanSeeKnowledge(k) {
			t.Errorf("knowledge %q hidden", k.Title)
		}
	}
	if would, denied := st.AccessCounters(); denied != 0 || would != 0 {
		t.Errorf("single owner was denied: would=%d denied=%d", would, denied)
	}
}

// Messung für den heißen Pfad: ein Bootstrap filtert so viele Einträge, wie der
// Kontext liefert. Der Lauf zeigt die Kosten je Anfrage (einmal Rollen laden,
// danach Map-Zugriffe) gegen die reine Schleife ohne Prüfung.
func benchEntries(n int) []Knowledge {
	ks := make([]Knowledge, n)
	for i := range ks {
		ks[i] = Knowledge{ID: int64(i), Type: "pitfall", Confidence: "trusted", Person: "mia"}
		switch i % 10 {
		case 0:
			ks[i].Scope.Project = "github.com/dw/q"
		case 1:
			ks[i].Scope.Machine = "mia-box"
		case 2:
		default:
			ks[i].Scope.Project = roleProject
		}
	}
	return ks
}

func BenchmarkBootstrapFilterWithoutAccess(b *testing.B) {
	ks := benchEntries(2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, k := range ks {
			if k.Confidence != "" {
				n++
			}
		}
		_ = n
	}
}

func BenchmarkBootstrapFilterEnforced2000Entries(b *testing.B) {
	t := &testing.T{}
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	ks := benchEntries(2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pa := st.Access(Principal{ID: "person:3", Label: "mia"}) // je Anfrage neu
		n := 0
		for _, k := range ks {
			if pa.CanSeeKnowledge(k) {
				n++
			}
		}
		_ = n
	}
}

func BenchmarkLoadRolesPerRequest(b *testing.B) {
	t := &testing.T{}
	st := accessFixture(t)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.Access(Principal{ID: "person:3"}).Role(roleProject)
	}
}

// ProjectWriters ist dieselbe Ableitung wie ProjectRole: member oder höher.
func TestProjectWritersMatchProjectRole(t *testing.T) {
	st := accessFixture(t)
	writers := st.ProjectWriters(roleProject)
	for id, name := range map[string]string{"person:1": "robin", "person:2": "lena", "person:3": "mia", "person:4": "rex", "person:5": "gus", "person:6": "nora"} {
		want := RoleRank(st.ProjectRole(roleProject, id).Role) >= 2
		if writers[name] != want {
			t.Errorf("%s: writers=%v, role says %v", name, writers[name], want)
		}
	}
}

func visibleRemotes(st *Store, who string) []string {
	all := []Project{}
	for _, org := range []string{"alpha", "beta"} {
		o, err := st.OrgByRef(org)
		if err != nil {
			continue
		}
		ps, _ := st.ListProjects(who, o.ID)
		all = append(all, ps...)
	}
	out := []string{}
	for _, p := range st.Access(Principal{ID: who}).VisibleProjects(all) {
		out = append(out, p.Remote)
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

// VisibleProjects ohne Instanz-Admin: Org-Owner sieht seine Org, nicht fremde;
// Gast nur sein Projekt; ohne Rolle oder Mitgliedschaft nichts.
func TestVisibleProjectsByRoleEnforced(t *testing.T) {
	st := accessFixture(t) // alpha: roleProject; person:6 (nora) ohne Org
	const alpha2, beta1 = "github.com/dw/alpha2", "github.com/dw/beta1"
	if _, err := st.EnsureProject("person:1", alpha2); err != nil {
		t.Fatal(err)
	}
	// nora (person:6) ist Owner von beta und nur Member in alpha.
	mustOrg(t, st, "person:6", "Beta", "beta")
	if _, err := st.ClaimProject("person:6", beta1, "beta"); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", 1, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:6", code); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})

	if got := visibleRemotes(st, "person:6"); !sameSet(got, beta1) {
		t.Fatalf("org owner of beta, only member of alpha: got %v", got)
	}
	if got := visibleRemotes(st, "person:5"); !sameSet(got, roleProject) {
		t.Fatalf("guest sees only its project: got %v", got)
	}

	if err := st.RemoveProjectRole("person:1", roleProject, "person:5", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if got := visibleRemotes(st, "person:5"); len(got) != 0 {
		t.Fatalf("after role removal: got %v", got)
	}

	// Zeile bleibt zurück, Org-Mitgliedschaft ist weg.
	if err := st.SetProjectRole("person:1", roleProject, "person:3", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM org_members WHERE org_id=1 AND account_id=3`); err != nil {
		t.Fatal(err)
	}
	if got := visibleRemotes(st, "person:3"); len(got) != 0 {
		t.Fatalf("left-over project_members row without org membership: got %v", got)
	}
	// Auch wenn die Liste das Projekt trotzdem enthielte, filtert die Rolle.
	if p, ok := st.ProjectByRemote(roleProject); !ok || len(st.Access(Principal{ID: "person:3"}).VisibleProjects([]Project{p})) != 0 {
		t.Fatal("left-over row must not grant visibility")
	}
}
