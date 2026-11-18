//go:build linux

package main

import (
	"syscall"
)

const prSetDumpable = 4 // PR_SET_DUMPABLE

// hideFromSessions marks this process as not dumpable. The sessions the gardener
// starts run as the same user; with the flag cleared the kernel keeps them from
// reading /proc/<pid>/environ and /proc/<pid>/mem of this process, where the
// gardener's key and token are. A program started afterwards is dumpable again
// (execve resets the flag), so the sessions themselves are not affected.
func hideFromSessions() error {
	return setDumpable(0)
}

func setDumpable(v uintptr) error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, v, 0); errno != 0 {
		return errno
	}
	return nil
}
