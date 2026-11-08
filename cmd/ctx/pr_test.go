package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestPRCLIListShowAndCommits(t *testing.T) {
	st := moveCLIFixture(t)
	p, _ := st.ProjectByRemote(cliHere)
	if _, err := st.ConnectSourceWithInstallation("person:1", p.OrgID, "github", "acme", 11, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []store.PullRequestChange{
		{Number: 3, Action: "opened", Title: "Erster", Head: "feat/a", Base: "main", State: "open", Author: "alice-gh"},
		{Number: 4, Action: "closed", Title: "Zweiter", Head: "feat/b", Base: "main", State: "merged"},
	} {
		c.InstallationID, c.Remote, c.HeadRemote = 11, cliHere, cliHere
		if _, err := st.RecordPullRequest(c); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := st.RecordPullCommits(store.PullCommitsChange{InstallationID: 11, Remote: cliHere, Number: 3,
		Commits: []store.PullCommit{{SHA: sha40(7), Author: "alice-gh", Title: "Titelzeile"}}}); err != nil || res.Recorded != 1 {
		t.Fatalf("commits: %+v %v", res, err)
	}
	do := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(args, &out)
		return code, out.String()
	}
	if code, out := do("pr", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "2 pull requests (open 1, merged 1, closed 0)") || !strings.Contains(out, "Erster") || !strings.Contains(out, "Zweiter") {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, out := do("pr", "list", "--project", cliHere, "--state", "merged"); code != 0 || strings.Contains(out, "Erster") || !strings.Contains(out, "Zweiter") {
		t.Fatalf("list merged: %d %s", code, out)
	}
	if code, out := do("pr", "list", "--state", "weird", "--project", cliHere); code == 0 || !strings.Contains(out, "pull_state_invalid") && !strings.Contains(out, "unknown pull request state") {
		t.Fatalf("a bad state must fail: %d %s", code, out)
	}
	// Die Nummer darf vor und nach den Flags stehen.
	for _, args := range [][]string{{"pr", "show", "3", "--project", cliHere}, {"pr", "show", "--project", cliHere, "#3"}} {
		if code, out := do(args...); code != 0 || !strings.Contains(out, "PR #3 [open] Erster") || !strings.Contains(out, "1 commits") || !strings.Contains(out, "link: ") {
			t.Fatalf("show %v: %d %s", args, code, out)
		}
	}
	if code, out := do("pr", "commits", "3", "--project", cliHere); code != 0 || !strings.Contains(out, sha40(7)[:12]) || !strings.Contains(out, "Titelzeile") {
		t.Fatalf("commits: %d %s", code, out)
	}
	if code, out := do("pr", "show", "3", "--project", cliHere, "--json"); code != 0 || !strings.Contains(out, `"number": 3`) && !strings.Contains(out, `"number":3`) {
		t.Fatalf("show json: %d %s", code, out)
	}
	if code, _ := do("pr", "show", "99", "--project", cliHere); code == 0 {
		t.Fatal("an unknown pull request must fail")
	}
	for _, args := range [][]string{{"pr"}, {"pr", "show"}, {"pr", "show", "x", "--project", cliHere}, {"pr", "nonsense", "--project", cliHere}} {
		if code, out := do(args...); code != 2 || !strings.Contains(out, "usage: ctx pr") {
			t.Errorf("%v: %d %s", args, code, out)
		}
	}
}
