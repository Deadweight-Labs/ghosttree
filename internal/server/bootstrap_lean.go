package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// leanPitfalls is how many pitfalls of the project the briefing of a lean
// session carries, if they fit.
const leanPitfalls = 3

// leanPitfallChars cuts the body of such a pitfall. Three entries must fit next
// to the instructions in profile.LeanStartChars.
const leanPitfallChars = 160

// leanBootstrap answers the briefing of a session with the lean profile
// (REQ-643): the binding instructions, complete and first, then at most three
// pitfalls of this project while they fit, then one line that says where the
// rest is. No work ledger, no work package, no list of what waits for a person,
// no unfinished work: a short helper run needs none of it, and the full briefing
// is what makes a briefing of 7,000 characters the median.
//
// The length is profile.LeanStartChars, instructions excepted: they bind
// whether or not anyone reads them, so a project whose instructions alone are
// longer gets those and no pitfalls.
func (a *api) leanBootstrap(w http.ResponseWriter, r *http.Request, entries []store.Knowledge) {
	ph := phases(r)
	end := ph.start("render")
	body, shown, sizes := renderLeanBootstrap(entries, a.clientVersionNote(r))
	end()
	end = ph.start("claim")
	a.rememberDelivered(r, store.DeliveredByBootstrap, shown)
	end()
	parts := make([]deliverylog.Part, 0, len(shown))
	for i, id := range shown {
		parts = append(parts, deliverylog.Part{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: id, Chars: sizes[i]})
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setDelivered(w, parts)
	w.WriteHeader(200)
	fmt.Fprint(w, body)
}

// renderLeanBootstrap builds the lean briefing and says which entries it carries
// and how many characters each took, in that order. versionNote is the hint that
// the client is too old (clientVersionNote), empty if there is none: a lean
// session is the one that is started by a script on a machine nobody looks at,
// so it needs the hint as much as a full one. It comes before the pitfalls and
// is counted in the length; the instructions bind either way.
func renderLeanBootstrap(entries []store.Knowledge, versionNote string) (string, []int64, []int) {
	var instructions, pitfalls []store.Knowledge
	for _, k := range entries {
		switch {
		case k.Confidence == "staged":
		case k.Type == "instruction":
			instructions = append(instructions, k)
		case k.Type == "pitfall" && k.Scope.Project != "":
			pitfalls = append(pitfalls, k)
		}
	}
	var b strings.Builder
	var shown []int64
	var sizes []int
	b.WriteString("## Known context (ghosttree)\n")
	b.WriteString(keepCurrentHint)
	if len(instructions) > 0 {
		b.WriteString("\n### Instructions (binding)\n")
		for _, k := range instructions {
			label := scopeLabel(k.Scope)
			if gate := activationLabel(k.Activation); gate != "" {
				label += " | " + gate
			}
			line := fmt.Sprintf("- [%s] %s — %s\n", label, k.Title, oneLine(k.Body))
			b.WriteString(line)
			shown = append(shown, k.ID)
			sizes = append(sizes, deliverylog.Runes(line))
		}
	}
	if versionNote != "" {
		fmt.Fprintf(&b, "\n> ghosttree: %s\n", versionNote)
	}
	footer := leanFooter()
	room := profile.LeanStartChars - deliverylog.Runes(b.String()) - deliverylog.Runes(footer)
	wrote := 0
	for _, k := range pitfalls {
		if wrote == leanPitfalls {
			break
		}
		line := fmt.Sprintf("- [%s] %s — %s\n", scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), leanPitfallChars))
		if wrote == 0 {
			line = "\n### pitfall\n" + line
		}
		if n := deliverylog.Runes(line); n <= room {
			b.WriteString(line)
			room -= n
			wrote++
			shown = append(shown, k.ID)
			sizes = append(sizes, n)
		}
	}
	b.WriteString(footer)
	return b.String(), shown, sizes
}

// leanFooter is the one line a lean briefing ends with: where the rest is, and
// which tools the session does not carry up front. It names them because an
// agent cannot look for a tool it has no reason to think exists.
func leanFooter() string {
	return "\nMore: `context_search`; the mirror starts at `.ghosttree/INDEX.md`. Not loaded up front, find them by tool search: " +
		strings.Join(profile.Lean.Deferred(), ", ") + ".\n"
}
