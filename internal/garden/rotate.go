package garden

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Seen is the last look at an entry: when, against which commit of the
// checkout, at which wording of the entry, and what came out.
type Seen struct {
	CheckedAt string
	Commit    string
	EntryHash string
	Verdict   string
}

// Rotation counts the entries a run left out because they were not due.
type Rotation struct {
	// Unchanged: neither the entry nor the code it names changed since the last look.
	Unchanged int
	// Recent: looked at less than MinAge ago and no cheap way to tell whether
	// anything changed.
	Recent int
	// Never: how many entries had never been looked at.
	Never int
	// MinAge is the window the Recent rule used.
	MinAge time.Duration
}

// HashOf identifies the wording of an entry that a verdict was made for.
func HashOf(e Entry) string {
	h := sha256.New()
	for _, p := range []string{e.Type, e.Title, strings.TrimSpace(e.Body)} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// Head is the commit of the checkout.
func (r *Repo) Head() (string, error) {
	out, err := r.git("rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// codeChangedSince says whether the code an entry names changed after commit:
// a file it names has a diff, or a symbol it names occurs on a changed line.
// ok is false when that cannot be told (the commit is not in this checkout, git
// failed), in which case the caller must not treat it as "unchanged".
func (r *Repo) codeChangedSince(commit string, files, symbols []string) (changed, ok bool) {
	if commit == "" {
		return false, false
	}
	if _, err := r.git("cat-file", "-e", commit+"^{commit}"); err != nil {
		return false, false
	}
	if len(files) > 0 {
		args := append([]string{"diff", "--name-only", commit, "HEAD", "--"}, files...)
		out, err := r.git(args...)
		if err != nil {
			return false, false
		}
		if strings.TrimSpace(out) != "" {
			return true, true
		}
	}
	for _, s := range symbols {
		out, err := r.git("diff", "--name-only", "-G"+regexp.QuoteMeta(s), commit, "HEAD", "--", ".", ":(exclude).ghosttree", ":(exclude)*.md")
		if err != nil {
			return false, false
		}
		if strings.TrimSpace(out) != "" {
			return true, true
		}
	}
	return false, true
}

// Rotate chooses and orders the entries for a run. Never-looked-at entries come
// first, then entries whose wording changed since the last look, then the rest
// by how long ago they were looked at, longest first. An entry is left out when
// it has not changed since the last look (the entry, and the files and symbols
// it names, which is only judged when it can be told cheaply), and otherwise
// when the last look was less than minAge ago.
func Rotate(repo *Repo, entries []Entry, seen map[int64]Seen, minAge time.Duration, now time.Time) ([]Entry, Rotation) {
	rot := Rotation{MinAge: minAge}
	head, _ := repo.Head()
	var never, edited, aged []Entry
	checkedAt := map[int64]string{}
	for _, e := range entries {
		s, ok := seen[e.ID]
		if !ok {
			never = append(never, e)
			rot.Never++
			continue
		}
		if s.EntryHash != HashOf(e) {
			edited = append(edited, e)
			continue
		}
		if head != "" && s.Commit == head {
			rot.Unchanged++
			continue
		}
		files, symbols := repo.Refs(e)
		if len(files)+len(symbols) > 0 {
			if changed, ok := repo.codeChangedSince(s.Commit, files, symbols); ok && !changed {
				rot.Unchanged++
				continue
			}
		}
		if t, err := time.Parse(time.RFC3339Nano, s.CheckedAt); err == nil && now.Sub(t) < minAge {
			rot.Recent++
			continue
		}
		checkedAt[e.ID] = s.CheckedAt
		aged = append(aged, e)
	}
	sort.SliceStable(aged, func(i, j int) bool { return checkedAt[aged[i].ID] < checkedAt[aged[j].ID] })
	out := append(append(never, edited...), aged...)
	return out, rot
}

// Summary is the sentence for the report and the command line.
func (r Rotation) Summary() string {
	if r.Unchanged == 0 && r.Recent == 0 {
		return ""
	}
	days := int(r.MinAge.Hours() / 24)
	return fmt.Sprintf("Nicht dran: %d seit der letzten Prüfung unverändert, %d vor weniger als %d Tagen geprüft.", r.Unchanged, r.Recent, days)
}
