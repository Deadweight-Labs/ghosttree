//go:build !linux

package main

// hideFromSessions has nothing to do outside Linux: the gardener's sandbox is
// a systemd unit.
func hideFromSessions() error { return nil }
