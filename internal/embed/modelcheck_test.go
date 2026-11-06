package embed_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
)

func newClient(t *testing.T, srv *embedtest.Server) *embed.Client {
	t.Helper()
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCompareServersAcceptsTheSameModel(t *testing.T) {
	a, b := embedtest.New(t), embedtest.New(t)
	ag, err := embed.CompareServers(context.Background(), newClient(t, a), newClient(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if ag.Dimensions != 64 || ag.Cosine < 0.99999 {
		t.Fatalf("%+v", ag)
	}
	for _, srv := range []*embedtest.Server{a, b} {
		reqs := srv.Requests()
		if len(reqs) != 1 || len(reqs[0]) != 1 || reqs[0][0] != embed.QueryText(embed.ProbeText) {
			t.Fatalf("each server gets the probe text once, in the query format: %q", reqs)
		}
	}
}

func TestCompareServersSeesAnotherModel(t *testing.T) {
	var diff *embed.DisagreementError

	// Another dimension, as another model has it.
	a, b := embedtest.New(t), embedtest.New(t)
	b.Dim = 128
	_, err := embed.CompareServers(context.Background(), newClient(t, a), newClient(t, b))
	if !errors.As(err, &diff) || diff.QueryDim != 64 || diff.JobDim != 128 || !strings.Contains(err.Error(), "64 dimensions") {
		t.Fatalf("different dimensions: %v", err)
	}

	// The same dimension, but the probe lands elsewhere: below the threshold.
	c, d := embedtest.New(t), embedtest.New(t)
	d.Concepts = map[string]string{"ghosttree": "something-else"}
	_, err = embed.CompareServers(context.Background(), newClient(t, c), newClient(t, d))
	if !errors.As(err, &diff) || diff.QueryDim != diff.JobDim || diff.Cosine >= embed.MinAgreement || diff.Cosine <= 0 {
		t.Fatalf("different vectors: %v (%+v)", err, diff)
	}
	if !strings.Contains(err.Error(), "cosine") {
		t.Fatal(err)
	}
}

func TestCompareServersSaysWhenAServerDoesNotAnswer(t *testing.T) {
	a, b := embedtest.New(t), embedtest.New(t)
	b.Fail.Store(true)
	_, err := embed.CompareServers(context.Background(), newClient(t, a), newClient(t, b))
	var diff *embed.DisagreementError
	if err == nil || errors.As(err, &diff) || !strings.Contains(err.Error(), "job server") {
		t.Fatalf("a server that fails says nothing about the model: %v", err)
	}
	a.Fail.Store(true)
	if _, err = embed.CompareServers(context.Background(), newClient(t, a), newClient(t, b)); err == nil ||
		!strings.Contains(err.Error(), "query server") || !strings.Contains(err.Error(), "job server") {
		t.Fatalf("both failures are named: %v", err)
	}
}

// fakeProber is a server whose vector can be changed while the check runs.
type fakeProber struct {
	mu    sync.Mutex
	vec   []float32
	err   error
	calls atomic.Int64
}

func (f *fakeProber) Query(ctx context.Context, _ string) ([]float32, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float32(nil), f.vec...), f.err
}

func (f *fakeProber) set(vec []float32, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vec, f.err = vec, err
}

// logBuffer collects the log of a check that runs in a goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuffer) count(msg string) int { return strings.Count(l.String(), "msg="+msg) }

func fastCheck() embed.CheckConfig {
	return embed.CheckConfig{Timeout: time.Second, Agree: 5 * time.Millisecond, Disagree: 5 * time.Millisecond,
		Retry: 2 * time.Millisecond, RetryMax: 8 * time.Millisecond, Alarm: time.Hour}
}

func runCheck(t *testing.T, q, j embed.Prober, cfg embed.CheckConfig) (*embed.ModelCheck, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	m := embed.NewModelCheck(q, j, cfg, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the check did not stop with its context")
		}
	})
	return m, logs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !embedtest.Wait(context.Background(), 5*time.Second, cond) {
		t.Fatalf("timed out waiting for: %s", what)
	}
}

func TestModelCheckLetsSearchesRunAndAnnouncesAgreementOnce(t *testing.T) {
	q, j := &fakeProber{vec: []float32{1, 0, 0}}, &fakeProber{vec: []float32{1, 0, 0}}
	m, logs := runCheck(t, q, j, fastCheck())
	waitFor(t, "several checks", func() bool { return q.calls.Load() >= 4 && j.calls.Load() >= 4 })
	if err := m.Err(); err != nil {
		t.Fatalf("servers that agree must not hold the search back: %v", err)
	}
	if n := logs.count("embed_models_agree"); n != 1 {
		t.Fatalf("agreement is said once, not on every check: %d\n%s", n, logs.String())
	}
	if logs.count("embed_models_differ")+logs.count("embed_models_unverified") != 0 {
		t.Fatalf("%s", logs.String())
	}
}

func TestModelCheckHoldsTheSearchBackWhileTheModelsDifferAndReleasesIt(t *testing.T) {
	q, j := &fakeProber{vec: []float32{1, 0, 0}}, &fakeProber{vec: []float32{0, 1, 0}}
	m, logs := runCheck(t, q, j, fastCheck())
	waitFor(t, "the verdict", func() bool { return m.Err() != nil })
	var diff *embed.DisagreementError
	if err := m.Err(); !errors.As(err, &diff) {
		t.Fatalf("%v", err)
	}
	// Loud: an error line, once while it lasts (the alarm interval is an hour).
	waitFor(t, "more checks", func() bool { return q.calls.Load() >= 5 })
	if n := logs.count("embed_models_differ"); n != 1 || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("one error line expected, got %d:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "full text") {
		t.Fatalf("the line says what the search does meanwhile:\n%s", logs.String())
	}

	// A server that does not answer says nothing: the verdict stands.
	j.set(nil, errors.New("connection refused"))
	waitFor(t, "a missed check", func() bool { return logs.count("embed_models_unverified") >= 1 })
	if m.Err() == nil {
		t.Fatal("an unanswered check must not lift the verdict")
	}

	// The fix is noticed by the next check, and said.
	j.set([]float32{1, 0.0001, 0}, nil)
	waitFor(t, "the release", func() bool { return m.Err() == nil })
	waitFor(t, "the line about it", func() bool { return logs.count("embed_models_agree") == 1 })
}

func TestModelCheckDoesNotHoldBackTheSearchForAServerThatDoesNotAnswer(t *testing.T) {
	q, j := &fakeProber{vec: []float32{1, 0, 0}}, &fakeProber{err: errors.New("busy")}
	m, logs := runCheck(t, q, j, fastCheck())
	waitFor(t, "several misses", func() bool { return j.calls.Load() >= 6 })
	if err := m.Err(); err != nil {
		t.Fatalf("a job server that is down or busy must not switch the vector part off: %v", err)
	}
	if n := logs.count("embed_models_unverified"); n != 1 {
		t.Fatalf("the miss is logged once while it lasts, got %d:\n%s", n, logs.String())
	}
	// It keeps trying, and once the server answers the verdict is "agree".
	j.set([]float32{1, 0, 0}, nil)
	waitFor(t, "the agreement", func() bool { return logs.count("embed_models_agree") == 1 })
	if m.Err() != nil {
		t.Fatal(m.Err())
	}
}

func TestModelCheckStopsWithItsContextEvenInsideAProbe(t *testing.T) {
	hung := hangingProber{}
	m := embed.NewModelCheck(hung, hung, fastCheck(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if m.Err() != nil {
		t.Fatal(m.Err())
	}
}

type hangingProber struct{}

func (hangingProber) Query(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
