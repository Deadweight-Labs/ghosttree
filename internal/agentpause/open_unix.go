//go:build !windows

package agentpause

import "syscall"

// openFlags keeps an open from blocking if the path was swapped for a FIFO
// between the Lstat and the open.
const openFlags = syscall.O_NONBLOCK
