package serverinstall

import (
	"bufio"
	"context"
	"errors"
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
	if !strings.Contains(unit, "LoadCredential=llm-key:"+LLMKey) || !strings.Contains(unit, "Environment=GHOSTTREE_LLM_CONFIG="+LLMConfig+"\n") || strings.Contains(unit, "sk-test") {
		t.Errorf("unit:\n%s", unit)
	}
}

// The service runs as a dynamic user, not as root. Every file the unit points
// it to must be one that user can open: a path it can walk to and read (a
// directory only root may enter, with a world-readable file in it, is the
// mistake this catches), or a credential that systemd hands over. A credential
// is not an excuse for a path: the service opens the path it is given, as itself.
func TestEveryFileTheServiceIsPointedToIsReadableByTheServiceUser(t *testing.T) {
	key := filepath.Join(t.TempDir(), "k")
	os.WriteFile(key, []byte("sk-test-123\n"), 0o600)
	for name, o := range map[string]Options{
		"proxy":   {AI: AIOptions{Format: "anthropic", KeyFile: key}},
		"builtin": {Domain: "gt.example.com", TLS: "builtin", AI: AIOptions{Format: "openai", BaseURL: "https://x.example/v1", KeyFile: key}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, prefix, _ := install(t, o)
			unit := read(t, prefix, UnitDir+"/"+ServiceID)
			byName, byPath := map[string]bool{}, map[string]bool{} // credentials systemd loads as root
			for _, line := range strings.Split(unit, "\n") {
				if rest, ok := strings.CutPrefix(line, "LoadCredential="); ok {
					if name, path, ok := strings.Cut(rest, ":"); ok {
						byName[name], byPath[path] = true, true
					}
				}
			}
			// Every path an Environment= line hands the service. One inside the
			// credentials directory (%d/name) must be a credential that is loaded;
			// any other is opened by the service itself, as itself, so a credential
			// of the same file does not help it.
			var pointed []string
			var inCredentials int
			for _, line := range strings.Split(unit, "\n") {
				rest, ok := strings.CutPrefix(line, "Environment=")
				if !ok {
					continue
				}
				_, v, ok := strings.Cut(rest, "=")
				switch {
				case ok && strings.HasPrefix(v, "%d/"):
					inCredentials++
					if name := strings.TrimPrefix(v, "%d/"); !byName[name] {
						t.Errorf("%s points into the credentials directory, but no credential %q is loaded:\n%s", line, name, unit)
					}
				case ok && strings.HasPrefix(v, "/"):
					pointed = append(pointed, v)
				}
			}
			if inCredentials == 0 && len(pointed) == 0 {
				t.Fatalf("the unit does not name the model configuration:\n%s", unit)
			}
			for _, p := range pointed {
				full := filepath.Join(prefix, p)
				for d := filepath.Dir(full); len(d) > len(prefix); d = filepath.Dir(d) {
					if fi, err := os.Stat(d); err != nil || fi.Mode().Perm()&0o001 == 0 {
						t.Errorf("the service user cannot enter %s to read %s (mode %v)", d, p, fi.Mode().Perm())
					}
				}
				if fi, err := os.Stat(full); err != nil || fi.Mode().Perm()&0o004 == 0 {
					t.Errorf("the service user cannot read %s", p)
				}
			}
			// The directory can be entered but not listed, and what is secret stays
			// root's and is only handed over by systemd.
			if fi, err := os.Stat(filepath.Join(prefix, EtcDir)); err != nil || fi.Mode().Perm() != EtcDirMode {
				t.Errorf("%s has mode %v, want %v", EtcDir, fi.Mode().Perm(), os.FileMode(EtcDirMode))
			}
			for _, p := range []string{SecretKey, LLMKey} {
				if !byPath[p] {
					t.Errorf("%s is not loaded as a credential", p)
				}
				if fi, err := os.Stat(filepath.Join(prefix, p)); err != nil || fi.Mode().Perm()&0o077 != 0 {
					t.Errorf("%s is readable by others: %v %v", p, fi, err)
				}
			}
		})
	}
}

// install over a server that is set up would swap ctx and settings under it
// without a copy of its data. It stops, and says what to run instead.
func TestInstallOnAServerThatExistsPointsToUpdateAndTouchesNothing(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	o := Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Domain: "gt.example.com", Admin: "alex"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	key1, env1, ctx1 := read(t, prefix, SecretKey), read(t, prefix, ServerEnv), read(t, prefix, BinPath)
	sys.cmds = nil

	o.CtxBinary = ctxSource(t, "0.0.9") // an older ctx, as install.sh used to put there
	o.Domain, o.Language = "other.example.com", "en"
	_, err := Install(context.Background(), o, env)
	if !errors.Is(err, ErrInstalled) || !strings.Contains(err.Error(), "ctx server update") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
	if len(sys.commands()) != 0 {
		t.Errorf("it ran commands:\n%s", strings.Join(sys.commands(), "\n"))
	}
	if read(t, prefix, BinPath) != ctx1 || read(t, prefix, ServerEnv) != env1 || read(t, prefix, SecretKey) != key1 {
		t.Error("the files of the running server were changed")
	}
}

// --force goes on, with what an update does: a copy of the database first, the
// program replaced with the old one kept, the schema upgraded; key and accounts stay.
func TestInstallForceOnAServerThatExistsCopiesTheDatabaseAndUpgradesTheSchema(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	o := Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Domain: "gt.example.com", Admin: "alex"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, StateDir), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(prefix, DBPath), []byte("the data"), 0o600)
	key1, env1 := read(t, prefix, SecretKey), read(t, prefix, ServerEnv)
	sys.cmds = nil

	o.CtxBinary = ctxSource(t, "0.2.0")
	o.Domain, o.Language, o.Force = "other.example.com", "en", true
	res, err := Install(context.Background(), o, env)
	if err != nil {
		t.Fatal(err)
	}
	if got := sys.binVersion(); got != "0.2.0" {
		t.Errorf("the binary is %q", got)
	}
	if v, _ := versionOf(filepath.Join(prefix, BinPath+".prev-0.1.0")); v != "0.1.0" {
		t.Errorf("the old binary was not kept: %q", v)
	}
	if !sys.saw("db backup --db "+DBPath+" --out "+BackupDir+"/pre-update-0.1.0-to-0.2.0-") || !sys.saw("upgrade-schema --db "+DBPath) {
		t.Errorf("no copy of the database or no schema upgrade:\n%s", strings.Join(sys.commands(), "\n"))
	}
	if i, j, k := sys.index("systemctl stop"), sys.index("db backup"), sys.index("upgrade-schema"); !(i >= 0 && i < j && j < k) {
		t.Errorf("order stop %d, backup %d, upgrade %d", i, j, k)
	}
	if read(t, prefix, SecretKey) != key1 {
		t.Error("the server key was replaced")
	}
	if read(t, prefix, ServerEnv) == env1 || !strings.Contains(read(t, prefix, ServerEnv), "other.example.com") {
		t.Error("--force did not write the answers")
	}
	if sys.saw("account add alex") || sys.saw("account add gardener") {
		t.Errorf("accounts were added twice:\n%s", strings.Join(sys.commands(), "\n"))
	}
	if res.Admin != "alex" || !sys.saw("account login-link alex") {
		t.Errorf("no new login link: %+v", res)
	}
}

// A forced install that cannot bring the new program up must leave the old
// server running, like an update.
func TestInstallForceGoesBackWhenTheNewProgramDoesNotComeUp(t *testing.T) {
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	o := Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Admin: "alex"}
	if _, err := Install(context.Background(), o, env); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(prefix, StateDir), 0o755)
	os.WriteFile(filepath.Join(prefix, DBPath), []byte("the data"), 0o600)
	p := filepath.Join(t.TempDir(), "ctx")
	os.WriteFile(p, []byte("v:0.2.0 broken\n"), 0o755)
	o.CtxBinary, o.Force = p, true
	if _, err := Install(context.Background(), o, env); err == nil || !strings.Contains(err.Error(), "runs again") {
		t.Fatalf("error = %v", err)
	}
	if got := sys.binVersion(); got != "0.1.0" || !sys.running || !sys.saw("db restore") {
		t.Errorf("binary %q, running %v:\n%s", got, sys.running, strings.Join(sys.commands(), "\n"))
	}
}

// The first line of defence of the one line that every admin runs twice.
func TestInstallFirstTimeOnAMachineWithOnlyAClientCtxWorks(t *testing.T) {
	prefix := t.TempDir()
	os.MkdirAll(filepath.Join(prefix, filepath.Dir(BinPath)), 0o755)
	os.WriteFile(filepath.Join(prefix, BinPath), []byte("v:0.0.1\n"), 0o755) // a client's ctx is not a server
	sys := newFakeSystem(t, prefix)
	env, out := testEnv(sys)
	if _, err := Install(context.Background(), Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0")}, env); err != nil {
		t.Fatalf("%v\n%s", err, out)
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
		// Whatever would end up as a line of server.env, Caddyfile or nginx.conf.
		"newline in domain":    {Domain: "gt.example.com\nGHOSTTREE_ENFORCE_ACCESS=0"},
		"brace in domain":      {Domain: "gt.example.com {\nredir"},
		"semicolon in domain":  {Domain: "gt.example.com;x.example.com"},
		"upper case domain":    {Domain: "GT.example.com"},
		"underscore in domain": {Domain: "gt_1.example.com"},
		"quote in domain":      {Domain: `gt".example.com`},
		"leading hyphen":       {Domain: "-gt.example.com"},
		"long label":           {Domain: strings.Repeat("a", 64) + ".example.com"},
		"numeric tld":          {Domain: "gt.example.123"},
		"newline in email":     {Domain: "gt.example.com", TLS: "builtin", Email: "ops@example.com\nGHOSTTREE_ENFORCE_ACCESS=0"},
		"space in email":       {Domain: "gt.example.com", TLS: "builtin", Email: "ops @example.com"},
		"two ats in email":     {Domain: "gt.example.com", TLS: "builtin", Email: "a@b@example.com"},
		"email w/o domain":     {Domain: "gt.example.com", TLS: "builtin", Email: "ops@localhost"},
		"newline in listen":    {Listen: "127.0.0.1\nX=1:8474"},
		"space in listen":      {Listen: "127.0.0.1 :8474"},
		"port 0":               {Listen: "127.0.0.1:0"},
		"port too big":         {Listen: "127.0.0.1:70000"},
		"port text":            {Listen: "127.0.0.1:http"},
		"control in admin":     {Admin: "al\x1b[2Jex"},
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

func TestServiceUnitsAreHardened(t *testing.T) {
	want := []string{"NoNewPrivileges=yes", "PrivateDevices=yes", "ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes",
		"ProtectControlGroups=yes", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "RestrictNamespaces=yes", "LockPersonality=yes",
		"SystemCallFilter=@system-service", "MemoryDenyWriteExecute=yes"}
	for name, text := range map[string]string{
		"proxy":   Options{Domain: "gt.example.com"}.ServiceText(),
		"builtin": Options{Domain: "gt.example.com", TLS: "builtin"}.ServiceText(),
		"backup":  BackupServiceText(),
	} {
		for _, w := range want {
			if !strings.Contains(text, w+"\n") {
				t.Errorf("%s unit lacks %s:\n%s", name, w, text)
			}
		}
	}
	// Behind a proxy no capability at all; the built-in certificate needs the
	// low ports and nothing else.
	if u := (Options{Domain: "gt.example.com"}).ServiceText(); !strings.Contains(u, "\nCapabilityBoundingSet=\n") || strings.Contains(u, "CAP_") {
		t.Errorf("proxy unit keeps a capability:\n%s", u)
	}
	if u := (Options{Domain: "gt.example.com", TLS: "builtin"}).ServiceText(); !strings.Contains(u, "\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n") || strings.Count(u, "CAP_") != 2 {
		t.Errorf("built-in TLS unit:\n%s", u)
	}
	if !strings.Contains(BackupServiceText(), "\nCapabilityBoundingSet=\n") {
		t.Error("the backup needs no capability")
	}
}

func TestPlainNamesAndAddressesAreAccepted(t *testing.T) {
	for _, o := range []Options{
		{Domain: "gt.example.com"},
		{Domain: "ghost-tree.eu"},
		{Domain: "a.b.c.example.org", TLS: "builtin", Email: "ops+gt@example.com"},
		{Domain: "xn--bcher-kva.example", TLS: "builtin", Email: "Ops.Team_1@mail.example.co.uk"},
		{Listen: "127.0.0.1:8474"}, {Listen: "[::1]:8474"}, {Listen: ":8474"}, {Listen: "localhost:9000"}, {Listen: "10.0.0.5:443"},
		{Admin: "alex"}, {Admin: "Álex Müller"},
	} {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

// An install of an earlier release made /etc/ghosttree 0700, which the service's
// user cannot enter; a new install opens it (for entering, not listing).
func TestInstallOpensAConfigDirectoryAnEarlierInstallClosed(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, EtcDir), 0o700); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	if _, err := Install(context.Background(), Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0")}, env); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(prefix, EtcDir)); err != nil || fi.Mode().Perm() != 0o711 {
		t.Errorf("%v %v", fi, err)
	}
}
