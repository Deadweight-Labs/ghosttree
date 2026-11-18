package installscript

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const releaseWorkflow = "../../.github/workflows/release.yml"

// steps splits the job's step list (the "      - " items) into their texts.
func steps(t *testing.T, workflow string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(workflow, "\n") {
		if strings.HasPrefix(line, "      - ") {
			out = append(out, "")
		}
		if len(out) > 0 {
			out[len(out)-1] += line + "\n"
		}
	}
	if len(out) == 0 {
		t.Fatal("no steps found in the release workflow")
	}
	return out
}

// The release key is the one secret that can make a hostile install.sh pass.
// These are the rules that keep it away from everything but the signing run.
func TestReleaseWorkflowKeepsTheSigningKeyAwayFromOtherCode(t *testing.T) {
	b, err := os.ReadFile(releaseWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	all := steps(t, wf)

	// The secret lives in the environment "release", which only v* tags reach.
	if !regexp.MustCompile(`(?m)^    environment: release$`).MatchString(wf) {
		t.Error("the release job does not run in the environment \"release\", so the key would have to be a repository secret")
	}

	// The job token must not stay in .git/config for the actions that follow.
	for _, s := range all {
		if strings.Contains(s, "actions/checkout@") && !strings.Contains(s, "persist-credentials: false") {
			t.Errorf("a checkout keeps the job token in .git/config:\n%s", s)
		}
	}

	// The secret is read by exactly one step, and goreleaser follows it directly.
	var write = -1
	for i, s := range all {
		if strings.Contains(s, "secrets.MINISIGN_SECRET_KEY") {
			if write != -1 {
				t.Fatalf("the signing key is handed to more than one step (%d and %d)", write, i)
			}
			write = i
		}
	}
	if write == -1 {
		t.Fatal("no step reads secrets.MINISIGN_SECRET_KEY")
	}
	if strings.Contains(all[write], "uses:") {
		t.Errorf("an action, not a script, gets the signing key:\n%s", all[write])
	}
	if write+2 >= len(all) {
		t.Fatal("no steps after the key is written")
	}
	if !strings.Contains(all[write+1], "goreleaser/goreleaser-action@") {
		t.Errorf("the step after the key is written is not goreleaser:\n%s", all[write+1])
	}
	if !strings.Contains(all[write+2], "if: always()") || !strings.Contains(all[write+2], "rm -f") ||
		!strings.Contains(all[write+2], "minisign.key") {
		t.Errorf("the key file is not removed right after goreleaser, whatever its result:\n%s", all[write+2])
	}
	// Nothing may leave the key file's place to the following steps through the environment file.
	if strings.Contains(wf, "MINISIGN_KEY_FILE=$") {
		t.Error("the key's path is exported to every later step through GITHUB_ENV")
	}
	if !strings.Contains(all[write+1], "MINISIGN_KEY_FILE:") {
		t.Error("goreleaser is not told where the key file is")
	}
}
