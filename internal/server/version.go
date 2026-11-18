package server

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/version"
)

// versionInfo answers GET /api/version. It is public like /api/health: a
// client has to read it before it has a working token (to see that it is too
// old to log in at all), and it names nothing that /install.sh and /dist/
// do not already show.
func (a *api) versionInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.version())
}

func (a *api) version() version.Info {
	caps := slices.Clone(version.Capabilities)
	for _, c := range a.extraCaps {
		if !slices.Contains(caps, c) {
			caps = append(caps, c)
		}
	}
	return version.NewInfo(a.buildVersion, caps)
}

// clientVersionNote is the one line the briefing carries when the asking
// client is older than this server, or too old for it. Empty when the client
// sent no version (older clients, browsers) or fits.
func (a *api) clientVersionNote(r *http.Request) string {
	cv := strings.TrimSpace(r.Header.Get("X-Ctx-Version"))
	if cv == "" {
		return ""
	}
	proto, _ := strconv.Atoi(r.Header.Get("X-Ctx-Protocol"))
	info := a.version()
	if proto > 0 && proto < info.MinClient {
		return fmt.Sprintf("ctx %s is too old for this server (needs protocol %d, has %d): run `ctx update`.", cv, info.MinClient, proto)
	}
	rel := version.Compare(cv, info.Version)
	if rel == version.Newer || (rel == version.Different && version.Channel(cv) == version.ChannelDev) {
		return fmt.Sprintf("This server runs ctx %s, this machine %s: run `ctx update` (restart running agents afterwards).", info.Version, cv)
	}
	return ""
}
