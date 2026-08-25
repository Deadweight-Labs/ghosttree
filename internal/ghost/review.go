package ghost

import (
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ReviewedEmpty reduces stored reviews to the paths where the decision still
// holds: someone looked at the file and deliberately left it undescribed, and
// the file has not changed since.
//
// The comparison lives here and not in BuildDocs for the same reason freshness
// does — it needs the actual files, and the server does not have them. A review
// whose blob no longer matches is silently dropped rather than reported: the
// path simply reappears on the work list, which is exactly what should happen
// when the file someone judged is no longer the file on disk.
func ReviewedEmpty(repoRoot string, reviews []store.GhostReview) map[string]bool {
	out := make(map[string]bool, len(reviews))
	for _, r := range reviews {
		if r.Path == "" || r.GitBlob == "" {
			continue
		}
		_, blob, _, err := HashFile(filepath.Join(repoRoot, filepath.FromSlash(r.Path)))
		if err != nil {
			// Deleted or unreadable: not our business here. The path is gone
			// from the entry list anyway, so saying anything would be noise.
			continue
		}
		if blob == r.GitBlob {
			out[r.Path] = true
		}
	}
	return out
}
