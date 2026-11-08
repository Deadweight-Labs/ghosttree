package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

// anchorRepo legt ein Git-Verzeichnis mit einem Commit an und wechselt hinein;
// geliefert wird der volle HEAD-Hash.
func anchorRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	t.Chdir(dir)
	return run("rev-parse", "HEAD")
}

// ctx-Befehle, die in den Ledger schreiben, tragen den Git-Stand des
// Arbeitsverzeichnisses mit (REQ-597, Kriterium 9): Request anlegen, Wissen
// hinzufügen und Pain anlegen aus dem Terminal.
func TestLedgerWritingCLICommandsCarryTheAnchorOfTheWorkingDirectory(t *testing.T) {
	st := moveCLIFixture(t)
	head := anchorRepo(t)
	exec := func(args ...string) {
		t.Helper()
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, out.String())
		}
	}
	exec("request", "create", "--type", "feature", "--title", "Aus dem Terminal", "--project", cliHere, "--ac", "c")
	exec("knowledge", "add", "--type", "note", "--title", "Wissen vom Terminal", "--body", "b", "--scope", "project", "--project", cliHere)
	exec("pain", "add", "Es hakt im Terminal", "--project", cliHere)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := client.New(cfg)

	d, err := c.GetRequest(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Activity) == 0 || d.Activity[0].GitAnchor == nil || d.Activity[0].GitAnchor.Commit != head || d.Activity[0].GitAnchor.Branch != "main" {
		t.Errorf("request created from the terminal: %+v", d.Activity)
	}
	ks, err := st.KnowledgeForProject(cliHere)
	if err != nil || len(ks) != 1 || ks[0].GitAnchor == nil || ks[0].GitAnchor.Commit != head {
		t.Errorf("knowledge added from the terminal: %+v %v", ks, err)
	}
	p, err := c.GetPain(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) == 0 || p.Events[0].GitAnchor == nil || p.Events[0].GitAnchor.Commit != head {
		t.Errorf("pain added from the terminal: %+v", p.Events)
	}
}

// Außerhalb eines Git-Verzeichnisses bleibt der Anker leer, und der Befehl läuft.
func TestLedgerWritingCLICommandLeavesTheAnchorEmptyWithoutGit(t *testing.T) {
	moveCLIFixture(t)
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if code := run([]string{"request", "create", "--type", "feature", "--title", "Ohne Git", "--project", cliHere, "--ac", "c"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	cfg, _ := config.Load()
	d, err := client.New(cfg).GetRequest(1)
	if err != nil || len(d.Activity) == 0 || d.Activity[0].GitAnchor != nil {
		t.Fatalf("activity %+v %v", d.Activity, err)
	}
}
