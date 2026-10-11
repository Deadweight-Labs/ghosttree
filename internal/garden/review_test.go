package garden

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Point 1: a git failure is a failure, not "no match". Without this a broken
// checkout yields a stale verdict for every entry.
func TestGitFailureFailsTheEntryWithoutAModelCall(t *testing.T) {
	repo := newRepo(t)
	if err := os.RemoveAll(filepath.Join(repo.Root, ".git")); err != nil {
		t.Fatal(err)
	}
	m := &fakeModel{text: `{"verdict":"stale","reason":"x","evidence":["internal/distill.go:3"]}`}
	res := Check(context.Background(), repo, []Entry{entry(1, "`SessionDistillationExists`")}, Options{Model: m, Pricing: price, MaxUSD: 1})
	if m.calls != 0 {
		t.Fatalf("model was called %d times on an unreadable checkout", m.calls)
	}
	if v := res.Verdicts[0]; v.Kind != Failed || !strings.Contains(v.Reason, "git") {
		t.Fatalf("want a failed verdict naming git, got %s: %s", v.Kind, v.Reason)
	}
}

func TestGrepWithoutAMatchIsStillNoMatch(t *testing.T) {
	repo := newRepo(t)
	cc, err := repo.Gather(entry(1, "`GoneSymbol`"), ContextChars)
	if err != nil || !strings.Contains(cc.Text, "KEIN Treffer") {
		t.Fatalf("err=%v text=%q", err, cc.Text)
	}
}

// Point 2: the commit list is budgeted first and every cut is at a line.
func TestTruncationKeepsCommitsAndNeverSplitsALine(t *testing.T) {
	repo := newRepo(t)
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "// BigSymbol Zeile %d mit Umlauten äöüß und viel Text zum Auffüllen der Zeile\n", i)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "internal", "big.go"), []byte("package internal\n"+b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git("add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "add big"); err != nil {
		t.Fatal(err)
	}
	repo2, err := OpenRepo(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := repo2.Gather(entry(1, "`BigSymbol` und internal/big.go"), 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Text) > 1500+200 {
		t.Errorf("context is %d bytes, budget was 1500", len(cc.Text))
	}
	if !strings.Contains(cc.Text, "add big") {
		t.Errorf("commit list was crowded out:\n%s", cc.Text)
	}
	if !utf8.ValidString(cc.Text) {
		t.Error("context contains broken UTF-8")
	}
	for _, l := range strings.Split(cc.Text, "\n") {
		if strings.HasPrefix(l, "internal/big.go-") || strings.HasPrefix(l, "internal/big.go:") {
			if !strings.HasSuffix(l, "Zeile") && !strings.Contains(l, "Auffüllen der Zeile") && !strings.Contains(l, "package internal") {
				t.Errorf("line was cut in the middle: %q", l)
			}
		}
	}
}

// Point 3 is tested in cmd/ctx (verifyCheckout).

// Point 7: only exact file:line and long commit hashes count.
func TestEvidenceMustBeExactLineOrLongHash(t *testing.T) {
	repo := newRepo(t)
	cc, err := repo.Gather(entry(1, "`SessionDistillationExists`"), ContextChars)
	if err != nil {
		t.Fatal(err)
	}
	full, err := repo.git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	full = strings.TrimSpace(full)
	cases := map[string]bool{
		"internal/distill.go:3":   true,
		"internal/distill.go:3-4": false, // range
		"internal/distill.go:99":  false, // line not shown
		"internal/distill.go":     false, // no line
		"distill.go:3":            false, // not the shown path
		"die Funktion in distill": false, // free text
		full[:12]:                 true,
		full:                      true,
		full[:7]:                  false, // too short
		"commit " + full[:12]:     true,
		"0123456789ab":            false, // not in the log
	}
	for ref, want := range cases {
		ok, _ := splitEvidence(cc, []string{ref})
		if (len(ok) == 1) != want {
			t.Errorf("evidence %q accepted=%v, want %v", ref, len(ok) == 1, want)
		}
	}
}

// Point 8: a tracked symlink is not read, whatever it points at.
func TestSymlinkedFileIsNotReadIntoTheContext(t *testing.T) {
	repo := newRepo(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("GEHEIMER-INHALT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(repo.Root, "internal", "link.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git("add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "add link"); err != nil {
		t.Fatal(err)
	}
	repo2, err := OpenRepo(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := repo2.Gather(entry(1, "siehe internal/link.go"), ContextChars)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cc.Text, "GEHEIMER-INHALT") {
		t.Fatalf("symlink target leaked into the context:\n%s", cc.Text)
	}
}

// Point 5: a rejected proposal is not made again; a changed entry may get one.
func TestRejectedProposalIsNotRepeatedUnlessTheEntryChanged(t *testing.T) {
	e := Entry{ID: 7, Title: "a", UpdatedAt: "2026-10-01T10:00:00Z"}
	v := []Verdict{{Entry: e, Kind: Stale, Reason: "weg", Evidence: []string{"x.go:1"}}}
	rejected := store.Knowledge{Harness: Harness, SessionRef: ProposalRef(7), Status: "deprecated", CreatedAt: "2026-10-05T10:00:00Z"}

	sink := &recordingSink{}
	if n, _ := Propose(sink, "p", []store.Knowledge{rejected}, v); n != 0 {
		t.Fatalf("a rejected proposal was filed again (%d)", n)
	}
	v[0].Entry.UpdatedAt = "2026-10-09T10:00:00Z" // edited after the proposal
	if n, _ := Propose(sink, "p", []store.Knowledge{rejected}, v); n != 1 {
		t.Fatalf("an entry changed since the proposal should be proposable again (%d)", n)
	}
}

// Extra: a failed call without usage is charged at its worst case.
func TestFailedCallIsChargedAtWorstCase(t *testing.T) {
	repo := newRepo(t)
	m := &fakeModel{err: errors.New("timeout")}
	res := Check(context.Background(), repo, []Entry{entry(1, "`SessionDistillationExists`")}, Options{Model: m, Pricing: price, MaxUSD: 1})
	v := res.Verdicts[0]
	if v.Kind != Failed || v.Out != MaxOutputTokens || res.Spent < price.Cost(0, MaxOutputTokens) {
		t.Fatalf("failed call not charged conservatively: %+v spent=%.5f", v, res.Spent)
	}
	var _ llm.UsageClient = m
}

// Extra: the key never goes over plain http to a remote host.
func TestGardenBaseURLMustBeHTTPSOrLocal(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "llm.json")
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	t.Setenv("GHOSTTREE_GARDEN_KEY", "sk-test")
	for url, ok := range map[string]bool{
		"https://api.anthropic.com": true,
		"http://localhost:8080":     true,
		"http://127.0.0.1:9":        true,
		"http://api.anthropic.com":  false,
		"http://evil.example":       false,
		"ftp://localhost":           false,
	} {
		body := fmt.Sprintf(`{"garden":{"base_url":%q}}`, url)
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := llm.LoadGardenConfig()
		if (err == nil) != ok {
			t.Errorf("base_url %s: err=%v, want ok=%v", url, err, ok)
		}
	}
}
