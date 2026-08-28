package hookstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const recordInterval = 30 * time.Second

type Receipt struct {
	Harness string    `json:"harness"`
	Event   string    `json:"event"`
	SeenAt  time.Time `json:"seen_at"`
}

type stateFile struct {
	Receipts map[string]Receipt `json:"receipts"`
}

var mu sync.Mutex

func DefaultPath() string {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		return filepath.Join(root, "ghosttree", "hook-activity.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "ghosttree", "hook-activity.json")
	}
	return "ghosttree-hook-activity.json"
}

func Record(harness, event string) error {
	return RecordAt(harness, event, time.Now().UTC())
}

func RecordAt(harness, event string, seenAt time.Time) error {
	mu.Lock()
	defer mu.Unlock()
	path := DefaultPath()
	unlock, err := lockState(path)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := load(path)
	if err != nil {
		return err
	}
	key := harness + "/" + event
	if previous, ok := state.Receipts[key]; ok && seenAt.Sub(previous.SeenAt) < recordInterval {
		return nil
	}
	state.Receipts[key] = Receipt{Harness: harness, Event: event, SeenAt: seenAt.UTC()}
	return save(path, state)
}

func Latest(harness, event string) (Receipt, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	path := DefaultPath()
	unlock, err := lockState(path)
	if err != nil {
		return Receipt{}, false, err
	}
	defer unlock()
	state, err := load(path)
	if err != nil {
		return Receipt{}, false, err
	}
	receipt, ok := state.Receipts[harness+"/"+event]
	return receipt, ok, nil
}

func lockState(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(lock.Name(), 0o600); err != nil {
		lock.Close()
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func load(path string) (stateFile, error) {
	state := stateFile{Receipts: map[string]Receipt{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.Receipts == nil {
		state.Receipts = map[string]Receipt{}
	}
	return state, nil
}

func save(path string, state stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hook-activity-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
