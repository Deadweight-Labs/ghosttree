package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeOpener replaces the three seams of ctx open: nothing real is started.
type fakeOpener struct {
	opened []string
	fail   error
}

func useOpener(t *testing.T, goos string, env map[string]string) *fakeOpener {
	t.Helper()
	f := &fakeOpener{}
	oldOS, oldEnv, oldOpen := openGOOS, openGetenv, openLink
	t.Cleanup(func() { openGOOS, openGetenv, openLink = oldOS, oldEnv, oldOpen })
	openGOOS = goos
	openGetenv = func(k string) string { return env[k] }
	openLink = func(u string) error { f.opened = append(f.opened, u); return f.fail }
	return f
}

func openFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	st := moveCLIFixture(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Kurzwege", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"erstes", "zweites"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Request.ID != 1 {
		t.Fatalf("request id %d", d.Request.ID)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st, cfg.ServerURL + "/ui/acme/ghosttree"
}

// REQ-603 AC 2: ctx open opens only where a browser can be opened.
func TestOpenOpensLocallyOnlyWithDisplayOrMacAndNeverOverSSH(t *testing.T) {
	_, base := openFixture(t)
	want := base + "/issues/1"
	cases := []struct {
		name string
		goos string
		env  map[string]string
		args []string
		open bool
		why  string
	}{
		{"linux with X11", "linux", map[string]string{"DISPLAY": ":0"}, nil, true, ""},
		{"linux with Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, nil, true, ""},
		{"linux without display", "linux", nil, nil, false, "no display"},
		{"macOS", "darwin", nil, nil, true, ""},
		{"ssh with display (forwarded)", "linux", map[string]string{"DISPLAY": "localhost:10", "SSH_CONNECTION": "1.2.3.4 5 6.7.8.9 22"}, nil, false, "ssh session"},
		{"ssh on macOS", "darwin", map[string]string{"SSH_CONNECTION": "x"}, nil, false, "ssh session"},
		{"windows", "windows", nil, nil, false, "no browser opener"},
		{"--print on a desktop", "linux", map[string]string{"DISPLAY": ":0"}, []string{"--print"}, false, "--print"},
	}
	for _, c := range cases {
		f := useOpener(t, c.goos, c.env)
		code, out := painTaskRun(t, append([]string{"open", "REQ-1"}, c.args...)...)
		if code != 0 || !strings.Contains(out, "link: "+want) || !strings.Contains(out, "Issue REQ-1: Kurzwege") {
			t.Errorf("%s: code %d, the link and card must always be printed:\n%s", c.name, code, out)
			continue
		}
		if c.open {
			if len(f.opened) != 1 || f.opened[0] != want || !strings.Contains(out, "opened in your browser") {
				t.Errorf("%s: want one open of %s, got %v\n%s", c.name, want, f.opened, out)
			}
		} else if len(f.opened) != 0 || !strings.Contains(out, "not opened ("+c.why) {
			t.Errorf("%s: must not open (%s), opened %v\n%s", c.name, c.why, f.opened, out)
		}
	}
	f := useOpener(t, "linux", map[string]string{"DISPLAY": ":0"})
	f.fail = errors.New("xdg-open: boom\nsecond line")
	code, out := painTaskRun(t, "open", "REQ-1")
	if code != 0 || !strings.Contains(out, "could not start the browser") || strings.Contains(out, "second line\n") && strings.Count(out, "\n") > 5 {
		t.Errorf("a failing opener must fall back to the link: %d\n%s", code, out)
	}
}

func TestOpenResolvesEveryShortFormToItsCanonicalURL(t *testing.T) {
	st, base := openFixture(t)
	root := strings.TrimSuffix(base, "/acme/ghosttree")
	if code, out := painTaskRun(t, "task", "add", "--project", cliHere, "--request", "REQ-1", "--kind", "action", "--title", "Handgriff"); code != 0 {
		t.Fatalf("task add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "add", "Qual", "--project", cliHere); code != 0 {
		t.Fatalf("pain add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "add", "Idee", "--project", cliHere); code != 0 {
		t.Fatalf("idea add: %d %s", code, out)
	}
	kid, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Merke", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	// Ein Arbeitsverzeichnis mit Remote, damit pr:, commit: und Pfade ein Projekt haben.
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "feat/x"}, {"remote", "add", "origin", "git@github.com:acme/ghosttree.git"},
		{"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
	useOpener(t, "linux", nil)

	cases := map[string]string{
		"REQ-1":                  base + "/issues/1",
		"REQ-1#2":                base + "/issues/1#criterion-2",
		"TASK-1":                 base + "/tasks/1",
		"PAIN-1":                 base + "/pains/1",
		"IDEA-1":                 base + "/ideas/1",
		"#" + itoa(kid):          base + "/knowledge/" + itoa(kid),
		"pr:155":                 base + "/pulls/155",
		"commit:2d950bf":         base + "/commit/2d950bf",
		"preset:pr-ergebnis 155": root + "/presets/repo/pr-ergebnis?pr=155&project=acme%2Fghosttree",
		"internal/store":         base + "/tree/feat/x/internal/store",
		"internal/store/a.go":    base + "/blob/feat/x/internal/store/a.go",
		"is:open label:bug":      base + "/issues?q=is%3Aopen+label%3Abug",
	}
	for ref, want := range cases {
		code, out := painTaskRun(t, append([]string{"open", "--print"}, strings.Fields(ref)...)...)
		if code != 0 || !strings.Contains(out, "link: "+want+"\n") {
			t.Errorf("ctx open %s: code %d, want link %s:\n%s", ref, code, want, out)
		}
	}
	// JSON: Karte und Zustand maschinenlesbar.
	code, out := painTaskRun(t, "open", "--print", "--json", "REQ-1")
	var got struct {
		OK     bool `json:"ok"`
		Opened bool `json:"opened"`
		Card   struct{ Kind, Ref, URL string }
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.OK || got.Opened || got.Card.URL != base+"/issues/1" {
		t.Errorf("json: %d %s", code, out)
	}
	// Unbekannt (und damit auch: nicht lesbar) gibt keinen Link und Exit 1.
	for _, ref := range []string{"REQ-999", "TASK-999", "PAIN-999", "IDEA-999", "#99999", "preset:gibtsnicht", "REQ-1#9"} {
		code, out := painTaskRun(t, "open", "--print", ref)
		if code != 1 || strings.Contains(out, "http") || !strings.Contains(out, "not found, or you may not read it") {
			t.Errorf("ctx open %s: want exit 1 without a link, got %d\n%s", ref, code, out)
		}
	}
	for _, ref := range []string{"../etc/passwd", "/etc/passwd"} {
		if code, out := painTaskRun(t, "open", "--print", ref); code == 0 || strings.Contains(out, "http") {
			t.Errorf("ctx open %s must be refused: %d %s", ref, code, out)
		}
	}
	if code, _ := painTaskRun(t, "open"); code != 2 {
		t.Errorf("bare ctx open: %d", code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// REQ-603 AC 3 am CLI: ein Task mit Schritten zeigt sie, die Karte und den Link;
// ohne Schritte kommt der freundliche Hinweis.
func TestTaskAddWithStepsPrintsCardAndWithoutStepsAHint(t *testing.T) {
	_, base := openFixture(t)
	code, out := painTaskRun(t, "task", "add", "--project", cliHere, "--request", "REQ-1", "--kind", "action", "--title", "GitHub verbinden",
		"--step", "Einstellungen öffnen | https://github.com/settings/apps", "--step", "App installieren")
	if code != 0 || !strings.Contains(out, "Task TASK-1: GitHub verbinden") || !strings.Contains(out, "human does: 1. Einstellungen öffnen") || !strings.Contains(out, "link: "+base+"/tasks/1") {
		t.Fatalf("add with steps: %d\n%s", code, out)
	}
	if strings.Contains(out, "hint:") {
		t.Errorf("no hint expected with steps:\n%s", out)
	}
	code, out = painTaskRun(t, "task", "show", "1")
	if code != 0 || !strings.Contains(out, "1. Einstellungen öffnen <https://github.com/settings/apps>") || !strings.Contains(out, "2. App installieren") || !strings.Contains(out, "link: "+base+"/tasks/1") {
		t.Errorf("show: %d\n%s", code, out)
	}
	code, out = painTaskRun(t, "task", "add", "--project", cliHere, "--request", "REQ-1", "--kind", "input", "--title", "Entscheiden")
	if code != 0 || !strings.Contains(out, "hint: no steps given") {
		t.Errorf("no steps: %d\n%s", code, out)
	}
	if code, out := painTaskRun(t, "task", "add", "--project", cliHere, "--request", "REQ-1", "--kind", "input", "--title", "x", "--step", "zwei\nZeilen"); code == 0 || !strings.Contains(out, "step 1") {
		t.Errorf("a step with a line break must be refused: %d %s", code, out)
	}
	code, out = painTaskRun(t, "task", "show", "1", "--json")
	if code != 0 || !strings.Contains(out, `"steps":[{"text":"Einstellungen öffnen"`) || !strings.Contains(out, `"card":`) {
		t.Errorf("json carries steps and card: %d %s", code, out)
	}
}

func TestCLIAnswersAboutOneObjectCarryTheCard(t *testing.T) {
	_, base := openFixture(t)
	code, out := painTaskRun(t, "request", "show", "1")
	if code != 0 || !strings.Contains(out, "Issue REQ-1: Kurzwege") || !strings.Contains(out, "link: "+base+"/issues/1") {
		t.Errorf("request show: %d %.300s", code, out)
	}
	code, out = painTaskRun(t, "pain", "add", "Qual", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "Pain PAIN-1: Qual") || !strings.Contains(out, "link: "+base+"/pains/1") {
		t.Errorf("pain add: %d %s", code, out)
	}
	code, out = painTaskRun(t, "idea", "add", "Idee", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "Idea IDEA-1: Idee") || !strings.Contains(out, "link: "+base+"/ideas/1") {
		t.Errorf("idea add: %d %s", code, out)
	}
	code, out = painTaskRun(t, "request", "create", "--type", "bug", "--title", "Neu", "--ac", "a", "--project", cliHere, "--json")
	if code != 0 || !strings.Contains(out, `"card":{"kind":"Issue","ref":"REQ-2"`) {
		t.Errorf("request create json: %d %s", code, out)
	}
}
