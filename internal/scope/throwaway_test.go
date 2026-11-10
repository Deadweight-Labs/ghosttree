package scope

import "testing"

func TestIsThrowawayRemote(t *testing.T) {
	throwaway := []string{
		"/home/u/.cache/rules-live-2568269239/origin",
		"/srv/git/repo.git",
		"file:///home/u/repo",
		"~/repos/x",
		"../origin",
		"C:/repos/x",
		"https://benchmark.invalid/proxy-bench",
		"example.invalid/lab/chat-test",
		"git@gitserver.test:team/repo.git",
		"https://localhost:3000/a/b",
		"https://example.com/a/b",
		"ssh://ci-runner/tmp/build-1/origin",
		"host:/home/u/.cache/x/origin",
		"mac-mini:/var/folders/ab/T/x/origin",
		"github.com/example-nobody/unknown-repo-xyz",
		"https://github.com/e2e-collab/guestbook.git",
		"github.com/pitwall-lab/phase9-a-tnam15",
	}
	for _, r := range throwaway {
		if !IsThrowawayRemote(r) {
			t.Errorf("%q should be a throwaway remote", r)
		}
	}
	real := []string{
		"",
		"https://github.com/deadweight-labs/ghosttree.git",
		"git@github.com:Example/agent-web.git",
		"gitlab.winehq.org/wine/wine",
		"gitserver/repo",
		"ai:/opt/assistant",
		"default/infra",
		"github.com/test/agentprobe",
		// Whole segments only, and temp folders only on a machine, never as
		// owners or names on a forge (security review 14).
		"github.com/tmp/tools",
		"github.com/acme/tmp",
		"github.com/acme/.cache",
		"gitlab.com/group/tmp/repo",
		"ci-runner/tmpfiles/repo",
		"github.com/example-nobody-else/x",
		"github.com/example/x",
	}
	for _, r := range real {
		if IsThrowawayRemote(r) {
			t.Errorf("%q must stay a project remote", r)
		}
	}
}
