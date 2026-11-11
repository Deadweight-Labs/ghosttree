package gitcmd

import (
	"reflect"
	"testing"
)

// Ausgaben von git push, wie sie in Transkripten von Claude Code und Codex stehen
// (Repo, Branches und SHAs anonymisiert).
func TestPushRefsReadsTheRangesOfARealPush(t *testing.T) {
	cases := []struct {
		name, out string
		want      []PushRef
	}{
		{"fast-forward",
			"To https://github.com/acme/widgets.git\n   5542e9f..ab1bdde  feat/garden-ledger -> feat/garden-ledger\n",
			[]PushRef{{Src: "feat/garden-ledger", Dst: "feat/garden-ledger", Old: "5542e9f", New: "ab1bdde"}}},
		{"new branch with upstream message",
			"remote: \nremote: Create a pull request for 'chore/go-1.26.9' on GitHub by visiting:\nremote:      https://github.com/acme/widgets/pull/new/chore/go-1.26.9\nremote: \nTo https://github.com/acme/widgets.git\n * [new branch]      chore/go-1.26.9 -> chore/go-1.26.9\nbranch 'chore/go-1.26.9' set up to track 'origin/chore/go-1.26.9'.\n",
			[]PushRef{{Src: "chore/go-1.26.9", Dst: "chore/go-1.26.9", NewBranch: true}}},
		{"forced update",
			"To github.com:acme/widgets.git\n + 2c23513...f19aa8d fix/domain-rename -> fix/domain-rename (forced update)\n",
			[]PushRef{{Src: "fix/domain-rename", Dst: "fix/domain-rename", Old: "2c23513", New: "f19aa8d", Forced: true}}},
		{"two refs, one new and one fast-forward",
			"To github.com:acme/widgets.git\n * [new branch]      feat/a -> feat/a\n   1a2b3c4..5d6e7f8  feat/b -> feat/b\n",
			[]PushRef{
				{Src: "feat/a", Dst: "feat/a", NewBranch: true},
				{Src: "feat/b", Dst: "feat/b", Old: "1a2b3c4", New: "5d6e7f8"}}},
		{"one accepted and one rejected",
			"To github.com:acme/widgets.git\n   1a2b3c4..5d6e7f8  feat/b -> feat/b\n ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to 'github.com:acme/widgets.git'\nhint: Updates were rejected because the tip of your current branch is behind\n",
			[]PushRef{
				{Src: "feat/b", Dst: "feat/b", Old: "1a2b3c4", New: "5d6e7f8"},
				{Src: "main", Dst: "main", Rejected: true}}},
		{"remote rejected by a hook",
			"To github.com:acme/widgets.git\n ! [remote rejected] feat/x -> feat/x (protected branch hook declined)\nerror: failed to push some refs\n",
			[]PushRef{{Src: "feat/x", Dst: "feat/x", Rejected: true}}},
		{"HEAD pushed to a named branch with refs/heads",
			"To github.com:acme/widgets.git\n   0123456..89abcde  HEAD -> refs/heads/feat/x\n",
			[]PushRef{{Src: "HEAD", Dst: "feat/x", Old: "0123456", New: "89abcde"}}},
		{"delete and tag",
			"To github.com:acme/widgets.git\n - [deleted]         feat/old\n * [new tag]         v1.2.0 -> v1.2.0\n",
			[]PushRef{
				{Src: "feat/old", Dst: "feat/old", Deleted: true},
				{Src: "v1.2.0", Dst: "v1.2.0", NewTag: true}}},
		{"already up to date", "Everything up-to-date\n", nil},
		{"a quiet push", "", nil},
		{"tail of a long push keeps the range line",
			"remote: Resolving deltas: 100% (3/3), completed with 3 local objects.\nTo github.com:acme/widgets.git\n   bb976ef..6293f7e  feat/embed-query-url -> feat/embed-query-url\n",
			[]PushRef{{Src: "feat/embed-query-url", Dst: "feat/embed-query-url", Old: "bb976ef", New: "6293f7e"}}},
		{"Windows line endings",
			"To github.com:acme/widgets.git\r\n   1a2b3c4..5d6e7f8  feat/b -> feat/b\r\n",
			[]PushRef{{Src: "feat/b", Dst: "feat/b", Old: "1a2b3c4", New: "5d6e7f8"}}},
		// Zeilen anderer Befehle (git log, git branch -vv) im selben Aufruf sind keine Refs.
		{"log lines are no refs",
			"5fba72a fix(garden): Gärtner-Herkunft (REQ-457)\n+ dev                  5a4b9bd (/work/repo) feat: x\n  feat/x              d8fce07 fix: y\n",
			nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PushRefs(tc.out); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestPushedBranchSkipsWhatDidNotChangeABranch(t *testing.T) {
	out := "To github.com:acme/widgets.git\n ! [rejected]        main -> main (non-fast-forward)\n * [new tag]         v1 -> v1\n   1a2b3c4..5d6e7f8  feat/b -> feat/b\n"
	if got := PushedBranch(out); got != "feat/b" {
		t.Fatalf("pushed branch %q", got)
	}
	if got := PushedBranch(" ! [rejected]        main -> main (non-fast-forward)\n"); got != "" {
		t.Fatalf("rejected %q", got)
	}
}

func TestPushRefsAreCapped(t *testing.T) {
	out := ""
	for i := 0; i < 20; i++ {
		out += " * [new branch]      b" + string(rune('a'+i)) + " -> b" + string(rune('a'+i)) + "\n"
	}
	if got := PushRefs(out); len(got) != maxPushRefs {
		t.Fatalf("%d refs", len(got))
	}
}
