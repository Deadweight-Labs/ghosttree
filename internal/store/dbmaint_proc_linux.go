//go:build linux

package store

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type fileID struct{ dev, ino uint64 }

// scanDatabaseHolders sucht unter /proc die Prozesse, die die Datenbank, ihr
// -wal oder ihr -shm offen haben. Verglichen wird nach Gerät und Inode, nicht
// nach Pfad: ein Dienst mit eigenem Mount-Namespace sieht dieselbe Datei unter
// einem anderen Pfad. Der eigene Prozess zählt nicht.
func scanDatabaseHolders(dbPath string) HolderScan {
	res := HolderScan{Supported: true}
	var targets []fileID
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			targets = append(targets, fileID{uint64(st.Dev), uint64(st.Ino)})
		}
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return HolderScan{}
	}
	self := os.Getpid()
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == self {
			continue
		}
		fdDir := "/proc/" + p.Name() + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				res.Unreadable++
			}
			continue
		}
		for _, fd := range fds {
			info, err := os.Stat(fdDir + "/" + fd.Name())
			if err != nil {
				continue
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				continue
			}
			id := fileID{uint64(st.Dev), uint64(st.Ino)}
			if containsFileID(targets, id) {
				res.Holders = append(res.Holders, DBHolder{PID: pid, Command: processCommand(p.Name())})
				break
			}
		}
	}
	return res
}

func containsFileID(ids []fileID, id fileID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// processCommand liefert die Kommandozeile eines Prozesses, gekürzt, für die Meldung.
func processCommand(pid string) string {
	b, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil || len(b) == 0 {
		b, err = os.ReadFile("/proc/" + pid + "/comm")
		if err != nil {
			return ""
		}
	}
	cmd := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
	if len(cmd) > 100 {
		cmd = cmd[:100] + "..."
	}
	return cmd
}
