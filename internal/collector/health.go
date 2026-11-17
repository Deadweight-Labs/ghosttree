package collector

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Was der Sammler nicht hochladen konnte, ist sonst nur eine Zeile im Journal des
// Dienstes. Im Lauf 3 sah man es an der Session (zehn Aufrufe, keine Antwort des
// Agenten), nicht am Sammler (REQ-690). Darum hält der Sammler fest, welche
// Transkripte seit wann scheitern, und `ctx status` nennt sie.

// syncRetries: so oft wiederholt ein Durchlauf ein Transkript, dessen Upload an
// einem Timeout oder einem Serverausfall scheiterte, bevor es bis zum nächsten
// Durchlauf wartet. Der Offset rückt erst nach der Bestätigung vor; eine Wiederholung
// lädt dieselben Zeilen, der Server hält jede nur einmal.
const syncRetries = 2

// syncRetryPause ist die Pause vor der ersten Wiederholung, danach doppelt so lang.
// Ein Test setzt sie auf null.
var syncRetryPause = 3 * time.Second

// healthStale: ein Befund, der älter ist, stammt von einem Sammler, der nicht
// mehr läuft; `ctx status` zeigt ihn dann nicht als aktuell.
const healthStale = 3 * time.Minute

type syncFailure struct {
	Since    time.Time
	Last     time.Time
	Attempts int
	Err      string
}

// Failure is one transcript that could not be uploaded.
type Failure struct {
	File     string    `json:"file"`
	Error    string    `json:"error"`
	Since    time.Time `json:"since"`
	Attempts int       `json:"attempts"`
}

// Health is what the collector wrote after its last sweep.
type Health struct {
	UpdatedAt time.Time `json:"updated_at"`
	Failing   []Failure `json:"failing"`
}

// Transient says whether an upload may work a moment later: the server did not
// answer in time, is not reachable or answered 429, 502, 503 or 504.
func Transient(err error) bool {
	if err == nil || errors.Is(err, errRegistrationPaused) || errors.Is(err, errNoSessionRef) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var se interface{ HTTPStatus() int }
	if errors.As(err, &se) {
		switch se.HTTPStatus() {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

// syncOne synchronises one transcript. A transient failure is repeated up to
// retries times with a growing pause; whatever still fails is remembered for the
// health report and logged with what happens next. The result says that the
// server is struggling, so the sweep stops repeating for the files after it.
func syncOne(path, harness string, up Uploader, st *State, machine string, retries int) (struggling bool) {
	err := SyncFile(path, harness, up, st, machine)
	pause := syncRetryPause
	for attempt := 0; attempt < retries && Transient(err); attempt++ {
		time.Sleep(pause)
		pause *= 2
		err = SyncFile(path, harness, up, st, machine)
	}
	st.noteSync(path, err)
	if err != nil && !errors.Is(err, errRegistrationPaused) {
		if Transient(err) {
			log.Printf("sync %s: %v (the server did not take it; the next sweep tries again)", path, err)
		} else {
			log.Printf("sync %s: %v", path, err)
		}
	}
	return Transient(err)
}

// noteSync records the outcome of one transcript for the health report.
func (st *State) noteSync(path string, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err == nil {
		delete(st.failures, path)
		return
	}
	if errors.Is(err, errRegistrationPaused) {
		return
	}
	if st.failures == nil {
		st.failures = map[string]*syncFailure{}
	}
	f := st.failures[path]
	if f == nil {
		f = &syncFailure{Since: backoffNow()}
		st.failures[path] = f
	}
	f.Last, f.Attempts, f.Err = backoffNow(), f.Attempts+1, err.Error()
}

// HealthPath is where the collector keeps its report, next to its state file.
func HealthPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "health.json")
}

// WriteHealth writes the report after a sweep. A file that gave up failing (or
// vanished) leaves it on its own; with nothing failing the report says so.
func (st *State) WriteHealth() error {
	st.mu.Lock()
	h := Health{UpdatedAt: backoffNow().UTC(), Failing: []Failure{}}
	for path, f := range st.failures {
		if _, err := os.Stat(path); err != nil {
			delete(st.failures, path)
			continue
		}
		h.Failing = append(h.Failing, Failure{File: path, Error: f.Err, Since: f.Since.UTC(), Attempts: f.Attempts})
	}
	statePath := st.path
	st.mu.Unlock()
	if statePath == "" {
		return nil
	}
	sort.Slice(h.Failing, func(i, j int) bool { return h.Failing[i].Since.Before(h.Failing[j].Since) })
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	path := HealthPath(statePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadHealth reads the report of the collector that uses statePath. ok is false
// when there is none or it is stale (the collector is not running).
func ReadHealth(statePath string) (h Health, ok bool) {
	b, err := os.ReadFile(HealthPath(statePath))
	if err != nil || json.Unmarshal(b, &h) != nil {
		return Health{}, false
	}
	if time.Since(h.UpdatedAt) > healthStale {
		return Health{}, false
	}
	return h, true
}

// Describe names what is failing, for `ctx status`.
func (h Health) Describe() string {
	if len(h.Failing) == 0 {
		return "uploading normally"
	}
	oldest := h.Failing[0]
	for _, f := range h.Failing {
		if f.Since.Before(oldest.Since) {
			oldest = f
		}
	}
	return fmt.Sprintf("%d transcript(s) could not be uploaded, the oldest since %s: %s (it tries again every sweep)",
		len(h.Failing), oldest.Since.Local().Format("15:04"), strings.TrimSpace(shortErr(oldest.Error)))
}

func shortErr(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
