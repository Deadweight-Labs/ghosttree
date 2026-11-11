package mcpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// machineWorld: Alice besitzt build-1 (Geräteanmeldung), die Org dw hat Alice als
// Owner und Lena als Mitglied, Gus ist ein Fremder. Je ein MCP-Server für jeden.
type machineWorld struct {
	st               *store.Store
	alice, lena, gus *Server
}

func newMachineWorld(t *testing.T) machineWorld {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetAccessMode(store.AccessMode{Enforce: true})
	tokens := map[string]string{}
	for _, n := range []string{"alice", "lena", "gus"} {
		if tokens[n], err = st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "DW", "dw")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	device, _, err := st.CreateDeviceToken("person:1", "build-1")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	mk := func(token, machine string) *Server {
		return &Server{client: client.New(config.Config{ServerURL: srv.URL, Token: token, Machine: machine}),
			ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: machine}}
	}
	return machineWorld{st: st, alice: mk(device, "build-1"), lena: mk(tokens["lena"], "laptop-lena"), gus: mk(tokens["gus"], "laptop-gus")}
}

func (w machineWorld) shareMachine(t *testing.T, role string) {
	t.Helper()
	web := store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}
	if _, err := w.st.ShareMachine(web, "build-1", "dw", role, "", ""); err != nil {
		t.Fatal(err)
	}
}

// REQ-615 (MCP): der Agent liest Maschinen und gibt Einträge frei, aber die
// Maschine selbst gibt kein Werkzeug frei.
func TestMachineToolsReadAndShareEntries(t *testing.T) {
	w := newMachineWorld(t)
	// Ein Eintrag über die Maschine, zunächst privat.
	got := wireText(t, w.alice, "context_remember", map[string]any{"type": "note", "title": "Runner-Cache liegt unter /var/cache", "body": "cache entry", "scope_hint": "machine"})
	mustHave(t, "remember machine entry", got, "stored KN-1", "[note|machine:build-1]")
	if strings.Contains(wireText(t, w.lena, "context_search", map[string]any{"query": "Runner-Cache", "machine": "build-1"}), "Runner-Cache") {
		t.Fatal("a private machine entry reached a colleague")
	}

	// Ohne Freigabe der Maschine gibt es nichts zu teilen.
	if got, failed := callTool(t, connect(t, w.alice), "machine_share_entry", map[string]any{"kind": "knowledge", "id": 1, "org": "dw"}); !failed || !strings.Contains(got, "not shared") {
		t.Fatalf("share entry without machine share: failed=%v %s", failed, got)
	}
	w.shareMachine(t, "read")

	got = wireText(t, w.alice, "machine_get", map[string]any{"name": "build-1"})
	mustHave(t, "machine_get owner", got, "build-1 | owner | owner: alice", "shared with: dw (read)", "Machine build-1", "with: alice", "/ui/m/build-1")
	got = wireText(t, w.lena, "machine_get", map[string]any{})
	mustHave(t, "machine_get list of a member", got, "build-1 | shared-read | owner: alice")
	if got, failed := callTool(t, connect(t, w.gus), "machine_get", map[string]any{"name": "build-1"}); !failed || !strings.Contains(got, "not found") {
		t.Fatalf("a stranger read the machine: failed=%v %s", failed, got)
	}
	if got := wireText(t, w.gus, "machine_get", map[string]any{}); !strings.Contains(got, "no machines") {
		t.Fatalf("a stranger lists machines: %s", got)
	}

	// Ein Fremder und ein Mitglied geben nichts frei, der Besitzer schon.
	for name, srv := range map[string]*Server{"member": w.lena, "stranger": w.gus} {
		if _, failed := callTool(t, connect(t, srv), "machine_share_entry", map[string]any{"kind": "knowledge", "id": 1, "org": "dw"}); !failed {
			t.Errorf("a %s shared an entry", name)
		}
	}
	got = wireText(t, w.alice, "machine_share_entry", map[string]any{"kind": "knowledge", "id": 1, "org": "dw"})
	mustHave(t, "machine_share_entry", got, "knowledge KN-1 shared with dw", "now shared with: dw", "Knowledge KN-1: Runner-Cache liegt unter /var/cache")
	mustHave(t, "member search", wireText(t, w.lena, "context_search", map[string]any{"query": "Runner-Cache", "machine": "build-1"}), "Runner-Cache")
	if strings.Contains(wireText(t, w.gus, "context_search", map[string]any{"query": "Runner-Cache", "machine": "build-1"}), "Runner-Cache") {
		t.Fatal("a stranger finds a shared machine entry")
	}

	// Zurücknehmen.
	got = wireText(t, w.alice, "machine_share_entry", map[string]any{"kind": "knowledge", "id": 1, "org": "dw", "unshare": true})
	mustHave(t, "unshare", got, "taken back from dw", "nobody (private)")
	if strings.Contains(wireText(t, w.lena, "context_search", map[string]any{"query": "Runner-Cache", "machine": "build-1"}), "Runner-Cache") {
		t.Fatal("a member still finds an entry that was taken back")
	}
}

func TestRememberCanShareAMachineEntryFromTheStart(t *testing.T) {
	w := newMachineWorld(t)
	w.shareMachine(t, "read")
	if _, failed := callTool(t, connect(t, w.alice), "context_remember", map[string]any{"type": "note", "title": "t", "body": "b", "scope_hint": "project", "share_with_orgs": []string{"dw"}}); !failed {
		t.Fatal("share_with_orgs is only for a machine entry")
	}
	if got, failed := callTool(t, connect(t, w.alice), "context_remember", map[string]any{"type": "note", "title": "t", "body": "b", "scope_hint": "machine", "share_with_orgs": []string{"nowhere"}}); !failed {
		t.Fatalf("unknown org accepted: %s", got)
	}
	wireText(t, w.alice, "context_remember", map[string]any{"type": "note", "title": "Wartungsfenster der Runner", "body": "sonntags", "scope_hint": "machine", "share_with_orgs": []string{"dw"}})
	mustHave(t, "member search", wireText(t, w.lena, "context_search", map[string]any{"query": "Wartungsfenster", "machine": "build-1"}), "Wartungsfenster")
	if strings.Contains(wireText(t, w.gus, "context_search", map[string]any{"query": "Wartungsfenster", "machine": "build-1"}), "Wartungsfenster") {
		t.Fatal("a stranger finds a shared machine entry")
	}
	// Der Agent des Mitglieds darf mit Lese-Freigabe nicht auf die Maschine schreiben.
	if _, failed := callTool(t, connect(t, w.lena), "context_remember", map[string]any{"type": "note", "title": "x", "body": "y", "scope_hint": "machine", "machine": "build-1", "share_with_orgs": []string{"dw"}}); !failed {
		t.Fatal("a member wrote onto a read-only shared machine")
	}
}

// Decision #4349 over MCP: machine knowledge that belongs to a repository carries
// project AND machine; the machine page (machine_get) lists it for whoever may
// read it through the project, and never for a colleague who only reaches the
// machine through the organization's share.
func TestMachineGetListsProjectEntriesThroughTheProjectRole(t *testing.T) {
	w := newMachineWorld(t)
	const project = "github.com/x/y"
	if _, err := claimInOrg(w.st, "person:1", project); err != nil {
		t.Fatal(err)
	}
	w.shareMachine(t, store.ShareRead)

	got := wireText(t, w.alice, "context_remember", map[string]any{"type": "note", "title": "Deploy of y on build-1", "body": "run deploy.sh", "scope_hint": "project", "machine": "build-1"})
	mustHave(t, "remember project plus machine", got, "stored KN-1", "machine:build-1", project)
	got = wireText(t, w.alice, "context_remember", map[string]any{"type": "note", "title": "Runner users of build-1", "body": "users", "scope_hint": "machine"})
	mustHave(t, "remember machine only", got, "stored KN-2", "[note|machine:build-1]")
	if got, failed := callTool(t, connect(t, w.alice), "context_remember", map[string]any{"type": "note", "title": "bad", "body": "b", "scope_hint": "project", "machine": "build-1", "share_with_orgs": []string{"dw"}}); !failed {
		t.Errorf("an entry with a project was shared with an organization: %s", got)
	}

	// The owner sees both on the machine page, the project one with its project.
	got = wireText(t, w.alice, "machine_get", map[string]any{"name": "build-1"})
	mustHave(t, "owner machine page", got, "entries that name this machine", "knowledge/note KN-1 (project "+project+")", "knowledge/note KN-2 (no project)")

	// lena reaches the machine through the share, but has no role in the project:
	// the project entry is not on her page, and the private machine entry is not either.
	got = wireText(t, w.lena, "machine_get", map[string]any{"name": "build-1"})
	if strings.Contains(got, "#1") || strings.Contains(got, "KN-2") || strings.Contains(got, "Deploy of y") {
		t.Errorf("a colleague without a project role reads entries on the machine page: %s", got)
	}
	// With a role in the project, the entry is on her page (and only that one).
	if err := w.st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	got = wireText(t, w.lena, "machine_get", map[string]any{"name": "build-1"})
	mustHave(t, "member machine page", got, "knowledge/note KN-1 (project "+project+")")
	if strings.Contains(got, "KN-2") {
		t.Errorf("the private machine entry is on a colleague's page: %s", got)
	}
	// Her session of the project, on another machine, reads the entry.
	got = wireText(t, w.lena, "context_search", map[string]any{"query": "deploy.sh"})
	mustHave(t, "member search", got, "Deploy of y on build-1")
	// A stranger has no part in the machine.
	if got, failed := callTool(t, connect(t, w.gus), "machine_get", map[string]any{"name": "build-1"}); !failed || !strings.Contains(got, "not found") {
		t.Errorf("a stranger reads the machine page: %v %s", failed, got)
	}
}
