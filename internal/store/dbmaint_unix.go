//go:build unix

package store

import (
	"os"
	"syscall"
)

// freeDiskBytes ist der Platz, den ein gewöhnlicher Benutzer im Dateisystem
// von dir noch belegen darf.
func freeDiskBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// sameFilesystem sagt, ob beide Verzeichnisse auf einem Dateisystem liegen. Im
// Zweifel ja: dann müssen beide Bedarfe zusammen hineinpassen.
func sameFilesystem(a, b string) bool {
	da, ok := deviceOf(a)
	db, ok2 := deviceOf(b)
	if !ok || !ok2 {
		return true
	}
	return da == db
}

func deviceOf(dir string) (uint64, bool) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
