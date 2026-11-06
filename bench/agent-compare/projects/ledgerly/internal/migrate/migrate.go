// Package migrate discovers and orders the SQL migrations in migrations/.
// Applying them is done by the deploy tooling; this package validates the
// directory and is used by `ledgerly migrate-check`.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration is one up migration and its optional down migration.
type Migration struct {
	Version int
	Name    string
	Up      string // path
	Down    string // path, empty if none
}

var fileRE = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+?)(\.down)?\.sql$`)

// Discover reads dir and returns the migrations ordered by version.
func Discover(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*Migration{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := fileRE.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: bad file name %q", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		mig, ok := byVersion[v]
		if !ok {
			mig = &Migration{Version: v, Name: m[2]}
			byVersion[v] = mig
		} else if mig.Name != m[2] {
			return nil, fmt.Errorf("migrate: version %04d used twice (%s, %s)", v, mig.Name, m[2])
		}
		p := filepath.Join(dir, e.Name())
		if m[3] != "" {
			mig.Down = p
		} else {
			mig.Up = p
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.Up == "" {
			return nil, fmt.Errorf("migrate: %04d_%s has a down but no up migration", m.Version, m.Name)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migrate: gap before version %04d", m.Version)
		}
	}
	return out, nil
}
