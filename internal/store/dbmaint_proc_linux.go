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
	owner := -1
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			targets = append(targets, fileID{uint64(st.Dev), uint64(st.Ino)})
			if owner < 0 {
				owner = int(st.Uid)
			}
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
				// A process of one's own user that hides its files is not
				// dumpable (ssh-agent and the like); a server of the database's
				// owner is suspect only when that owner is somebody else.
				if (owner != os.Getuid() && processUID(p.Name()) == owner) || mayBeServer(processCommand(p.Name())) {
					res.Suspect++
				}
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

// processUID is the real user of a process, -1 when unknown.
func processUID(pid string) int {
	b, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				if uid, err := strconv.Atoi(f[0]); err == nil {
					return uid
				}
			}
		}
	}
	return -1
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
