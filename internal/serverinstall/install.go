package serverinstall

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// System is what installation does to the machine besides writing files:
// running programs (systemctl, systemd-run, the ctx just installed) and asking
// the server whether it is up. Tests replace it.
type System interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	// Get fetches a URL and returns the body; any status other than 200 is an error.
	Get(ctx context.Context, url string) ([]byte, error)
}

// ExecSystem is the real thing.
type ExecSystem struct{ Client *http.Client }

func (ExecSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (s ExecSystem) Get(ctx context.Context, url string) ([]byte, error) {
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return b, nil
}

// Env is what Install and Update run against.
type Env struct {
	Sys System
	Out io.Writer
	// HealthTimeout is how long the server may take to answer after a start.
	HealthTimeout time.Duration
	// Poll is the pause between health checks (default one second).
	Poll time.Duration
	// Root requires root; tests set it false and use Options.Prefix.
	RequireRoot bool
	// Systemd says whether the machine runs systemd; the check is skipped when
	// the real system is not used (Options.Prefix set).
	Systemd func() bool
}

func (e Env) say(format string, a ...any) { fmt.Fprintf(e.Out, format+"\n", a...) }

func (e Env) timeout() time.Duration {
	if e.HealthTimeout > 0 {
		return e.HealthTimeout
	}
	return 90 * time.Second
}

func (e Env) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return time.Second
}

// SystemdRunning reports whether systemd is the init of this machine.
func SystemdRunning() bool {
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

// Result is what Install tells the caller for its last lines.
type Result struct {
	LoginURL  string
	PublicURL string
	Checks    []Check
}

// Install sets a server up and starts it. It can be run again: an existing
// secret key and server.env stay as they are (Options.Force replaces server.env
// and the units), a started service is restarted, an existing admin account is
// kept and gets a new login link.
func Install(ctx context.Context, o Options, env Env) (*Result, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Prefix == "" {
		if runtimeGOOS() != "linux" {
			return nil, errors.New("ctx server install sets up a systemd service and runs on Linux only; use --docker on other systems")
		}
		if env.RequireRoot && os.Geteuid() != 0 {
			return nil, errors.New("this needs root: run it with sudo (it writes /etc/ghosttree and /etc/systemd/system)")
		}
		if env.Systemd != nil && !env.Systemd() {
			return nil, errors.New("this machine does not run systemd; use --docker, or set the server up by hand (deploy/README.md)")
		}
	}
	src := o.CtxBinary
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		src = exe
	}

	// 1. The program, where every user finds it.
	if err := installBinary(src, o.path(BinPath)); err != nil {
		return nil, fmt.Errorf("install ctx: %w", err)
	}
	env.say("ctx installed as %s", BinPath)

	// 2. Configuration. Nothing that holds a secret is ever replaced.
	if err := os.MkdirAll(o.path(EtcDir), 0o700); err != nil {
		return nil, err
	}
	wrote, err := writeFile(o.path(SecretKey), []byte(newSecretKey()+"\n"), 0o600, false)
	if err != nil {
		return nil, err
	}
	if wrote {
		env.say("server key made: %s (keep a copy with your backups: without it the AI keys stored in the database are lost)", SecretKey)
	} else {
		env.say("server key kept: %s", SecretKey)
	}
	if o.AI.enabled() {
		key, err := os.ReadFile(o.AI.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("--ai-key-file: %w", err)
		}
		if _, err := writeFile(o.path(LLMKey), []byte(strings.TrimSpace(string(key))+"\n"), 0o600, true); err != nil {
			return nil, err
		}
		if _, err := writeFile(o.path(LLMConfig), []byte(o.AI.LLMConfigText()), 0o644, true); err != nil {
			return nil, err
		}
		env.say("model provider %s configured (%s)", o.AI.Format, LLMConfig)
	}
	if wrote, err := writeFile(o.path(ServerEnv), []byte(o.ServerEnvText()), 0o640, o.Force); err != nil {
		return nil, err
	} else if wrote {
		env.say("settings written: %s", ServerEnv)
	} else {
		env.say("settings kept: %s (--force writes them again from the answers)", ServerEnv)
	}
	for name, text := range map[string]string{
		ServiceID:  o.ServiceText(),
		BackupID:   BackupServiceText(),
		BackupTime: BackupTimerText(),
	} {
		if _, err := writeFile(o.path(filepath.Join(UnitDir, name)), []byte(text), 0o644, true); err != nil {
			return nil, err
		}
	}
	env.say("units written: %s, %s, %s", ServiceID, BackupID, BackupTime)
	if err := writeProxyConfigs(o); err != nil {
		return nil, err
	}

	// 3. Start.
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for _, step := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", ServiceID, BackupTime},
		{"systemctl", "restart", ServiceID},
		{"systemctl", "start", BackupTime},
	} {
		if out, err := env.Sys.Run(ctx2, step[0], step[1:]...); err != nil {
			return nil, fmt.Errorf("%s: %v: %s", strings.Join(step, " "), err, strings.TrimSpace(out))
		}
	}
	local := "http://" + localAddr(o.listen())
	if err := waitHealthy(ctx2, env, local, ""); err != nil {
		return nil, fmt.Errorf("the server did not come up: %w (journalctl -u ghosttree shows why)", err)
	}
	env.say("server is up on %s", local)

	// 4. First accounts, through the service's own identity.
	if err := ensureAccounts(ctx2, env, o); err != nil {
		return nil, err
	}
	link, err := loginLink(ctx2, env, o)
	if err != nil {
		return nil, err
	}
	// The first backup, so the timer has proved itself and a restore has a start.
	if out, err := env.Sys.Run(ctx2, "systemctl", "start", BackupID); err != nil {
		env.say("first backup did not run: %v: %s", err, strings.TrimSpace(out))
	}

	res := &Result{LoginURL: link, PublicURL: o.PublicURL()}
	res.Checks = Doctor(ctx2, o, env)
	return res, nil
}

func runtimeGOOS() string { return goos }

func newSecretKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// installBinary copies src to dst atomically, unless it already is dst.
func installBinary(src, dst string) error {
	if a, b := mustAbs(src), mustAbs(dst); a == b {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".ctx-new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		if r, err := filepath.EvalSymlinks(a); err == nil {
			return r
		}
		return a
	}
	return p
}

func writeProxyConfigs(o Options) error {
	if o.Domain == "" || o.tls() != "proxy" {
		return nil
	}
	for name, text := range map[string]string{"Caddyfile": o.CaddyfileText(), "nginx.conf": o.NginxConfText()} {
		if _, err := writeFile(o.path(filepath.Join(ProxyDir, name)), []byte(text), 0o644, true); err != nil {
			return err
		}
	}
	return nil
}

// waitHealthy polls /api/health until it answers, and, with wantVersion set,
// until /api/version names that version.
func waitHealthy(ctx context.Context, env Env, base, wantVersion string) error {
	deadline := time.Now().Add(env.timeout())
	var last error
	for {
		_, err := env.Sys.Get(ctx, base+"/api/health")
		if err == nil && wantVersion != "" {
			var b []byte
			if b, err = env.Sys.Get(ctx, base+"/api/version"); err == nil {
				var v struct {
					Version string `json:"version"`
				}
				if json.Unmarshal(b, &v) != nil || v.Version != wantVersion {
					err = fmt.Errorf("the server runs %q, not %q", v.Version, wantVersion)
				}
			}
		}
		if err == nil {
			return nil
		}
		last = err
		if time.Now().After(deadline) || ctx.Err() != nil {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(env.poll()):
		}
	}
}

// asService runs ctx as the service's user, in a transient unit: a dynamic user
// does not exist outside its unit, and the database belongs to it.
func asService(ctx context.Context, env Env, args ...string) (string, error) {
	full := append([]string{"--pipe", "--wait", "--collect", "--quiet",
		"-p", "DynamicUser=yes", "-p", "User=" + ServiceUser, "-p", "StateDirectory=ghosttree", "-p", "UMask=0077", BinPath}, args...)
	return env.Sys.Run(ctx, "systemd-run", full...)
}

func ensureAccounts(ctx context.Context, env Env, o Options) error {
	list, err := asService(ctx, env, "account", "list", "--db", DBPath)
	if err != nil {
		return fmt.Errorf("account list: %v: %s", err, strings.TrimSpace(list))
	}
	has := func(name string) (found, gardener bool) {
		for _, line := range strings.Split(list, "\n") {
			f := strings.Split(line, "\t")
			if len(f) >= 2 && f[1] == name {
				return true, strings.Contains(line, "gardener=true")
			}
		}
		return false, false
	}
	if found, _ := has(o.admin()); !found {
		if out, err := asService(ctx, env, "account", "add", o.admin(), "--admin", "--db", DBPath); err != nil {
			return fmt.Errorf("account add %s: %v: %s", o.admin(), err, strings.TrimSpace(out))
		}
		env.say("administrator %q created", o.admin())
	} else {
		env.say("administrator %q exists", o.admin())
	}
	const gardener = "gardener"
	found, isGardener := has(gardener)
	if !found {
		for _, args := range [][]string{
			{"account", "add", gardener, "--db", DBPath},
			{"account", "set-kind", gardener, "--kind", "service", "--db", DBPath},
		} {
			if out, err := asService(ctx, env, args...); err != nil {
				return fmt.Errorf("%s: %v: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(out))
			}
		}
	}
	if !isGardener {
		if out, err := asService(ctx, env, "account", "set-gardener", gardener, "--db", DBPath); err != nil {
			return fmt.Errorf("account set-gardener: %v: %s", err, strings.TrimSpace(out))
		}
		env.say("account %q is the gardener (its sandboxed runs are set up as described in deploy/README.md)", gardener)
	}
	return nil
}

func loginLink(ctx context.Context, env Env, o Options) (string, error) {
	out, err := asService(ctx, env, "account", "login-link", o.admin(), "--url", o.PublicURL(), "--db", DBPath)
	if err != nil {
		return "", fmt.Errorf("login link: %v: %s", err, strings.TrimSpace(out))
	}
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
			return l, nil
		}
	}
	return "", fmt.Errorf("login link: no address in the answer: %s", strings.TrimSpace(out))
}
