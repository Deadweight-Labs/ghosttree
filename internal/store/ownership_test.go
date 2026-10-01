package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Eine alte Datenbank hat weder machines.account_id noch sessions.account_id,
// und ihre Agenten haben keinen principal_id. Der Open-Schritt ergänzt das,
// ohne sessions umzuschreiben, und bleibt bei einem zweiten Öffnen wirkungslos.
func TestOwnershipMigrationOnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"robin", "philipp"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "old", Scope: scope.Axes{Machine: "mainex"}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE sessions DROP COLUMN account_id`,
		`ALTER TABLE machines DROP COLUMN account_id`,
		`INSERT INTO machines(hostname,first_seen,last_seen) VALUES('mainex','a','b'),('philipp-box','a','b')`,
		`INSERT INTO coord_agents(external_id,provider,room_key,display_name,person,principal_id,registered_at,last_seen_at)
			VALUES('claude:1','claude','project:x','a','philipp','','a','b'),('claude:2','claude','project:x','b',NULL,'','a','b'),('claude:3','claude','project:x','c','robin','person:1','a','b')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	for pass := 0; pass < 2; pass++ {
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		machines, err := st.ListMachines("")
		if err != nil || len(machines) != 2 {
			t.Fatalf("machines = %+v %v", machines, err)
		}
		for _, m := range machines {
			if m.Owner != "robin" {
				t.Fatalf("machine %s owner %q", m.Name, m.Owner)
			}
		}
		sessions, err := st.ListSessions(scope.Axes{}, 10)
		if err != nil || len(sessions) != 1 || sessions[0].Owner != "robin" {
			t.Fatalf("legacy session must default to person:1: %+v %v", sessions, err)
		}
		var stored int64
		if err := st.db.QueryRow(`SELECT account_id FROM sessions`).Scan(&stored); err != nil || stored != 0 {
			t.Fatalf("sessions must not be rewritten: %d %v", stored, err)
		}
		want := map[string]string{"claude:1": "person:2", "claude:2": "person:1", "claude:3": "person:1"}
		for ext, principal := range want {
			if got, _, _ := st.CoordAgentOwner(ext); got != principal {
				t.Fatalf("%s owner = %q, want %q", ext, got, principal)
			}
		}
		st.Close()
	}
}

func TestMachineClaimAndSessionOwnership(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddPerson("robin")
	st.AddPerson("anna")
	if err := st.ClaimMachine("Box", "person:2"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimMachine("box", "person:2"); err != nil {
		t.Fatalf("own claim must be idempotent: %v", err)
	}
	if err := st.ClaimMachine("box", "person:1"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("foreign claim: %v", err)
	}
	if _, _, err := st.CreateToken("robin", TokenSpec{Machine: "BOX"}); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("token bound to a foreign machine: %v", err)
	}
	if _, _, err := st.CreateDeviceToken("person:1", "box"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("device token on a foreign machine: %v", err)
	}
	if _, info, err := st.CreateDeviceToken("person:2", "Box"); err != nil || info.Machine != "box" {
		t.Fatalf("own device token: %+v %v", info, err)
	}

	s := Session{Harness: "codex", ExternalID: "x", Scope: scope.Axes{Machine: "box"}, AccountID: 2}
	id, err := st.UpsertSession(s)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := st.UpsertSession(s); err != nil || again != id {
		t.Fatalf("same account and machine: %d %v", again, err)
	}
	s.AccountID = 1
	if _, err := st.UpsertSession(s); !errors.Is(err, ErrSessionCollision) {
		t.Fatalf("other account: %v", err)
	}
	s.AccountID, s.Scope.Machine = 2, "other"
	if _, err := st.UpsertSession(s); !errors.Is(err, ErrSessionCollision) {
		t.Fatalf("other machine: %v", err)
	}
	got, err := st.SessionByID(id)
	if err != nil || got.Owner != "anna" || got.Scope.Machine != "box" {
		t.Fatalf("session = %+v %v", got, err)
	}
}

// Der Claim darf an der PK-Verletzung nicht als Serverfehler enden: liest er
// "frei", während ein anderer Prozess den Namen einträgt, ist das ein 409.
type staleReadTx struct {
	txExec
	missed bool
}

func (s *staleReadTx) QueryRow(q string, args ...any) *sql.Row {
	if !s.missed {
		s.missed = true
		return s.txExec.QueryRow(`SELECT 0, '' WHERE 0`)
	}
	return s.txExec.QueryRow(q, args...)
}

func TestClaimRaceOnPrimaryKeyIsMachineTaken(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddPerson("robin")
	st.AddPerson("anna")
	if err := st.ClaimMachine("box", "person:2"); err != nil {
		t.Fatal(err)
	}
	tx, _ := st.db.Begin()
	defer tx.Rollback()
	if err := claimMachineTx(&staleReadTx{txExec: tx}, "box", 1); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("race with foreign owner: %v", err)
	}
	if err := claimMachineTx(&staleReadTx{txExec: tx}, "box", 2); err != nil {
		t.Fatalf("race with own claim: %v", err)
	}
}

func TestSessionCollisionEmptyVersusSetMachine(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddPerson("robin")
	// Leer gespeichert: eine Maschine darf nachgetragen werden.
	id, err := st.UpsertSession(Session{Harness: "codex", ExternalID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := st.UpsertSession(Session{Harness: "codex", ExternalID: "a", Scope: scope.Axes{Machine: "box"}}); err != nil || again != id {
		t.Fatalf("empty -> set: %d %v", again, err)
	}
	// Gesetzt gespeichert: leer ist eine andere Maschine und kollidiert.
	if _, err := st.UpsertSession(Session{Harness: "codex", ExternalID: "a"}); !errors.Is(err, ErrSessionCollision) {
		t.Fatalf("set -> empty: %v", err)
	}
}

func TestReleaseAndTransferMachine(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddPerson("robin")
	st.AddPerson("anna")
	if err := st.ClaimMachine("squat", "person:2"); err != nil {
		t.Fatal(err)
	}
	if err := st.TransferMachine("Squat", "robin"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimMachine("squat", "person:2"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("after transfer: %v", err)
	}
	if err := st.ReleaseMachine("squat"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseMachine("squat"); err == nil {
		t.Fatal("releasing an unknown machine must fail")
	}
	if err := st.ClaimMachine("squat", "person:2"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if err := st.TransferMachine("squat", "nobody"); err == nil {
		t.Fatal("transfer to an unknown account must fail")
	}
}
