package installer

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// The collector (`ctx watch`) uploads transcripts; without it no session ever
// appears on the server and request_start_work fails. It runs as a user
// service so it starts at login and needs no root.

const (
	// WatchUnit is the systemd user unit of the collector.
	WatchUnit = "ghosttree-watch.service"
	// WatchLaunchdLabel is the launchd label of the collector on macOS.
	WatchLaunchdLabel = "com.deadweightlabs.ghosttree-watch"
)

// serviceGOOS and serviceControl are seams: tests must never reach the real
// systemctl or launchctl of the machine they run on.
var (
	serviceGOOS    = runtime.GOOS
	serviceControl = runServiceControl
	serviceUID     = os.Getuid
)

// runServiceControl runs systemctl --user or launchctl and returns its combined
// output as the error text, because that is where the reason is.
func runServiceControl(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := exec.Command(path, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("%v: %s", err, bounded(msg, 300))
		}
		return err
	}
	return nil
}

// WatchServicePath is where the service definition lives for this platform, ""
// where there is no supported service manager.
func WatchServicePath(home string) string {
	switch serviceGOOS {
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", WatchUnit)
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", WatchLaunchdLabel+".plist")
	}
	return ""
}

// systemdQuote makes one word safe for ExecStart: quotes around anything with
// whitespace or quotes, and % doubled because systemd expands specifiers.
func systemdQuote(w string) string {
	w = strings.ReplaceAll(w, "%", "%%")
	if !strings.ContainsAny(w, " \t\"'\\") {
		return w
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(w) + `"`
}

func watchUnitText(ctx string) string {
	return "[Unit]\nDescription=ghosttree session collector\nAfter=network-online.target\n\n" +
		"[Service]\nExecStart=" + systemdQuote(ctx) + " watch\nRestart=on-failure\nRestartSec=30\n\n" +
		"[Install]\nWantedBy=default.target\n"
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func watchPlistText(ctx, logPath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + WatchLaunchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(ctx) + `</string>
		<string>watch</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(logPath) + `</string>
</dict>
</plist>
`
}

// InstallWatchService writes the collector's service definition with the
// configured ctx command and starts it. An existing definition is updated in
// place. A service manager that cannot be reached (no user bus in an SSH
// session, no launchd in a container) is not an error: the file is written and
// the returned change says what to run.
func InstallWatchService(home string) ([]Change, error) {
	path := WatchServicePath(home)
	if path == "" {
		return []Change{{Path: serviceGOOS, Action: "collector service not supported on this platform; run 'ctx watch'"}}, nil
	}
	if !filepath.IsAbs(ctxCommand) {
		return []Change{{Path: path, Action: "collector service skipped: ctx path is not absolute"}}, nil
	}
	var text string
	if serviceGOOS == "darwin" {
		text = watchPlistText(ctxCommand, filepath.Join(home, "Library", "Logs", "ghosttree-watch.log"))
	} else {
		text = watchUnitText(ctxCommand)
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	changed := string(old) != text
	action := "unchanged"
	if changed {
		action = "updated"
		if len(old) == 0 {
			action = "created"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if serviceGOOS == "darwin" {
			if err := os.MkdirAll(filepath.Join(home, "Library", "Logs"), 0o755); err != nil {
				return nil, err
			}
		}
		if err := writeAtomic(path, []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	changes := []Change{{Path: path, Action: action}}
	var startErr error
	if serviceGOOS == "darwin" {
		startErr = startLaunchAgent(path, changed)
	} else {
		startErr = startSystemdUnit(changed, len(old) > 0)
	}
	if startErr != nil {
		changes = append(changes, Change{Path: path, Action: "collector not started (" + startErr.Error() + "); start it with 'ctx watch' or " + manualStartHint()})
	} else {
		changes = append(changes, Change{Path: path, Action: "collector running"})
	}
	return changes, nil
}

func manualStartHint() string {
	if serviceGOOS == "darwin" {
		return "'launchctl bootstrap gui/" + strconv.Itoa(serviceUID()) + " <plist>'"
	}
	return "'systemctl --user enable --now " + WatchUnit + "'"
}

func startSystemdUnit(changed, existed bool) error {
	if changed {
		if err := serviceControl("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
	}
	if err := serviceControl("systemctl", "--user", "enable", "--now", WatchUnit); err != nil {
		return err
	}
	if changed && existed {
		// enable --now leaves an already running service on its old command line.
		return serviceControl("systemctl", "--user", "restart", WatchUnit)
	}
	return nil
}

func startLaunchAgent(plist string, changed bool) error {
	domain := "gui/" + strconv.Itoa(serviceUID())
	target := domain + "/" + WatchLaunchdLabel
	loaded := serviceControl("launchctl", "print", target) == nil
	if loaded && !changed {
		return nil
	}
	if loaded {
		// A definition that changed on disk is only read again after bootout.
		_ = serviceControl("launchctl", "bootout", target)
	}
	return serviceControl("launchctl", "bootstrap", domain, plist)
}

// Service states reported by WatchServiceState.
const (
	ServiceActive   = "active"
	ServiceMissing  = "missing"
	ServiceInactive = "inactive"
	ServiceUnknown  = "unknown"
)

// WatchServiceState asks the platform's service manager about the collector
// and returns one of the Service* states with the manager's own words.
func WatchServiceState() (state, detail string) {
	switch serviceGOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return ServiceUnknown, "systemd user manager unavailable"
		}
		out, err := exec.Command("systemctl", "--user", "show", WatchUnit, "--property=LoadState", "--property=ActiveState", "--value").Output()
		if err != nil {
			return ServiceUnknown, "systemd user state unavailable"
		}
		values := strings.Fields(string(out))
		for _, v := range values {
			if v == "not-found" {
				return ServiceMissing, WatchUnit + " not installed"
			}
		}
		for _, v := range values {
			if v == "active" {
				return ServiceActive, WatchUnit + " active"
			}
		}
		return ServiceInactive, WatchUnit + " " + strings.Join(values, "/")
	case "darwin":
		target := "gui/" + strconv.Itoa(serviceUID()) + "/" + WatchLaunchdLabel
		if _, err := exec.LookPath("launchctl"); err != nil {
			return ServiceUnknown, "launchctl unavailable"
		}
		if err := exec.Command("launchctl", "print", target).Run(); err != nil {
			return ServiceMissing, WatchLaunchdLabel + " not loaded"
		}
		return ServiceActive, WatchLaunchdLabel + " loaded"
	}
	return ServiceUnknown, "no service manager on this platform"
}

// SetServiceBackend replaces the platform and the service-manager runner and
// returns a function that restores them. It exists so tests of any package can
// run the installer without reaching the systemctl or launchctl of the machine.
func SetServiceBackend(goos string, ctl func(name string, args ...string) error, uid int) (restore func()) {
	prevOS, prevCtl, prevUID := serviceGOOS, serviceControl, serviceUID
	serviceGOOS, serviceControl, serviceUID = goos, ctl, func() int { return uid }
	return func() { serviceGOOS, serviceControl, serviceUID = prevOS, prevCtl, prevUID }
}
