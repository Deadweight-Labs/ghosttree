package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pasteTTY is a terminal that can be asked for a pasted line and can abandon the
// wait, like /dev/tty with a read deadline. Questions (Ask) go to the embedded
// scripted terminal, so a reader left behind would show up as a stolen answer.
type pasteTTY struct {
	*fakeTTY
	lines   chan string
	active  atomic.Int32
	prompts atomic.Int32
}

func newPasteTTY(answers ...string) *pasteTTY {
	return &pasteTTY{fakeTTY: &fakeTTY{answers: answers}, lines: make(chan string, 8)}
}

func (p *pasteTTY) CanPaste() bool { return true }

func (p *pasteTTY) Paste(ctx context.Context, prompt string) (string, error) {
	p.active.Add(1)
	defer p.active.Add(-1)
	p.prompts.Add(1)
	select {
	case l := <-p.lines:
		return l, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

const (
	goodCode    = "BCDFGHJKMNPQRTVWX346"
	goodGrouped = "bcdf-ghjk-mnpq-rtvw-x346"
	otherCode   = "WWWWWWWWWWWWWWWWWWWW" // well formed, belongs to nobody
)

// pasteServer plays the server for the race: the claim answers loopback or
// paste, and the exchange takes goodCode exactly once and nothing else.
type pasteServer struct {
	*fakeServer
	used      atomic.Bool
	exchanged []string
	mu        sync.Mutex
	onClaim   func(port int, state string)
	expired   bool // the exchange refuses every code
}

func newPasteServer(t *testing.T) *pasteServer {
	t.Helper()
	ps := &pasteServer{}
	f := newFakeServer(t)
	ps.fakeServer = f
	f.claimRes = func(body map[string]any) (int, string) {
		port, _ := body["loopback_port"].(float64)
		state, _ := body["state"].(string)
		mode := "paste"
		if port != 0 {
			mode = "loopback"
		}
		if ps.onClaim != nil && port != 0 {
			go ps.onClaim(int(port), state)
		}
		return 200, `{"mode":"` + mode + `","expires_in":600,"token_endpoint":"/api/join/token"}`
	}
	f.tokenRes = func(body map[string]string) (int, string) {
		ps.mu.Lock()
		ps.exchanged = append(ps.exchanged, body["code"])
		ps.mu.Unlock()
		code := strings.ToUpper(strings.ReplaceAll(body["code"], "-", ""))
		if ps.expired || code != goodCode || !ps.used.CompareAndSwap(false, true) {
			return 400, `{"error":"invalid_grant"}`
		}
		return 200, `{"access_token":"tok-secret-1","token_type":"bearer","machine":"box","token_id":7}`
	}
	return ps
}

func (ps *pasteServer) codes() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]string(nil), ps.exchanged...)
}

func joinWithPasteTTY(t *testing.T, tty *pasteTTY, srv *pasteServer, extra ...string) (int, string) {
	t.Helper()
	f := newJoinFixture(t)
	joinTTY = func() (terminal, error) { return tty, nil }
	f.tty = tty.fakeTTY
	var out syncBuffer
	args := append([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box"}, extra...)
	code := cmdJoin(args, &out)
	return code, out.String()
}

func hitCallback(port int, query string) {
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := noFollow.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/callback?" + query); err == nil {
		resp.Body.Close()
	}
}

func TestJoinPasteBeatsTheCallbackAndTheLateCallbackIsHarmless(t *testing.T) {
	tty := newPasteTTY("y")
	srv := newPasteServer(t)
	lateDone := make(chan int, 1)
	srv.onClaim = func(port int, state string) {
		// The browser comes too late: the code was pasted a moment ago.
		for i := 0; i < 100 && !srv.used.Load(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/callback?code=" + goodCode + "&state=" + state)
		if err != nil {
			lateDone <- -1
			return
		}
		resp.Body.Close()
		lateDone <- resp.StatusCode
	}
	tty.lines <- goodGrouped
	code, out := joinWithPasteTTY(t, tty, srv)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got := srv.codes(); len(got) != 1 || strings.ToUpper(strings.ReplaceAll(got[0], "-", "")) != goodCode {
		t.Fatalf("exchanged %v", got)
	}
	if tty.active.Load() != 0 {
		t.Fatal("the paste reader is still running")
	}
	if q := tty.questions(); len(q) != 1 || !strings.Contains(q[0], "Connect this machine as account") {
		t.Fatalf("the question was lost or doubled: %v", q)
	}
	if _, ok := readConfig(t); !ok {
		t.Fatal("no config written")
	}
	select {
	case st := <-lateDone:
		if st == 200 {
			t.Fatalf("late callback answered %d", st)
		}
	case <-time.After(3 * time.Second):
	}
	if n := len(srv.codes()); n != 1 {
		t.Fatalf("the late callback caused another exchange: %v", srv.codes())
	}
}

func TestJoinCallbackBeatsPasteAndTheReaderEndsBeforeTheQuestion(t *testing.T) {
	tty := newPasteTTY("y")
	srv := newPasteServer(t)
	srv.onClaim = func(port int, state string) {
		time.Sleep(100 * time.Millisecond)
		hitCallback(port, "code="+goodCode+"&state="+state)
	}
	code, out := joinWithPasteTTY(t, tty, srv)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if tty.active.Load() != 0 {
		t.Fatal("the paste reader outlived the join")
	}
	if tty.prompts.Load() == 0 {
		t.Fatal("paste was never offered next to the callback")
	}
	if q := tty.questions(); len(q) != 1 {
		t.Fatalf("questions %v", q)
	}
	if got := srv.codes(); len(got) != 1 || got[0] != goodCode {
		t.Fatalf("exchanged %v", got)
	}
	// A line pasted after the fact must not be read by anybody.
	tty.lines <- "late"
	time.Sleep(50 * time.Millisecond)
	if len(tty.lines) != 1 {
		t.Fatal("something still reads the terminal")
	}
}

func TestJoinWrongPastedCodeIsRefusedAndTheRightOneStillWorks(t *testing.T) {
	tty := newPasteTTY("y")
	srv := newPasteServer(t)
	tty.lines <- otherCode // well formed, but another flow's (or nobody's)
	tty.lines <- "not a code"
	tty.lines <- goodCode
	code, out := joinWithPasteTTY(t, tty, srv)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "does not belong to this setup") || !strings.Contains(out, "does not look like the code") {
		t.Fatalf("no hints: %s", out)
	}
	got := srv.codes()
	if len(got) != 2 || got[0] != otherCode || got[1] != goodCode {
		t.Fatalf("exchanged %v (a malformed line must not reach the server)", got)
	}
}

func TestJoinExpiredPastedCodeEndsInterruptedAndWritesNothing(t *testing.T) {
	tty := newPasteTTY()
	srv := newPasteServer(t)
	srv.expired = true
	tty.lines <- goodCode
	f := newJoinFixture(t)
	joinTTY = func() (terminal, error) { return tty, nil }
	joinTimeout = 400 * time.Millisecond
	_ = f
	var out syncBuffer
	code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--yes"}, &out)
	if code != 1 || !strings.Contains(out.String(), errInterrupted) {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written for an expired code")
	}
	if tty.active.Load() != 0 {
		t.Fatal("the paste reader is still running")
	}
}

func TestJoinWithoutLoopbackAsksForThePastedCodeAndSendsNoLoopbackFields(t *testing.T) {
	tty := newPasteTTY("y")
	srv := newPasteServer(t)
	tty.lines <- goodCode
	f := newJoinFixture(t)
	joinTTY = func() (terminal, error) { return tty, nil }
	joinHasDisplay = func() bool { return false }
	_ = f
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	claim := srv.claims[0]
	for _, k := range []string{"loopback_port", "loopback_host", "state"} {
		if _, ok := claim[k]; ok {
			t.Fatalf("paste-only claim sends %s: %v", k, claim)
		}
	}
	if claim["code_challenge_method"] != "S256" || claim["code_challenge"] == nil {
		t.Fatalf("claim %v", claim)
	}
	if !strings.Contains(out.String(), "Open this link in your browser") || len(f.opened) != 0 {
		t.Fatalf("output %s opened %v", out.String(), f.opened)
	}
	// The terminal question defaults to no when the approval happened elsewhere.
	if q := tty.questions(); len(q) != 1 || !strings.Contains(q[0], "[y/N]") {
		t.Fatalf("questions %v", q)
	}
}

func TestJoinNoBrowserStaysAHiddenAliasForTheTypedCodeWay(t *testing.T) {
	var out strings.Builder
	cmdJoin([]string{"--help"}, &out)
	if strings.Contains(out.String(), "no-browser") {
		t.Fatalf("usage still advertises the flag: %s", out.String())
	}
	f := newJoinFixture(t, "y")
	noSleep(t)
	srv := fallbackServer(t)
	var buf syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--no-browser"}, &buf); code != 0 {
		t.Fatalf("exit %d: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "Type this code in your browser: 7Q4K") || len(f.opened) != 0 {
		t.Fatalf("alias changed behavior: %s", buf.String())
	}
}

func TestNormalizePasted(t *testing.T) {
	for in, want := range map[string]string{
		goodCode:                              goodCode,
		" " + goodGrouped + "\n":              goodCode,
		"BCDF GHJK MNPQ RTVW X346":            goodCode,
		"":                                    "",
		"BCDFGHJKMNPQRTVWX34":                 "",
		goodCode + "B":                        "",
		"0CDFGHJKMNPQRTVWX346":                "",
		"https://x/callback?code=" + goodCode: "",
	} {
		if got := normalizePasted(in); got != want {
			t.Errorf("normalizePasted(%q) = %q, want %q", in, got, want)
		}
	}
}
