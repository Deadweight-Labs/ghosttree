package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func machineFail(stdout io.Writer, what string, err error) int {
	var api *client.APIError
	if errors.As(err, &api) {
		fmt.Fprintf(stdout, "%s: %s [%s]\n", what, api.Message, api.Code)
		if api.Code == "web_session_required" {
			link := "/ui/m/<machine>"
			if cfg, err := config.Load(); err == nil && cfg.ServerURL != "" {
				link = strings.TrimRight(cfg.ServerURL, "/") + "/ui/m/<machine>"
			}
			fmt.Fprintf(stdout, "Open %s in your browser and sign in. On the server host the operator can run ctx machine share with --db <path> instead.\n", link)
		}
		return 1
	}
	fmt.Fprintf(stdout, "%s: %v\n", what, err)
	return 1
}

// cmdMachineShare ist der Teil von ctx machine, der Freigaben betrifft: Lesen
// und das Freigeben einzelner Einträge über den Server, das Freigeben der
// Maschine selbst nur für den Betreiber mit --db.
func cmdMachineShare(args []string, stdout io.Writer) int {
	sub := args[0]
	fs := flag.NewFlagSet("machine "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "", "database path: operator access on the server host")
	role := fs.String("role", store.ShareRead, "share role: read or write")
	osUser := fs.String("user", "", "operating user the share is for (a note, not a limit)")
	path := fs.String("path", "", "path prefix the share is for (a note, not a limit)")
	unshare := fs.Bool("unshare", false, "take the entry sharing back")
	usage := func() int { fmt.Fprintln(stdout, machineUsage); return 2 }
	switch sub {
	case "mine", "show", "share-entry", "share", "unshare", "owner-org":
	default:
		return usage()
	}
	pos, err := interspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	switch sub {
	case "share", "unshare", "owner-org":
		want := 2
		if len(pos) != want || *db == "" {
			if *db == "" {
				fmt.Fprintln(stdout, "this needs --db <path> on the server host; otherwise a person does it in the web UI")
			}
			return usage()
		}
		st, ok := openAccountStore(*db, stdout)
		if !ok {
			return 1
		}
		defer st.Close()
		switch sub {
		case "share":
			sh, err := st.ShareMachineAsOperator(pos[0], pos[1], *role, *osUser, *path)
			if err != nil {
				return machineFail(stdout, "share machine", err)
			}
			fmt.Fprintf(stdout, "machine %s shared with %s (%s)\n", strings.ToLower(pos[0]), sh.Org, sh.Role)
		case "unshare":
			if err := st.UnshareMachineAsOperator(pos[0], pos[1]); err != nil {
				return machineFail(stdout, "unshare machine", err)
			}
			fmt.Fprintf(stdout, "machine %s is no longer shared with %s; its entries are private again\n", strings.ToLower(pos[0]), pos[1])
		case "owner-org":
			if err := st.SetMachineOwnerOrgAsOperator(pos[0], pos[1]); err != nil {
				return machineFail(stdout, "set owner organization", err)
			}
			fmt.Fprintf(stdout, "machine %s now belongs to organization %s\n", strings.ToLower(pos[0]), pos[1])
		}
		return 0
	}
	if *db != "" {
		fmt.Fprintln(stdout, "mine, show and share-entry talk to the server; drop --db")
		return 2
	}
	c, ok := orgClient(stdout)
	if !ok {
		return 1
	}
	switch sub {
	case "mine":
		if len(pos) != 0 {
			return usage()
		}
		list, err := c.Machines()
		if err != nil {
			return machineFail(stdout, "list machines", err)
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, m := range list {
			owner := m.Owner
			if m.OwnerOrg != "" {
				owner = "org " + m.OwnerOrg
			}
			var shared []string
			for _, sh := range m.Shares {
				shared = append(shared, sh.Org+" ("+sh.Role+")")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Name, m.Relation, owner, strings.Join(shared, ", "))
		}
		tw.Flush()
	case "show":
		if len(pos) != 1 {
			return usage()
		}
		m, err := c.MachinePage(pos[0])
		if err != nil {
			return machineFail(stdout, "show machine", err)
		}
		owner := m.Owner
		if m.OwnerOrg != "" {
			owner = "organization " + m.OwnerOrg
		}
		fmt.Fprintf(stdout, "%s: owner %s, you are %s\n", m.Name, owner, m.Relation)
		for _, sh := range m.Shares {
			fmt.Fprintf(stdout, "  shared with %s (%s)", sh.Org, sh.Role)
			if sh.OSUser != "" {
				fmt.Fprintf(stdout, " user %s", sh.OSUser)
			}
			if sh.PathPrefix != "" {
				fmt.Fprintf(stdout, " path %s", sh.PathPrefix)
			}
			fmt.Fprintln(stdout)
		}
		for _, e := range m.Events {
			fmt.Fprintf(stdout, "  %s %s %s %s by %s via %s\n", e.CreatedAt, e.Action, e.Subject, e.Detail, e.Actor, e.Via)
		}
	case "share-entry":
		if len(pos) != 3 || (pos[0] != store.EntryKnowledge && pos[0] != store.EntryRunbook) {
			return usage()
		}
		id, ok := knref.Parse(pos[1])
		if !ok {
			return usage()
		}
		shared, err := c.ShareEntry(pos[0], id, pos[2], !*unshare)
		if err != nil {
			return machineFail(stdout, "share entry", err)
		}
		if len(shared) == 0 {
			shared = []string{"nobody (private)"}
		}
		fmt.Fprintf(stdout, "%s %s is now shared with: %s\n", pos[0], entryRef(pos[0], id), strings.Join(shared, ", "))
	}
	return 0
}

// entryRef names a shared entry: knowledge as KN-n, a runbook by its number.
func entryRef(kind string, id int64) string {
	if kind == store.EntryKnowledge {
		return knref.Format(id)
	}
	return "#" + strconv.FormatInt(id, 10)
}
