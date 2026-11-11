package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	ver "github.com/Deadweight-Labs/ghosttree/internal/version"
)

// serverVersionCheck is the doctor line that sets this ctx against the server
// it talks to (REQ-436 AC-8). A ctx older than its server fails: it does not
// know the commands and fields the server already has (coord, for one), and
// agents then fail in ways nobody reports. A server older than this ctx only
// leaves a note, because the client switches off what the server does not
// offer.
func serverVersionCheck(cfg config.Config) installer.Check {
	info, err := client.New(cfg).Version()
	if err != nil {
		return installer.Check{
			Name:       "server version",
			Unverified: true,
			Detail:     "the server does not answer /api/version (" + links.Clean(err.Error(), 120) + "): an older server, or a proxy in the way",
			Fix:        "ask whoever runs the server to roll out a current ghosttree",
		}
	}
	return judgeServerVersion(version, info)
}

// judgeServerVersion compares the version of this ctx with what the server
// reports about itself. Version strings are cleaned: they come over the wire.
func judgeServerVersion(clientVersion string, info ver.Info) installer.Check {
	check := installer.Check{Name: "server version"}
	cv, sv := versionLabel(clientVersion), links.Clean(versionLabel(info.Version), 60)
	both := fmt.Sprintf("this ctx %s, server %s", cv, sv)
	missing := missingCapabilities(info.Capabilities)

	if ver.Protocol < info.MinClient {
		check.Detail = fmt.Sprintf("this ctx is too old for the server: it speaks protocol %d, the server needs at least %d (%s)", ver.Protocol, info.MinClient, both)
		check.Fix = "run 'ctx update'; agents that are already running need a restart afterwards"
		return check
	}

	switch ver.Compare(clientVersion, info.Version) {
	case ver.Newer:
		check.Detail = fmt.Sprintf("this ctx is older than the server (%s), so it may not know newer commands such as coord", both)
		check.Fix = "run 'ctx update'" + channelHint(info) + "; agents that are already running need a restart afterwards"
	case ver.Older:
		check.Unverified = true
		check.Detail = fmt.Sprintf("the server is older than this ctx (%s): newer commands may fail against it", both)
		if len(missing) > 0 {
			check.Detail += "; it does not offer " + strings.Join(missing, ", ")
		}
		check.Fix = "whoever runs the server rolls out a current ghosttree; this ctx works around what the server lacks"
	case ver.Different:
		check.Unverified = true
		check.Detail = fmt.Sprintf("this ctx and the server are different builds of the same version (%s); which one is newer is not known", both)
		check.Fix = "run 'ctx update' if the server is the newer one"
	case ver.Same:
		check.OK = true
		check.Detail = both + " (protocol " + fmt.Sprint(info.Protocol) + ")"
		if len(missing) > 0 {
			check.OK, check.Unverified = false, true
			check.Detail += "; the server does not offer " + strings.Join(missing, ", ")
			check.Fix = "whoever runs the server rolls out a current ghosttree"
		}
	default:
		check.Unverified = true
		check.Detail = fmt.Sprintf("the versions cannot be compared (%s)", both)
	}
	return check
}

// channelHint names the channel when the server runs a build of another
// channel than the one this machine updates from: an update from stable never
// reaches a server that runs dev.
func channelHint(info ver.Info) string {
	cfg, err := config.Load()
	if err != nil || info.Channel == "" || cfg.UpdateChannel() == info.Channel {
		return ""
	}
	return fmt.Sprintf(" (the server runs the %s channel, this machine updates from %s: 'ctx config set channel %s')", links.Clean(info.Channel, 16), cfg.UpdateChannel(), links.Clean(info.Channel, 16))
}

// missingCapabilities are what this ctx knows how to use and the server does
// not list.
func missingCapabilities(server []string) []string {
	var out []string
	for _, c := range ver.Capabilities {
		if !slices.Contains(server, c) {
			out = append(out, c)
		}
	}
	return out
}
