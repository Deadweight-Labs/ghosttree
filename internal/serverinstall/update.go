package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UpdateOptions are the answers of ctx server update.
type UpdateOptions struct {
	Prefix  string
	Version string // release tag such as v0.2.0; empty: the newest stable release
	// Force installs the release even if that version already runs.
	Force bool
}

// UpdateResult says what happened.
type UpdateResult struct {
	From, To   string
	Unchanged  bool // the version already ran
	RolledBack bool // the new release did not come up; the old one runs again
	Backup     string
	Checks     []Check
}

var versionLineRE = regexp.MustCompile(`^ctx (\S+)`)

// binVersion runs a ctx binary and reads its version.
func binVersion(ctx context.Context, env Env, path string) (string, error) {
	out, err := env.Sys.Run(ctx, path, "version")
	if err != nil {
		return "", fmt.Errorf("%s version: %v: %s", path, err, strings.TrimSpace(out))
	}
	m := versionLineRE.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("%s version: unexpected answer %q", path, strings.TrimSpace(out))
	}
	return m[1], nil
}

// asServiceBin is asService with another binary (the old one, after the new
// one has taken its place).
func asServiceBin(ctx context.Context, env Env, bin string, args ...string) (string, error) {
	full := append([]string{"--pipe", "--wait", "--collect", "--quiet",
		"-p", "DynamicUser=yes", "-p", "User=" + ServiceUser, "-p", "StateDirectory=ghosttree", "-p", "UMask=0077", bin}, args...)
	return env.Sys.Run(ctx, "systemd-run", full...)
}

// Update moves the server to another release: the release is fetched and
// checked first (signature and checksum, as install.sh does), then the service
// stops, the database is copied, the binary is replaced, the schema is brought
// up to date, and the service starts again. If it does not answer with the new
// version in time, the old binary and the copied database are put back and the
// service is started again: the update failed, the server did not.
func Update(ctx context.Context, uo UpdateOptions, rel Release, env Env) (*UpdateResult, error) {
	o := OptionsFromDisk(uo.Prefix)
	if uo.Prefix == "" {
		if goos != "linux" {
			return nil, errors.New("ctx server update works on a systemd server on Linux; for Docker run: docker compose pull && docker compose up -d")
		}
		if env.RequireRoot && os.Geteuid() != 0 {
			return nil, errors.New("this needs root: run it with sudo")
		}
	}
	bin := o.path(BinPath)
	if _, err := os.Stat(o.path(filepath.Join(UnitDir, ServiceID))); err != nil {
		return nil, fmt.Errorf("no ghosttree server is installed here (%s is missing); set one up with: ctx server install", ServiceID)
	}
	old, err := binVersion(ctx, env, bin)
	if err != nil {
		return nil, err
	}
	tag := uo.Version
	if tag == "" {
		if tag, err = rel.Latest(ctx); err != nil {
			return nil, err
		}
	} else if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	res := &UpdateResult{From: old, To: strings.TrimPrefix(tag, "v")}
	if res.To == old && !uo.Force {
		env.say("ghosttree %s already runs; nothing to do (--force installs it again)", old)
		res.Unchanged = true
		return res, nil
	}
	if IsDowngrade(old, res.To) && !uo.Force {
		return nil, fmt.Errorf("%s is older than the %s that runs here: going back is a downgrade, and the database of the newer release may not open with the older one; nothing was changed. Add --force to do it anyway (the database is copied first)", tag, old)
	}

	env.say("fetching %s and checking its signature", tag)
	got, err := rel.Fetch(ctx, tag, filepath.Dir(bin))
	if err != nil {
		return nil, err
	}
	defer os.Remove(got.Binary) // gone after the swap, and a failed update leaves nothing
	if v, err := binVersion(ctx, env, got.Binary); err != nil || v != got.Version {
		return nil, fmt.Errorf("the downloaded ctx does not run as %s (%v %s); nothing was changed", got.Version, err, v)
	}
	env.say("signature and checksum ok, ctx %s -> %s", old, got.Version)
	return moveTo(ctx, env, o, old, got.Binary, got.Version, res)
}

// moveTo is the part of an update that touches the server: stop it, copy the
// database with the old binary, swap in the new binary (a file next to it,
// already checked), bring the schema up to date and start it; if that does not
// end in a server that answers with the new version, everything goes back. o is
// the configuration on disk, which is what the old server runs with.
func moveTo(ctx context.Context, env Env, o Options, old, newBinary, newVersion string, res *UpdateResult) (*UpdateResult, error) {
	bin := o.path(BinPath)
	got := struct{ Binary, Version string }{newBinary, newVersion}
	local := "http://" + localAddr(o.listen())
	if out, err := env.Sys.Run(ctx, "systemctl", "stop", ServiceID); err != nil {
		return nil, fmt.Errorf("systemctl stop %s: %v: %s", ServiceID, err, strings.TrimSpace(out))
	}
	start := func() error {
		if out, err := env.Sys.Run(ctx, "systemctl", "start", ServiceID); err != nil {
			return fmt.Errorf("systemctl start %s: %v: %s", ServiceID, err, strings.TrimSpace(out))
		}
		return nil
	}
	// Until the binary is swapped nothing has changed, so a failure only
	// needs the service started again.
	abort := func(err error) (*UpdateResult, error) {
		if serr := start(); serr != nil {
			err = fmt.Errorf("%w; and the old server could not be started: %v", err, serr)
		}
		return nil, err
	}

	res.Backup = fmt.Sprintf("%s/pre-update-%s-to-%s-%s.db", BackupDir, safeName(old), safeName(got.Version), time.Now().UTC().Format("20060102-150405"))
	if out, err := asServiceBin(ctx, env, bin, "db", "backup", "--db", DBPath, "--out", res.Backup); err != nil {
		return abort(fmt.Errorf("the database could not be copied, so nothing was changed: %v: %s", err, strings.TrimSpace(out)))
	}
	env.say("database copied to %s", res.Backup)

	prev := bin + ".prev-" + safeName(old)
	if err := keepOld(bin, prev); err != nil {
		return abort(fmt.Errorf("cannot keep the old ctx: %w", err))
	}
	if err := os.Rename(got.Binary, bin); err != nil {
		return abort(fmt.Errorf("cannot replace %s: %w", bin, err))
	}
	env.say("ctx %s installed; the old one stays as %s", got.Version, prev)

	rollback := func(cause error) (*UpdateResult, error) {
		env.say("the update failed (%v): going back to %s", cause, old)
		env.Sys.Run(ctx, "systemctl", "stop", ServiceID)
		if err := os.Rename(prev, bin); err != nil {
			return nil, fmt.Errorf("%w; and the old ctx could not be put back: %v (it is %s)", cause, err, prev)
		}
		// The new release may have changed the schema: the copy from before
		// is the state the old binary understands.
		if out, err := asServiceBin(ctx, env, bin, "db", "restore", "--db", DBPath, "--from", res.Backup); err != nil {
			return nil, fmt.Errorf("%w; and the database could not be put back: %v: %s (copy: %s)", cause, err, strings.TrimSpace(out), res.Backup)
		}
		if err := start(); err != nil {
			return nil, fmt.Errorf("%w; and %v", cause, err)
		}
		if err := waitHealthy(ctx, env, local, old); err != nil {
			return nil, fmt.Errorf("%w; and the old server does not answer either: %v", cause, err)
		}
		res.RolledBack = true
		res.Checks = Doctor(ctx, o, env)
		return res, fmt.Errorf("the update to %s failed and ghosttree %s runs again (%w); the failed database is kept as %s.failed-*", got.Version, old, cause, DBPath)
	}

	if out, err := asServiceBin(ctx, env, bin, "upgrade-schema", "--db", DBPath); err != nil {
		return rollback(fmt.Errorf("upgrade-schema: %v: %s", err, strings.TrimSpace(out)))
	}
	if err := start(); err != nil {
		return rollback(err)
	}
	if err := waitHealthy(ctx, env, local, got.Version); err != nil {
		return rollback(err)
	}
	env.say("ghosttree %s runs", got.Version)
	prunePrev(bin, prev)
	res.Checks = Doctor(ctx, o, env)
	return res, nil
}

var unsafeNameRE = regexp.MustCompile(`[^A-Za-z0-9._+-]`)

// safeName makes a version fit to stand in a file name.
func safeName(v string) string { return unsafeNameRE.ReplaceAllString(v, "-") }

var releaseVersionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$`)

// parseVersion reads 1.2.3 or 1.2.3-rc.4 (what releases are tagged with).
func parseVersion(v string) (parts [4]int, ok bool) {
	m := releaseVersionRE.FindStringSubmatch(v)
	if m == nil {
		return parts, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	// A release is newer than its own candidates: 1.2.3 > 1.2.3-rc.9.
	parts[3] = int(^uint(0) >> 1)
	if m[4] != "" {
		n, err := strconv.Atoi(m[4])
		if err != nil {
			return parts, false
		}
		parts[3] = n
	}
	return parts, true
}

// IsDowngrade reports whether moving from the version that runs to next goes
// back. Versions that are not release versions (a local build) cannot be
// ordered and are never called a downgrade.
func IsDowngrade(running, next string) bool {
	a, ok1 := parseVersion(running)
	b, ok2 := parseVersion(next)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if b[i] != a[i] {
			return b[i] < a[i]
		}
	}
	return false
}

// keepOld preserves the installed binary under prev (a hard link, so a running
// process still has its file, else a copy).
func keepOld(bin, prev string) error {
	_ = os.Remove(prev)
	if err := os.Link(bin, prev); err == nil {
		return nil
	}
	return installBinaryCopy(bin, prev)
}

func installBinaryCopy(src, dst string) error { return installBinary(src, dst) }

// prunePrev keeps the newest three old binaries, never the one just made.
func prunePrev(bin, keep string) {
	old, _ := filepath.Glob(bin + ".prev-*")
	type f struct {
		path string
		mod  time.Time
	}
	var all []f
	for _, p := range old {
		if fi, err := os.Lstat(p); err == nil {
			all = append(all, f{p, fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	for i, b := range all {
		if i >= 3 && b.path != keep {
			os.Remove(b.path)
		}
	}
}
