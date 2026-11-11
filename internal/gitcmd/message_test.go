package gitcmd

import "testing"

// message liest die Nachricht des ersten Commit-Aufrufs im Befehl.
func message(t *testing.T, cmd string) (string, bool) {
	t.Helper()
	for _, c := range Parse(cmd) {
		if c.Kind == KindCommit {
			return c.Message, c.MessageAdded
		}
	}
	t.Fatalf("no commit call in %q", cmd)
	return "", false
}

// Die Formen sind anonymisierte Ausschnitte echter Transkripte: Claude Code ruft
// git commit fast immer mit -q auf und gibt die Nachricht als -m (auch mehrere) oder
// als -F - mit Heredoc mit; selten als "$(cat <<'EOF' … EOF)". Codex führt dasselbe in
// bash -lc aus.
func TestCommitMessageFromTheForms(t *testing.T) {
	cases := []struct {
		name, cmd, want string
	}{
		{"one -m after add and before push",
			`cd /work/repo && git add -A docs && git status --short && git commit -q -m "docs: Entscheidungen nach Empfehlung: Kalender, Belohnung, Preise" && git push -q origin main 2>&1 | tail -2; git log --oneline -1`,
			"docs: Entscheidungen nach Empfehlung: Kalender, Belohnung, Preise"},
		{"two -m become two paragraphs",
			`git add -A && git commit -q -m "feat(hooks): register running sessions in their project room" -m "Sessions erscheinen in der Agentenliste, sobald ein Hook läuft." && git push -q -u origin feat/hook-registration 2>&1 | tail -2`,
			"feat(hooks): register running sessions in their project room\n\nSessions erscheinen in der Agentenliste, sobald ein Hook läuft."},
		{"a multi-line message inside one -m",
			"git commit -q -m \"feat(portal): redesign preview host (worker part)\n\nRunner uploads the built redesign to R2 under /admin/api/runner.\" && git log --oneline | head -5",
			"feat(portal): redesign preview host (worker part)\n\nRunner uploads the built redesign to R2 under /admin/api/runner."},
		{"-F - with a quoted heredoc",
			"cd /work/repo/.claude/worktrees/agent-1 && git commit -q -F - <<'EOF'\nfix(security): CodeQL-Funde beheben (PR-Links nur mit Host-Grenze)\n\n- explain: prURL verlangt eine Grenze vor dem Host; evil.github.com trifft nicht mehr.\n- web: die Weiterleitung ist ein fester Pfad, $HOME und `cmd` stehen wörtlich.\nEOF\ngit push",
			"fix(security): CodeQL-Funde beheben (PR-Links nur mit Host-Grenze)\n\n- explain: prURL verlangt eine Grenze vor dem Host; evil.github.com trifft nicht mehr.\n- web: die Weiterleitung ist ein fester Pfad, $HOME und `cmd` stehen wörtlich."},
		{"-F - and the push on the line of the heredoc operator",
			"git commit -q -F - <<'EOF' && git push -q origin HEAD\nfix: thing\n\nbody line\nEOF\n",
			"fix: thing\n\nbody line"},
		{"cat heredoc piped into -F -",
			"cat <<'EOF' | git commit -q -F -\nchore: bump\n\nlong body\nEOF\n",
			"chore: bump\n\nlong body"},
		{"command substitution with a quoted heredoc",
			"git add -A && git commit -q -m \"$(cat <<'EOF'\nfix(web): it's broken, don't panic\n\nsays \"hi\nEOF\n)\" && git push",
			"fix(web): it's broken, don't panic\n\nsays \"hi"},
		{"unquoted heredoc without expansions",
			"git commit -q -m \"$(cat <<EOF\nfeat: plain text\nEOF\n)\"",
			"feat: plain text"},
		{"bundle -qam", `git commit -qam "fix: bundle"`, "fix: bundle"},
		{"attached value", `git commit -q -m"fix: attached"`, "fix: attached"},
		{"--message with equals", `git commit -q --message="fix: long form"`, "fix: long form"},
		{"--message with a separate value", `git commit -q --message "fix: separate"`, "fix: separate"},
		{"single quotes keep a dollar and a backtick", `git commit -q -m 'fix: costs $5 and ` + "`x`" + `'`, "fix: costs $5 and `x`"},
		{"an escaped dollar in double quotes", `git commit -q -m "fix: costs \$5"`, "fix: costs $5"},
		{"amend with a new message", `git commit --amend -q -m "fix: reworded"`, "fix: reworded"},
		{"merge message", `git merge --no-ff -m "merge: dev (#186) in feat/git-native" origin/dev`, "merge: dev (#186) in feat/git-native"},
		{"the message after other options", `git commit -q --no-verify --author "A B <a@b.c>" --date "2026-01-01" -m "fix: options"`, "fix: options"},
		{"untracked-files bundle is no message", `git commit -q -unormal -m "fix: u"`, "fix: u"},
		{"Codex bash -lc wrapper", `bash -lc 'cd /work/repo && git add -A && git commit -q -m "fix(ui): wrap long titles" && git push -q'`, "fix(ui): wrap long titles"},
		{"Codex bash -lc with a heredoc",
			"bash -lc 'git commit -q -F - <<\"EOF\"\nfeat: from codex\n\nbody\nEOF\n'",
			"feat: from codex\n\nbody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := message(t, tc.cmd)
			if got != tc.want {
				t.Fatalf("%q\n got: %q\nwant: %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// Was die Shell erst auflöst oder was der Befehl gar nicht trägt, ergibt keine Nachricht:
// der Server hielte sie sonst für den Text eines Commits, der er nicht ist.
func TestCommitMessageIsEmptyWhenTheCommandDoesNotSayIt(t *testing.T) {
	for _, cmd := range []string{
		`git commit -q -m "fix: $TITLE"`,
		`git commit -q -m "fix: ${TITLE}"`,
		`git commit -q -m "fix: $(date +%F)"`,
		"git commit -q -m \"fix: `date`\"",
		`git commit -q -m $MSG`,
		`git commit -q -m "$MSG" -m "ok"`,
		`git commit -q -F notes.txt`,
		`git commit -q --file=notes.txt`,
		`git commit --amend --no-edit`,
		`git commit -q -C HEAD`,
		`git commit -q -c HEAD~1`,
		`git commit -q --fixup=abc1234`,
		`git commit -q --squash abc1234`,
		`git commit -q -m`,
		`git commit -q`,
		// -F - ohne Rumpf (die Eingabe kommt von etwas anderem) oder mit Ersetzung im Rumpf.
		`git commit -q -F -`,
		`printf 'fix: x' | git commit -q -F -`,
		"git commit -q -F - <<EOF\nfix: $TITLE\nEOF\n",
		"git commit -q -m \"$(cat <<EOF\nfix: $(date)\nEOF\n)\"",
		"git commit -q -m \"$(cat <<'EOF'\nfix: never closed\n)\"",
		`git rebase --continue`,
		`git cherry-pick abc1234`,
	} {
		got, _ := message(t, cmd)
		if got != "" {
			t.Errorf("%q: message %q, want none", cmd, got)
		}
	}
}

func TestCommitMessageNotesWhatGitAdds(t *testing.T) {
	for _, cmd := range []string{
		`git commit -q -s -m "fix: x"`,
		`git commit -q -sm "fix: x"`,
		`git commit -q --signoff -m "fix: x"`,
		`git commit -q --trailer "Reviewed-by: A" -m "fix: x"`,
	} {
		msg, added := message(t, cmd)
		if msg != "fix: x" || !added {
			t.Errorf("%q: %q added=%v", cmd, msg, added)
		}
	}
	if msg, added := message(t, `git commit -q -m "fix: x"`); msg != "fix: x" || added {
		t.Errorf("plain: %q added=%v", msg, added)
	}
}

// Die Wörter einer Nachricht mit Befehlen darin sind kein Aufruf: die Erkennung der
// Befehle bleibt, wie sie war.
func TestCommitMessageDoesNotMakeItsWordsCalls(t *testing.T) {
	cmd := "git commit -q -F - <<'EOF'\nfix: x\n\ngit push origin main\ngh pr create\nEOF\n"
	got := Parse(cmd)
	if len(got) != 1 || got[0].Kind != KindCommit || got[0].Message != "fix: x\n\ngit push origin main\ngh pr create" {
		t.Fatalf("%+v", got)
	}
}
