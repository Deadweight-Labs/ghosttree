// Package testtmp moves the temporary directory of a test binary into RAM.
package testtmp

import (
	"os"
	"testing"
)

// shm is the RAM-backed directory on Linux.
const shm = "/dev/shm"

// Run is the body of a TestMain: when TMPDIR is not set and /dev/shm is
// writable, t.TempDir() and os.MkdirTemp land in a fresh directory below
// /dev/shm, removed again afterwards. An explicit TMPDIR always wins, and
// GHOSTTREE_TEST_DISK=1 keeps the default.
//
// Every fresh SQLite store runs its migrations as separate commits, each
// waiting for fsync; on a disk that made one open take seconds.
func Run(m *testing.M) int {
	dir := use()
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	return code
}

func use() string {
	if os.Getenv("TMPDIR") != "" || os.Getenv("GHOSTTREE_TEST_DISK") == "1" {
		return ""
	}
	if info, err := os.Stat(shm); err != nil || !info.IsDir() {
		return ""
	}
	dir, err := os.MkdirTemp(shm, "gotest-")
	if err != nil {
		return ""
	}
	if err := os.Setenv("TMPDIR", dir); err != nil {
		_ = os.RemoveAll(dir)
		return ""
	}
	return dir
}
