package hookbudget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
)

const Limit = 24000

const notice = "\n\n[ghosttree: Session hook context budget reached (24000 characters). This response was shortened; further automatic context is suppressed. Fetch full context with `context_get` or start at `.ghosttree/INDEX.md`.]\n"

var ErrBusy = errors.New("context budget lock timed out")

type Receipt struct {
	Version       int       `json:"version"`
	SessionHash   string    `json:"session_hash"`
	ReservedChars int       `json:"reserved_chars"`
	EmittedChars  int       `json:"emitted_chars"`
	Exhausted     bool      `json:"exhausted"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func stateDir() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("context budget state directory must be absolute (XDG_STATE_HOME or HOME)")
	}
	return filepath.Join(root, "ghosttree", "context-budget"), nil
}

func Deliver(sessionID, text string, emit func(string) error) error {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 4096 {
		return errors.New("context budget requires a session identity")
	}
	if text == "" {
		return emit("")
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(sessionID))
	key := hex.EncodeToString(digest[:])
	path := filepath.Join(dir, key+".json")
	unlock, err := lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	r, err := readReceipt(path, key)
	if errors.Is(err, os.ErrNotExist) {
		r = Receipt{Version: 1, SessionHash: key, StartedAt: time.Now().UTC()}
	} else if err != nil {
		return err
	}
	if r.Exhausted {
		return emit("")
	}
	text = strings.ToValidUTF8(text, "�")
	n := utf8.RuneCountInString(text)
	available := Limit - r.ReservedChars - utf8.RuneCountInString(notice)
	if n > available {
		text = string([]rune(text)[:available]) + notice
		n = utf8.RuneCountInString(text)
		r.Exhausted = true
	}
	r.ReservedChars += n
	r.UpdatedAt = time.Now().UTC()
	if err := saveReceipt(path, r); err != nil {
		return err
	}
	if err := emit(text); err != nil {
		return err
	}
	r.EmittedChars += n
	return saveReceipt(path, r)
}

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrBusy
		}
		time.Sleep(time.Millisecond)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func readReceipt(path, key string) (Receipt, error) {
	var r Receipt
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return r, fmt.Errorf("invalid context budget receipt: %w", err)
	}
	for _, key := range []string{"version", "session_hash", "reserved_chars", "emitted_chars", "exhausted", "started_at", "updated_at"} {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return r, fmt.Errorf("context budget receipt missing %s", key)
		}
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("invalid context budget receipt: %w", err)
	}
	if r.Version != 1 || r.SessionHash != key || len(key) != 64 || r.StartedAt.IsZero() ||
		r.UpdatedAt.Before(r.StartedAt) || r.ReservedChars < 0 || r.ReservedChars > Limit ||
		r.EmittedChars < 0 || r.EmittedChars > r.ReservedChars ||
		(!r.Exhausted && r.ReservedChars > Limit-utf8.RuneCountInString(notice)) {
		return r, errors.New("invalid context budget receipt counters or identity")
	}
	return r, nil
}

func saveReceipt(path string, r Receipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return privatefile.WriteSyncedNoFollow(path, append(raw, '\n'), 0o600)
}

func Recent() ([]Receipt, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []Receipt
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".json")
		r, err := readReceipt(filepath.Join(dir, entry.Name()), key)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, r)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].UpdatedAt.After(receipts[j].UpdatedAt) })
	return receipts, nil
}
