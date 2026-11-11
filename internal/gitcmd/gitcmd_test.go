package gitcmd

import (
	"reflect"
	"testing"
)

func kinds(cs []Call) []string {
	var out []string
	for _, c := range cs {
		s := c.Kind
		if c.Op != "" && c.Op != OpCommit {
			s += ":" + c.Op
		}
		out = append(out, s)
	}
	return out
}

func TestParseRecognizesCallsAtTheStartOfASimpleCommand(t *testing.T) {
	cases := []struct {
		name, cmd string
		want      []string
	}{
		{"plain commit", `git commit -m "fix: thing"`, []string{"commit"}},
		{"quiet bundle", `git commit -qam "fix: thing"`, []string{"commit"}},
		{"after add", `git add -A && git commit -q -m x`, []string{"commit"}},
		{"after cd", `cd /work/repo && git commit -q -m x`, []string{"commit"}},
		{"commit then push", `git commit -qm x && git push -u origin feat/x`, []string{"commit", "push"}},
		{"semicolon and pipe", `ls; git push | tee out.txt`, []string{"push"}},
		{"newline separated", "git add .\ngit commit -m x\ngit push", []string{"commit", "push"}},
		{"env prefix", `GIT_AUTHOR_NAME=x git commit -m y`, []string{"commit"}},
		{"git options before the subcommand", `git -C /work/repo -c user.name=x commit -m y`, []string{"commit"}},
		{"amend", `git commit --amend --no-edit`, []string{"commit:amend"}},
		{"rebase continue", `git rebase --continue`, []string{"commit:rebase"}},
		{"cherry-pick", `git cherry-pick abc1234`, []string{"commit:cherry-pick"}},
		{"merge", `git merge --no-ff feat/x`, []string{"commit:merge"}},
		{"am", `git am < patch.mbox`, []string{"commit:am"}},
		{"revert", `git revert abc1234`, []string{"commit:revert"}},
		{"gh pr create", `gh pr create --title "x" --body "y"`, []string{"pr_create"}},
		{"gh pr merge", `gh pr merge 231 --squash --delete-branch`, []string{"pr_merge"}},
		{"bash -lc wrapper", `bash -lc 'git add -A && git commit -q -m "x"'`, []string{"commit"}},
		{"sh -c wrapper in a cd", `cd /work && sh -c "git push origin HEAD"`, []string{"push"}},
		{"subshell", `(cd /work/repo && git commit -q -m x)`, []string{"commit"}},
		{"command substitution", `C=$(git commit-tree HEAD^{tree} -m x) && git push`, []string{"push"}},
		{"redirection with ampersand", `git commit -q -m x 2>&1 | tail -3`, []string{"commit"}},
		{"commit with heredoc message", "git commit -q -F - <<'EOF'\nfix: thing\n\nbody mentions git push\nEOF\ngit push", []string{"commit", "push"}},
		{"sudo and timeout", `timeout 60 git push`, []string{"push"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kinds(Parse(tc.cmd)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q: got %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestParseIgnoresTextThatOnlyMentionsAnOperation(t *testing.T) {
	// Aufträge an Sub-Agenten, echo, Heredocs, Skripte und Befehle in einer
	// anderen Umgebung enthalten oft "git commit" und sind keiner.
	for _, cmd := range []string{
		``,
		`echo "run git commit -m x when done"`,
		`printf 'git push\n' > notes.txt`,
		"cat > brief.md <<'EOF'\n1. git commit -q\n2. git push\nEOF",
		"cat <<EOF\ngh pr create\nEOF",
		`python3 -c "import os; os.system('git commit -m x')"`,
		`grep -rn "git commit" internal/`,
		`git log --grep="git commit" --oneline`,
		`git status && git diff --stat`,
		`git commit --dry-run`,
		`git commit -h`,
		`git push --dry-run origin main`,
		`git merge --abort`,
		`git rebase --abort`,
		`git cherry-pick --abort`,
		`git cherry-pick -n abc1234`,
		`git merge --squash feat/x`,
		`git merge --ff-only origin/main`,
		`ssh build-host 'git commit -am x'`,
		`docker exec ci git push`,
		`gh pr view 12 --json state`,
		`gh pr list`,
		`gh pr create --help`,
		`# git commit -m x`,
		`git commit-tree HEAD^{tree}`,
		`git pull --rebase`,
		`bash -c`,
		`gh issue create`,
	} {
		if got := Parse(cmd); len(got) != 0 {
			t.Errorf("%q must not be recognized, got %+v", cmd, got)
		}
	}
}

func TestParseKeepsTheDirectoryAndQuietFlag(t *testing.T) {
	c := Parse(`cd /work/repo && git commit -qm x`)
	if len(c) != 1 || c[0].Dir != "/work/repo" || !c[0].Quiet {
		t.Fatalf("cd + quiet: %+v", c)
	}
	c = Parse(`cd /work && git -C sub/repo commit -m x`)
	if len(c) != 1 || c[0].Dir != "/work/sub/repo" || c[0].Quiet {
		t.Fatalf("git -C relative to cd: %+v", c)
	}
	// -m trägt einen Wert: ein q dahinter ist kein Schalter.
	if c := Parse(`git commit -mq`); len(c) != 1 || c[0].Quiet {
		t.Fatalf("-mq is a message: %+v", c)
	}
	if c := Parse(`git commit -m "x" --quiet`); len(c) != 1 || !c[0].Quiet {
		t.Fatalf("--quiet: %+v", c)
	}
}

func TestParseReadsThePullRequestOfAMerge(t *testing.T) {
	for cmd, want := range map[string]int64{
		`gh pr merge 231 --squash`:                                 231,
		`gh pr merge "#231"`:                                       231,
		`gh pr merge https://github.com/acme/widgets/pull/42 -d`:   42,
		`gh -R acme/widgets pr merge 7 --admin`:                    7,
		`gh pr merge --squash --body "closes #99" --delete-branch`: 0,
		`gh pr merge -t "Title 12" 5`:                              5,
	} {
		c := Parse(cmd)
		if len(c) != 1 || c[0].Kind != KindPRMerge || c[0].PR != want {
			t.Errorf("%q: %+v, want PR %d", cmd, c, want)
		}
	}
}

func TestCommitSHAsReadsOnlyTheCommitLines(t *testing.T) {
	out := "[feat/x 1a2b3c4] fix: thing\n 2 files changed, 9 insertions(+)\n create mode 100644 a.go\n" +
		"[feat/x (root-commit) 9f8e7d6c5b] init\n[detached HEAD abcdef0123456789abcdef0123456789abcdef01] x\n" +
		"[1.2.3] not a commit\n[main] no sha here\n"
	got := CommitSHAs(out)
	want := []string{"1a2b3c4", "9f8e7d6c5b", "abcdef0123456789abcdef0123456789abcdef01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := CommitSHAs(""); len(got) != 0 {
		t.Fatalf("a quiet commit prints nothing: %v", got)
	}
	if got := CommitSHAs("git log shows\n  abc1234 fix: thing\n"); len(got) != 0 {
		t.Fatalf("a log line is not a commit line: %v", got)
	}
}

func TestPushedBranchAndPullURLs(t *testing.T) {
	push := "remote: Resolving deltas: 100% (3/3), completed with 3 local objects.\nTo github.com:acme/widgets.git\n   1a2b3c4..5d6e7f8  feat/x -> feat/x\n"
	if got := PushedBranch(push); got != "feat/x" {
		t.Fatalf("pushed branch %q", got)
	}
	if got := PushedBranch(" * [new branch]      feat/y -> feat/y\n"); got != "feat/y" {
		t.Fatalf("new branch %q", got)
	}
	if got := PushedBranch(" + 1a2b3c4...5d6e7f8 feat/z -> feat/z (forced update)\n"); got != "feat/z" {
		t.Fatalf("forced %q", got)
	}
	if got := PushedBranch("Everything up-to-date\n"); got != "" {
		t.Fatalf("up to date: %q", got)
	}
	remote, n := PullCreated("Creating pull request for feat/x into main in acme/widgets\n\nhttps://github.com/Acme/Widgets/pull/231\n")
	if remote != "github.com/acme/widgets" || n != 231 {
		t.Fatalf("created: %q %d", remote, n)
	}
	if r, n := PullCreated("a pull request for this branch already exists"); r != "" || n != 0 {
		t.Fatalf("no URL: %q %d", r, n)
	}
	for out, want := range map[string]int64{
		"✓ Squashed and merged pull request #231 (fix: thing)\n":          231,
		"✓ Merged pull request acme/widgets#42 (feat: x)\n":               42,
		"! Pull request #5 will be automatically merged when checks pass": 0,
		"": 0,
	} {
		if got := PullMerged(out); got != want {
			t.Errorf("%q: %d, want %d", out, got, want)
		}
	}
}

// Eine Commit-Nachricht in "$(cat <<'EOF' … EOF)" ist Text, auch mit ungeradem
// Anführungszeichen; früher endete das Wort beim ersten ", und Zeilen der Nachricht
// wie "git push" galten als Befehl.
func TestParseReadsACommandSubstitutionInsideDoubleQuotesAsText(t *testing.T) {
	cases := []struct {
		name, cmd string
		want      []string
	}{
		{"heredoc message with an odd double quote",
			"git commit -q -m \"$(cat <<'EOF'\nfix: say \"hi\n\ngit push origin main\nEOF\n)\"",
			[]string{"commit"}},
		{"heredoc message with an odd single quote",
			"git commit -q -m \"$(cat <<'EOF'\nfix: it's broken, don't panic\n\ngit push origin main\ngh pr create --title x\nEOF\n)\"",
			[]string{"commit"}},
		{"the command after the message is still read",
			"git add -A && git commit -q -m \"$(cat <<'EOF'\nfix: it's\n\ngit push\nEOF\n)\" && git push -u origin HEAD",
			[]string{"commit", "push"}},
		{"unquoted delimiter and tab-stripped body",
			"git commit -q -m \"$(cat <<-EOF\n\tit's \"half\n\tgit push\n\tEOF\n)\"\ngit push",
			[]string{"commit", "push"}},
		{"nested substitution and quotes",
			`git commit -q -m "a $(echo "b $(date +%s) c") d" && git push`,
			[]string{"commit", "push"}},
		{"parenthesis and quote inside single quotes",
			`git commit -q -m "$(printf '%s' ') " git push')"`,
			[]string{"commit"}},
		{"escaped quote after the substitution",
			`git commit -q -m "$(date) \"quoted\"" && git push`,
			[]string{"commit", "push"}},
		{"a substitution that never closes swallows the rest",
			"git commit -q -m \"$(cat <<'EOF'\nit's\ngit push\n",
			[]string{"commit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kinds(Parse(tc.cmd)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q: got %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestParseSeesACallBehindIfWhileAndUntil(t *testing.T) {
	cases := []struct {
		name, cmd string
		want      []string
	}{
		{"if", `if git commit -q -m x; then echo done; fi`, []string{"commit"}},
		{"if and then", `if git diff --quiet; then echo clean; else git commit -qam x; fi`, []string{"commit"}},
		{"then on its own line", "if true; then\n  git commit -q -m x\n  git push\nfi", []string{"commit", "push"}},
		{"negated if", `if ! git push; then echo failed; fi`, []string{"push"}},
		{"while with a retry", `while ! git push origin HEAD; do sleep 2; done`, []string{"push"}},
		{"until", `until git push; do sleep 2; done`, []string{"push"}},
		{"do", `for i in 1 2; do git commit -q --allow-empty -m x; done`, []string{"commit"}},
		{"elif", `if false; then :; elif git commit -q -m x; then :; fi`, []string{"commit"}},
		{"a condition that is no git call", `if git diff --quiet; then echo clean; fi`, nil},
		{"the word as an argument", `echo if git commit`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kinds(Parse(tc.cmd)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q: got %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
