// Package releasekey holds the public minisign key that signs every Ghosttree
// release: the one install.sh checks releases against, here for ctx itself
// (ctx server update).
//
// The key is the file minisign.pub, a copy of
// deploy/downloads/ghosttree/minisign.pub that scripts/release-key.sh keeps in
// step; a test fails when the two differ. Until the release key exists the file
// holds a placeholder and Public reports that.
package releasekey

import (
	_ "embed"
	"errors"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
)

//go:embed minisign.pub
var embedded string

// override replaces the embedded key in a build that is only for tests, set
// with -ldflags "-X github.com/Deadweight-Labs/ghosttree/internal/releasekey.override=<key line>".
// A release is never built with it; goreleaser's flags do not set it.
var override string

// ErrNoKey says this build carries no release key yet.
var ErrNoKey = errors.New("this ctx carries no release key yet, so it cannot check a release")

// Public returns the key that releases are checked against.
func Public() (minisign.PublicKey, error) {
	text := embedded
	if override != "" {
		text = override
	}
	if strings.Contains(text, "__GHOSTTREE") {
		return minisign.PublicKey{}, ErrNoKey
	}
	return minisign.ParsePublicKey(text)
}
