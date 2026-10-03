package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

// Variablen, damit Tests weder /proc noch systemctl des Rechners anfassen.
var (
	procRoot      = "/proc"
	systemctlUser = func(args ...string) error {
		path, err := exec.LookPath("systemctl")
		if err != nil {
			return err
		}
		return exec.Command(path, append([]string{"--user"}, args...)...).Run()
	}
)

const watchUnit = "ghosttree-watch.service"

// installedCtx ist das ctx, das ein neuer Aufruf auf dem PATH fände.
func installedCtx() string {
	if p, err := exec.LookPath("ctx"); err == nil {
		return p
	}
	p, _ := os.Executable()
	return p
}

// staleWatchReason sagt, warum der Prozess pid nicht das installierte Binary
// ausführt ("" wenn er es tut oder sich das nicht feststellen lässt). Ein
// ersetztes Binary lässt den laufenden Prozess mit dem alten Programm zurück,
// das /proc/<pid>/exe als "(deleted)" meldet.
func staleWatchReason(pid int, installed string) string {
	link, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	if err != nil {
		return ""
	}
	if strings.HasSuffix(link, " (deleted)") {
		return "its binary was replaced or deleted (" + link + ")"
	}
	running, err1 := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	current, err2 := os.Stat(installed)
	if err1 != nil || err2 != nil {
		return ""
	}
	if !os.SameFile(running, current) {
		return "it runs " + link + ", not the installed " + installed
	}
	return ""
}

// watchBinaryCheck warnt, wenn der laufende Watch-Prozess ein anderes Binary
// nutzt als das installierte ctx: ein alter Collector versteht neue
// Serverantworten nicht und lädt dann womöglich nach Nummer 0.
func watchBinaryCheck() (installer.Check, bool) {
	pid, running := watchProcess()
	if !running {
		return installer.Check{}, false
	}
	check := installer.Check{Name: "collector binary", Detail: fmt.Sprintf("watch pid %d runs the installed ctx", pid), OK: true}
	if why := staleWatchReason(pid, installedCtx()); why != "" {
		check.OK = false
		check.Detail = fmt.Sprintf("watch pid %d is outdated: %s", pid, why)
		check.Fix = "systemctl --user restart " + watchUnit
	}
	return check, true
}

// restartStaleWatch startet die Watch-Unit des Nutzers neu, wenn sie läuft und
// ein anderes Binary ausführt als das installierte. Nur die User-Unit; läuft
// der Collector von Hand, bleibt er unberührt.
func restartStaleWatch(stdout io.Writer) {
	pid, running := watchProcess()
	if !running {
		return
	}
	why := staleWatchReason(pid, installedCtx())
	if why == "" {
		return
	}
	if err := systemctlUser("is-active", "--quiet", watchUnit); err != nil {
		fmt.Fprintf(stdout, "collector   watch pid %d is outdated (%s); restart it with 'ctx watch'\n", pid, why)
		return
	}
	if err := systemctlUser("restart", watchUnit); err != nil {
		fmt.Fprintf(stdout, "collector   watch pid %d is outdated (%s); restarting %s failed: %v\n", pid, why, watchUnit, err)
		return
	}
	fmt.Fprintf(stdout, "restarted   %s (it ran an outdated ctx: %s)\n", watchUnit, why)
}
