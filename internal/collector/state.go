package collector

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileState is the per-transcript progress. The offset IS the offline queue:
// it only advances after the server confirmed the upload, so a failed or
// offline run replays the same lines on the next sweep.
type fileState struct {
	Offset    int64 `json:"offset"`
	SessionID int64 `json:"session_id"`
	// PublicID ist die Adresse der Session, wenn der Server keine Nummer nennt
	// (der Token gehört einem Gast).
	PublicID        string `json:"public_id,omitempty"`
	Seq             int    `json:"seq"`
	MetadataVersion int    `json:"metadata_version,omitempty"`
}

type State struct {
	mu   sync.Mutex
	path string
	// retry hält je Transkript fest, wann die Anmeldung wieder versucht wird,
	// nachdem der Server keine brauchbare Session genannt hat (nur im Speicher).
	retry map[string]*retryState
	// idents hält die Herkunft je Transkript im Speicher, damit eine aktive
	// Session nicht bei jeder neuen Zeile Kopfzeilen liest und git startet.
	idents map[string]identEntry
	// gitScans hält je Transkript die Git-Aufrufe, die auf ihr Ergebnis warten
	// (nur im Speicher, siehe gitScan).
	gitScans map[string]*gitScan
	// saved ist der zuletzt geschriebene Inhalt; unverändert wird nicht geschrieben.
	saved []byte
	// lastWrite und dirty bündeln das Speichern während einer aktiven Session
	// (siehe SaveSoon).
	lastWrite time.Time
	dirty     bool
	Files     map[string]*fileState `json:"files"`
}

type identEntry struct {
	ident touchIdentity
	at    time.Time
}

// identTTL begrenzt, wie lange eine zwischengespeicherte Herkunft gilt: ein
// später verlinkter Ordner oder ein neues origin kommt so ohne Neustart an.
const identTTL = 10 * time.Minute

func (st *State) identity(path string, compute func() touchIdentity) touchIdentity {
	st.mu.Lock()
	e, ok := st.idents[path]
	st.mu.Unlock()
	if ok && backoffNow().Sub(e.at) < identTTL {
		return e.ident
	}
	id := compute()
	st.mu.Lock()
	if st.idents == nil {
		st.idents = map[string]identEntry{}
	}
	st.idents[path] = identEntry{ident: id, at: backoffNow()}
	st.mu.Unlock()
	return id
}

// gitScanner gibt den Leser der Git-Vorgänge dieses Transkripts zurück. Die
// Zuordnung zu einer Session kann sich ändern (späte Metadaten); der Leser
// behält dann seine wartenden Aufrufe und übernimmt die neue Kennung.
func (st *State) gitScanner(path, harness, externalID string) *gitScan {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.gitScans == nil {
		st.gitScans = map[string]*gitScan{}
	}
	g, ok := st.gitScans[path]
	if !ok {
		g = newGitScan(harness, externalID)
		st.gitScans[path] = g
	}
	g.harness, g.externalID = harness, externalID
	return g
}

func DefaultStatePath() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".local", "state", "ghosttree", "state.json")
	}
	return "ghosttree-state.json"
}

func LoadState(path string) (*State, error) {
	st := &State{path: path, Files: map[string]*fileState{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	if st.Files == nil {
		st.Files = map[string]*fileState{}
	}
	return st, nil
}

func (st *State) Save() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if bytes.Equal(b, st.saved) {
		return nil
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.saved = b
	st.lastWrite = backoffNow()
	st.dirty = false
	return nil
}

// saveEvery ist der Mindestabstand zwischen zwei Schreibvorgängen aus SaveSoon.
const saveEvery = 5 * time.Second

// SaveSoon schreibt den Zustand, wenn der letzte Schreibvorgang mindestens
// saveEvery zurückliegt, und merkt ihn sonst für SaveIfDirty vor. Die Datei
// hält tausende Transkripte; sie bei jeder neuen Zeile einer aktiven Session
// neu zu schreiben kostete Megabytes pro Sekunde. Geht ein vorgemerkter Stand
// verloren, werden höchstens diese Sekunden noch einmal hochgeladen.
func (st *State) SaveSoon() error {
	st.mu.Lock()
	due := backoffNow().Sub(st.lastWrite) >= saveEvery
	if !due {
		st.dirty = true
	}
	st.mu.Unlock()
	if !due {
		return nil
	}
	return st.Save()
}

// SaveIfDirty schreibt einen von SaveSoon vorgemerkten Stand.
func (st *State) SaveIfDirty() error {
	st.mu.Lock()
	dirty := st.dirty
	st.mu.Unlock()
	if !dirty {
		return nil
	}
	return st.Save()
}

func (st *State) file(path string) *fileState {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, ok := st.Files[path]
	if !ok {
		f = &fileState{}
		st.Files[path] = f
	}
	return f
}

type retryState struct {
	fails int
	until time.Time
}

// backoffNow ist die Uhr des Backoffs; Tests setzen sie.
var backoffNow = time.Now

const (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)

func (st *State) backoffLeft(path string) time.Duration {
	st.mu.Lock()
	defer st.mu.Unlock()
	if r := st.retry[path]; r != nil {
		return r.until.Sub(backoffNow())
	}
	return 0
}

func (st *State) backoffFail(path string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.retry == nil {
		st.retry = map[string]*retryState{}
	}
	r := st.retry[path]
	if r == nil {
		r = &retryState{}
		st.retry[path] = r
	}
	d := backoffBase << min(r.fails, 5)
	r.fails++
	r.until = backoffNow().Add(min(d, backoffMax))
}

func (st *State) backoffClear(path string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.retry, path)
}
