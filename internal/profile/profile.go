// Package profile is the profile of an agent session: how much of ghosttree it
// carries up front. A short helper run (a reviewer, a script that calls
// `claude -p`) needs search, read, remember and the ledger and little else; a
// working session needs everything. The profile says which tools are loaded up
// front, how long the briefing at the start may be and how much context the
// hooks may add during the session.
//
// For now the profile is only ever set on purpose, with GHOSTTREE_PROFILE
// (REQ-643). Without the variable, and with a value this package does not know,
// the session is full: better too much context than a tool that is missing.
package profile

import (
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
)

// Profile is the kind of session.
type Profile string

const (
	// Full is a working session: the tools of the core and work tiers up front,
	// the whole briefing, the whole hook budget.
	Full Profile = "full"
	// Lean is a short helper run: the core tier up front, a briefing of at most
	// LeanStartChars characters, a hook budget of LeanHookBudget.
	Lean Profile = "lean"
)

// EnvVar is the environment variable that sets the profile. ctx mcp and every
// hook read it from their own environment, which a harness passes on from the
// process that started it.
const EnvVar = "GHOSTTREE_PROFILE"

const (
	// FullHookBudget is what the hooks may add to a full session, in characters.
	FullHookBudget = 24000
	// LeanHookBudget is what the hooks may add to a lean session.
	LeanHookBudget = 6000
	// LeanStartChars is the length of the briefing a lean session gets at its
	// start. Binding instructions are always complete and come first, so a
	// project whose instructions alone are longer gets those and nothing else.
	LeanStartChars = 1500
)

// Parse reads a value of EnvVar. An empty value is Full, as it always was. ok
// is false for a value that names no profile; the profile is Full then too, and
// a caller that can say so (ctx status) does.
func Parse(value string) (p Profile, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(Full):
		return Full, true
	case string(Lean):
		return Lean, true
	}
	return Full, false
}

// FromEnv is the profile of the process.
func FromEnv() Profile {
	p, _ := Parse(os.Getenv(EnvVar))
	return p
}

// Valid reports whether p is a profile this package knows. The zero value is
// not, and behaves like Full everywhere.
func (p Profile) Valid() bool { return p == Full || p == Lean }

// String is the name of the profile; the zero value is full.
func (p Profile) String() string {
	if p == Lean {
		return string(Lean)
	}
	return string(Full)
}

// Preloads says whether a tool of this tier is loaded up front under the
// profile. A tool that is not stays in the tool list with its name and is
// loaded by the harness when an agent looks for it.
func (p Profile) Preloads(t coretools.Tier) bool {
	switch t {
	case coretools.TierCore:
		return true
	case coretools.TierWork:
		return p != Lean
	}
	return false
}

// Deferred lists, in table order, the core tools the profile does not load up
// front.
func (p Profile) Deferred() []string {
	var out []string
	for _, c := range coretools.Table() {
		if !p.Preloads(c.Tier) {
			out = append(out, c.Name)
		}
	}
	return out
}

// HookBudget is how many characters the hooks may add to a session of this
// profile.
func (p Profile) HookBudget() int {
	if p == Lean {
		return LeanHookBudget
	}
	return FullHookBudget
}
