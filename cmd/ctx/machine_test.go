package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMachineReleaseAndTransfer(t *testing.T) {
	db := filepath.Join(t.TempDir(), "m.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.AddPerson("robin")
	st.AddPerson("anna")
	st.ClaimMachine("box", "person:2")
	st.Close()
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := cmdMachine(args, &out)
		return code, out.String()
	}
	if code, out := run("transfer", "box", "robin", "--db", db); code != 0 || !strings.Contains(out, "robin") {
		t.Fatalf("transfer: %d %s", code, out)
	}
	if _, out := run("list", "--db", db); !strings.Contains(out, "box\trobin") {
		t.Fatalf("list: %s", out)
	}
	if code, out := run("release", "box", "--db", db); code != 0 {
		t.Fatalf("release: %d %s", code, out)
	}
	if code, _ := run("release", "box", "--db", db); code == 0 {
		t.Fatal("second release must fail")
	}
}

// REQ-615: der Betreiber gibt eine Maschine mit --db an eine Org frei, überträgt
// sie an eine Org und nimmt beides zurück; ohne --db geht es nicht, und ohne
// Nachweis der Maschine auch nicht.
func TestMachineShareOperatorCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "m.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.AddPerson("robin")
	if _, err := st.CreateOrg("person:1", "DW", "dw"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateDeviceToken("person:1", "build-1"); err != nil {
		t.Fatal(err)
	}
	st.ClaimMachine("squat-box", "person:1")
	st.Close()
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := cmdMachine(args, &out)
		return code, out.String()
	}
	if code, out := run("share", "build-1", "dw"); code != 2 || !strings.Contains(out, "--db") {
		t.Fatalf("share without --db: %d %s", code, out)
	}
	if code, out := run("share", "squat-box", "dw", "--db", db); code != 1 || !strings.Contains(out, "not proven") {
		t.Fatalf("share of an unproven machine: %d %s", code, out)
	}
	if code, out := run("share", "build-1", "dw", "--role", "boss", "--db", db); code != 1 {
		t.Fatalf("bad role: %d %s", code, out)
	}
	if code, out := run("share", "build-1", "dw", "--role", "write", "--user", "ci", "--path", "/opt/ci", "--db", db); code != 0 || !strings.Contains(out, "shared with dw (write)") {
		t.Fatalf("share: %d %s", code, out)
	}
	st, _ = store.Open(db)
	m, err := st.MachineFor("person:1", "build-1")
	st.Close()
	if err != nil || len(m.Shares) != 1 || m.Shares[0].OSUser != "ci" || len(m.Events) != 1 || m.Events[0].Via != store.MachineViaCLIDB {
		t.Fatalf("machine = %+v %v", m, err)
	}
	if code, out := run("unshare", "build-1", "dw", "--db", db); code != 0 || !strings.Contains(out, "private again") {
		t.Fatalf("unshare: %d %s", code, out)
	}
	if code, _ := run("unshare", "build-1", "dw", "--db", db); code == 0 {
		t.Fatal("second unshare must fail")
	}
	if code, out := run("owner-org", "build-1", "dw", "--db", db); code != 0 || !strings.Contains(out, "belongs to organization dw") {
		t.Fatalf("owner-org: %d %s", code, out)
	}
	if _, out := run("list", "--db", db); !strings.Contains(out, "build-1\trobin") || !strings.Contains(out, "org dw") {
		t.Fatalf("list: %s", out)
	}
	// transfer beendet die Org-Zugehörigkeit
	if code, out := run("transfer", "build-1", "robin", "--db", db); code != 0 {
		t.Fatalf("transfer: %d %s", code, out)
	}
	if _, out := run("list", "--db", db); strings.Contains(out, "org dw") {
		t.Fatalf("transfer kept the org: %s", out)
	}
}
