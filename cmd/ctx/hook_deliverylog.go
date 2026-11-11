package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The delivery log from the hook's side (REQ-636, P0). A hook is the one place
// that knows what actually reached the model: the text after the session budget
// cut it. So the hook writes down what it delivered, in parts that add up to
// the characters the budget counted, and sends them to the server later with
// the tool counts. Nothing is sent while the hook runs: the cost on the hot
// path is one append to a file of the session's log directory.
//
//	deliveries: one JSON line per hook call that delivered something, and one per
//	            batch of session signals; sent by flushContextLog
//	signals:    exists once the session's signals were written
const (
	contextLogMaxSpool   = 256 << 10 // a server that stays away does not make this grow
	contextLogBatchSize  = 100       // deliveries per report
	contextLogReportWait = 1500 * time.Millisecond
)

// deliveryTrace collects the parts of what one hook call delivers while the
// functions that build the text run. The text is built from several sources
// that run side by side (the entries, the runbooks, one answer per path), so
// the trace is the place where each says what it contributed.
//
// A hook is one process with one call; the trace is process-wide for the
// duration of that call and nil otherwise, which turns every add into a no-op
// for the callers that are not a hook (the tests of the pieces).
type deliveryTrace struct {
	project string
	parts   []deliverylog.Part
}

var activeTrace atomic.Pointer[deliveryTrace]

// beginTrace starts collecting for one hook call and returns the function that
// ends it.
func beginTrace() (*deliveryTrace, func()) {
	t := &deliveryTrace{}
	activeTrace.Store(t)
	return t, func() { activeTrace.CompareAndSwap(t, nil) }
}

// traceParts adds parts in the order they stand in the text. Callers that
// gather in several goroutines add after they have joined, in text order.
func traceParts(parts ...deliverylog.Part) {
	if t := activeTrace.Load(); t != nil {
		t.parts = append(t.parts, parts...)
	}
}

func traceProject(project string) {
	if t := activeTrace.Load(); t != nil && project != "" {
		t.project = project
	}
}

// spoolLine is one line of the deliveries file.
type spoolLine struct {
	Project string               `json:"project"`
	Event   *store.DeliveryEvent `json:"event,omitempty"`
	Signal  *store.SessionSignal `json:"signal,omitempty"`
}

// contextLogSignals are the environment variables the session reports: the
// names are the ones the concept lists, and the values are whatever the harness
// sets, cut short.
var contextLogSignals = store.SessionSignalNames

// logDelivery writes down one delivery. emitted is the text the budget let
// through; its length is what the budget counted, so the parts are fitted to it.
// Without a project (the server only takes projects) or a usable session
// nothing is written. It never fails the hook.
func logDelivery(t *deliveryTrace, sessionID, harness, frameVia string, subagent bool, emitted string) {
	if t == nil || t.project == "" || (harness != "claude" && harness != "codex") {
		return
	}
	total := deliverylog.Runes(emitted)
	if total == 0 {
		return
	}
	parts := deliverylog.Fit(t.parts, total, frameVia)
	if len(parts) == 0 {
		return
	}
	appendSpool(sessionID, spoolLine{Project: t.project, Event: &store.DeliveryEvent{
		At: time.Now().UTC().Format(time.RFC3339), Call: callID(), Parts: parts, Subagent: subagent,
	}})
}

func callID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func appendSpool(sessionID string, line spoolLine) {
	if !store.ValidExternalID(sessionID) {
		return
	}
	dir, err := sessionLogDir(sessionID)
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return
	}
	appendLine(filepath.Join(dir, "deliveries"), string(raw), contextLogMaxSpool)
}

// noteSessionSignals writes the signals of the session once: the names and
// values of the environment variables of the process that started the hook. A
// variable that is not set is written with an empty value; that it is absent
// tells apart the kinds of start as much as a value does.
func noteSessionSignals(sessionID, harness, project string) {
	if harness != "claude" || project == "" || !store.ValidExternalID(sessionID) {
		return
	}
	dir, err := sessionLogDir(sessionID)
	if err != nil {
		return
	}
	marker := filepath.Join(dir, "signals")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(marker, nil, 0o600) != nil {
		return
	}
	for _, name := range contextLogSignals {
		value := os.Getenv(name)
		sig := store.SessionSignal{Name: name, Value: value}
		if sig.Valid() != nil {
			sig.Value = "(invalid)"
		}
		appendSpool(sessionID, spoolLine{Project: project, Signal: &sig})
	}
}

// flushContextLog sends the written-down deliveries and signals of a session to
// the server, like flushSessionUses: the sender claims the file by renaming it,
// so two hooks never send the same lines; without force it sends only when the
// last report is older than usesFlushInterval; a report the server could not
// take goes back for the next attempt, one it refused for good is dropped.
func flushContextLog(raw []byte, harness string, force bool) {
	var in hookToolCall
	if json.Unmarshal(raw, &in) != nil || !store.ValidExternalID(trimmed(in.SessionID)) {
		return
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil {
		return
	}
	spool := filepath.Join(dir, "deliveries")
	stamp := filepath.Join(dir, "flushed-log")
	if !force {
		if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < usesFlushInterval {
			return
		}
	}
	if _, err := os.Stat(spool); err != nil {
		return
	}
	claimed := spool + ".send-" + strconv.Itoa(os.Getpid())
	if os.Rename(spool, claimed) != nil {
		return
	}
	_ = os.WriteFile(stamp, nil, 0o600)
	_ = os.Chtimes(stamp, time.Now(), time.Now())
	content, err := os.ReadFile(claimed)
	_ = os.Remove(claimed)
	if err != nil || len(content) == 0 {
		return
	}
	batches := contextLogBatches(content, trimmed(in.SessionID), harness)
	if len(batches) == 0 {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	c := client.NewWithTimeout(cfg, contextLogReportWait)
	var back [][]byte
	for _, b := range batches {
		if err := c.RecordContextLog(b.batch); err != nil && !permanentRefusal(err) {
			back = append(back, b.lines...)
		}
	}
	if len(back) == 0 {
		return
	}
	f, err := os.OpenFile(spool, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, l := range back {
		_, _ = f.Write(append(l, '\n'))
	}
}

// flushSessionReports sends the tool counts and the delivery log of a session
// side by side, so that a slow server costs one deadline and not two.
func flushSessionReports(raw []byte, harness string, force bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flushContextLog(raw, harness, force)
	}()
	flushSessionUses(raw, harness, force)
	wg.Wait()
}

func trimmed(s string) string { return string(bytes.TrimSpace([]byte(s))) }

type sentBatch struct {
	batch store.ContextLogBatch
	lines [][]byte
}

// contextLogBatches reads the spool into reports, one per project, at most
// contextLogBatchSize deliveries each. lines keeps the source lines of a report
// so they can be put back whole.
func contextLogBatches(content []byte, session, harness string) []sentBatch {
	var out []sentBatch
	open := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var l spoolLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Project == "" || (l.Event == nil && l.Signal == nil) {
			continue
		}
		i, ok := open[l.Project]
		if !ok || (l.Event != nil && len(out[i].batch.Events) >= contextLogBatchSize) {
			out = append(out, sentBatch{batch: store.ContextLogBatch{Project: l.Project, SessionRef: session, Harness: harness}})
			i = len(out) - 1
			open[l.Project] = i
		}
		if l.Event != nil {
			out[i].batch.Events = append(out[i].batch.Events, *l.Event)
		} else {
			out[i].batch.Signals = append(out[i].batch.Signals, *l.Signal)
		}
		out[i].lines = append(out[i].lines, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

// permanentRefusal says the server understood the report and will never take
// it: sending it again would only repeat the refusal.
func permanentRefusal(err error) bool {
	var api *client.APIError
	if errors.As(err, &api) {
		return api.Status >= 400 && api.Status < 500 && api.Status != 408 && api.Status != 429
	}
	var st *client.StatusError
	if errors.As(err, &st) {
		return st.Status >= 400 && st.Status < 500 && st.Status != 408 && st.Status != 429
	}
	return false
}
