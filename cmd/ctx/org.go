package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const orgUsage = `usage: ctx org <command>

  list                                    organizations you belong to
  create <name> [--slug S]                create an organization (instance admin)
  members <org>                           list members
  members <org> set-role <account> <owner|member>
  members <org> remove <account>          remove a member (or yourself)
  invite <org> [--email E] [--role owner|member] [--days N]
                                          print a single-use invitation code
  invitations <org> [revoke <id>]         list or revoke invitations
  accept <code>                           join an organization with a code
  rename <org> <name> [--slug S]          rename an organization (owner)
  default <org>                           organization for your new projects ("none" clears)`

const projectUsage = `usage: ctx project <command>

  list [--org O]                          projects of your organizations
  claim [remote] [--org O]                assign a project to an organization
                                          (default remote: the current repository)
  move <remote> --org O                   move a project (owner of both organizations)
  move --force <remote> --org O --db P    admin: move without permission checks
                                          (database access, logged in org_events)`

// interspersed parst Flags auch hinter Positionsargumenten.
func interspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// orgClient lädt die Client-Konfiguration; die Org-Befehle sprechen die API
// mit dem Token des Kontos, nicht die Datenbank.
func orgClient(stdout io.Writer) (*client.Client, bool) {
	cfg, err := config.Load()
	if err != nil || cfg.ServerURL == "" {
		fmt.Fprintln(stdout, "no server configured: run ctx login or ctx setup first")
		return nil, false
	}
	return client.New(cfg), true
}

// orgFail schreibt einen Fehler lesbar, bei einem unbeanspruchten Projekt mit
// den Auswahlmöglichkeiten.
func orgFail(stdout io.Writer, what string, err error) int {
	var api *client.APIError
	if errors.As(err, &api) {
		msg := api.Message
		if choices, ok := api.Details["choices"].([]any); ok && len(choices) > 0 {
			parts := make([]string, len(choices))
			for i, c := range choices {
				parts[i] = fmt.Sprint(c)
			}
			msg += " (use --org with one of: " + strings.Join(parts, ", ") + ")"
		}
		fmt.Fprintf(stdout, "%s: %s [%s]\n", what, msg, api.Code)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %v\n", what, err)
	return 1
}

func cmdOrg(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, orgUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("org "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	slug := fs.String("slug", "", "organization slug")
	email := fs.String("email", "", "bind the invitation to this email address")
	role := fs.String("role", "member", "invitation role")
	days := fs.Int("days", 0, "invitation lifetime in days (default 7, at most 30)")
	switch sub {
	case "list", "create", "rename", "members", "invite", "invitations", "accept", "default":
	default:
		fmt.Fprintln(stdout, orgUsage)
		return 2
	}
	pos, err := interspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	usage := func() int { fmt.Fprintln(stdout, orgUsage); return 2 }
	c, ok := orgClient(stdout)
	if !ok {
		return 1
	}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usage()
		}
		orgs, err := c.ListOrgs()
		if err != nil {
			return orgFail(stdout, "list organizations", err)
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, o := range orgs {
			def := ""
			if o.Default {
				def = "default"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", o.Slug, o.Name, o.Role, def)
		}
		tw.Flush()
	case "create":
		if len(pos) != 1 {
			return usage()
		}
		o, err := c.CreateOrg(pos[0], *slug)
		if err != nil {
			return orgFail(stdout, "create organization", err)
		}
		fmt.Fprintf(stdout, "organization %s created (you are its owner)\n", o.Slug)
	case "rename":
		if len(pos) != 2 {
			return usage()
		}
		o, err := c.RenameOrg(pos[0], pos[1], *slug)
		if err != nil {
			return orgFail(stdout, "rename organization", err)
		}
		fmt.Fprintf(stdout, "organization is now %s (%s)\n", o.Name, o.Slug)
	case "members":
		switch {
		case len(pos) == 1:
			members, err := c.OrgMembers(pos[0])
			if err != nil {
				return orgFail(stdout, "list members", err)
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			for _, m := range members {
				fmt.Fprintf(tw, "%s\t%s\tjoined %s\n", m.Account, m.Role, m.JoinedAt)
			}
			tw.Flush()
		case len(pos) == 4 && pos[1] == "set-role":
			if err := c.SetOrgMemberRole(pos[0], pos[2], pos[3]); err != nil {
				return orgFail(stdout, "set role", err)
			}
			fmt.Fprintf(stdout, "%s is now %s in %s\n", pos[2], pos[3], pos[0])
		case len(pos) == 3 && pos[1] == "remove":
			if err := c.RemoveOrgMember(pos[0], pos[2]); err != nil {
				return orgFail(stdout, "remove member", err)
			}
			fmt.Fprintf(stdout, "%s removed from %s\n", pos[2], pos[0])
		default:
			return usage()
		}
	case "invite":
		if len(pos) != 1 {
			return usage()
		}
		if *days < 0 {
			return usage()
		}
		inv, err := c.CreateInvitation(pos[0], *email, *role, *days*24)
		if err != nil {
			return orgFail(stdout, "invite", err)
		}
		fmt.Fprintf(stdout, "invitation code (shown once, single use, valid until %s):\n%s\n", inv.Invitation.ExpiresAt, inv.Code)
		if inv.Invitation.Email != "" {
			fmt.Fprintf(stdout, "bound to %s: the identity provider must report this address as verified\n", inv.Invitation.Email)
		}
		fmt.Fprintf(stdout, "The invitee opens <server>/ui/login/code?code=<code> and signs in; an existing account can run: ctx org accept <code>\n")
	case "invitations":
		switch {
		case len(pos) == 1:
			list, err := c.ListInvitations(pos[0])
			if err != nil {
				return orgFail(stdout, "list invitations", err)
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			for _, i := range list {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\tby %s\texpires %s\n", i.ID, i.Status, i.Role, i.Email, i.InvitedBy, i.ExpiresAt)
			}
			tw.Flush()
		case len(pos) == 3 && pos[1] == "revoke":
			var id int64
			if _, err := fmt.Sscan(pos[2], &id); err != nil {
				return usage()
			}
			if err := c.RevokeInvitation(pos[0], id); err != nil {
				return orgFail(stdout, "revoke invitation", err)
			}
			fmt.Fprintf(stdout, "invitation %d revoked\n", id)
		default:
			return usage()
		}
	case "accept":
		if len(pos) != 1 {
			return usage()
		}
		o, err := c.AcceptInvitation(strings.TrimSpace(pos[0]))
		if err != nil {
			return orgFail(stdout, "accept invitation", err)
		}
		fmt.Fprintf(stdout, "you joined %s\n", o.Slug)
	case "default":
		if len(pos) != 1 {
			return usage()
		}
		ref := pos[0]
		if ref == "none" {
			ref = "0"
		}
		if _, err := c.SetDefaultOrg(ref); err != nil {
			return orgFail(stdout, "set default organization", err)
		}
		fmt.Fprintf(stdout, "default organization: %s\n", pos[0])
	}
	return 0
}

func cmdProject(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, projectUsage)
		return 2
	}
	sub := args[0]
	args, force := removeBoolArg(args, "--force")
	fs := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	org := fs.String("org", "", "organization (slug or id)")
	db := fs.String("db", "ghosttree.db", "database path (with --force)")
	switch sub {
	case "list", "claim", "move":
	default:
		fmt.Fprintln(stdout, projectUsage)
		return 2
	}
	pos, err := interspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	usage := func() int { fmt.Fprintln(stdout, projectUsage); return 2 }
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usage()
		}
	case "claim":
		if len(pos) > 1 {
			return usage()
		}
	case "move":
		if len(pos) != 1 || *org == "" {
			return usage()
		}
	}
	if force {
		if sub != "move" {
			return usage()
		}
		st, ok := openAccountStore(*db, stdout)
		if !ok {
			return 1
		}
		defer st.Close()
		p, err := st.ForceMoveProject(pos[0], *org)
		if err != nil {
			fmt.Fprintf(stdout, "move project: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s now belongs to %s\n", p.Remote, p.Org)
		return 0
	}
	c, ok := orgClient(stdout)
	if !ok {
		return 1
	}
	switch sub {
	case "list":
		projects, err := c.ListProjects(*org)
		if err != nil {
			return orgFail(stdout, "list projects", err)
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, p := range projects {
			fmt.Fprintf(tw, "%s\t%s\n", p.Remote, p.Org)
		}
		tw.Flush()
	case "claim":
		remote := ""
		if len(pos) == 1 {
			remote = pos[0]
		} else if remote = scope.NormalizeRemote(currentAxes("").Project); remote == "" {
			fmt.Fprintln(stdout, "no remote given and the current directory has no git remote")
			return 2
		}
		p, err := c.ClaimProject(remote, *org)
		if err != nil {
			return orgFail(stdout, "claim project", err)
		}
		fmt.Fprintf(stdout, "%s belongs to %s\n", p.Remote, p.Org)
	case "move":
		p, err := c.MoveProject(pos[0], *org)
		if err != nil {
			return orgFail(stdout, "move project", err)
		}
		fmt.Fprintf(stdout, "%s now belongs to %s\n", p.Remote, p.Org)
	}
	return 0
}
