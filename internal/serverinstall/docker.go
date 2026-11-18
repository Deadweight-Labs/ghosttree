package serverinstall

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Docker way of running a server (DEC-54): the image of a release on
// ghcr.io, a compose.yaml, volumes for the data and the backups, the server key
// as a compose secret. Everything below runs docker compose against the
// directory the installation made; nothing else of the machine is touched.
const (
	DockerRepo    = "ghcr.io/deadweight-labs/ghosttree"
	DockerDB      = "/data/ghosttree.db"
	DockerBackups = "/backups"
	containerCtx  = "/usr/local/bin/ctx"
)

//go:embed compose.yaml
var composeYAML string

// ComposeText is compose.yaml, the file that is also published next to install.sh.
func ComposeText() string { return composeYAML }

// DefaultDockerDir is where the Docker setup goes without --dir.
func DefaultDockerDir() string {
	if os.Geteuid() == 0 {
		return "/opt/ghosttree"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "ghosttree")
	}
	return "ghosttree"
}

func (o Options) dockerDir() string { return orDefault(o.Dir, DefaultDockerDir()) }

func (o Options) dockerPath(rel string) string { return o.path(filepath.Join(o.dockerDir(), rel)) }

// dockerImage is the image to run.
func (o Options) dockerImage() (string, error) {
	if o.Image != "" {
		return o.Image, nil
	}
	v := strings.TrimPrefix(o.Version, "v")
	if v == "" || strings.Contains(v, "-dev") {
		return "", fmt.Errorf("this ctx (%q) is a development build and has no image of its own: name one with --image, for example %s:0.1.0", o.Version, DockerRepo)
	}
	return DockerRepo + ":" + v, nil
}

// DockerEnvText is the .env next to compose.yaml.
func (o Options) DockerEnvText(image string) string {
	var b strings.Builder
	b.WriteString("# Written by ctx server install --docker; read by docker compose.\n")
	fmt.Fprintf(&b, "GHOSTTREE_IMAGE=%s\n", image)
	fmt.Fprintf(&b, "GHOSTTREE_BIND=%s\n", o.listen())
	if o.Subnet != "" {
		fmt.Fprintf(&b, "GHOSTTREE_SUBNET=%s\nGHOSTTREE_GATEWAY=%s\n", o.Subnet, o.Gateway)
	}
	if o.Domain != "" {
		fmt.Fprintf(&b, "GHOSTTREE_PUBLIC_URL=%s\n", o.PublicURL())
	}
	if o.Language != "" {
		fmt.Fprintf(&b, "GHOSTTREE_LANGUAGE=%s\n", o.Language)
	}
	if o.tls() == "builtin" {
		fmt.Fprintf(&b, "GHOSTTREE_TLS_ACME_HOST=%s\n", o.Domain)
		if o.Email != "" {
			fmt.Fprintf(&b, "GHOSTTREE_TLS_ACME_EMAIL=%s\n", o.Email)
		}
		b.WriteString("# No proxy in front: forwarding headers are nobody's to set.\nGHOSTTREE_TRUSTED_PROXIES=none\n")
	}
	return b.String()
}

// DockerOverrideText is compose.override.yaml for the choices that need more
// than compose.yaml: the server's own certificate (ports 80 and 443, which the
// non-root user may bind inside its own network namespace) and a model provider.
// Empty when there is nothing to add.
func (o Options) DockerOverrideText() string {
	if o.tls() != "builtin" && !o.AI.enabled() {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Written by ctx server install --docker, for what compose.yaml does not do by itself.\nservices:\n  ghosttree:\n")
	if o.tls() == "builtin" {
		b.WriteString("    ports:\n      - \"80:80\"\n      - \"443:443\"\n    sysctls:\n      net.ipv4.ip_unprivileged_port_start: 0\n")
	}
	if o.AI.enabled() {
		b.WriteString("    environment:\n      GHOSTTREE_LLM_CONFIG: /etc/ghosttree/llm.json\n")
		b.WriteString("    volumes:\n      - ./llm.json:/etc/ghosttree/llm.json:ro\n    secrets:\n      - llm-key\n")
		b.WriteString("secrets:\n  llm-key:\n    file: ./secrets/llm-key\n")
	}
	return b.String()
}

// dockerLLMConfig is llm.json for the container: the key is a compose secret.
func (a AIOptions) dockerLLMConfig() string {
	text := a.LLMConfigText()
	return strings.Replace(text, `"credential": "llm-key"`, `"api_key_file": "/run/secrets/llm-key"`, 1)
}

// pickSubnet finds a /24 that no Docker network and no route of this machine
// uses, and its gateway (.1): 172.29.84.0/24 if it is free, else the next ones.
// Without a way to ask it gives the compose.yaml default.
func pickSubnet(ctx context.Context, env Env) (subnet, gateway string) {
	var used []*net.IPNet
	if ids, err := env.Sys.Run(ctx, "docker", "network", "ls", "-q"); err == nil && strings.TrimSpace(ids) != "" {
		args := append([]string{"network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}"}, strings.Fields(ids)...)
		if out, err := env.Sys.Run(ctx, "docker", args...); err == nil {
			for _, f := range strings.Fields(out) {
				if _, n, err := net.ParseCIDR(f); err == nil {
					used = append(used, n)
				}
			}
		}
	}
	if routes, err := env.Sys.Run(ctx, "cat", "/proc/net/route"); err == nil {
		used = append(used, parseProcRoutes(routes)...)
	}
	return chooseSubnet(used)
}

// parseProcRoutes reads the networks of /proc/net/route (little-endian hex);
// the default route covers everything and says nothing.
func parseProcRoutes(text string) []*net.IPNet {
	var out []*net.IPNet
	for _, line := range strings.Split(text, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		dest, err1 := strconv.ParseUint(f[1], 16, 32)
		mask, err2 := strconv.ParseUint(f[7], 16, 32)
		if err1 != nil || err2 != nil || mask == 0 {
			continue
		}
		ip := net.IPv4(byte(dest), byte(dest>>8), byte(dest>>16), byte(dest>>24))
		m := net.IPv4Mask(byte(mask), byte(mask>>8), byte(mask>>16), byte(mask>>24))
		out = append(out, &net.IPNet{IP: ip.Mask(m), Mask: m})
	}
	return out
}

// candidateSubnets lists the /24s a Docker bridge may take, the usual private
// ranges for containers first: 172.29.84.0/24 and on, the rest of 172.16.0.0/12,
// then 10.89.0.0/16 and 192.168.100.0/22.
func candidateSubnets() []string {
	var out []string
	for i := 0; i < 256; i++ {
		out = append(out, fmt.Sprintf("172.29.%d.0/24", (84+i)%256))
	}
	for second := 16; second < 32; second++ {
		if second == 29 {
			continue
		}
		for third := 0; third < 256; third++ {
			out = append(out, fmt.Sprintf("172.%d.%d.0/24", second, third))
		}
	}
	for third := 0; third < 256; third++ {
		out = append(out, fmt.Sprintf("10.89.%d.0/24", third))
	}
	for third := 100; third < 104; third++ {
		out = append(out, fmt.Sprintf("192.168.%d.0/24", third))
	}
	return out
}

func chooseSubnet(used []*net.IPNet) (subnet, gateway string) {
	for _, c := range candidateSubnets() {
		_, cand, _ := net.ParseCIDR(c)
		free := true
		for _, u := range used {
			if u.Contains(cand.IP) || cand.Contains(u.IP) {
				free = false
				break
			}
		}
		if free {
			ip := cand.IP.To4()
			return cand.String(), fmt.Sprintf("%d.%d.%d.1", ip[0], ip[1], ip[2])
		}
	}
	return "", ""
}

func compose(ctx context.Context, env Env, o Options, args ...string) (string, error) {
	full := append([]string{"compose", "--project-directory", o.dockerDir()}, args...)
	return env.Sys.Run(ctx, "docker", full...)
}

func composeCtx(env Env, o Options) ctxRunner {
	return func(ctx context.Context, args ...string) (string, error) {
		return compose(ctx, env, o, append([]string{"exec", "-T", "ghosttree", containerCtx}, args...)...)
	}
}

func installDocker(ctx context.Context, o Options, env Env) (*Result, error) {
	if o.Prefix == "" && goos != "linux" && goos != "darwin" {
		return nil, errors.New("ctx server install --docker runs on Linux and macOS")
	}
	image, err := o.dockerImage()
	if err != nil {
		return nil, err
	}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if out, err := env.Sys.Run(ctx2, "docker", "compose", "version"); err != nil {
		return nil, fmt.Errorf("Docker with the compose plugin is needed (docker compose version: %v %s)", err, strings.TrimSpace(out))
	}
	dir := o.dockerDir()
	if err := os.MkdirAll(o.dockerPath("secrets"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(o.dockerPath("secrets"), 0o700)
	// The secrets are files bind-mounted into the container, where the server
	// runs as an unprivileged user: they are readable (0644), and the directory
	// that holds them is the lock (0700).
	wrote, err := writeFile(o.dockerPath("secrets/secret-key"), []byte(newSecretKey()+"\n"), 0o644, false)
	if err != nil {
		return nil, err
	}
	if wrote {
		env.say("server key made: %s/secrets/secret-key (keep a copy with your backups: without it the AI keys stored in the database are lost)", dir)
	} else {
		env.say("server key kept: %s/secrets/secret-key", dir)
	}
	if o.AI.enabled() {
		key, err := os.ReadFile(o.AI.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("--ai-key-file: %w", err)
		}
		if _, err := writeFile(o.dockerPath("secrets/llm-key"), []byte(strings.TrimSpace(string(key))+"\n"), 0o644, true); err != nil {
			return nil, err
		}
		if _, err := writeFile(o.dockerPath("llm.json"), []byte(o.AI.dockerLLMConfig()), 0o644, true); err != nil {
			return nil, err
		}
	}
	if _, err := writeFile(o.dockerPath("compose.yaml"), []byte(composeYAML), 0o644, o.Force); err != nil {
		return nil, err
	}
	if _, err := os.Stat(o.dockerPath(".env")); err != nil || o.Force {
		o.Subnet, o.Gateway = pickSubnet(ctx2, env)
	}
	if wrote, err := writeFile(o.dockerPath(".env"), []byte(o.DockerEnvText(image)), 0o600, o.Force); err != nil {
		return nil, err
	} else if wrote {
		env.say("settings written: %s/.env", dir)
	} else {
		env.say("settings kept: %s/.env (--force writes them again from the answers)", dir)
	}
	if text := o.DockerOverrideText(); text != "" {
		if _, err := writeFile(o.dockerPath("compose.override.yaml"), []byte(text), 0o644, true); err != nil {
			return nil, err
		}
	} else if o.Force {
		_ = os.Remove(o.dockerPath("compose.override.yaml"))
	}
	if o.Domain != "" && o.tls() == "proxy" {
		for name, text := range map[string]string{"Caddyfile": o.CaddyfileText(), "nginx.conf": o.NginxConfText()} {
			if _, err := writeFile(o.dockerPath("proxy/"+name), []byte(text), 0o644, true); err != nil {
				return nil, err
			}
		}
	}
	env.say("compose.yaml written: %s", dir)

	if out, err := compose(ctx2, env, o, "up", "-d"); err != nil {
		return nil, fmt.Errorf("docker compose up: %v: %s", err, strings.TrimSpace(out))
	}
	local := "http://" + localAddr(o.listen())
	if err := waitHealthy(ctx2, env, local, ""); err != nil {
		return nil, fmt.Errorf("the server did not come up: %w (docker compose -f %s/compose.yaml logs ghosttree shows why)", err, dir)
	}
	env.say("server is up on %s", local)

	run := composeCtx(env, o)
	if err := ensureAccounts(ctx2, env, o, run, DockerDB); err != nil {
		return nil, err
	}
	link, err := loginLink(ctx2, o, run, DockerDB)
	if err != nil {
		return nil, err
	}
	res := &Result{LoginURL: link, PublicURL: o.PublicURL()}
	res.Checks = dockerDoctor(ctx2, o, env)
	return res, nil
}

// DockerOptionsFromDisk recovers the answers of a Docker installation from its .env.
func DockerOptionsFromDisk(prefix, dir string) (Options, string) {
	o := Options{Prefix: prefix, Docker: true, Dir: dir}
	f, err := os.Open(o.dockerPath(".env"))
	if err != nil {
		return o, ""
	}
	defer f.Close()
	image := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		switch k {
		case "GHOSTTREE_IMAGE":
			image = v
		case "GHOSTTREE_BIND":
			o.Listen = v
		case "GHOSTTREE_PUBLIC_URL":
			o.Domain = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
		case "GHOSTTREE_TLS_ACME_HOST":
			o.TLS = "builtin"
		case "GHOSTTREE_LANGUAGE":
			o.Language = v
		}
	}
	return o, image
}

func imageVersion(image string) string {
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		return image[i+1:]
	}
	return ""
}

// dockerDoctor is Doctor for containers.
func dockerDoctor(ctx context.Context, o Options, env Env) []Check {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }
	local := "http://" + localAddr(o.listen())
	logs := "docker compose --project-directory " + o.dockerDir() + " logs ghosttree"

	out, err := compose(ctx, env, o, "ps", "--status", "running", "--services")
	running := map[string]bool{}
	for _, l := range strings.Fields(out) {
		running[l] = true
	}
	switch {
	case err != nil:
		add(Check{Name: "containers", Detail: "docker compose ps: " + firstLine(out, err), Fix: "is Docker running? docker info"})
	case !running["ghosttree"]:
		add(Check{Name: "containers", Detail: "the ghosttree container is not running", Fix: logs})
	default:
		add(Check{Name: "containers", OK: true, Detail: "the ghosttree container runs, the backup container " + map[bool]string{true: "too", false: "does not"}[running["backup"]]})
	}

	if _, err := env.Sys.Get(ctx, local+"/api/health"); err != nil {
		add(Check{Name: "server", Detail: fmt.Sprintf("no answer on %s: %v", local, err), Fix: logs})
	} else {
		detail := "answers on " + local
		if b, err := env.Sys.Get(ctx, local+"/api/version"); err == nil {
			if v := jsonField(b, "version"); v != "" {
				detail += ", ctx " + v
			}
		}
		add(Check{Name: "server", OK: true, Detail: detail})
	}

	if st, err := os.Stat(o.dockerPath("secrets/secret-key")); err != nil {
		add(Check{Name: "server key", Detail: o.dockerDir() + "/secrets/secret-key is missing", Fix: "ctx server install --docker"})
	} else if st.Mode().Perm()&0o004 == 0 {
		add(Check{Name: "server key", Detail: "secrets/secret-key is not readable for the container's user", Fix: "chmod 644 " + o.dockerDir() + "/secrets/secret-key"})
	} else {
		add(Check{Name: "server key", OK: true, Detail: o.dockerDir() + "/secrets/secret-key (keep a copy with your backups)"})
	}

	if envText, _ := os.ReadFile(o.dockerPath(".env")); strings.Contains(string(envText), "GHOSTTREE_ENFORCE_ACCESS=0") {
		add(Check{Name: "access control", Detail: "GHOSTTREE_ENFORCE_ACCESS=0 in .env: roles are only logged, not enforced", Fix: "remove the line and run: docker compose up -d"})
	} else {
		add(Check{Name: "access control", OK: true, Detail: "roles are enforced"})
	}

	if !running["backup"] {
		add(Check{Name: "backup", Detail: "the backup container is not running", Fix: "docker compose --project-directory " + o.dockerDir() + " up -d backup"})
	} else {
		add(Check{Name: "backup", OK: true, Detail: "a copy every 24 hours into the volume ghosttree_backups, the newest 14 kept"})
	}

	run := composeCtx(env, o)
	if out, err := run(ctx, "db", "check", "--db", DockerDB); err != nil || !strings.Contains(out, "ok") {
		add(Check{Name: "database", Detail: "ctx db check: " + firstLine(out, err), Fix: "restore a copy from the backups volume: ctx db restore"})
	} else {
		add(Check{Name: "database", OK: true, Detail: "integrity check ok"})
	}
	if out, err := run(ctx, "account", "list", "--db", DockerDB); err != nil {
		add(Check{Name: "accounts", Detail: firstLine(out, err)})
	} else {
		switch {
		case !strings.Contains(out, "admin=true"):
			add(Check{Name: "accounts", Detail: "no administrator", Fix: "docker compose exec ghosttree ctx account add <name> --admin --db " + DockerDB})
		case !strings.Contains(out, "gardener=true"):
			add(Check{Name: "accounts", Detail: "no gardener account: nobody may write gardener entries", Fix: "docker compose exec ghosttree ctx account set-gardener <name> --db " + DockerDB})
		default:
			add(Check{Name: "accounts", OK: true, Detail: "an administrator and the gardener's account exist"})
		}
	}

	if o.Domain != "" {
		if _, err := env.Sys.Get(ctx, o.PublicURL()+"/api/health"); err != nil {
			fix := "point " + o.Domain + " at this machine and open ports 80 and 443"
			if o.tls() == "proxy" {
				fix = "point " + o.Domain + " at this machine and set up your proxy: " + o.dockerDir() + "/proxy/Caddyfile or nginx.conf"
			}
			add(Check{Name: "public address", Unverified: true, Detail: o.PublicURL() + " is not reachable from here yet (" + shortErr(err) + ")", Fix: fix})
		} else {
			add(Check{Name: "public address", OK: true, Detail: o.PublicURL() + " answers"})
		}
	}
	return checks
}

// DockerDoctor checks a Docker installation.
func DockerDoctor(ctx context.Context, prefix, dir string, env Env) []Check {
	o, _ := DockerOptionsFromDisk(prefix, dir)
	return dockerDoctor(ctx, o, env)
}

// updateDocker moves a Docker installation to another release. The release tag
// has to be a signed one (its checksums.txt is checked as for a systemd update);
// the image is pulled by that tag first, so a missing image changes nothing. Then
// the containers stop, the database is copied into the backups volume, the new
// image gets the schema upgrade and starts. If it does not answer with its version,
// the old image and the copy are put back.
func updateDocker(ctx context.Context, uo UpdateOptions, rel Release, env Env) (*UpdateResult, error) {
	o, oldImage := DockerOptionsFromDisk(uo.Prefix, uo.Dir)
	if oldImage == "" {
		return nil, fmt.Errorf("no Docker installation in %s (no .env there); set one up with: ctx server install --docker", o.dockerDir())
	}
	old := imageVersion(oldImage)
	tag := uo.Version
	var err error
	if tag == "" {
		if tag, err = rel.Latest(ctx); err != nil {
			return nil, err
		}
	} else if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	newVer := strings.TrimPrefix(tag, "v")
	res := &UpdateResult{From: old, To: newVer}
	if newVer == old && !uo.Force {
		env.say("ghosttree %s already runs; nothing to do (--force pulls it again)", old)
		res.Unchanged = true
		return res, nil
	}
	if err := rel.VerifyTag(ctx, tag); err != nil {
		return nil, err
	}
	newImage := DockerRepo + ":" + newVer
	if i := strings.LastIndex(oldImage, ":"); i > 0 && !strings.Contains(oldImage[i:], "/") {
		newImage = oldImage[:i] + ":" + newVer
	}
	if _, err := env.Sys.Run(ctx, "docker", "image", "inspect", newImage); err == nil {
		env.say("release %s is signed; %s is here already", tag, newImage)
	} else {
		env.say("release %s is signed; pulling %s", tag, newImage)
		if out, err := env.Sys.Run(ctx, "docker", "pull", newImage); err != nil {
			return nil, fmt.Errorf("docker pull %s: %v: %s; nothing was changed", newImage, err, strings.TrimSpace(out))
		}
	}

	local := "http://" + localAddr(o.listen())
	envFile := o.dockerPath(".env")
	original, err := os.ReadFile(envFile)
	if err != nil {
		return nil, err
	}
	setImage := func(image string) error {
		lines := strings.Split(string(original), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "GHOSTTREE_IMAGE=") {
				lines[i] = "GHOSTTREE_IMAGE=" + image
			}
		}
		_, err := writeFile(envFile, []byte(strings.Join(lines, "\n")), 0o600, true)
		return err
	}
	up := func() error {
		if out, err := compose(ctx, env, o, "up", "-d"); err != nil {
			return fmt.Errorf("docker compose up: %v: %s", err, strings.TrimSpace(out))
		}
		return nil
	}
	if out, err := compose(ctx, env, o, "stop"); err != nil {
		return nil, fmt.Errorf("docker compose stop: %v: %s", err, strings.TrimSpace(out))
	}
	abort := func(err error) (*UpdateResult, error) {
		if uerr := up(); uerr != nil {
			err = fmt.Errorf("%w; and the old server could not be started: %v", err, uerr)
		}
		return nil, err
	}
	res.Backup = fmt.Sprintf("%s/pre-update-%s-to-%s-%s.db", DockerBackups, old, newVer, time.Now().UTC().Format("20060102-150405"))
	if out, err := compose(ctx, env, o, "run", "--rm", "--no-deps", "backup", "db", "backup", "--db", DockerDB, "--out", res.Backup); err != nil {
		return abort(fmt.Errorf("the database could not be copied, so nothing was changed: %v: %s", err, strings.TrimSpace(out)))
	}
	env.say("database copied to %s (volume ghosttree_backups)", res.Backup)
	if err := setImage(newImage); err != nil {
		return abort(err)
	}

	rollback := func(cause error) (*UpdateResult, error) {
		env.say("the update failed (%v): going back to %s", cause, old)
		compose(ctx, env, o, "stop")
		if err := setImage(oldImage); err != nil {
			return nil, fmt.Errorf("%w; and %s could not be put back: %v", cause, envFile, err)
		}
		if out, err := compose(ctx, env, o, "run", "--rm", "--no-deps", "backup", "db", "restore", "--db", DockerDB, "--from", res.Backup); err != nil {
			return nil, fmt.Errorf("%w; and the database could not be put back: %v: %s (copy: %s)", cause, err, strings.TrimSpace(out), res.Backup)
		}
		if err := up(); err != nil {
			return nil, fmt.Errorf("%w; and %v", cause, err)
		}
		if err := waitHealthy(ctx, env, local, old); err != nil {
			return nil, fmt.Errorf("%w; and the old server does not answer either: %v", cause, err)
		}
		res.RolledBack = true
		res.Checks = dockerDoctor(ctx, o, env)
		return res, fmt.Errorf("the update to %s failed and ghosttree %s runs again (%w)", newVer, old, cause)
	}
	if out, err := compose(ctx, env, o, "run", "--rm", "--no-deps", "ghosttree", "upgrade-schema", "--db", DockerDB); err != nil {
		return rollback(fmt.Errorf("upgrade-schema: %v: %s", err, strings.TrimSpace(out)))
	}
	if err := up(); err != nil {
		return rollback(err)
	}
	if err := waitHealthy(ctx, env, local, newVer); err != nil {
		return rollback(err)
	}
	env.say("ghosttree %s runs", newVer)
	res.Checks = dockerDoctor(ctx, o, env)
	return res, nil
}
