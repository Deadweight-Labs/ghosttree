package serverinstall

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signedEnv is an Env whose release key knows the fake releases, with v0.1.0 published.
func signedEnv(t *testing.T, sys System) (Env, *bytes.Buffer, *fakeReleases, testKey) {
	t.Helper()
	k := newTestKey(t)
	f := newFakeReleases(t)
	f.publish(t, k, "v0.1.0", "x")
	env, out := testEnv(sys)
	rel := f.release(k)
	env.Release = &rel
	return env, out, f, k
}

// pinned is the image reference of version v as the installer writes it.
func pinned(v string) string { return imageRef(DockerRepo, v, imageDigestOf("v"+v)) }

func dockerInstall(t *testing.T, o Options) (*Result, *fakeSystem, string, string) {
	t.Helper()
	prefix := t.TempDir()
	o.Prefix, o.Docker, o.Dir, o.Version = prefix, true, "/opt/ghosttree", "0.1.0"
	sys := newFakeSystem(t, prefix)
	env, out, _, _ := signedEnv(t, sys)
	res, err := Install(context.Background(), o, env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return res, sys, prefix, out.String()
}

func TestDockerInstallWritesComposeAndSecretsAndEndsWithALoginLink(t *testing.T) {
	res, sys, prefix, out := dockerInstall(t, Options{Domain: "gt.example.com", Admin: "alex", Language: "de"})
	if res.LoginURL == "" || res.PublicURL != "https://gt.example.com" || !Healthy(res.Checks) {
		t.Fatalf("%+v\n%s", res, out)
	}
	dir := filepath.Join(prefix, "opt/ghosttree")
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != ComposeText() {
		t.Error("compose.yaml is not the embedded one")
	}
	envText, _ := os.ReadFile(filepath.Join(dir, ".env"))
	for _, want := range []string{"GHOSTTREE_IMAGE=" + pinned("0.1.0"), "GHOSTTREE_BIND=127.0.0.1:8474", "GHOSTTREE_PUBLIC_URL=https://gt.example.com", "GHOSTTREE_LANGUAGE=de"} {
		if !strings.Contains(string(envText), want) {
			t.Errorf(".env lacks %q:\n%s", want, envText)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "secrets")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("secrets directory: %v %v", fi, err)
	}
	// Readable for the container's user, locked by the directory above it.
	if fi, err := os.Stat(filepath.Join(dir, "secrets/secret-key")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("secret key: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".env")); fi.Mode().Perm() != 0o600 {
		t.Errorf(".env mode %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.override.yaml")); err == nil {
		t.Error("nothing needs an override here")
	}
	for _, f := range []string{"proxy/Caddyfile", "proxy/nginx.conf"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	order := []string{"docker compose version", "compose --project-directory /opt/ghosttree up -d", "GET http://127.0.0.1:8474/api/health",
		"exec -T ghosttree /usr/local/bin/ctx account add alex --admin --db /data/ghosttree.db", "account set-gardener gardener --db /data/ghosttree.db",
		"account login-link alex --url https://gt.example.com --db /data/ghosttree.db"}
	last := -1
	for _, c := range order {
		i := sys.index(c)
		if i <= last {
			t.Fatalf("%q at %d after %d:\n%s", c, i, last, strings.Join(sys.commands(), "\n"))
		}
		last = i
	}
}

func TestDockerInstallWithBuiltInTLSPublishesThePortsAndTrustsNoProxy(t *testing.T) {
	_, _, prefix, _ := dockerInstall(t, Options{Domain: "gt.example.com", TLS: "builtin", Email: "ops@example.com"})
	dir := filepath.Join(prefix, "opt/ghosttree")
	over, _ := os.ReadFile(filepath.Join(dir, "compose.override.yaml"))
	for _, want := range []string{`"80:80"`, `"443:443"`, "net.ipv4.ip_unprivileged_port_start: 0"} {
		if !strings.Contains(string(over), want) {
			t.Errorf("override lacks %q:\n%s", want, over)
		}
	}
	envText, _ := os.ReadFile(filepath.Join(dir, ".env"))
	for _, want := range []string{"GHOSTTREE_TLS_ACME_HOST=gt.example.com", "GHOSTTREE_TLS_ACME_EMAIL=ops@example.com", "GHOSTTREE_TRUSTED_PROXIES=none"} {
		if !strings.Contains(string(envText), want) {
			t.Errorf(".env lacks %q:\n%s", want, envText)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "proxy")); err == nil {
		t.Error("built-in TLS needs no proxy configuration")
	}
}

func TestDockerInstallConnectsAModelProviderThroughASecret(t *testing.T) {
	key := filepath.Join(t.TempDir(), "k")
	os.WriteFile(key, []byte("sk-test-1\n"), 0o600)
	_, _, prefix, _ := dockerInstall(t, Options{AI: AIOptions{Format: "anthropic", Model: "m", KeyFile: key}})
	dir := filepath.Join(prefix, "opt/ghosttree")
	cfg, _ := os.ReadFile(filepath.Join(dir, "llm.json"))
	if !strings.Contains(string(cfg), `"api_key_file": "/run/secrets/llm-key"`) || strings.Contains(string(cfg), "sk-test") {
		t.Errorf("llm.json:\n%s", cfg)
	}
	if k, _ := os.ReadFile(filepath.Join(dir, "secrets/llm-key")); strings.TrimSpace(string(k)) != "sk-test-1" {
		t.Errorf("secret: %q", k)
	}
	over, _ := os.ReadFile(filepath.Join(dir, "compose.override.yaml"))
	if !strings.Contains(string(over), "GHOSTTREE_LLM_CONFIG: /etc/ghosttree/llm.json") || !strings.Contains(string(over), "- llm-key") {
		t.Errorf("override:\n%s", over)
	}
}

func TestDockerInstallNeedsAnImageAndDocker(t *testing.T) {
	prefix := t.TempDir()
	env, _, _, _ := signedEnv(t, newFakeSystem(t, prefix))
	// A development build has no image of its own.
	_, err := Install(context.Background(), Options{Prefix: prefix, Docker: true, Dir: "/opt/g", Version: "0.1.0-dev"}, env)
	if err == nil || !strings.Contains(err.Error(), "--image") {
		t.Errorf("dev build: %v", err)
	}
	// ... unless one is named.
	if _, err := Install(context.Background(), Options{Prefix: prefix, Docker: true, Dir: "/opt/g", Version: "0.1.0-dev", Image: "example.org/gt:x"}, env); err != nil {
		t.Errorf("named image: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(prefix, "opt/g/.env")); !strings.Contains(string(b), "GHOSTTREE_IMAGE=example.org/gt:x") {
		t.Errorf(".env: %s", b)
	}
	// Without a release key the image cannot be checked, so it must be named.
	bare, _ := testEnv(newFakeSystem(t, t.TempDir()))
	if _, err := Install(context.Background(), Options{Prefix: t.TempDir(), Docker: true, Dir: "/opt/g", Version: "0.1.0"}, bare); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Errorf("no key: %v", err)
	}
	// A release that does not vouch for an image is not installed from.
	e2, _, f, _ := signedEnv(t, newFakeSystem(t, t.TempDir()))
	f.set("v0.1.0/checksums.txt", []byte("0000000000000000000000000000000000000000000000000000000000000000  other\n"))
	if _, err := Install(context.Background(), Options{Prefix: t.TempDir(), Docker: true, Dir: "/opt/g", Version: "0.1.0"}, e2); err == nil {
		t.Error("an unsigned list was accepted")
	}
}

func TestDockerInstallAgainKeepsKeyAndSettings(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _, _, _ := signedEnv(t, sys)
	o := Options{Prefix: prefix, Docker: true, Dir: "/opt/ghosttree", Version: "0.1.0", Domain: "gt.example.com"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(prefix, "opt/ghosttree")
	key1, _ := os.ReadFile(filepath.Join(dir, "secrets/secret-key"))
	env1, _ := os.ReadFile(filepath.Join(dir, ".env"))
	sys.cmds = nil
	o.Domain, o.Language = "other.example.com", "en"
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	key2, _ := os.ReadFile(filepath.Join(dir, "secrets/secret-key"))
	env2, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(key1) != string(key2) || string(env1) != string(env2) {
		t.Error("key or settings were replaced without --force")
	}
	if sys.saw("account add alex") || sys.saw("account add admin") {
		t.Errorf("accounts were added twice:\n%s", strings.Join(sys.commands(), "\n"))
	}
}

// The compose.yaml people download is the one the installer writes.
func TestPublishedComposeFileIsTheEmbeddedOne(t *testing.T) {
	b, err := os.ReadFile("../../deploy/downloads/ghosttree/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != ComposeText() {
		t.Fatal("deploy/downloads/ghosttree/compose.yaml differs from internal/serverinstall/compose.yaml; copy one over the other")
	}
	for _, want := range []string{"read_only: true", "cap_drop: [ALL]", "no-new-privileges:true", "GHOSTTREE_SECRET_KEY_FILE: /run/secrets/ghosttree-secret-key", "data:/data", "backups:/backups", "--every", "service_healthy"} {
		if !strings.Contains(ComposeText(), want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
}

type dockerUpdateFixture struct {
	prefix string
	sys    *fakeSystem
	env    Env
	rel    Release
	fake   *fakeReleases
	key    testKey
}

func newDockerUpdateFixture(t *testing.T) *dockerUpdateFixture {
	t.Helper()
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _, f, k := signedEnv(t, sys)
	if _, err := Install(context.Background(), Options{Prefix: prefix, Docker: true, Dir: "/opt/ghosttree", Version: "0.1.0"}, env); err != nil {
		t.Fatal(err)
	}
	sys.cmds = nil
	return &dockerUpdateFixture{prefix, sys, env, f.release(k), f, k}
}

func (u *dockerUpdateFixture) update(tag string) (*UpdateResult, error) {
	return Update(context.Background(), UpdateOptions{Prefix: u.prefix, Docker: true, Dir: "/opt/ghosttree", Version: tag}, u.rel, u.env)
}

func (u *dockerUpdateFixture) image() string {
	b, _ := os.ReadFile(filepath.Join(u.prefix, "opt/ghosttree/.env"))
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "GHOSTTREE_IMAGE="); ok {
			return v
		}
	}
	return ""
}

func TestDockerUpdatePullsFirstThenSavesTheDatabaseAndChecksTheNewVersion(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	res, err := u.update("v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "0.1.0" || res.To != "0.2.0" || res.RolledBack || u.image() != pinned("0.2.0") {
		t.Errorf("%+v, image %q", res, u.image())
	}
	order := []string{"docker pull " + pinned("0.2.0"), "compose --project-directory /opt/ghosttree stop", "run --rm --no-deps backup db backup --db /data/ghosttree.db --out /backups/pre-update-0.1.0-to-0.2.0-",
		"run --rm --no-deps ghosttree upgrade-schema", "up -d", "/api/version"}
	last := -1
	for _, c := range order {
		i := u.sys.index(c)
		if i <= last {
			t.Fatalf("%q at %d after %d:\n%s", c, i, last, strings.Join(u.sys.commands(), "\n"))
		}
		last = i
	}
	if u.sys.saw("backup written by 0.2.0") || !u.sys.saw("backup written by 0.1.0") {
		t.Error("the copy must be made by the old image")
	}
	if !Healthy(res.Checks) {
		t.Errorf("checks: %+v", res.Checks)
	}
}

func TestDockerUpdateGoesBackWhenTheNewImageDoesNotAnswer(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	u.sys.brokenImages = map[string]bool{"0.2.0": true}
	res, err := u.update("v0.2.0")
	if err == nil || res == nil || !res.RolledBack || !strings.Contains(err.Error(), "runs again") {
		t.Fatalf("%+v %v", res, err)
	}
	if u.image() != pinned("0.1.0") {
		t.Errorf("image %q after the rollback", u.image())
	}
	i, j := u.sys.index("db restore --db /data/ghosttree.db --from "+res.Backup), lastIndex(u.sys.commands(), "up -d")
	if i < 0 || j < i {
		t.Errorf("restore at %d, last up at %d:\n%s", i, j, strings.Join(u.sys.commands(), "\n"))
	}
	if !u.sys.running {
		t.Error("the old server is not running")
	}
}

func TestDockerUpdateGoesBackWhenTheSchemaUpgradeFails(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	u.sys.badSchema = map[string]bool{"0.2.0": true}
	res, err := u.update("v0.2.0")
	if err == nil || res == nil || !res.RolledBack || u.image() != pinned("0.1.0") {
		t.Fatalf("%+v %v image %q", res, err, u.image())
	}
}

func TestDockerUpdateTouchesNothingWhenTheReleaseOrTheImageIsWrong(t *testing.T) {
	u := newDockerUpdateFixture(t)
	// Not a release the key signed.
	other := newTestKey(t)
	u.fake.publish(t, other, "v0.2.0", "x")
	if _, err := u.update("v0.2.0"); err == nil || !strings.Contains(err.Error(), "does not match the ghosttree release key") {
		t.Fatalf("foreign signature: %v", err)
	}
	// A signed release without an image.
	u.fake.publish(t, u.key, "v0.3.0", "x")
	u.sys.pullFails = map[string]bool{pinned("0.3.0"): true}
	if _, err := u.update("v0.3.0"); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("missing image: %v", err)
	}
	if u.sys.saw(" stop") || u.image() != pinned("0.1.0") || !u.sys.running {
		t.Errorf("the server was touched:\n%s", strings.Join(u.sys.commands(), "\n"))
	}
	// Same version: nothing to do.
	if res, err := u.update("v0.1.0"); err != nil || !res.Unchanged {
		t.Errorf("same version: %+v %v", res, err)
	}
}

func TestDockerUpdateStopsBeforeChangingAnythingWhenTheDatabaseCannotBeCopied(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	u.sys.failBackup = true
	if _, err := u.update("v0.2.0"); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("%v", err)
	}
	if u.image() != pinned("0.1.0") || !u.sys.running {
		t.Errorf("image %q, running %v", u.image(), u.sys.running)
	}
}

func TestDockerUpdateNeedsAnInstallation(t *testing.T) {
	prefix := t.TempDir()
	env, _ := testEnv(newFakeSystem(t, prefix))
	_, err := Update(context.Background(), UpdateOptions{Prefix: prefix, Docker: true, Dir: "/opt/none", Version: "v0.2.0"}, Release{}, env)
	if err == nil || !strings.Contains(err.Error(), "--docker") {
		t.Fatalf("%v", err)
	}
}

func TestDockerInstallPicksASubnetNoOtherNetworkUses(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	sys.networkIDs = "aaa\nbbb\n"
	sys.networkSubnets = "172.17.0.0/16 172.29.84.0/24 172.29.85.0/25 \n"
	env, _, _, _ := signedEnv(t, sys)
	o := Options{Prefix: prefix, Docker: true, Dir: "/opt/ghosttree", Version: "0.1.0"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(prefix, "opt/ghosttree/.env"))
	if !strings.Contains(string(b), "GHOSTTREE_SUBNET=172.29.86.0/24\nGHOSTTREE_GATEWAY=172.29.86.1\n") {
		t.Errorf(".env:\n%s", b)
	}
}

func TestChooseSubnetSkipsWhatOverlapsInEitherDirection(t *testing.T) {
	parse := func(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }
	cases := []struct {
		used []string
		want string
	}{
		{nil, "172.29.84.0/24"},
		{[]string{"172.29.84.0/24"}, "172.29.85.0/24"},
		{[]string{"172.29.0.0/16"}, "172.16.0.0/24"}, // the rest of 172.16.0.0/12 is next
		{[]string{"172.29.84.128/25"}, "172.29.85.0/24"},
		{[]string{"172.16.0.0/12"}, "10.89.0.0/24"}, // all of the 172 range in one network
		{[]string{"10.0.0.0/8", "192.168.0.0/16"}, "172.29.84.0/24"},
		{[]string{"172.16.0.0/12", "10.89.0.0/16"}, "192.168.100.0/24"},
		{[]string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"}, ""}, // nothing left
	}
	for _, c := range cases {
		var used []*net.IPNet
		for _, u := range c.used {
			used = append(used, parse(u))
		}
		if got, _ := chooseSubnet(used); got != c.want {
			t.Errorf("used %v: %q, want %q", c.used, got, c.want)
		}
	}
}

func TestDockerUpdateDoesNotPullAnImageThatIsHereAlready(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	u.sys.images = map[string]bool{pinned("0.2.0"): true}
	if _, err := u.update("v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if u.sys.saw("docker pull") {
		t.Error("it pulled an image that was there")
	}
}

func TestParseProcRoutesReadsLittleEndianNetworksAndSkipsTheDefaultRoute(t *testing.T) {
	text := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"wlan0\t00000000\t012A0A0A\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"wlan0\t00002A0A\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n" +
		"wt0\t0000FD0A\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	var got []string
	for _, n := range parseProcRoutes(text) {
		got = append(got, n.String())
	}
	if strings.Join(got, " ") != "10.42.0.0/24 10.253.0.0/16" {
		t.Errorf("networks: %v", got)
	}
}

// The image that runs is the one the signed release names, whatever the registry
// says about the tag.
func TestDockerUpdateRunsTheSignedDigestNotTheTag(t *testing.T) {
	u := newDockerUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "x")
	if _, err := u.update("v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.image(), "@"+imageDigestOf("v0.2.0")) {
		t.Errorf("the image is %q: not pinned to the digest of the release", u.image())
	}
	// A release without a vouched-for image is not updated to, whatever the tag says.
	u.fake.publish(t, u.key, "v0.3.0", "x")
	u.fake.set("v0.3.0/"+ImageFile, []byte("sha256:"+strings.Repeat("b", 64)+"\n"))
	before := u.image()
	if _, err := u.update("v0.3.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a swapped digest file: %v", err)
	}
	if u.image() != before || u.sys.saw("docker pull "+DockerRepo+":0.3.0") {
		t.Errorf("image %q; the server was touched:\n%s", u.image(), strings.Join(u.sys.commands(), "\n"))
	}
}

func TestSplitImageTakesRepoTagAndDigestApart(t *testing.T) {
	d := "sha256:" + strings.Repeat("c", 64)
	for in, want := range map[string][3]string{
		"ghcr.io/x/y:1.2.3@" + d:     {"ghcr.io/x/y", "1.2.3", d},
		"ghcr.io/x/y:1.2.3":          {"ghcr.io/x/y", "1.2.3", ""},
		"localhost:5000/gt:0.1@" + d: {"localhost:5000/gt", "0.1", d},
		"localhost:5000/gt":          {"localhost:5000/gt", "", ""},
	} {
		repo, tag, digest := splitImage(in)
		if [3]string{repo, tag, digest} != want {
			t.Errorf("%s: %q %q %q", in, repo, tag, digest)
		}
	}
}

func TestDockerEnvTrustsTheGatewayOnlyBehindTheProxyOfTheGeneratedConfiguration(t *testing.T) {
	for name, c := range map[string]struct {
		o    Options
		want string
	}{
		"domain behind a proxy": {Options{Domain: "gt.example.com", Gateway: "10.89.0.1", Subnet: "10.89.0.0/24"}, "GHOSTTREE_TRUSTED_PROXIES=10.89.0.1"},
		"no domain":             {Options{}, "GHOSTTREE_TRUSTED_PROXIES=none"},
		"built-in TLS":          {Options{Domain: "gt.example.com", TLS: "builtin"}, "GHOSTTREE_TRUSTED_PROXIES=none"},
	} {
		text := c.o.DockerEnvText("img")
		if !strings.Contains(text, c.want+"\n") || strings.Count(text, "GHOSTTREE_TRUSTED_PROXIES=") != 1 {
			t.Errorf("%s:\n%s", name, text)
		}
	}
	if !strings.Contains(ComposeText(), `GHOSTTREE_TRUSTED_PROXIES: "${GHOSTTREE_TRUSTED_PROXIES:-none}"`) {
		t.Error("compose.yaml must trust nobody by default")
	}
}

func TestDockerInstallPullsTheSignedDigestFromAMirror(t *testing.T) {
	prefix := t.TempDir()
	env, _, _, _ := signedEnv(t, newFakeSystem(t, prefix))
	o := Options{Prefix: prefix, Docker: true, Dir: "/opt/g", Version: "0.1.0", ImageRepo: "registry.example.org/gt"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(prefix, "opt/g/.env"))
	want := "GHOSTTREE_IMAGE=registry.example.org/gt:0.1.0@" + imageDigestOf("v0.1.0") + "\n"
	if !strings.Contains(string(b), want) {
		t.Errorf(".env:\n%s", b)
	}
}
