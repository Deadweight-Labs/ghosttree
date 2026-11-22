//go:build linux

package main

import (
	"os"
	"syscall"
	"testing"
)

// After hideFromSessions the files of this process under /proc belong to root:
// a session of the same user cannot open its environment or memory.
func TestHideFromSessionsTakesTheProcFilesFromTheSameUser(t *testing.T) {
	owner := func() uint32 {
		info, err := os.Stat("/proc/self/environ")
		if err != nil {
			t.Skipf("no /proc: %v", err)
		}
		return info.Sys().(*syscall.Stat_t).Uid
	}
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	if owner() != uint32(os.Getuid()) {
		t.Skip("the test process is not dumpable to begin with")
	}
	if err := hideFromSessions(); err != nil {
		t.Fatal(err)
	}
	defer setDumpable(1)
	if got := owner(); got != 0 {
		t.Errorf("/proc/self/environ belongs to %d, want root (0)", got)
	}
}
