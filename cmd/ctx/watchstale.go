package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// unitState beschreibt die Watch-Unit des Nutzers laut systemd.
type unitState struct {
	Active   bool
	MainPID  int
	ExecPath string
}

// watchUnitState fragt systemd; Tests ersetzen es, damit kein echtes systemctl
// läuft. Ohne systemctl oder ohne Unit ist der Zustand leer.
var watchUnitState = func() unitState {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return unitState{}
	}
	out, err := exec.Command(path, "--user", "show", "-p", "ActiveState", "-p", "MainPID", "-p", "ExecStart", watchUnit).Output()
	if err != nil {
		return unitState{}
	}
	return parseUnitShow(string(out))
}

var execStartPath = regexp.MustCompile(`path=([^ ;]+)`)

func parseUnitShow(out string) unitState {
	var u unitState
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			u.Active = v == "active"
		case "MainPID":
			u.MainPID, _ = strconv.Atoi(v)
		case "ExecStart":
			if m := execStartPath.FindStringSubmatch(v); m != nil {
				u.ExecPath = m[1]
			}
		}
	}
	return u
}

// installedCtx ist das ctx, das die Watch-Unit startet (ExecStart), sonst
// ~/.local/bin/ctx, wohin ctx install es legt, erst zuletzt das ctx vom PATH.
func installedCtx(u unitState) string {
	if u.ExecPath != "" {
		return u.ExecPath
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "ctx")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ctx"); err == nil {
		return p
	}
	p, _ := os.Executable()
	return p
}

// runningWatch findet den Watch-Prozess und das Binary, das er haben sollte.
// Läuft die Unit, zählt ihr MainPID, nie die pid-Datei: sie kann von einem
// beendeten Lauf stammen, und ihre pid kann inzwischen jemand anderem gehören.
// Ohne Unit zählt die pid-Datei, sofern der Prozess ein "watch" ist.
func runningWatch() (pid int, installed string, ok bool) {
	u := watchUnitState()
	if u.Active && u.MainPID > 0 {
		return u.MainPID, installedCtx(u), true
	}
	pid, running := watchProcess()
	if !running {
		return 0, "", false
	}
	if cmd, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline")); err == nil {
		if !slices.Contains(strings.Split(string(cmd), "\x00"), "watch") {
			return 0, "", false
		}
	}
	return pid, installedCtx(u), true
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
	pid, installed, running := runningWatch()
	if !running {
		return installer.Check{}, false
	}
	check := installer.Check{Name: "collector binary", Detail: fmt.Sprintf("watch pid %d runs the installed ctx", pid), OK: true}
	if why := staleWatchReason(pid, installed); why != "" {
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
	pid, installed, running := runningWatch()
	if !running {
		return
	}
	why := staleWatchReason(pid, installed)
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
