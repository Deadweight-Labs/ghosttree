package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type ownershipFixture struct {
	srv                                *httptest.Server
	st                                 *store.Store
	robinLegacy, annaBound, annaLegacy string
}

// Zwei Konten: robin (person:1, Legacy-Token wie der Produktions-Collector) und
// anna (person:2) mit einem an "annabox" gebundenen Token und einem Legacy-Token.
func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := ownershipFixture{st: st}
	if f.robinLegacy, err = st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if f.annaLegacy, err = st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	if f.annaBound, _, err = st.CreateToken("anna", store.TokenSpec{Label: "box", Machine: "annabox"}); err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(New(st))
	t.Cleanup(f.srv.Close)
	return f
}

func (f ownershipFixture) post(t *testing.T, path, token string, body any) (int, map[string]any) {
	t.Helper()
	resp := req(t, "POST", f.srv.URL+path, token, body)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func session(machine, ext string) map[string]any {
	return map[string]any{"harness": "claude-code", "external_id": ext, "scope": map[string]any{"machine": machine, "project": "github.com/x/y"}}
}

func TestMachineBoundTokenWritesOnlyUnderItsMachine(t *testing.T) {
	f := newOwnershipFixture(t)
	if code, _ := f.post(t, "/api/sessions", f.annaBound, session("annabox", "s1")); code != 200 {
		t.Fatalf("own machine: %d", code)
	}
	code, body := f.post(t, "/api/sessions", f.annaBound, session("mainex", "s2"))
	if code != 403 || body["code"] != "machine_bound" {
		t.Fatalf("foreign machine with bound token: %d %v", code, body)
	}
	if code, _ := f.post(t, "/api/knowledge", f.annaBound, map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"machine": "mainex"}}); code != 403 {
		t.Fatalf("knowledge under another machine: %d", code)
	}
}

func TestForeignMachineNameIsTakenForUnboundToken(t *testing.T) {
	f := newOwnershipFixture(t)
	if code, _ := f.post(t, "/api/sessions", f.robinLegacy, session("mainex", "r1")); code != 200 {
		t.Fatalf("robin claims mainex: %d", code)
	}
	code, body := f.post(t, "/api/sessions", f.annaLegacy, session("mainex", "a1"))
	if code != 409 || body["code"] != "machine_name_taken" {
		t.Fatalf("anna on robin's machine: %d %v", code, body)
	}
	// annabox gehört anna seit der Bindung ihres Tokens; robin kann sie nicht nutzen.
	code, body = f.post(t, "/api/sessions", f.robinLegacy, session("annabox", "r2"))
	if code != 409 || body["code"] != "machine_name_taken" {
		t.Fatalf("robin on anna's machine: %d %v", code, body)
	}
	resp := req(t, "GET", f.srv.URL+"/api/machines", f.robinLegacy, nil)
	defer resp.Body.Close()
	var machines []store.Machine
	_ = json.NewDecoder(resp.Body).Decode(&machines)
	owners := map[string]string{}
	for _, m := range machines {
		owners[m.Name] = m.Owner
	}
	if owners["mainex"] != "robin" || owners["annabox"] != "anna" {
		t.Fatalf("owners = %v", owners)
	}
}

func TestSessionCollisionAcrossAccountsIsConflictAndSameAccountIsIdempotent(t *testing.T) {
	f := newOwnershipFixture(t)
	_, first := f.post(t, "/api/sessions", f.annaBound, session("annabox", "same"))
	_, again := f.post(t, "/api/sessions", f.annaBound, session("annabox", "same"))
	if first["id"] != again["id"] || first["id"] == nil {
		t.Fatalf("re-upload not idempotent: %v %v", first, again)
	}
	code, body := f.post(t, "/api/sessions", f.robinLegacy, session("mainex", "same"))
	if code != 409 || body["code"] != "session_id_collision" {
		t.Fatalf("cross-account collision: %d %v", code, body)
	}
	// Dasselbe Konto, andere Maschine: ebenfalls Kollision, nichts wandert still.
	f.post(t, "/api/sessions", f.robinLegacy, session("mainex", "mine"))
	if code, body = f.post(t, "/api/sessions", f.robinLegacy, session("robinlap", "mine")); code != 409 || body["code"] != "session_id_collision" {
		t.Fatalf("same account, other machine: %d %v", code, body)
	}
	// Die Session ist nicht überschrieben worden.
	resp := req(t, "GET", f.srv.URL+"/api/sessions?owner=me", f.annaBound, nil)
	defer resp.Body.Close()
	var list []store.Session
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 1 || list[0].Owner != "anna" || list[0].Scope.Machine != "annabox" {
		t.Fatalf("anna's sessions = %+v", list)
	}
}

func TestChunksRequireSessionOwner(t *testing.T) {
	f := newOwnershipFixture(t)
	_, s := f.post(t, "/api/sessions", f.robinLegacy, session("mainex", "robins"))
	path := "/api/sessions/" + jsonNumber(s["id"]) + "/chunks"
	chunk := map[string]any{"chunks": []map[string]any{{"seq": 0, "role": "user", "text": "hi", "raw": "{}"}}}
	if code, _ := f.post(t, path, f.annaBound, chunk); code != 403 {
		t.Fatalf("foreign chunks: %d", code)
	}
	if code, _ := f.post(t, path, f.robinLegacy, chunk); code != 204 {
		t.Fatalf("own chunks: %d", code)
	}
}

func TestAgentIdentityCannotBeTakenOverByAnotherAccount(t *testing.T) {
	f := newOwnershipFixture(t)
	reg := map[string]any{"external_id": "claude:abc", "provider": "claude", "room_key": "project:github.com/x/y", "display_name": "a"}
	if code, _ := f.post(t, "/api/coord/agents", f.robinLegacy, reg); code != 200 {
		t.Fatalf("register: %d", code)
	}
	if code, _ := f.post(t, "/api/coord/agents", f.annaLegacy, reg); code != 403 {
		t.Fatalf("takeover: %d", code)
	}
	peers, err := f.st.CoordPeers("project:github.com/x/y", "")
	if err != nil || len(peers) != 1 || peers[0].Owner != "robin" {
		t.Fatalf("peers = %+v %v", peers, err)
	}
	// cli:<machine> steht unter der Maschinenregel des Tokens.
	cli := map[string]any{"external_id": "cli:mainex", "provider": "ctx-cli", "room_key": "machine:mainex", "display_name": "c"}
	if code, _ := f.post(t, "/api/coord/agents", f.annaBound, cli); code != 403 {
		t.Fatalf("bound token registering another machine's cli identity: %d", code)
	}
}

// Der Produktions-Collector: Legacy-Token von person:1, mehrfache Läufe über
// dieselben Transkripte, vorhandene Maschine aus dem Altbestand.
func TestLegacyCollectorUnchanged(t *testing.T) {
	f := newOwnershipFixture(t)
	ids := map[string]any{}
	for run := 0; run < 3; run++ {
		for _, ext := range []string{"u1", "u2"} {
			code, body := f.post(t, "/api/sessions", f.robinLegacy, session("mainex", ext))
			if code != 200 {
				t.Fatalf("run %d %s: %d %v", run, ext, code, body)
			}
			if prev, ok := ids[ext]; ok && prev != body["id"] {
				t.Fatalf("%s got a new row", ext)
			}
			ids[ext] = body["id"]
		}
	}
}

func jsonNumber(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestDeviceLoginOnTakenMachineNameIs409(t *testing.T) {
	f := newOwnershipFixture(t)
	f.post(t, "/api/sessions", f.robinLegacy, session("mainex", "r1"))
	_, start := f.post(t, "/api/auth/device", "", map[string]string{"machine": "mainex"})
	if err := f.st.Device().Decide(start["user_code"].(string), "person:2", true); err != nil {
		t.Fatal(err)
	}
	// Der Poll-Abstand gilt auch hier; ohne Uhr-Eingriff ist er nicht erreicht.
	clock := time.Now().Add(time.Minute)
	f.st.Device().SetClock(func() time.Time { return clock })
	code, body := f.post(t, "/api/auth/device/token", "", map[string]string{"device_code": start["device_code"].(string)})
	if code != 409 || body["error"] != "machine_name_taken" {
		t.Fatalf("device token on taken machine: %d %v", code, body)
	}
}
