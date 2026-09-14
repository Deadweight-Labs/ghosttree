package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const coordUsage = `usage: ctx coord <command>

  peers [--machine] [repo]        who else is registered in this room
  send <text> [--machine] [repo]  say something to the other agents
  inbox [--machine] [--all] [repo] read what others said since your cursor
  rooms                           private conversations you take part in

The project room follows the repository's normalised remote, not the working
directory: two agents in different subdirectories of one repo share it.
--machine addresses everyone on this computer instead, including agents
without a repository.

This is the way in for humans, for harnesses without MCP, and for measuring
whether a message actually crosses between two harnesses. Agents use the
coord_* tools.`

// coordSession ist die Identität, unter der die CLI schreibt. Ein Mensch am
// Terminal ist keine Agent-Session, und ihn als eine auszugeben würde die
// Herkunftstrennung unterlaufen, auf der der Rest aufbaut — deshalb trägt der
// Absender den Maschinennamen und nicht eine erfundene Sitzungskennung.
func coordSession(cfg config.Config) string {
	return "cli:" + cfg.Machine
}

func cmdCoord(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, coordUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, rest := args[0], args[1:]

	machine := false
	all := false
	var positional []string
	for _, a := range rest {
		switch a {
		case "--machine":
			machine = true
		case "--all":
			all = true
		default:
			positional = append(positional, a)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)
	me := coordSession(cfg)

	room, code := coordRoomKey(machine, positional, cfg, stdout)
	if code != 0 && sub != "rooms" {
		return code
	}
	// Auch die CLI ist Teilnehmerin. Wer schreibt und liest, gehört in die
	// Liste — sonst zeigt `coord peers` für einen Agenten "niemand da",
	// während am selben Raum gerade jemand mitliest.
	if code == 0 {
		_, _ = c.RegisterCoordAgent(store.CoordAgent{
			ExternalID: me, Provider: "ctx-cli", RoomKey: room, DisplayName: me,
		})
	}

	switch sub {
	case "peers":
		peers, err := c.CoordPeers(room, "")
		if err != nil {
			fmt.Fprintf(stdout, "peers: %v\n", err)
			return 1
		}
		if len(peers) == 0 {
			fmt.Fprintf(stdout, "nobody registered in %s\n", room)
			return 0
		}
		for _, p := range peers {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\tlast observed %s\n",
				p.ExternalID, p.DisplayName, p.Provider, p.Branch, p.LastSeenAt)
		}
		return 0

	case "send":
		body := strings.TrimSpace(strings.Join(positionalText(positional), " "))
		if body == "" {
			fmt.Fprintln(stdout, "nothing to say")
			return 2
		}
		id, err := c.SendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: me, ClientID: newCoordClientID(), Body: body,
		})
		if err != nil {
			fmt.Fprintf(stdout, "send: %v\n", err)
			return 1
		}
		// Gespeichert, nicht zugestellt — dieselbe Ehrlichkeit wie im
		// Werkzeug. Was ein anderer Harness daraus macht, weiß dieser Aufruf
		// nicht.
		fmt.Fprintf(stdout, "stored as %d in %s\n", id, room)
		return 0

	case "inbox":
		after := int64(0)
		if !all {
			after, _ = c.CoordCursor(me, store.DestinationRoom, room)
		}
		msgs, err := c.CoordInbox(store.DestinationRoom, room, me, after, 0)
		if err != nil {
			fmt.Fprintf(stdout, "inbox: %v\n", err)
			return 1
		}
		if len(msgs) == 0 {
			fmt.Fprintf(stdout, "nothing new in %s\n", room)
			return 0
		}
		var highest int64
		for _, m := range msgs {
			if m.ID > highest {
				highest = m.ID
			}
			mark := ""
			if m.Expired {
				mark = " [expired]"
			}
			fmt.Fprintf(stdout, "#%d\t%s\t%s%s\t%s\n",
				m.Sequence, m.SenderExternalID, m.AuthorKind, mark, m.Body)
		}
		if highest > 0 {
			if err := c.SetCoordCursor(me, store.DestinationRoom, room, highest); err != nil {
				fmt.Fprintf(stdout, "cursor: %v\n", err)
				return 1
			}
		}
		return 0

	case "rooms":
		rooms, err := c.CoordRoomsFor(me)
		if err != nil {
			fmt.Fprintf(stdout, "rooms: %v\n", err)
			return 1
		}
		if len(rooms) == 0 {
			fmt.Fprintln(stdout, "no private conversations")
			return 0
		}
		for _, r := range rooms {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", r.Kind, r.Label, strings.Join(r.Members, ","))
		}
		return 0
	}

	fmt.Fprintln(stdout, coordUsage)
	return 2
}

// coordRoomKey löst den Raum genauso auf wie das MCP-Werkzeug: aus der
// Repo-Identität, nicht aus dem Arbeitsverzeichnis. Beide Wege müssen
// denselben Schlüssel bilden, sonst redet die CLI in einem anderen Raum als
// die Agenten im selben Projekt.
func coordRoomKey(machine bool, positional []string, cfg config.Config, stdout io.Writer) (string, int) {
	if machine {
		return store.RoomKeyForMachine(cfg.Machine), 0
	}
	repoArg := "."
	if len(positional) > 0 {
		if last := positional[len(positional)-1]; looksLikePath(last) {
			repoArg = last
		}
	}
	repo, err := filepath.Abs(repoArg)
	if err != nil {
		fmt.Fprintf(stdout, "bad repository path: %v\n", err)
		return "", 2
	}
	gitCtx := collector.ResolveGitContext(repo)
	if gitCtx.Project == "" {
		fmt.Fprintln(stdout, "not a repository with an origin remote; use --machine")
		return "", 1
	}
	return store.RoomKeyForProject(gitCtx.Project), 0
}

// positionalText lässt einen abschließenden Pfad weg, damit
// `ctx coord send "text" /repo` den Pfad nicht mitsendet.
func positionalText(positional []string) []string {
	if len(positional) > 1 && looksLikePath(positional[len(positional)-1]) {
		return positional[:len(positional)-1]
	}
	return positional
}

func looksLikePath(s string) bool {
	return s == "." || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~")
}

// newCoordClientID macht das Senden wiederholbar. Derselbe Aufruf nach einem
// Timeout ist dieselbe Nachricht, nicht die zweite.
func newCoordClientID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
