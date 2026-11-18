package installscript

import (
	"os"
	"strings"
	"testing"
)

const (
	goreleaserConfig = "../../.goreleaser.yaml"
	securityPolicy   = "../../SECURITY.md"
	releaseKeyFile   = "../../deploy/downloads/ghosttree/minisign.pub"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A release is public only after the signature of the files that were really
// uploaded has been checked: goreleaser leaves a draft, a step checks the
// uploaded files, and the last step publishes.
func TestReleaseIsPublishedOnlyAfterTheUploadedFilesWereChecked(t *testing.T) {
	cfg := readFile(t, goreleaserConfig)
	rel := cfg[strings.Index(cfg, "\nrelease:"):]
	if !strings.Contains(rel, "\n  draft: true") {
		t.Fatal(".goreleaser.yaml does not create the release as a draft, so it is public before its signature was checked")
	}
	all := steps(t, readFile(t, releaseWorkflow))
	goreleaser, check, publish := -1, -1, -1
	for i, s := range all {
		switch {
		case strings.Contains(s, "goreleaser/goreleaser-action@"):
			goreleaser = i
		case strings.Contains(s, "gh release download") && strings.Contains(s, "minisign -V"):
			check = i
		case strings.Contains(s, "--draft=false"):
			if publish != -1 {
				t.Fatalf("the release is published in more than one step (%d and %d)", publish, i)
			}
			publish = i
		}
	}
	if goreleaser == -1 || check == -1 || publish == -1 {
		t.Fatalf("goreleaser step %d, check of the uploaded files %d, publishing step %d", goreleaser, check, publish)
	}
	if !(goreleaser < check && check < publish) {
		t.Errorf("order goreleaser %d, check %d, publish %d: the release is published before its files were checked", goreleaser, check, publish)
	}
	if publish != len(all)-1 {
		t.Errorf("a step follows the publishing step; the release is final only when everything before it passed")
	}
	for _, i := range []int{check, publish} {
		s := all[i]
		if strings.Contains(s, "if: always()") || strings.Contains(s, "if: failure()") || strings.Contains(s, "continue-on-error") {
			t.Errorf("a failed check does not stop the release:\n%s", s)
		}
	}
	for _, want := range []string{"checksums.txt.minisig", "sha256sum -c", "ghosttree $TAG", "minisign.pub"} {
		if !strings.Contains(all[check], want) {
			t.Errorf("the check of the uploaded files lacks %q:\n%s", want, all[check])
		}
	}
}

// The key has a password, which sits next to it in the environment "release"
// and reaches only the signing run, never a step that runs other people's code.
func TestSigningKeyPasswordReachesOnlyTheSigningRun(t *testing.T) {
	cfg := readFile(t, goreleaserConfig)
	if !strings.Contains(cfg, `stdin: "{{ .Env.MINISIGN_PASSWORD }}"`) {
		t.Error("goreleaser does not hand the key password to minisign")
	}
	all := steps(t, readFile(t, releaseWorkflow))
	var holders []int
	for i, s := range all {
		if strings.Contains(s, "secrets.MINISIGN_PASSWORD") {
			holders = append(holders, i)
		}
	}
	if len(holders) != 2 {
		t.Fatalf("the password is given to %d steps, want the key probe and goreleaser", len(holders))
	}
	if !strings.Contains(all[holders[0]], "secrets.MINISIGN_SECRET_KEY") || !strings.Contains(all[holders[1]], "goreleaser/goreleaser-action@") || holders[1] != holders[0]+1 {
		t.Errorf("the password is in steps %v, want the step that writes the key and the goreleaser step after it", holders)
	}
	for _, i := range holders {
		if strings.Contains(all[i], "run: |") && strings.Contains(all[i], "echo \"$MINISIGN_PASSWORD") {
			t.Errorf("the password is echoed:\n%s", all[i])
		}
	}
}

// The release key stands in more than one place, so a swapped install.sh can be
// caught by comparing it with another source. SECURITY.md carries it (or says
// that it is not issued yet), and the release stops if it does not.
func TestReleaseKeyIsPublishedInSecurityMd(t *testing.T) {
	pub := strings.Split(strings.TrimSpace(readFile(t, releaseKeyFile)), "\n")[1]
	policy := readFile(t, securityPolicy)
	if strings.HasPrefix(pub, "__GHOSTTREE") {
		if !strings.Contains(policy, "Release key: not issued yet") {
			t.Error("SECURITY.md has to say that the release key is not issued yet")
		}
	} else if !strings.Contains(policy, "`"+pub+"`") {
		t.Errorf("SECURITY.md does not carry the release key %s", pub)
	}
	wf := readFile(t, releaseWorkflow)
	if !strings.Contains(wf, "SECURITY.md") {
		t.Error("the release workflow does not check that SECURITY.md carries the key")
	}
}
