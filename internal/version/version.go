// Package version reads, orders and describes ctx version strings, and holds
// the numbers client and server use to find out whether they fit together.
package version

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// Protocol is the wire protocol this build speaks. It rises only when client
// and server can no longer work together without the other being updated.
const Protocol = 1

// MinClient is the lowest client protocol this server still serves.
const MinClient = 1

const (
	ChannelStable = "stable"
	ChannelDev    = "dev"
)

// Capabilities are what this server build can do; a client switches off what
// the server does not list instead of failing on it (REQ-610).
var Capabilities = []string{"anchor", "origin", "packages", "query", "labels", "presets", "ideas", "runbooks"}

// Info is the body of GET /api/version.
type Info struct {
	Version      string   `json:"version"`
	Commit       string   `json:"commit,omitempty"`
	Channel      string   `json:"channel"`
	Protocol     int      `json:"protocol"`
	MinClient    int      `json:"min_client"`
	Capabilities []string `json:"capabilities"`
}

// NewInfo describes a build with the given version string and capabilities.
func NewInfo(v string, caps []string) Info {
	return Info{
		Version: v, Commit: Commit(v), Channel: Channel(v),
		Protocol: Protocol, MinClient: MinClient, Capabilities: caps,
	}
}

// Channel derives the channel from the version string: exactly X.Y.Z or
// X.Y.Z-rc.N is a release (stable); everything else, including 0.1.0-dev+<sha>
// and unknown builds, is dev.
func Channel(v string) string {
	s, ok := parse(v)
	if !ok || s.build != "" {
		return ChannelDev
	}
	if s.pre == "" || (strings.HasPrefix(s.pre, "rc.") && isNumber(s.pre[3:])) {
		return ChannelStable
	}
	return ChannelDev
}

// Commit is the short commit a version string names after "+", else the one the
// Go toolchain stamped into this binary, else "".
func Commit(v string) string {
	if _, build, ok := strings.Cut(v, "+"); ok && build != "" {
		return build
	}
	return BuildCommit()
}

// BuildCommit is the vcs revision stamped into the running binary, shortened.
func BuildCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value[:7]
		}
	}
	return ""
}

type semver struct {
	nums  [3]int
	pre   string
	build string
}

func parse(v string) (semver, bool) {
	var s semver
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, s.build, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	s.pre = pre
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.nums[i] = n
	}
	return s, true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Relation says how "to" stands to "from".
type Relation int

const (
	// Unknown: at least one string is not a version.
	Unknown Relation = iota
	Same
	Newer
	Older
	// Different: equal precedence but another build (two dev builds of the
	// same base version differ only by commit, which has no order).
	Different
)

// Compare orders by semver precedence (build metadata ignored) and reports a
// differing build of equal precedence as Different.
func Compare(from, to string) Relation {
	a, aok := parse(from)
	b, bok := parse(to)
	if !aok || !bok {
		return Unknown
	}
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if b.nums[i] > a.nums[i] {
				return Newer
			}
			return Older
		}
	}
	if c := comparePre(a.pre, b.pre); c != 0 {
		if c < 0 {
			return Newer
		}
		return Older
	}
	if a.build != b.build {
		return Different
	}
	return Same
}

// comparePre follows semver: no prerelease beats any prerelease; identifiers
// compare numerically when both are numbers, numbers sort before text.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		xn, yn := isNumber(x), isNumber(y)
		switch {
		case xn && yn:
			xi, _ := strconv.Atoi(x)
			yi, _ := strconv.Atoi(y)
			if xi < yi {
				return -1
			}
			return 1
		case xn:
			return -1
		case yn:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

// Label is how a version reads to a person: "v0.2.0" for a release,
// "0.1.0-dev+1c48c74" for a dev build.
func Label(v string) string {
	if Channel(v) == ChannelStable && !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}
