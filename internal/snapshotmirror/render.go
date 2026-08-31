package snapshotmirror

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

// RenderIndex renders only immutable snapshot heads. Snapshot payloads stay in
// the store and are fetched explicitly through show, export, or verify.
func RenderIndex(heads []snapshot.Head) []byte {
	ordered := append([]snapshot.Head(nil), heads...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt != ordered[j].CreatedAt {
			return ordered[i].CreatedAt > ordered[j].CreatedAt
		}
		return ordered[i].Name < ordered[j].Name
	})

	var b strings.Builder
	b.WriteString("# Context snapshots\n\n")
	b.WriteString("Immutable project-context marks. Payloads are loaded explicitly; this index contains metadata only.\n")
	for _, head := range ordered {
		fmt.Fprintf(&b, "\n## %s\n\n", markdownText(head.Name))
		fmt.Fprintf(&b, "- Created: %s\n", head.CreatedAt)
		fmt.Fprintf(&b, "- Git: `%s`", markdownCode(head.GitCommit))
		if head.GitRef != nil {
			fmt.Fprintf(&b, " (`%s`)", markdownCode(*head.GitRef))
		}
		b.WriteByte('\n')
		fmt.Fprintf(&b, "- Dirty: %s\n", yesNo(head.GitDirty))
		fmt.Fprintf(&b, "- Digest: `%s`\n", head.ContentDigest.String())
		fmt.Fprintf(&b, "- Counts: %s\n", renderCounts(head.Counts))
		fmt.Fprintf(&b, "- Payload bytes: %d\n", head.PayloadBytesTotal)
		quoted := shellQuote(head.Name)
		fmt.Fprintf(&b, "- Show: `ctx snapshot show %s`\n", quoted)
		fmt.Fprintf(&b, "- Export: `ctx snapshot export %s`\n", quoted)
		fmt.Fprintf(&b, "- Verify: `ctx snapshot verify %s`\n", quoted)
	}
	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

func renderCounts(counts map[string]int64) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func markdownText(value string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "\n", " ", "\r", " ").Replace(value)
}
func markdownCode(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "`", ""), "\n", " ")
}
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
