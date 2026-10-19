package collector

import (
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
	Files map[string]*fileState `json:"files"`
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
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
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
