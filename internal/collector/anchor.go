package collector

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// AnchorTimeout begrenzt die gesamte Ermittlung eines Ankers. Ein Schreibzugriff
// wartet darauf, also darf ein hängendes git (NFS, riesiges Repo, Sperre) ihn
// nicht aufhalten: nach der Frist bleibt der Anker leer.
const AnchorTimeout = 3 * time.Second

// baseRefFallbacks sind die Default-Branches, die gelten, wenn das Remote keinen
// origin/HEAD kennt (ein Klon ohne `git remote set-head`, ein Mirror). Die
// Reihenfolge ist die Entscheidung: main vor master vor dev. Ein Repo mit main
// UND dev, aber ohne origin/HEAD, hat main als Default behandelt; wer das
// anders will, setzt origin/HEAD.
var baseRefFallbacks = []string{"origin/main", "origin/master", "origin/dev"}

// ResolveAnchor ermittelt den Git-Anker eines Arbeitsverzeichnisses.
//
// Leer (Anchor{}) bei allem, was keinen belastbaren Commit liefert: kein git,
// kein Repo, ungeborener Branch, Zeitüberschreitung. Raten gibt es nicht. Ein
// Anker mit Commit ist dagegen auch dann gültig, wenn einzelne Teile fehlen:
// ohne ermittelbaren Default-Branch bleibt MergeBase leer, bei losem HEAD
// Branch. Schlägt nur die Dirty-Prüfung fehl, bleibt Dirty false; das ist die
// einzige Stelle, an der "unbekannt" und "sauber" zusammenfallen.
func ResolveAnchor(cwd string) gitanchor.Anchor {
	if cwd == "" {
		return gitanchor.Anchor{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), AnchorTimeout)
	defer cancel()
	commit, err := anchorGit(ctx, cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || !gitanchor.ValidPrefix(commit) {
		return gitanchor.Anchor{}
	}
	a := gitanchor.Anchor{Commit: commit}
	if b, err := anchorGit(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "HEAD" {
		a.Branch = b
	}
	if base := DefaultBaseRef(ctx, cwd); base != "" {
		if mb, err := anchorGit(ctx, cwd, "merge-base", "HEAD", base); err == nil && gitanchor.ValidPrefix(mb) {
			a.MergeBase = mb
		}
	}
	if out, err := anchorGit(ctx, cwd, "status", "--porcelain", "--untracked-files=no"); err == nil && out != "" {
		a.Dirty = true
	}
	return a
}

// DefaultBaseRef nennt den Ref, gegen den die Merge-Base gebildet wird: der
// Default-Branch des Remotes. Erst origin/HEAD (so sagt das Remote selbst, was
// sein Default ist), dann der erste vorhandene aus baseRefFallbacks. Leer, wenn
// nichts davon existiert; dann gibt es keine Merge-Base.
func DefaultBaseRef(ctx context.Context, cwd string) string {
	if ref, err := anchorGit(ctx, cwd, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		// Ein origin/HEAD, dessen Ziel fehlt, wäre ein toter Verweis.
		if _, err := anchorGit(ctx, cwd, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	for _, ref := range baseRefFallbacks {
		if _, err := anchorGit(ctx, cwd, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return ""
}

// anchorGit ist gitOut mit Frist. GIT_OPTIONAL_LOCKS=0, damit `status` nicht
// den Index des Arbeitsverzeichnisses neu schreibt und einem laufenden
// git-Befehl des Agenten in die Quere kommt.
func anchorGit(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
