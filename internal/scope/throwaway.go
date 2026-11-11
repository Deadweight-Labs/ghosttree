package scope

import "strings"

// reservedHostSuffixes are the test and documentation names of RFC 2606 and
// RFC 6761. No real repository lives under them.
var reservedHostSuffixes = []string{".invalid", ".test", ".example", ".localhost"}

var reservedHosts = map[string]bool{
	"localhost": true, "127.0.0.1": true, "::1": true,
	"example.com": true, "example.net": true, "example.org": true,
}

// tempDirs mark a checkout that lives in a temporary or cache directory, as
// whole path segments (var/folders is the macOS temp root). A remote pointing
// into one on a machine (a host without a dot, as "ci-runner") comes from a
// test run, not from a repository anybody shares. On a forge's host the same
// names are only owners or repositories ("github.com/tmp/x") and count as real.
var tempDirs = [][]string{{"tmp"}, {".cache"}, {"var", "folders"}}

// reservedTestOwners are owners that test harnesses use for repositories that
// do not exist; a remote under one is never a project. The list is kept short
// on purpose: each entry hides a real account of that name.
// pitwall-lab is the owner the pitwall live tests give their throwaway
// checkouts; no such account exists on GitHub (checked 2026-11-09).
var reservedTestOwners = map[string]bool{"example-nobody": true, "e2e-collab": true, "pitwall-lab": true}

// IsThrowawayRemote reports a remote that names no shared repository: a local
// path, a file URL, a checkout in a temporary or cache directory, or a host
// under a reserved test name. Such a value never becomes a project; the
// client treats the directory as one without a remote and the server refuses
// to create a project for it. The argument may be raw or normalized.
func IsThrowawayRemote(remote string) bool {
	raw := strings.ToLower(strings.TrimSpace(remote))
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "file:") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "~") ||
		strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || isDrivePath(raw) {
		return true
	}
	r := NormalizeRemote(raw)
	if strings.HasPrefix(r, "/") || strings.HasPrefix(r, "file/") {
		return true
	}
	segs := strings.Split(r, "/")
	host, _, _ := strings.Cut(segs[0], ":") // a port that stayed in the id
	if !strings.Contains(host, ".") && hasSegments(segs[1:], tempDirs) {
		return true
	}
	if len(segs) > 2 && reservedTestOwners[segs[1]] {
		return true
	}
	if reservedHosts[host] {
		return true
	}
	for _, suffix := range reservedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// hasSegments says whether one of the runs of whole segments occurs in path.
func hasSegments(path []string, runs [][]string) bool {
	for i := range path {
		for _, run := range runs {
			if i+len(run) <= len(path) && equalSegments(path[i:i+len(run)], run) {
				return true
			}
		}
	}
	return false
}

func equalSegments(a, b []string) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// isDrivePath: "c:\repo" or "c:/repo", a Windows path given as a remote.
func isDrivePath(r string) bool {
	return len(r) >= 3 && r[1] == ':' && (r[2] == '/' || r[2] == '\\') && r[0] >= 'a' && r[0] <= 'z'
}
