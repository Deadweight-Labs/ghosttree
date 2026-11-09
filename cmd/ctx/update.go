package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/selfupdate"
	ver "github.com/Deadweight-Labs/ghosttree/internal/version"
)

const updateUsage = `usage: ctx update [--channel stable|dev] [--source] [--check] [--force]

  stable  newest signed release (Sigstore signature and sha256 checked; needs cosign)
  dev     the build the connected server offers under /dist/ (sha256 from the server)
  --source  build the newest origin/dev commit locally with go instead
  --pre     stable channel only: also install release candidates (v0.1.0-rc.3);
            signature and checksum are checked exactly as for a final release
  --check   only show what would happen
  --force   also reinstall the same version or go back to an older one`

// Replaceable in tests, so no update ever touches the real binary, GitHub or
// systemd.
var (
	updateHTTP      = &http.Client{Timeout: 5 * time.Minute}
	updateAPIBase   = "https://api.github.com"
	updateRepo      = "Deadweight-Labs/ghosttree" // matches the module path
	updateSourceURL = ""                          // default: https://github.com/<repo>.git
	updateGOOS      = runtime.GOOS
	updateGOARCH    = runtime.GOARCH
	updateVerifier  = func() selfupdate.Verifier {
		return selfupdate.CosignVerifier(updateRepo, os.Getenv("GHOSTTREE_COSIGN"))
	}
	updateTargetPath = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	updateCacheDir = func() string {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = filepath.Join(os.Getenv("HOME"), ".cache")
		}
		return filepath.Join(dir, "ghosttree", "ctx-src")
	}
)

func cmdUpdate(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stdout)
	channel := fs.String("channel", "", "stable or dev (default: the configured channel)")
	source := fs.Bool("source", false, "build origin/dev locally (implies the dev channel)")
	pre := fs.Bool("pre", false, "stable channel: also install release candidates")
	check := fs.Bool("check", false, "only show what would happen")
	force := fs.Bool("force", false, "reinstall the same version or go back to an older one")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, updateUsage)
		return 2
	}
	cfg, cfgErr := config.Load()
	ch := *channel
	if ch == "" {
		ch = cfg.UpdateChannel()
		if *source {
			ch = config.ChannelDev
		}
	}
	if !config.ValidChannel(ch) {
		fmt.Fprintf(stdout, "unknown channel %q: use stable or dev\n", ch)
		return 2
	}
	if *source && ch != config.ChannelDev {
		fmt.Fprintln(stdout, "--source builds origin/dev and cannot be combined with the stable channel")
		return 2
	}
	if *pre && (*source || ch != config.ChannelStable) {
		fmt.Fprintln(stdout, "--pre selects release candidates of the stable channel and cannot be combined with the dev channel or --source")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	target, err := updateTargetPath()
	if err != nil {
		fmt.Fprintf(stdout, "cannot locate the installed ctx: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed   %s (%s) at %s\n", ver.Label(version0()), ver.Channel(version0()), target)
	fmt.Fprintf(stdout, "channel     %s%s%s\n", ch, map[bool]string{true: " (--source)", false: ""}[*source], map[bool]string{true: " (--pre)", false: ""}[*pre])

	if *source {
		return updateFromSource(ctx, stdout, target, *check, *force)
	}
	src := selfupdate.Source{HTTP: updateHTTP, GOOS: updateGOOS, GOARCH: updateGOARCH}
	var cand selfupdate.Candidate
	switch ch {
	case config.ChannelStable:
		cand, err = src.LatestRelease(ctx, updateAPIBase, updateRepo, *pre)
	default:
		if cfgErr != nil || cfg.ServerURL == "" {
			fmt.Fprintln(stdout, "the dev channel fetches from the connected server: run 'ctx setup' or 'ctx login' first (or use --source)")
			return 1
		}
		cand, err = src.DevFromServer(ctx, cfg.ServerURL)
	}
	if err != nil {
		fmt.Fprintf(stdout, "no update: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "available   %s from %s\n", ver.Label(cand.Version), cand.Where)

	switch ver.Compare(version0(), cand.Version) {
	case ver.Same:
		if !*force {
			fmt.Fprintln(stdout, "result      already up to date")
			return 0
		}
	case ver.Older:
		if !*force {
			fmt.Fprintf(stdout, "result      refusing to go back from %s to %s (use --force)\n", version0(), cand.Version)
			return 1
		}
	}
	if *check {
		fmt.Fprintln(stdout, "result      update available; --check changed nothing")
		return 0
	}
	newPath, err := src.Fetch(ctx, cand, filepath.Dir(target), updateVerifier())
	if err != nil {
		fmt.Fprintf(stdout, "update aborted, nothing changed: %v\n", err)
		return 1
	}
	return finishUpdate(stdout, target, newPath, cand.Version)
}

// version0 is the version of the running binary.
func version0() string { return version }

func updateFromSource(ctx context.Context, stdout io.Writer, target string, check, force bool) int {
	url := updateSourceURL
	if url == "" {
		url = "https://github.com/" + updateRepo + ".git"
	}
	if check {
		out, err := exec.CommandContext(ctx, "git", "ls-remote", url, "refs/heads/dev").Output()
		f := strings.Fields(string(out))
		if err != nil || len(f) == 0 || len(f[0]) < 7 {
			fmt.Fprintf(stdout, "no update: cannot read origin/dev of %s\n", url)
			return 1
		}
		sha := f[0][:7]
		fmt.Fprintf(stdout, "available   origin/dev at %s\n", sha)
		if ver.Commit(version0()) == sha && !force {
			fmt.Fprintln(stdout, "result      already up to date")
		} else {
			fmt.Fprintln(stdout, "result      update available; --check changed nothing")
		}
		return 0
	}
	newPath, v, err := selfupdate.BuildFromSource(ctx, url, "dev", updateCacheDir(), filepath.Dir(target))
	if err != nil {
		fmt.Fprintf(stdout, "update aborted, nothing changed: %v\n", err)
		return 1
	}
	if version0() == v && !force {
		os.Remove(newPath)
		fmt.Fprintf(stdout, "available   %s\nresult      already up to date\n", v)
		return 0
	}
	fmt.Fprintf(stdout, "built       %s from %s\n", v, url)
	return finishUpdate(stdout, target, newPath, v)
}

// finishUpdate swaps the checked binary in and tells what still runs the old one.
func finishUpdate(stdout io.Writer, target, newPath, newVersion string) int {
	backup, err := selfupdate.Install(target, newPath, version0())
	if err != nil {
		os.Remove(newPath)
		fmt.Fprintf(stdout, "update aborted, nothing changed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed   %s (previous binary kept as %s)\n", ver.Label(newVersion), backup)
	if u := watchUnitState(); u.ExecPath != "" && !sameFile(u.ExecPath, target) {
		// Der Dienst startet ein anderes ctx: ihn neu zu starten, brächte das
		// gerade installierte nicht in Gang.
		fmt.Fprintf(stdout, "watch       %s runs %s, not this ctx; left alone\n", watchUnit, u.ExecPath)
	} else if systemctlUser("is-active", "--quiet", watchUnit) == nil {
		if err := systemctlUser("restart", watchUnit); err != nil {
			fmt.Fprintf(stdout, "watch       restart failed: %v; run 'systemctl --user restart %s'\n", err, watchUnit)
		} else {
			fmt.Fprintf(stdout, "restarted   %s\n", watchUnit)
		}
	} else {
		fmt.Fprintf(stdout, "watch       no %s running; if you run 'ctx watch' by hand, restart it\n", watchUnit)
	}
	fmt.Fprintln(stdout, "note        running MCP servers and hooks of open Claude/Codex sessions keep the old binary until those sessions are restarted")
	return 0
}
