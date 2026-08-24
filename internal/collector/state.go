package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// fileState is the per-transcript progress. The offset IS the offline queue:
// it only advances after the server confirmed the upload, so a failed or
// offline run replays the same lines on the next sweep.
type fileState struct {
	Offset          int64 `json:"offset"`
	SessionID       int64 `json:"session_id"`
	Seq             int   `json:"seq"`
	MetadataVersion int   `json:"metadata_version,omitempty"`
}

type State struct {
	mu    sync.Mutex
	path  string
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
