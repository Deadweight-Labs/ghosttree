package web

import (
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The work package as a filter of the lists (REQ-599). package:7 (or
// package:PKG-7) in the search text narrows the issue list to the members of
// package 7 and the pull request list to its pull request; the "Package"
// dropdown of both lists writes that word into the same text. Which packages
// there are is what ProjectAccess.PackagesOfProject lets the viewer list, so a
// number of another project or of a package the viewer may not see is as
// unknown as one that does not exist, and a name hidden from a guest stays
// hidden (packageName).

// packageMenuMax is how many packages the dropdown lists; the newest come first.
const packageMenuMax = 40

// parsePackageTerm reads the value of package: as a package number: "7" or "PKG-7".
func parsePackageTerm(val string) (int64, bool) {
	digits := val
	if len(val) > 4 && strings.EqualFold(val[:4], "pkg-") {
		digits = val[4:]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return n, err == nil && n > 0
}

// packageChoices are the packages of the project the viewer may list.
func packageChoices(pa *store.ProjectAccess, project string) []requestdomain.PackageSummary {
	list, err := pa.PackagesOfProject(project, "")
	if err != nil {
		return nil
	}
	return list
}

// packageNumbered finds a package by its number among the ones the viewer may list.
func packageNumbered(list []requestdomain.PackageSummary, n int64) (requestdomain.PackageSummary, bool) {
	for _, p := range list {
		if p.ID == n {
			return p, true
		}
	}
	return requestdomain.PackageSummary{}, false
}

// packageMembers are the requests of the package the viewer may list; none when
// the package is not one of theirs.
func (a *app) packageMembers(pa *store.ProjectAccess, project string, n int64) map[int64]bool {
	members := map[int64]bool{}
	if _, ok := packageNumbered(packageChoices(pa, project), n); !ok {
		return members
	}
	if d, err := a.store.PackageByID(n); err == nil && d.Project == project {
		for _, m := range d.Members {
			members[m.RequestID] = true
		}
	}
	return members
}

// packageMenu is the dropdown "Package" of a list: every package that keep
// admits, the newest first, and the one the list is narrowed to even when keep
// does not admit it. The entries are links that write package:N into the text.
func packageMenu(l lang, q string, current int64, list []requestdomain.PackageSummary, keep func(requestdomain.PackageSummary) bool, href func(string) string) (issueMenu, bool) {
	var items []issueMenuItem
	for _, p := range list {
		if p.ID != current && (len(items) >= packageMenuMax || !keep(p)) {
			continue
		}
		items = append(items, issueMenuItem{Label: "#" + strconv.FormatInt(p.ID, 10) + " " + packageName(l, p.Package),
			Href: href(withQualifier(q, dropKey("package"), "package:"+strconv.FormatInt(p.ID, 10))), Current: p.ID == current})
	}
	if len(items) == 0 {
		return issueMenu{}, false
	}
	m := issueMenu{Label: l.msg("filter.package"), Active: current != 0}
	m.Items = append([]issueMenuItem{{Label: l.msg("filter.any_package"), Href: href(withQualifier(q, dropKey("package"), "")), Current: current == 0}}, items...)
	return m, true
}
