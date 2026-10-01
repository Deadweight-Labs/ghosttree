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
