package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// mentionRepoOf is the project an @name on the page is read in: the repository
// of the page, or the project of the room the page shows. "" when the page has
// no project, where a name stays as it was typed.
func mentionRepoOf(d pageData) string {
	if d.Repo != nil && d.Repo.Project != "" {
		return d.Repo.Project
	}
	if room := d.Coord.Active; room != nil {
		if remote, ok := strings.CutPrefix(room.Room.Key, "project:"); ok {
			return remote
		}
	}
	return ""
}

// maxMentionHandles bounds what one page asks about.
const maxMentionHandles = 50

// GET /ui/mentions?project=…: the people and agents of a project a text there
// can address. With ?options=1 the answer lists them as the editor offers them
// after @; with ?h=mara&h=jonas it names the handles written in texts, so the
// pill shows the full name. A handle that fits nobody, or several, is not in
// the answer, and neither is anything for someone who may not see the members
// of the project: the answer is then the same empty one.
func (a *app) mentionsLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	project := scope.NormalizeRemote(q.Get("project"))
	pa := a.access(r)
	pa.Filtered()
	out := struct {
		Options []store.MentionOption        `json:"options"`
		Names   map[string]store.MentionName `json:"names"`
	}{Options: []store.MentionOption{}, Names: map[string]store.MentionName{}}
	if project != "" {
		if q.Get("options") == "1" {
			l := a.langFor(r)
			for _, o := range pa.MentionOptions(project) {
				if o.Agent {
					o.Note = l.msg("coord.mention.agent")
					if o.Owner != "" {
						o.Note = l.msg("coord.mention.agent_of", o.Owner)
					}
				}
				out.Options = append(out.Options, o)
			}
		}
		if h := q["h"]; len(h) > 0 {
			if len(h) > maxMentionHandles {
				h = h[:maxMentionHandles]
			}
			for k, v := range pa.MentionNames(project, h) {
				out.Names[k] = v
			}
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}
