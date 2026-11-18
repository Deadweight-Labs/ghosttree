package web

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/mail"
	"github.com/Deadweight-Labs/ghosttree/internal/passhash"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// lockoutRig is an instance with mail and passwords whose requests come from chosen
// source addresses (loopback is a trusted proxy, so X-Forwarded-For names the client).
type lockoutRig struct {
	t   *testing.T
	st  *store.Store
	h   http.Handler
	dir string
}

// mails counts the mails written for an address once the background work is done.
func (g *lockoutRig) mails(to string) int {
	g.h.(*appHandler).app.mailWG.Wait()
	files, _ := filepath.Glob(filepath.Join(g.dir, "*-"+to+".eml"))
	return len(files)
}

func newLockoutRig(t *testing.T) *lockoutRig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := filepath.Join(t.TempDir(), "mailbox")
	sender, err := mail.New(mail.Config{Dir: dir, From: "ghosttree <noreply@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, WithMail(sender), WithPasswordLogin(true), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	return &lockoutRig{t: t, st: st, h: h, dir: dir}
}

func (g *lockoutRig) post(from, path string, form url.Values) *httptest.ResponseRecorder {
	g.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://gt.test"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://gt.test")
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header.Set("X-Forwarded-For", from)
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	return rec
}

// Wer von einer Adresse aus falsche Passwörter für ein Konto schickt, sperrt dieses
// Konto nur für diese Adresse: die Person, der es gehört, kommt von ihrer weiter hinein,
// und ein richtiges Passwort setzt den Zähler zurück.
func TestPasswordLockoutIsBoundToTheSourceAndResetByASuccess(t *testing.T) {
	g := newLockoutRig(t)
	jonas, err := g.st.AddAccount("jonas", "jonas@example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := passhash.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.st.SetPassword(jonas.ID, hash); err != nil {
		t.Fatal(err)
	}
	form := func(pw string) url.Values { return url.Values{"email": {"jonas@example.test"}, "password": {pw}} }
	limited := false
	for i := 0; i < 14; i++ {
		if g.post("203.0.113.5", "/ui/login/password", form("wrong"+string(rune('a'+i)))).Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("the attacker is not stopped")
	}
	if rec := g.post("198.51.100.9", "/ui/login/password", form("correct horse battery")); rec.Code != http.StatusSeeOther {
		t.Fatalf("the owner of the account from another address: %d, want a sign-in", rec.Code)
	}
	// Nine misses and a success from one address leave the counter empty.
	for i := 0; i < 9; i++ {
		g.post("198.51.100.20", "/ui/login/password", form("nope"+string(rune('a'+i))))
	}
	if rec := g.post("198.51.100.20", "/ui/login/password", form("correct horse battery")); rec.Code != http.StatusSeeOther {
		t.Fatalf("a success after nine misses: %d", rec.Code)
	}
	for i := 0; i < 9; i++ {
		if rec := g.post("198.51.100.20", "/ui/login/password", form("again"+string(rune('a'+i)))); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("miss %d after a success was refused: the success did not reset the count", i)
		}
	}
}

// Mails für eine Adresse fordert jede Quelle in ihrem eigenen Rahmen an; die eines
// Fremden verbrauchen den Rahmen der Person nicht.
func TestMailRequestLimitIsBoundToTheSource(t *testing.T) {
	g := newLockoutRig(t)
	if _, err := g.st.AddAccount("jonas", "jonas@example.test", false); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"email": {"jonas@example.test"}}
	for i := 0; i < 8; i++ {
		g.post("203.0.113.5", "/ui/mail/login/request", form)
	}
	if n := g.mails("jonas@example.test"); n != 5 {
		t.Fatalf("%d mails for 8 requests from one source, want 5", n)
	}
	// The owner asks from elsewhere: the page is the same, and a mail is made for them.
	rec := g.post("198.51.100.9", "/ui/mail/login/request", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("the owner asks for a mail: %d", rec.Code)
	}
	if n := g.mails("jonas@example.test"); n != 6 {
		t.Fatalf("%d mails after the owner asked, want 6: the stranger used up the owner's share", n)
	}
}

// Die Antwort auf "Anmelde-Mail anfordern" hängt nicht davon ab, was im Hintergrund
// geschieht: auch wenn die Herausforderung nicht entsteht (hier ist der Store zu),
// ist sie dieselbe Seite wie für eine Adresse mit Konto.
func TestLoginMailAnswerDoesNotDependOnTheAccount(t *testing.T) {
	g := newLockoutRig(t)
	if _, err := g.st.AddAccount("jonas", "jonas@example.test", false); err != nil {
		t.Fatal(err)
	}
	known := g.post("203.0.113.5", "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}})
	unknown := g.post("203.0.113.6", "/ui/mail/login/request", url.Values{"email": {"nobody@example.test"}})
	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("status %d / %d", known.Code, unknown.Code)
	}
	if err := g.st.Close(); err != nil {
		t.Fatal(err)
	}
	broken := g.post("203.0.113.7", "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}})
	if broken.Code != http.StatusOK {
		t.Fatalf("with a broken store the page is %d, not the same page", broken.Code)
	}
}

// While many sources guess at one address, unknown sources go one at a time with a
// pause and may be told to wait; a source the address has signed in from before is
// never held up, and the account is not locked for its owner.
func TestGuessingFromManySourcesSlowsDownButSparesTheKnownSource(t *testing.T) {
	old := failDelay
	failDelay = 60 * time.Millisecond
	t.Cleanup(func() { failDelay = old })
	g := newLockoutRig(t)
	jonas, err := g.st.AddAccount("jonas", "jonas@example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := passhash.Hash("correct horse battery")
	if err := g.st.SetPassword(jonas.ID, hash); err != nil {
		t.Fatal(err)
	}
	form := func(pw string) url.Values { return url.Values{"email": {"jonas@example.test"}, "password": {pw}} }
	if rec := g.post("198.51.100.9", "/ui/login/password", form("correct horse battery")); rec.Code != http.StatusSeeOther {
		t.Fatalf("first sign-in: %d", rec.Code)
	}
	guards := g.h.(*appHandler).app.guards
	for i := 0; i < 30; i++ {
		guards.failAddrAll.hit("pw|jonas@example.test")
	}
	// The known source does not queue.
	start := time.Now()
	if rec := g.post("198.51.100.9", "/ui/login/password", form("correct horse battery")); rec.Code != http.StatusSeeOther {
		t.Fatalf("the known source under attack: %d", rec.Code)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("the known source waited %v", took)
	}
	// Twelve unknown sources at once: some get through in turn, some are told to wait.
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := g.post(fmt.Sprintf("203.0.113.%d", 10+i), "/ui/login/password", form("guess"))
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[http.StatusTooManyRequests] == 0 || codes[http.StatusUnprocessableEntity] == 0 {
		t.Fatalf("twelve parallel guesses ended in %v: want some answered in turn and some told to wait", codes)
	}
}
