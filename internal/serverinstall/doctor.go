package serverinstall

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var goos = runtime.GOOS

// Check is one line of the server check, shaped like the lines of ctx doctor.
type Check struct {
	Name       string
	OK         bool
	Unverified bool
	Detail     string
	Fix        string
}

// OptionsFromDisk recovers the answers of an installation from its server.env,
// for ctx server doctor and ctx server update. A missing file gives the defaults.
func OptionsFromDisk(prefix string) Options {
	o := Options{Prefix: prefix}
	f, err := os.Open(o.path(ServerEnv))
	if err != nil {
		return o
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		switch k {
		case "GHOSTTREE_LISTEN":
			o.Listen = v
		case "GHOSTTREE_PUBLIC_URL":
			o.Domain = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
		case "GHOSTTREE_TLS_ACME_HOST":
			o.TLS = "builtin"
		case "GHOSTTREE_LANGUAGE":
			o.Language = v
		}
	}
	return o
}

// Doctor checks what a server needs to be well: the service, the answer on its
// port, the key, the nightly backup, the database, the gardener's account and,
// if a name was given, whether that name reaches the server from here.
func Doctor(ctx context.Context, o Options, env Env) []Check {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }
	local := "http://" + localAddr(o.listen())

	if out, err := env.Sys.Run(ctx, "systemctl", "is-active", ServiceID); err != nil || strings.TrimSpace(out) != "active" {
		add(Check{Name: "service", Detail: ServiceID + " is " + orDefault(strings.TrimSpace(out), "not running"), Fix: "journalctl -u ghosttree -n 50"})
	} else {
		add(Check{Name: "service", OK: true, Detail: ServiceID + " is active"})
	}

	if _, err := env.Sys.Get(ctx, local+"/api/health"); err != nil {
		add(Check{Name: "server", Detail: fmt.Sprintf("no answer on %s: %v", local, err), Fix: "systemctl restart ghosttree"})
	} else {
		detail := "answers on " + local
		if b, err := env.Sys.Get(ctx, local+"/api/version"); err == nil {
			if v := jsonField(b, "version"); v != "" {
				detail += ", ctx " + v
			}
		}
		add(Check{Name: "server", OK: true, Detail: detail})
	}

	if st, err := os.Stat(o.path(SecretKey)); err != nil {
		add(Check{Name: "server key", Detail: SecretKey + " is missing: stored AI keys cannot be sealed", Fix: "ctx server install"})
	} else if st.Mode().Perm()&0o077 != 0 {
		add(Check{Name: "server key", Detail: fmt.Sprintf("%s is readable by others (%v)", SecretKey, st.Mode().Perm()), Fix: "chmod 600 " + SecretKey})
	} else {
		add(Check{Name: "server key", OK: true, Detail: SecretKey + " (keep a copy with your backups)"})
	}

	// Roles are enforced unless the environment file switches them off.
	if envText, _ := os.ReadFile(o.path(ServerEnv)); !accessSwitchedOff(string(envText)) {
		add(Check{Name: "access control", OK: true, Detail: "roles are enforced"})
	} else {
		add(Check{Name: "access control", Detail: "GHOSTTREE_ENFORCE_ACCESS=0 in " + ServerEnv + ": roles inside an organization are only logged, not enforced", Fix: "remove GHOSTTREE_ENFORCE_ACCESS=0 and restart"})
	}

	if out, err := env.Sys.Run(ctx, "systemctl", "is-active", BackupTime); err != nil || strings.TrimSpace(out) != "active" {
		add(Check{Name: "backup timer", Detail: BackupTime + " is " + orDefault(strings.TrimSpace(out), "not active"), Fix: "systemctl enable --now " + BackupTime})
	} else {
		add(Check{Name: "backup timer", OK: true, Detail: BackupTime + " is active (nightly, newest 14 kept in " + BackupDir + ")"})
	}
	if name, age, ok := newestBackup(o.path(BackupDir)); !ok {
		add(Check{Name: "backup", Detail: "no copy in " + BackupDir + " yet", Fix: "systemctl start " + BackupID})
	} else if age > 36*time.Hour {
		add(Check{Name: "backup", Detail: fmt.Sprintf("the newest copy, %s, is %s old", name, age.Round(time.Hour)), Fix: "journalctl -u " + BackupID})
	} else {
		add(Check{Name: "backup", OK: true, Detail: "newest copy " + name})
	}

	if out, err := asService(ctx, env, "db", "check", "--db", DBPath); err != nil || !strings.Contains(out, "ok") {
		add(Check{Name: "database", Detail: "ctx db check: " + firstLine(out, err), Fix: "restore a copy from " + BackupDir})
	} else {
		add(Check{Name: "database", OK: true, Detail: "integrity check ok"})
	}

	if out, err := asService(ctx, env, "account", "list", "--db", DBPath); err != nil {
		add(Check{Name: "accounts", Detail: firstLine(out, err)})
	} else {
		gardener, admin := strings.Contains(out, "gardener=true"), strings.Contains(out, "admin=true")
		switch {
		case !admin:
			add(Check{Name: "accounts", Detail: "no administrator", Fix: "ctx account add <name> --admin --db " + DBPath})
		case !gardener:
			add(Check{Name: "accounts", Detail: "no gardener account: nobody may write gardener entries", Fix: "ctx account set-gardener <name> --db " + DBPath})
		default:
			add(Check{Name: "accounts", OK: true, Detail: "an administrator and the gardener's account exist"})
		}
	}

	if o.Domain != "" {
		if _, err := env.Sys.Get(ctx, o.PublicURL()+"/api/health"); err != nil {
			fix := "point " + o.Domain + " at this machine and open ports 80 and 443"
			if o.tls() == "proxy" {
				fix = "point " + o.Domain + " at this machine and set up your proxy: " + ProxyDir + "/Caddyfile or nginx.conf"
			}
			add(Check{Name: "public address", Unverified: true, Detail: o.PublicURL() + " is not reachable from here yet (" + shortErr(err) + ")", Fix: fix})
		} else {
			add(Check{Name: "public address", OK: true, Detail: o.PublicURL() + " answers"})
		}
	}
	return checks
}

// Healthy reports whether no check failed (an unverified one is not a failure).
func Healthy(checks []Check) bool {
	for _, c := range checks {
		if !c.OK && !c.Unverified {
			return false
		}
	}
	return true
}

func newestBackup(dir string) (name string, age time.Duration, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, false
	}
	var newest time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest, name = info.ModTime(), filepath.Base(e.Name())
		}
	}
	if name == "" {
		return "", 0, false
	}
	return name, time.Since(newest), true
}

func jsonField(b []byte, key string) string {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func firstLine(out string, err error) string {
	if l := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]); l != "" {
		return l
	}
	if err != nil {
		return err.Error()
	}
	return "no answer"
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// accessSwitchedOff reports whether an environment file sets
// GHOSTTREE_ENFORCE_ACCESS to 0; the last assignment wins, as for systemd.
func accessSwitchedOff(envText string) bool {
	off := false
	for _, line := range strings.Split(envText, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if v, ok := strings.CutPrefix(line, "GHOSTTREE_ENFORCE_ACCESS="); ok {
			off = strings.Trim(strings.TrimSpace(v), `"'`) == "0"
		}
	}
	return off
}
