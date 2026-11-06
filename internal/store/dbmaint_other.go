//go:build !unix

package store

import "errors"

// Ohne statfs lässt sich der freie Platz nicht lesen; VACUUM verweigert dann,
// statt blind zu laufen.
func freeDiskBytes(string) (uint64, error) {
	return 0, errors.New("free disk space cannot be read on this platform")
}

func sameFilesystem(string, string) bool { return true }
