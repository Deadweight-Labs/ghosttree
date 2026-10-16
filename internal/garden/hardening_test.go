package garden

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func argVal(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Errorf("no %s", flag)
	return ""
}

func TestClaudeArgsAreLockedDown(t *testing.T) {
	c := ClaudeConfig{Bin: "claude", Model: "claude-sonnet-5-5", RepoRoot: "/r", CtxBin: "/usr/bin/ctx", DenyRead: []string{"/srv/x/private"},
		ParentEnv: []string{"HOME=/h", "PATH=/bin", "GHOSTTREE_GARDEN_KEY=geheim"}}
	args, err := c.Args(SessionSpec{MaxTurns: 60, BudgetUSD: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, bad := range []string{"bypassPermissions", "acceptEdits", "dangerously", "--allowedTools\x00Edit", "--allow-dangerously", "geheim"} {
		if strings.Contains(joined, bad) {
			t.Errorf("args contain %q", bad)
		}
	}
	if argVal(t, args, "--permission-mode") != "dontAsk" || argVal(t, args, "--max-turns") != "60" || argVal(t, args, "--max-budget-usd") != "1.50" {
		t.Errorf("%v", args)
	}
	allowed := argVal(t, args, "--allowedTools")
	for _, w := range []string{"Read(//r/**)", "mcp__ghosttree__context_search", "mcp__ghosttree__context_get", "mcp__gardengit__git_log", "mcp__gardengit__git_show"} {
		if !strings.Contains(allowed, w) {
			t.Errorf("allowlist lacks %s", w)
		}
	}
	denied := argVal(t, args, "--disallowedTools")
	for _, w := range []string{"context_remember", "context_retire", "context_supersede", "context_describe_file", "Edit", "Write", "WebFetch", "Bash"} {
		if strings.Contains(allowed, w) {
			t.Errorf("allowlist contains %s", w)
		}
		if !strings.Contains(denied, w) {
			t.Errorf("denylist lacks %s", w)
		}
	}
	if !strings.Contains(denied, "Read(//srv/x/private/**)") {
		t.Errorf("deny-read missing: %s", denied)
	}
	cfg := argVal(t, args, "--mcp-config")
	if !contains(args, "--strict-mcp-config") || !strings.Contains(cfg, `"args":["mcp"]`) ||
		!strings.Contains(cfg, `"args":["garden","git-mcp","--repo","/r"]`) || strings.Contains(cfg, "geheim") {
		t.Errorf("mcp: %s", cfg)
	}
	if argVal(t, args, "--setting-sources") != "user" {
		t.Error("project settings of the checkout must not apply")
	}
	c.CtxBin = ""
	args, _ = c.Args(SessionSpec{MaxTurns: 20})
	if strings.Contains(strings.Join(args, " "), "ghosttree\":{") || strings.Contains(strings.Join(args, " "), "gardengit") && strings.Contains(argVal(t, args, "--mcp-config"), "command") || contains(args, "--max-budget-usd") {
		t.Errorf("no MCP and no budget expected: %v", args)
	}
	if _, err := (ClaudeConfig{RepoRoot: "/r", DenyRead: []string{"relative"}}).Args(SessionSpec{MaxTurns: 20}); err == nil {
		t.Error("relative deny path accepted")
	}
}

// REQ-491 (1): no free Bash. The tool set has no Bash and nothing Bash-shaped is
// allowed; git comes only through the fixed tools.
func TestClaudeSessionHasNoBash(t *testing.T) {
	args, err := ClaudeConfig{RepoRoot: "/srv/repo", CtxBin: "/ctx"}.Args(SessionSpec{MaxTurns: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := argVal(t, args, "--tools"); got != "Read,Grep,Glob" {
		t.Errorf("tool set %q", got)
	}
	for _, a := range strings.Split(argVal(t, args, "--allowedTools"), ",") {
		if strings.HasPrefix(a, "Bash") || strings.Contains(a, "git ") {
			t.Errorf("allowed %q", a)
		}
	}
	if !contains(strings.Split(argVal(t, args, "--disallowedTools"), ","), "Bash") {
		t.Error("Bash is not denied")
	}
}

// REQ-491 (2): Grep and Glob are not allowed by name, so they only reach what
// the Read allowlist for the checkout reaches.
func TestClaudeArgsReadOnlyInsideTheCheckout(t *testing.T) {
	args, err := ClaudeConfig{RepoRoot: "/srv/repo"}.Args(SessionSpec{MaxTurns: 20})
	if err != nil {
		t.Fatal(err)
	}
	allowed := strings.Split(argVal(t, args, "--allowedTools"), ",")
	for _, a := range allowed {
		if a == "Read" || a == "Grep" || a == "Glob" || strings.HasPrefix(a, "Grep(") || strings.HasPrefix(a, "Glob(") {
			t.Errorf("allowed %q reaches beyond the checkout", a)
		}
	}
	if !contains(allowed, "Read(//srv/repo/**)") {
		t.Errorf("Read is not scoped to the checkout: %v", allowed)
	}
	denied := argVal(t, args, "--disallowedTools")
	for _, d := range []string{"Read(//proc/**)", "Read(//etc/**)", "Read(//root/**)"} {
		if !strings.Contains(denied, d) {
			t.Errorf("denylist lacks %s", d)
		}
	}
	// A checkout below a system directory is not locked out by it.
	args, _ = ClaudeConfig{RepoRoot: "/run/gt/repo"}.Args(SessionSpec{MaxTurns: 20})
	if strings.Contains(argVal(t, args, "--disallowedTools"), "Read(//run/**)") {
		t.Error("denied the directory the checkout lies in")
	}
	if _, err := (ClaudeConfig{RepoRoot: "rel"}).Args(SessionSpec{MaxTurns: 20}); err == nil {
		t.Error("relative repo root accepted")
	}
	if _, err := (ClaudeConfig{}).Args(SessionSpec{MaxTurns: 20}); err == nil {
		t.Error("no repo root accepted: the allowlist would not be scoped")
	}
}

// REQ-491 (5): the session's environment is a short allowlist and the only
// secret in it is the value that assigns the session to its account.
func TestClaudeEnvironIsAnAllowlist(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/home/robin", "USER=robin", "LANG=de_DE.UTF-8", "GHOSTTREE_GARDEN_KEY=geheim", "ANTHROPIC_API_KEY=alt",
		"XDG_CONFIG_HOME=/x", "AWS_SECRET_ACCESS_KEY=s", "GIT_CONFIG_GLOBAL=/g", "HTTPS_PROXY=http://u:pw@proxy", "GITHUB_TOKEN=t",
		"SSH_AUTH_SOCK=/s", "GHOSTTREE_DB=/db", "CLAUDE_CODE_OAUTH_TOKEN=o", "SSL_CERT_FILE=/c"}
	c := ClaudeConfig{BaseURL: "http://proxy:18317", Token: "dwl-garden", ParentEnv: parent}
	run := "/cfg/run-1"
	env := c.Environ(run)
	allowedKeys := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"CLAUDE_CONFIG_DIR": true, "HOME": true, "XDG_CACHE_HOME": true, "ANTHROPIC_BASE_URL": true, "ANTHROPIC_AUTH_TOKEN": true,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true, "DISABLE_AUTOUPDATER": true, "DISABLE_TELEMETRY": true, "DISABLE_ERROR_REPORTING": true}
	var keys []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
		if !allowedKeys[k] {
			t.Errorf("environment has %q", kv)
		}
		for _, secret := range []string{"geheim", "alt", "pw@", "AKIA"} {
			if strings.Contains(v, secret) {
				t.Errorf("environment leaks %q in %q", secret, kv)
			}
		}
	}
	sort.Strings(keys)
	for _, want := range []string{"PATH", "CLAUDE_CONFIG_DIR", "HOME", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
		if !contains(keys, want) {
			t.Errorf("environment lacks %s: %v", want, keys)
		}
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"CLAUDE_CONFIG_DIR=" + run, "HOME=" + run, "ANTHROPIC_AUTH_TOKEN=dwl-garden"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q", want)
		}
	}
}

// A stand-in for the claude binary that is told by the test where to record.
func TestClaudeRunnerStartsInTheCheckoutWithThePromptOnStdin(t *testing.T) {
	t.Setenv("GHOSTTREE_GARDEN_KEY", "geheim")
	bin, record := fakeClaude(t, streamFixture)
	root := t.TempDir()
	cfgRoot := t.TempDir()
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: root, ConfigRoot: cfgRoot, Model: "m", ParentEnv: os.Environ(), Timeout: 30 * time.Second}}
	res, err := r.RunSession(context.Background(), SessionSpec{Prompt: "PRÜFE DAS", MaxTurns: 20})
	if err != nil || res.Turns != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	raw, _ := os.ReadFile(record)
	got := string(raw)
	real, _ := filepath.EvalSymlinks(root)
	for _, want := range []string{real + "\n", "CFG=" + cfgRoot + "/run-", "KEY=\n", "ARG=dontAsk\n", "ARG=--max-turns\n", "PROMPT\nPRÜFE DAS"} {
		if !strings.Contains(got, want) {
			t.Errorf("record lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "geheim") {
		t.Errorf("the garden key reached the session:\n%s", got)
	}
}

// REQ-491 (4): without a config root the agent mode does not start, and every
// run gets its own empty directory, which is gone afterwards.
func TestClaudeRunnerNeedsAConfigRootAndGivesEachRunAnEmptyOne(t *testing.T) {
	bin, record := fakeClaude(t, streamFixture)
	root := t.TempDir()
	_, err := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: root}}.RunSession(context.Background(), SessionSpec{MaxTurns: 20})
	if err != ErrNoConfigRoot {
		t.Fatalf("started without a config root: %v", err)
	}
	if _, serr := os.Stat(record); serr == nil {
		t.Error("claude was started anyway")
	}
	if _, err := (ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: root, ConfigRoot: "relativ"}}).RunSession(context.Background(), SessionSpec{MaxTurns: 20}); err == nil {
		t.Error("relative config root accepted")
	}

	cfgRoot := t.TempDir()
	// What an earlier run or a person left in the root must not reach a session.
	if err := os.WriteFile(filepath.Join(cfgRoot, "settings.json"), []byte(`{"permissions":{"allow":["Bash"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: root, ConfigRoot: cfgRoot, Timeout: 30 * time.Second}}
	var dirs []string
	for i := 0; i < 2; i++ {
		if _, err := r.RunSession(context.Background(), SessionSpec{Prompt: "x", MaxTurns: 20}); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(record)
		var cfg, ls string
		for _, l := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(l, "CFG="); ok {
				cfg = v
			}
			if v, ok := strings.CutPrefix(l, "CFGLS="); ok {
				ls = strings.TrimSpace(v)
			}
		}
		if cfg == "" || cfg == cfgRoot || filepath.Dir(cfg) != cfgRoot {
			t.Fatalf("config dir %q is not a fresh directory below %s", cfg, cfgRoot)
		}
		if ls != "" {
			t.Errorf("config dir was not empty at the start: %q", ls)
		}
		if _, err := os.Stat(cfg); err == nil {
			t.Errorf("config dir %s was left behind", cfg)
		}
		dirs = append(dirs, cfg)
	}
	if dirs[0] == dirs[1] {
		t.Errorf("two runs shared %s", dirs[0])
	}
}

// REQ-491 (5), on the real process: what the started program sees.
func TestClaudeRunnerStartsWithTheAllowlistedEnvironment(t *testing.T) {
	t.Setenv("GHOSTTREE_GARDEN_KEY", "geheim")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "geheim2")
	t.Setenv("GITHUB_TOKEN", "geheim3")
	bin, record := fakeClaude(t, streamFixture)
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: t.TempDir(), ConfigRoot: t.TempDir(), Token: "dwl", ParentEnv: os.Environ()}}
	if _, err := r.RunSession(context.Background(), SessionSpec{MaxTurns: 20}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(l, "ENV:") {
			continue
		}
		k, _, _ := strings.Cut(strings.TrimPrefix(l, "ENV:"), "=")
		switch k {
		case "PATH", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "CLAUDE_CONFIG_DIR", "HOME", "XDG_CACHE_HOME", "ANTHROPIC_AUTH_TOKEN",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING",
			// set by the shell that runs the stand-in itself
			"PWD", "OLDPWD", "SHLVL", "_":
		default:
			t.Errorf("the session sees %s", l)
		}
	}
	if strings.Contains(string(raw), "geheim") {
		t.Error("a secret reached the session")
	}
}

// REQ-491 (1): every way of getting an option, an exec path, another directory
// or a path outside the checkout past the git tool is refused before git runs.
func TestGitToolRefusesForbiddenArguments(t *testing.T) {
	badRevs := []string{
		"--output=/tmp/x", "--ou=/tmp/x", "--out", "--exec-path=/tmp", "--exec-path", "-c", "-c core.pager=sh", "-C", "-C /", "--git-dir=/etc",
		"--contents", "--ignore-revs-file=/etc/passwd", "-S", "--no-index", "--help", "-h", "--", "--all", "-p",
		"HEAD --output=x", "HEAD\n--output=x", "HEAD;id", "HEAD^", "HEAD~1", "HEAD..HEAD", "HEAD...HEAD", "main", "refs/heads/main", "@{u}", "@", ":/x",
		"HEAD:internal/distill.go", "abc", "abcdefg", "ABCDEF1", "0123456789abcdef0123456789abcdef012345678", " abcdef1", "abcdef1 ", "abcdef1\x00", "$(id)", "`id`",
		"-abcdef1", "--abcdef1",
	}
	for _, rev := range badRevs {
		if _, err := ShowArgs(GitShowParams{Commit: rev}); err == nil {
			t.Errorf("show accepted revision %q", rev)
		}
	}
	badPaths := []string{
		"../x", "..", "a/../../x", "/etc/passwd", "/", "~", "~/x", "~root/x", "-x", "--output=x", "-C", "--exec-path", ":(top)x", ":!x", ":/x", ":(glob)**",
		"*.go", "a*", "a?", "[a]", "a\\b", "a b", "a\nb", "a\x00b", ".git", ".git/config", ".git/hooks/pre-commit", "x/.git/config", "a/.git", "$HOME", "`id`", "a;b", "a|b", "a>b",
		strings.Repeat("a", 301),
	}
	for _, p := range badPaths {
		if _, err := LogArgs(GitLogParams{Path: p}); err == nil {
			t.Errorf("log accepted path %q", p)
		}
		if _, err := ShowArgs(GitShowParams{Commit: "HEAD", Path: p}); err == nil {
			t.Errorf("show accepted path %q", p)
		}
	}
	for _, n := range []int{-1, 101, 1 << 20} {
		if _, err := LogArgs(GitLogParams{Max: n}); err == nil {
			t.Errorf("log accepted max %d", n)
		}
	}
	for _, pick := range []string{strings.Repeat("x", 201), "a\nb", "a\x00b", "\xff\xfe"} {
		if _, err := LogArgs(GitLogParams{Pickaxe: pick}); err == nil {
			t.Errorf("log accepted pickaxe %q", pick)
		}
	}
}

func TestGitToolBuildsOneFixedCommandLine(t *testing.T) {
	args, err := LogArgs(GitLogParams{Path: "internal/garden", Max: 5, Pickaxe: "--output=/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	// The pickaxe text is glued to -S, so it is one argument that can never be
	// read as an option of its own, and the path sits behind "--".
	if !contains(args, "-S--output=/tmp/x") || contains(args, "--output=/tmp/x") {
		t.Errorf("pickaxe not glued to -S: %v", args)
	}
	dd := -1
	for i, a := range args {
		if a == "--" {
			dd = i
		}
		if strings.HasPrefix(a, "--output") || a == "--pickaxe-regex" || a == "-G" {
			t.Errorf("unexpected argument %q", a)
		}
	}
	if dd < 0 || args[len(args)-1] != "internal/garden" || dd != len(args)-2 {
		t.Errorf("path not behind --: %v", args)
	}
	if !contains(args, "--no-ext-diff") || !contains(args, "--no-textconv") || args[0] != "--literal-pathspecs" {
		t.Errorf("not locked down: %v", args)
	}
	sh, err := ShowArgs(GitShowParams{Commit: "abcdef12", Path: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if sh[len(sh)-3] != "abcdef12" || sh[len(sh)-2] != "--" || sh[len(sh)-1] != "a.go" || !contains(sh, "--no-ext-diff") {
		t.Errorf("show: %v", sh)
	}
}

func TestGitToolRunsAgainstTheCheckoutAndWritesNothing(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	out, err := repo.GitLog(ctx, GitLogParams{Path: "internal/distill.go"})
	if err != nil || !strings.Contains(out, "add distill") {
		t.Fatalf("%q %v", out, err)
	}
	hash := strings.Fields(out)[0]
	if len(hash) < 8 {
		t.Errorf("hash %q is too short to be evidence", hash)
	}
	show, err := repo.GitShow(ctx, GitShowParams{Commit: hash, Path: "internal/distill.go"})
	if err != nil || !strings.Contains(show, "SessionDistillationExists") {
		t.Fatalf("%q %v", show, err)
	}
	if out, err := repo.GitLog(ctx, GitLogParams{Pickaxe: "SessionDistillationExists"}); err != nil || !strings.Contains(out, "add distill") {
		t.Errorf("pickaxe: %q %v", out, err)
	}
	// Neither a pickaxe that looks like an option nor a path that looks like one
	// writes a file or reads another.
	target := filepath.Join(t.TempDir(), "geschrieben")
	_, _ = repo.GitLog(ctx, GitLogParams{Pickaxe: "--output=" + target})
	_, _ = repo.GitLog(ctx, GitLogParams{Path: "--output=" + target})
	_, _ = repo.GitShow(ctx, GitShowParams{Commit: "HEAD", Path: "--output=" + target})
	if _, err := os.Stat(target); err == nil {
		t.Error("the git tool wrote a file")
	}
	if _, err := repo.GitShow(ctx, GitShowParams{Commit: "deadbeefdeadbeef"}); err == nil {
		t.Error("unknown commit did not fail")
	}
}

// REQ-491 (6): git calls of ctx run with core.hooksPath=/dev/null, no
// fsmonitor, no system config and a neutral global one.
func TestSafeGitIsNeutral(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("GIT_EXTERNAL_DIFF", "/bin/sh")
	t.Setenv("GIT_SSH_COMMAND", "evil")
	t.Setenv("GIT_CONFIG_GLOBAL", "/home/robin/.gitconfig")
	t.Setenv("HOME", "/home/robin")
	cmd := SafeGit("/r", "status")
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "-C /r status"} {
		if !strings.Contains(args, want) {
			t.Errorf("git command lacks %q: %s", want, args)
		}
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=/nonexistent"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %q:\n%s", want, env)
		}
	}
	for _, bad := range []string{"GIT_DIR", "GIT_EXTERNAL_DIFF", "GIT_SSH_COMMAND", "/home/robin"} {
		if strings.Contains(env, bad) {
			t.Errorf("environment has %q:\n%s", bad, env)
		}
	}
	// hooks and fsmonitor are in the command line before the subcommand, so
	// nothing after it can switch them back on
	if i := strings.Index(args, "status"); i < strings.Index(args, "core.hooksPath") {
		t.Errorf("config after the subcommand: %s", args)
	}
}

// A repository config that names a program to run (fsmonitor, a hook, an
// external diff) must not run it when ctx looks at the checkout afterwards.
func TestRepoGitDoesNotRunWhatTheCheckoutConfigNames(t *testing.T) {
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "gelaufen")
	script := filepath.Join(t.TempDir(), "boese.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "\n[core]\n\tfsmonitor = " + script + "\n\thooksPath = " + filepath.Dir(script) + "\n[diff]\n\texternal = " + script + "\n[alias]\n\tstatus = !" + script + "\n"
	f, err := os.OpenFile(filepath.Join(repo.Root, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cfg)
	f.Close()
	hook := filepath.Join(repo.Root, ".git", "hooks", "post-index-change")
	os.MkdirAll(filepath.Dir(hook), 0o755)
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	// A global config of the user running ctx is not read either.
	global := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(global, []byte("[core]\n\tfsmonitor = "+script+"\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	if _, err := repo.Status(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Head(); err != nil {
		t.Fatal(err)
	}
	_, _ = repo.GitLog(context.Background(), GitLogParams{})
	_, _ = repo.GitShow(context.Background(), GitShowParams{Commit: "HEAD"})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program named by the checkout's config or hooks ran")
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// REQ-491 (6): the check of the checkout after a session sees ignored files,
// .git/config and .git/hooks, not only what git status lists.
func TestCheckAgentDiscardsASessionThatTouchedWhatGitStatusDoesNotShow(t *testing.T) {
	cases := map[string]func(repo *Repo){
		"ignorierte Datei": func(r *Repo) { writeFile(t, filepath.Join(r.Root, "versteckt.log"), "x") },
		"ignorierte Datei in neuem Ordner": func(r *Repo) {
			writeFile(t, filepath.Join(r.Root, "build", "out.log"), "x")
		},
		".git/config": func(r *Repo) {
			f, err := os.OpenFile(filepath.Join(r.Root, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("\n[core]\n\tfsmonitor = /bin/true\n")
			f.Close()
		},
		".git/hooks": func(r *Repo) { writeFile(t, filepath.Join(r.Root, ".git", "hooks", "pre-commit"), "#!/bin/sh\n") },
		".git/info/exclude": func(r *Repo) {
			writeFile(t, filepath.Join(r.Root, ".git", "info", "exclude"), "alles\n")
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			writeFile(t, filepath.Join(repo.Root, ".gitignore"), "*.log\nbuild/\n")
			run := func(args ...string) {
				cmd := exec.Command("git", "-C", repo.Root, "-c", "user.name=t", "-c", "user.email=t@example.invalid")
				cmd.Args = append(cmd.Args, args...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			}
			run("add", ".gitignore")
			run("commit", "-q", "-m", "ignore")
			fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){func(s SessionSpec) (SessionResult, error) {
				tamper(repo)
				return holdsAll(s)
			}}}
			var judged int
			res := CheckAgent(context.Background(), repo, agentEntries(4), AgentOptions{
				Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 2, PerSession: 2, Turns: 30,
				OnJudged: func(Verdict) { judged++ },
			})
			if res.StoppedBy != "tamper" || judged != 0 || len(fr.specs) != 1 {
				t.Fatalf("stopped=%q judged=%d sessions=%d", res.StoppedBy, judged, len(fr.specs))
			}
		})
	}
}

func TestCheckAgentKeepsAnUntouchedCheckout(t *testing.T) {
	repo := newRepo(t)
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	var judged int
	res := CheckAgent(context.Background(), repo, agentEntries(4), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 1, PerSession: 4, Turns: 30,
		OnJudged: func(Verdict) { judged++ },
	})
	if res.StoppedBy == "tamper" || judged != 4 {
		t.Fatalf("stopped=%q judged=%d", res.StoppedBy, judged)
	}
}
