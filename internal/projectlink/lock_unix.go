//go:build !windows

package projectlink

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lock serializes read-modify-write cycles on the registry across processes.
// It locks a sibling file, never the registry itself, because the registry is
// replaced by rename.
func lock() (func(), error) {
	path := Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
