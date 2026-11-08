package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestIdeaWorkshopCLI(t *testing.T) {
	st := moveCLIFixture(t)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if code, out := painTaskRun(t, "idea", "add", "Anmeldung per OAuth planen", "--project", cliHere); code != 0 || !strings.Contains(out, "IDEA-1") {
		t.Fatalf("add: %d %s", code, out)
	}
	raw := "  Wörtlich\r\nso gesagt  \n"
	if code, out := painTaskRun(t, "idea", "raw", "IDEA-1", "--file", write("raw.txt", []byte(raw)), "--source", "Chat"); code != 0 || !strings.Contains(out, "raw r1 of IDEA-1") {
		t.Fatalf("raw: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "raw", "1"); code != 0 || out != raw {
		t.Fatalf("raw read back: %d %q", code, out)
	}
	if code, out := painTaskRun(t, "idea", "raw", "1", "--file", write("same.txt", []byte(raw))); code != 0 || !strings.Contains(out, "unchanged") {
		t.Fatalf("same raw text: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "raw", "1", "--file", dir); code == 0 {
		t.Fatalf("a directory as text: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "summary", "1", "--file", write("sum.txt", []byte("Kurz: Anmeldung ohne Passwort."))); code != 0 {
		t.Fatalf("summary: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "concept", "1", "--about", "Anmeldung", "--requirement", "Anmelden ohne Passwort", "--requirement", "Rechtetest", "--message", "Entwurf"); code != 0 || !strings.Contains(out, "concept r1") {
		t.Fatalf("concept: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "concept", "1"); code != 0 || !strings.Contains(out, "Anforderung 2: Rechtetest") {
		t.Fatalf("concept read: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "discuss", "1"); code != 0 || !strings.Contains(out, "discussion") {
		t.Fatalf("discuss: %d %s", code, out)
	}
	// Ein Token ist kein Mensch: Freigeben und Erlassen gehören in die Weboberfläche.
	if code, out := painTaskRun(t, "idea", "phase", "1", "approved"); code == 0 || !strings.Contains(out, "person") {
		t.Fatalf("approve by token: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "issues", "1"); code == 0 || !strings.Contains(out, "approv") {
		t.Fatalf("issues before the approval: %d %s", code, out)
	}
	web := store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}
	if _, err := st.Access(web).SetIdeaPhase(1, idea.PhaseApproved, store.IdeaActorFor(web, ""), headRev(st, 1)); err != nil {
		t.Fatal(err)
	}
	code, out := painTaskRun(t, "idea", "issues", "1", "--issue", "Login ohne Passwort|1,2", "--branch", "feat/anmeldung")
	if code != 0 || !strings.Contains(out, "created REQ-1") || !strings.Contains(out, "work package PKG-1") {
		t.Fatalf("issues: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "waive", "1", "--requirement", "2", "--reason", "später"); code == 0 || !strings.Contains(out, "person") {
		t.Fatalf("waive by token: %d %s", code, out)
	}
	// Entscheidungen.
	if code, out := painTaskRun(t, "decision", "open", "--idea", "1", "Welcher Anbieter", "--aspect", "security", "--variant", "Eigener Server|selbst betreiben|keine Abhängigkeit|Aufwand", "--variant", "Fremddienst"); code != 0 ||
		!strings.Contains(out, "DEC-1 [open|human] Welcher Anbieter") || !strings.Contains(out, "A Eigener Server | pro: keine Abhängigkeit | contra: Aufwand") {
		t.Fatalf("decision open: %d %s", code, out)
	}
	png := write("mock.png", testCLIPNG(t))
	if code, out := painTaskRun(t, "idea", "attach", "1", png, "--decision", "DEC-1", "--variant", "A", "--alt", "Mock"); code != 0 || !strings.Contains(out, "ATT-1 image/png") {
		t.Fatalf("attach: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "attach", "1", write("x.svg", []byte(`<svg onload="x"/>`))); code == 0 {
		t.Fatalf("svg: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "vote", "DEC-1", "A", "--reason", "ich würde A nehmen"); code != 0 || !strings.Contains(out, "vote alice (agent): A, ich würde A nehmen") {
		t.Fatalf("vote: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "close", "DEC-1", "--variant", "A", "--reason", "Hoheit"); code == 0 || !strings.Contains(out, "person") {
		t.Fatalf("close a human decision by token: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "list", "--waiting", "--project", cliHere); code != 0 || !strings.Contains(out, "DEC-1") {
		t.Fatalf("inbox: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "open", "--via-request", "1", "Rückfrage aus der Umsetzung"); code != 0 || !strings.Contains(out, "(IDEA-1)") {
		t.Fatalf("via request: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "close", "DEC-2", "--defer", "wenn wir den Export bauen"); code != 0 || !strings.Contains(out, "DEC-2 [deferred|deferred]") {
		t.Fatalf("a deferral of an agent decision: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "policy", "human", "--project", cliHere); code == 0 || !strings.Contains(out, "person") {
		t.Fatalf("policy by token: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "policy", "--project", cliHere); code != 0 || !strings.Contains(out, `"mode": "rule"`) {
		t.Fatalf("policy read: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "show", "1"); code != 0 || !strings.Contains(out, "phase: building") || !strings.Contains(out, "raw r1 Chat") || !strings.Contains(out, "summary (r1)") ||
		!strings.Contains(out, "Anforderung 1: Anmelden ohne Passwort") || !strings.Contains(out, "requirement 1 is open") || !strings.Contains(out, "ATT-1") {
		t.Fatalf("show: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision"); code != 2 {
		t.Fatalf("bare decision: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "decision", "show", "x"); code != 2 {
		t.Fatalf("bad ref: %d %s", code, out)
	}
}

func testCLIPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Die Beispieldatei bildet die Idee dieser Werkstatt als erstes Beispiel ab; die
// Rohfassung liest der Import aus dem Dokument des Projekts.
func TestIdeaImportOfTheExampleFile(t *testing.T) {
	st := moveCLIFixture(t)
	original := "Rohfassung der Sprachnachricht, wörtlich.\n  Mit Einrückung und allem. "
	if _, err := st.CreateDocument(store.Document{Project: cliHere, Slug: "2026-11-08-idee-ideen-werkstatt-und-features-original", Kind: "other", Title: "Original", Person: "alice"}, original, "init"); err != nil {
		t.Fatal(err)
	}
	example := filepath.Join("..", "..", "docs", "examples", "idea-werkstatt-import.json")
	code, out := painTaskRun(t, "idea", "import", example, "--project", cliHere, "--apply")
	if code != 0 || !strings.Contains(out, "IDEA-1 Ideen-Werkstatt") || !strings.Contains(out, "raw, summary, concept") || !strings.Contains(out, "decision Wo liegen die Bild-Anhänge") {
		t.Fatalf("import: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "raw", "1"); code != 0 || out != original {
		t.Fatalf("the raw version is not the document: %d %q", code, out)
	}
	code, out = painTaskRun(t, "idea", "show", "1")
	for _, want := range []string{"phase: concept", "raw r1 Sprachnachricht", "summary (r1)", "Anforderung 9:", "DEC-1 [open|agent] Wo liegen die Bild-Anhänge", "DEC-2 [open|human] Dürfen Agenten eine Idee freigeben",
		"A Als Blob in der Datenbank | pro: Ein Backup enthält alles", "who decides: touches security or permissions"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	// Ein zweiter Import legt nichts doppelt an.
	code, out = painTaskRun(t, "idea", "import", example, "--project", cliHere, "--apply")
	if code != 0 || !strings.Contains(out, "skipped") {
		t.Fatalf("second import: %d %s", code, out)
	}
	var ideas int
	st.DB().QueryRow(`SELECT COUNT(*) FROM ideas`).Scan(&ideas)
	if ideas != 1 {
		t.Fatalf("%d ideas", ideas)
	}
	// Fehler: eine Datei mit unbekanntem Feld, ein Dokument, das es nicht gibt, kein Verzeichnis.
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"version":1,"ideas":[{"title":"x","surprise":1}]}`), 0o600)
	if code, out := painTaskRun(t, "idea", "import", bad, "--project", cliHere, "--apply"); code == 0 {
		t.Fatalf("unknown field: %d %s", code, out)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	os.WriteFile(missing, []byte(`{"version":1,"ideas":[{"title":"Anderes","raw":{"document":"gibt-es-nicht"}}]}`), 0o600)
	if code, out := painTaskRun(t, "idea", "import", missing, "--project", cliHere, "--apply"); code == 0 || !strings.Contains(out, "gibt-es-nicht") {
		t.Fatalf("missing document: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "import", t.TempDir(), "--project", cliHere, "--apply"); code == 0 {
		t.Fatalf("a directory: %d %s", code, out)
	}
}

func headRev(st *store.Store, ideaID int64) int {
	var n int
	st.DB().QueryRow(`SELECT COALESCE(MAX(revision),0) FROM idea_texts WHERE idea_id=? AND part='concept'`, ideaID).Scan(&n)
	return n
}

// Eine Importdatei darf keine fremden Dateien hochladen lassen: kein .., kein
// absoluter Pfad, kein Symlink nach außen, nur Bilder bei Bildern.
func TestImportRefusesFilesOutsideItsFolder(t *testing.T) {
	st := moveCLIFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_ed25519")
	if err := os.WriteFile(secret, []byte("geheim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.png"), testCLIPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("no symlinks here")
	}
	os.Symlink(filepath.Join(outside, "outside.png"), filepath.Join(dir, "link.png"))
	os.WriteFile(filepath.Join(dir, "ok.png"), testCLIPNG(t), 0o600)
	os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("Rohtext"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("kein Bild"), 0o600)
	rel, _ := filepath.Rel(dir, secret)
	for name, tc := range map[string]string{
		"parent":       `{"file":"../../etc/passwd"}`,
		"relative out": `{"file":"` + filepath.ToSlash(rel) + `"}`,
		"absolute":     `{"file":"` + secret + `"}`,
		"symlink":      `{"file":"link.txt"}`,
	} {
		path := filepath.Join(dir, "import-"+strings.ReplaceAll(name, " ", "-")+".json")
		os.WriteFile(path, []byte(`{"version":1,"ideas":[{"title":"Idee `+name+`","raw":`+strings.Replace(tc, `{"file"`, `{"source":"x","file"`, 1)+`}]}`), 0o600)
		if code, out := painTaskRun(t, "idea", "import", path, "--project", cliHere, "--apply"); code == 0 || strings.Contains(out, "geheim") {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	for name, image := range map[string]string{"parent": "../" + filepath.Base(outside) + "/outside.png", "absolute": filepath.Join(outside, "outside.png"), "symlink": "link.png", "not an image": "notes.txt"} {
		path := filepath.Join(dir, "img-"+strings.ReplaceAll(name, " ", "-")+".json")
		doc := `{"version":1,"ideas":[{"title":"Bild ` + name + `","decisions":[{"question":"Welches Bild","variants":[{"title":"A","image":"` + image + `"},{"title":"B"}]}]}]}`
		os.WriteFile(path, []byte(doc), 0o600)
		if code, out := painTaskRun(t, "idea", "import", path, "--project", cliHere, "--apply"); code == 0 {
			t.Errorf("image %s: %d %s", name, code, out)
		}
	}
	var attachments int
	st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&attachments)
	if attachments != 0 {
		t.Fatalf("%d foreign files were uploaded", attachments)
	}
	// Eine Datei im eigenen Ordner geht.
	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`{"version":1,"ideas":[{"title":"Gut","raw":{"source":"x","file":"ok.txt"},"decisions":[{"question":"Welches Bild","variants":[{"title":"A","image":"ok.png"},{"title":"B"}]}]}]}`), 0o600)
	if code, out := painTaskRun(t, "idea", "import", good, "--project", cliHere, "--apply"); code != 0 || !strings.Contains(out, "image of variant A") {
		t.Fatalf("a file of its own folder: %d %s", code, out)
	}
}

// Standard ist ein Probelauf: er listet die lokalen Dateien mit Größe und lädt nichts hoch.
func TestImportIsADryRunUnlessApplied(t *testing.T) {
	st := moveCLIFixture(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "raw.md"), []byte("Rohtext"), 0o600)
	os.WriteFile(filepath.Join(dir, "a.png"), testCLIPNG(t), 0o600)
	file := filepath.Join(dir, "i.json")
	os.WriteFile(file, []byte(`{"version":1,"ideas":[{"title":"Probe","raw":{"source":"x","file":"raw.md"},"decisions":[{"question":"Q","variants":[{"title":"A","image":"a.png"},{"title":"B"}]}]}]}`), 0o600)
	code, out := painTaskRun(t, "idea", "import", file, "--project", cliHere)
	if code != 0 || !strings.Contains(out, "reads text raw.md (7 bytes)") || !strings.Contains(out, "reads image a.png") || !strings.Contains(out, "nothing was uploaded") {
		t.Fatalf("dry run: %d %s", code, out)
	}
	var ideas, attachments int
	st.DB().QueryRow(`SELECT COUNT(*) FROM ideas`).Scan(&ideas)
	st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&attachments)
	if ideas != 0 || attachments != 0 {
		t.Fatalf("a dry run wrote: %d ideas, %d attachments", ideas, attachments)
	}
	if code, out := painTaskRun(t, "idea", "import", file, "--project", cliHere, "--apply"); code != 0 || !strings.Contains(out, "IDEA-1") {
		t.Fatalf("apply: %d %s", code, out)
	}
}

// Nur Dateien neben der Importdatei oder im genannten Ordner; keine Punktordner,
// nur .md/.txt als Text, nur echte Bilder. Eine Importdatei im Home-Verzeichnis
// liest weder ~/.ssh noch ~/.config.
func TestImportReadsOnlyBesideItsFileAndInTheNamedFolder(t *testing.T) {
	st := moveCLIFixture(t)
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.MkdirAll(filepath.Join(home, ".config", "x"), 0o700)
	os.MkdirAll(filepath.Join(home, "sub"), 0o700)
	os.MkdirAll(filepath.Join(home, "pics"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.txt"), []byte("geheim"), 0o600)
	os.WriteFile(filepath.Join(home, ".config", "x", "token.txt"), []byte("geheim"), 0o600)
	os.WriteFile(filepath.Join(home, ".hidden.txt"), []byte("geheim"), 0o600)
	os.WriteFile(filepath.Join(home, "sub", "t.txt"), []byte("Unterordner"), 0o600)
	os.WriteFile(filepath.Join(home, "run.sh"), []byte("echo"), 0o600)
	os.WriteFile(filepath.Join(home, "latin1.txt"), []byte("caf\xe9"), 0o600)
	os.WriteFile(filepath.Join(home, "fake.png"), []byte("<html>kein Bild</html>"), 0o600)
	os.WriteFile(filepath.Join(home, "pics", "a.png"), testCLIPNG(t), 0o600)
	os.WriteFile(filepath.Join(home, "pics", "svg.png"), []byte(`<svg onload="x"/>`), 0o600)
	try := func(name, doc string) (int, string) {
		path := filepath.Join(home, name+".json")
		os.WriteFile(path, []byte(doc), 0o600)
		return painTaskRun(t, "idea", "import", path, "--project", cliHere, "--apply")
	}
	raw := func(file string) string {
		return `{"version":1,"ideas":[{"title":"` + file + `","raw":{"source":"x","file":"` + file + `"}}]}`
	}
	for name, file := range map[string]string{"ssh": ".ssh/id_ed25519.txt", "config": ".config/x/token.txt", "hidden": ".hidden.txt", "undeclared folder": "sub/t.txt",
		"extension": "run.sh", "dotdot": "pics/../.ssh/id_ed25519.txt", "latin1": "latin1.txt"} {
		if code, out := try("raw-"+strings.ReplaceAll(name, " ", "-"), raw(file)); code == 0 || strings.Contains(out, "geheim") {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	// Auch mit attachments_dir: nur dieser eine Ordner, kein Punktname.
	for name, doc := range map[string]string{
		"dir with slash": `{"version":1,"attachments_dir":"pics/x","ideas":[{"title":"a"}]}`,
		"dir with dot":   `{"version":1,"attachments_dir":".ssh","ideas":[{"title":"a","raw":{"file":".ssh/id_ed25519.txt"}}]}`,
		"dir dotdot":     `{"version":1,"attachments_dir":"..","ideas":[{"title":"a"}]}`,
		"other folder":   `{"version":1,"attachments_dir":"pics","ideas":[{"title":"a","raw":{"file":"sub/t.txt"}}]}`,
	} {
		if code, out := try("dir-"+strings.ReplaceAll(name, " ", "-"), doc); code == 0 || strings.Contains(out, "geheim") {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	image := func(img string) string {
		return `{"version":1,"attachments_dir":"pics","ideas":[{"title":"` + img + `","decisions":[{"question":"Q","variants":[{"title":"A","image":"` + img + `"},{"title":"B"}]}]}]}`
	}
	for name, img := range map[string]string{"not an image": "fake.png", "svg bytes": "pics/svg.png", "script": "run.sh", "outside": "../a.png"} {
		if code, out := try("img-"+strings.ReplaceAll(name, " ", "-"), image(img)); code == 0 {
			t.Errorf("image %s: %d %s", name, code, out)
		}
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM ideas`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d ideas were recorded from refused files", n)
	}
	// Erlaubt: neben der Importdatei, und im genannten Ordner.
	if code, out := try("ok", `{"version":1,"attachments_dir":"pics","ideas":[{"title":"Erlaubt","decisions":[{"question":"Q","variants":[{"title":"A","image":"pics/a.png"},{"title":"B"}]}]}]}`); code != 0 || !strings.Contains(out, "image of variant A") {
		t.Fatalf("allowed: %d %s", code, out)
	}
}
