package serverinstall

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ctxSource is a "binary" to install: a file the fake system can run.
func ctxSource(t *testing.T, version string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ctx")
	if err := os.WriteFile(p, []byte("v:"+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func install(t *testing.T, o Options) (*Result, *fakeSystem, string, string) {
	t.Helper()
	prefix := t.TempDir()
	o.Prefix = prefix
	if o.CtxBinary == "" {
		o.CtxBinary = ctxSource(t, "0.1.0")
	}
	sys := newFakeSystem(t, prefix)
	env, out := testEnv(sys)
	res, err := Install(context.Background(), o, env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return res, sys, prefix, out.String()
}

func read(t *testing.T, prefix, abs string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(prefix, abs))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallBehindAProxyWritesTheFilesAndEndsWithALoginLink(t *testing.T) {
	res, sys, prefix, out := install(t, Options{Domain: "gt.example.com", Admin: "alex", Language: "de"})

	if res.LoginURL != "https://login.example/ui/login/code?code=ABCD" || res.PublicURL != "https://gt.example.com" {
		t.Errorf("result: %+v", res)
	}
	if !Healthy(res.Checks) {
		t.Errorf("checks: %+v\n%s", res.Checks, out)
	}
	if fi, err := os.Stat(filepath.Join(prefix, SecretKey)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("secret key: %v %v", fi, err)
	}
	if k := strings.TrimSpace(read(t, prefix, SecretKey)); len(k) != 44 { // 32 bytes, base64
		t.Errorf("secret key %q", k)
	}
	env := read(t, prefix, ServerEnv)
	for _, want := range []string{"GHOSTTREE_LISTEN=127.0.0.1:8474", "GHOSTTREE_PUBLIC_URL=https://gt.example.com", "GHOSTTREE_LANGUAGE=de", "GHOSTTREE_ENFORCE_ACCESS=1", "GHOSTTREE_TRUSTED_PROXIES=127.0.0.1"} {
		if !strings.Contains(env, want) {
			t.Errorf("server.env lacks %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "ACME") {
		t.Errorf("proxy mode must not switch the built-in TLS on:\n%s", env)
	}
	unit := read(t, prefix, UnitDir+"/"+ServiceID)
	if !strings.Contains(unit, "LoadCredential=ghosttree-secret-key:"+SecretKey) || strings.Contains(unit, "NET_BIND_SERVICE") {
		t.Errorf("unit:\n%s", unit)
	}
	timer := read(t, prefix, UnitDir+"/"+BackupTime)
	if !strings.Contains(timer, "OnCalendar=") || !strings.Contains(read(t, prefix, UnitDir+"/"+BackupID), "db backup --db "+DBPath+" --out "+BackupDir+" --keep 14") {
		t.Error("backup units are incomplete")
	}
	caddy, nginx := read(t, prefix, ProxyDir+"/Caddyfile"), read(t, prefix, ProxyDir+"/nginx.conf")
	if !strings.Contains(caddy, "gt.example.com {") || !strings.Contains(caddy, "reverse_proxy 127.0.0.1:8474") {
		t.Errorf("Caddyfile:\n%s", caddy)
	}
	for _, want := range []string{"server_name gt.example.com;", "proxy_pass http://127.0.0.1:8474;", "X-Forwarded-Proto $scheme", "X-Forwarded-Host $host", "X-Forwarded-For $remote_addr", "client_max_body_size 256m"} {
		if !strings.Contains(nginx, want) {
			t.Errorf("nginx.conf lacks %q", want)
		}
	}
	if b := read(t, prefix, BinPath); !strings.HasPrefix(b, "v:0.1.0") {
		t.Errorf("ctx was not installed: %q", b)
	}

	// The order that matters: units known before the start, the server up
	// before accounts are made, accounts before the link.
	order := []string{"systemctl daemon-reload", "systemctl restart ghosttree.service", "GET http://127.0.0.1:8474/api/health", "account add alex --admin", "account add gardener", "account set-gardener gardener", "account login-link alex --url https://gt.example.com"}
	last := -1
	for _, c := range order {
		i := sys.index(c)
		if i <= last {
			t.Fatalf("%q ran at %d, after %d; commands:\n%s", c, i, last, strings.Join(sys.commands(), "\n"))
		}
		last = i
	}
	if !sys.saw("systemctl start ghosttree-backup.service") {
		t.Error("no first backup")
	}
	if !sys.saw("User=ghosttree") || !sys.saw("StateDirectory=ghosttree") {
		t.Error("accounts must be made as the service's user")
	}
}

func TestInstallWithBuiltInTLSNeedsNoProxyAndMayBindThePorts(t *testing.T) {
	_, _, prefix, _ := install(t, Options{Domain: "gt.example.com", TLS: "builtin", Email: "ops@example.com"})
	env := read(t, prefix, ServerEnv)
	for _, want := range []string{"GHOSTTREE_TLS_ACME_HOST=gt.example.com", "GHOSTTREE_TLS_ACME_EMAIL=ops@example.com", "GHOSTTREE_PUBLIC_URL=https://gt.example.com", "GHOSTTREE_TRUSTED_PROXIES=none"} {
		if !strings.Contains(env, want) {
			t.Errorf("server.env lacks %q:\n%s", want, env)
		}
	}
	unit := read(t, prefix, UnitDir+"/"+ServiceID)
	if !strings.Contains(unit, "AmbientCapabilities=CAP_NET_BIND_SERVICE") {
		t.Errorf("the server cannot bind 443:\n%s", unit)
	}
	if _, err := os.Stat(filepath.Join(prefix, ProxyDir)); err == nil {
		t.Error("built-in TLS needs no proxy configuration")
	}
}

func TestInstallWithoutADomainStaysOnThisMachine(t *testing.T) {
	res, _, prefix, _ := install(t, Options{})
	if res.PublicURL != "http://127.0.0.1:8474" || !strings.HasPrefix(res.LoginURL, "https://login.example") {
		t.Errorf("%+v", res)
	}
	if env := read(t, prefix, ServerEnv); strings.Contains(env, "PUBLIC_URL") {
		t.Errorf("a local server has no public address:\n%s", env)
	}
	for _, c := range res.Checks {
		if c.Name == "public address" {
			t.Errorf("nothing to reach from outside: %+v", c)
		}
	}
}

func TestInstallConnectsAModelProviderThroughACredential(t *testing.T) {
	key := filepath.Join(t.TempDir(), "k")
	os.WriteFile(key, []byte("sk-test-123\n"), 0o600)
	_, _, prefix, _ := install(t, Options{AI: AIOptions{Format: "anthropic", Model: "claude-x", KeyFile: key}})
	if k := read(t, prefix, LLMKey); strings.TrimSpace(k) != "sk-test-123" {
		t.Errorf("key file: %q", k)
	}
	if fi, _ := os.Stat(filepath.Join(prefix, LLMKey)); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	cfg := read(t, prefix, LLMConfig)
	if !strings.Contains(cfg, `"credential": "llm-key"`) || strings.Contains(cfg, "sk-test") {
		t.Errorf("llm.json:\n%s", cfg)
	}
	unit := read(t, prefix, UnitDir+"/"+ServiceID)
	if !strings.Contains(unit, "LoadCredential=llm-key:"+LLMKey) || !strings.Contains(unit, "GHOSTTREE_LLM_CONFIG="+LLMConfig) || strings.Contains(unit, "sk-test") {
		t.Errorf("unit:\n%s", unit)
	}
}

// Running it again must not cost anyone their key, their settings or their account.
func TestInstallAgainKeepsKeyAndSettingsAndAccounts(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	o := Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Domain: "gt.example.com", Admin: "alex"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	key1, env1 := read(t, prefix, SecretKey), read(t, prefix, ServerEnv)
	sys.cmds = nil

	o.Domain, o.Language = "other.example.com", "en"
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	if read(t, prefix, SecretKey) != key1 {
		t.Error("the server key was replaced")
	}
	if read(t, prefix, ServerEnv) != env1 {
		t.Error("server.env was replaced without --force")
	}
	if sys.saw("account add alex") || sys.saw("account add gardener") {
		t.Errorf("accounts were added twice:\n%s", strings.Join(sys.commands(), "\n"))
	}
	if !sys.saw("account login-link alex") {
		t.Error("no new login link")
	}
	o.Force = true
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, prefix, ServerEnv), "other.example.com") {
		t.Error("--force did not write the answers")
	}
	if read(t, prefix, SecretKey) != key1 {
		t.Error("--force replaced the server key")
	}
}

func TestInstallRefusesAnswersItCannotUse(t *testing.T) {
	good := Options{Domain: "gt.example.com"}
	bad := map[string]Options{
		"ip as domain":        {Domain: "192.0.2.1"},
		"scheme in domain":    {Domain: "https://gt.example.com"},
		"no dot":              {Domain: "ghosttree"},
		"builtin w/o domain":  {TLS: "builtin"},
		"unknown tls":         {Domain: good.Domain, TLS: "magic"},
		"public listen+proxy": {Domain: good.Domain, Listen: "0.0.0.0:8474"},
		"no port":             {Listen: "127.0.0.1"},
		"odd admin":           {Admin: "a/b"},
		"odd language":        {Language: "fr"},
		"ai w/o key file":     {AI: AIOptions{Format: "anthropic"}},
		"ai unknown format":   {AI: AIOptions{Format: "x", KeyFile: "k"}},
		"openai w/o base url": {AI: AIOptions{Format: "openai", KeyFile: "k"}},
	}
	for name, o := range bad {
		prefix := t.TempDir()
		o.Prefix, o.CtxBinary = prefix, ctxSource(t, "0.1.0")
		env, _ := testEnv(newFakeSystem(t, prefix))
		if _, err := Install(context.Background(), o, env); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(filepath.Join(prefix, EtcDir)); err == nil {
			t.Errorf("%s: wrote files before refusing", name)
		}
	}
}

func TestDoctorSaysWhatIsWrongAndTreatsAnUnreachableNameAsUnverified(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	o := Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Domain: "gt.example.com"}
	res, err := Install(context.Background(), o, env)
	if err != nil {
		t.Fatal(err)
	}
	var public *Check
	for i, c := range res.Checks {
		if c.Name == "public address" {
			public = &res.Checks[i]
		}
	}
	if public == nil || !public.Unverified || public.OK || !strings.Contains(public.Fix, "Caddyfile") {
		t.Errorf("public address: %+v", public)
	}
	if !Healthy(res.Checks) {
		t.Errorf("an unreachable name is not a failure: %+v", res.Checks)
	}

	// A stopped service and a loose key file are failures.
	sys.running = false
	os.Chmod(filepath.Join(prefix, SecretKey), 0o644)
	failed := map[string]bool{}
	for _, c := range Doctor(context.Background(), OptionsFromDisk(prefix), env) {
		if !c.OK && !c.Unverified {
			failed[c.Name] = true
			if c.Fix == "" {
				t.Errorf("%s fails without saying what to do", c.Name)
			}
		}
	}
	if !failed["service"] || !failed["server"] || !failed["server key"] {
		t.Errorf("failures: %v", failed)
	}
}

// deploy/ghosttree.service is the sample people copy by hand; the installer's
// unit must not drift from it.
func TestServiceUnitMatchesTheSampleInDeploy(t *testing.T) {
	f, err := os.Open("../../deploy/ghosttree.service")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	generated := Options{}.ServiceText()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !strings.Contains(generated, line+"\n") {
			t.Errorf("the generated unit lacks the sample's line %q", line)
		}
		n++
	}
	if n < 20 {
		t.Fatalf("read only %d lines of the sample", n)
	}
}

func TestOptionsFromDiskReadsWhatInstallWrote(t *testing.T) {
	_, _, prefix, _ := install(t, Options{Domain: "gt.example.com", TLS: "builtin", Language: "de"})
	got := OptionsFromDisk(prefix)
	if got.Domain != "gt.example.com" || got.TLS != "builtin" || got.Language != "de" || got.PublicURL() != "https://gt.example.com" {
		t.Errorf("%+v", got)
	}
	if o := OptionsFromDisk(t.TempDir()); o.PublicURL() != "http://127.0.0.1:8474" {
		t.Errorf("defaults: %+v", o)
	}
}
