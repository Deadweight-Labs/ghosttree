package web

import (
	"net/url"
	"strings"
	"testing"
)

// Der Filter branch: verriete einem Gast, welche Branches ein Arbeitspaket
// haben; die PR-Liste sperrt ihn schon, die Issue-Liste muss dasselbe tun.
func TestIssueListBranchFilterIsNoOracleForGuests(t *testing.T) {
	e, _ := pullsEnv(t)
	list := func(who string, branch string) string {
		c := e.Guest
		if who == "member" {
			c = e.Member
		}
		_, page := fetchPage(t, c, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("branch:"+branch))
		return strings.ReplaceAll(strings.ReplaceAll(page, branch, "BRANCH"), url.QueryEscape(branch), "BRANCH")
	}
	if list("guest", "feat/x") != list("guest", "feat/nope") {
		t.Error("guest gets a different answer for a branch with a package and one without")
	}
	if !strings.Contains(list("guest", "feat/x"), "Pull request tab") {
		t.Error("the guest list should ignore the filter and still show the issue")
	}
	m1, m2 := list("member", "feat/x"), list("member", "feat/nope")
	if !strings.Contains(m1, "Pull request tab") || strings.Contains(m2, "Pull request tab") {
		t.Error("the branch filter stopped working for members")
	}
}
