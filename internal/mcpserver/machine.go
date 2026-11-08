package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MachineGetInput is the machine_get tool.
type MachineGetInput struct {
	Name string `json:"name,omitempty" jsonschema:"the machine; omit it to list the machines that concern you (your own, those of your organizations, those shared with them)"`
}

// MachineShareEntryInput is the machine_share_entry tool.
type MachineShareEntryInput struct {
	Kind    string `json:"kind" jsonschema:"knowledge or runbook"`
	ID      int64  `json:"id" jsonschema:"the knowledge entry or runbook with a machine scope"`
	Org     string `json:"org" jsonschema:"the organization (slug) to share it with or take it back from; the machine must already be shared with it by a person"`
	Unshare bool   `json:"unshare,omitempty" jsonschema:"take the sharing back instead of giving it"`
}

func renderMachineLine(m store.Machine) string {
	owner := m.Owner
	if m.OwnerOrg != "" {
		owner = "org " + m.OwnerOrg
	}
	line := fmt.Sprintf("%s | %s | owner: %s", m.Name, m.Relation, owner)
	if len(m.Shares) > 0 {
		var parts []string
		for _, sh := range m.Shares {
			parts = append(parts, sh.Org+" ("+sh.Role+")")
		}
		line += " | shared with: " + strings.Join(parts, ", ")
	}
	return line
}

func renderMachine(m store.Machine) string {
	var b strings.Builder
	b.WriteString(renderMachineLine(m) + "\n")
	for _, sh := range m.Shares {
		fmt.Fprintf(&b, "- %s: %s", sh.Org, sh.Role)
		var hints []string
		if sh.OSUser != "" {
			hints = append(hints, "operating user "+sh.OSUser)
		}
		if sh.PathPrefix != "" {
			hints = append(hints, "paths under "+sh.PathPrefix)
		}
		if len(hints) > 0 {
			b.WriteString(" (" + strings.Join(hints, ", ") + "; a note on what the share is for, not a limit: only entries shared one by one are visible)")
		}
		b.WriteString("\n")
	}
	if len(m.Events) > 0 {
		b.WriteString("latest changes:\n")
		for _, e := range m.Events {
			fmt.Fprintf(&b, "- %s %s %s %s by %s via %s\n", e.CreatedAt, e.Action, e.Subject, e.Detail, e.Actor, e.Via)
		}
	}
	return b.String()
}

func (s *Server) handleMachineGet(_ context.Context, _ *mcp.CallToolRequest, in MachineGetInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Name) == "" {
		list, err := s.client.Machines()
		if err != nil {
			return nil, nil, err
		}
		if len(list) == 0 {
			return textResult("no machines concern this account"), nil, nil
		}
		var b strings.Builder
		for _, m := range list {
			line := renderMachineLine(m)
			if url := s.builder().Machine(m.Name); url != "" {
				line += " " + url
			}
			b.WriteString(line + "\n")
		}
		return textResult(b.String()), nil, nil
	}
	m, err := s.client.MachinePage(in.Name)
	if err != nil {
		return nil, nil, err
	}
	return s.withCard(textResult(renderMachine(m)), s.builder().MachineCard(m)), nil, nil
}

func (s *Server) handleMachineShareEntry(_ context.Context, _ *mcp.CallToolRequest, in MachineShareEntryInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 || strings.TrimSpace(in.Org) == "" {
		return nil, nil, fmt.Errorf("kind, id and org are required")
	}
	if in.Kind != store.EntryKnowledge && in.Kind != store.EntryRunbook {
		return nil, nil, fmt.Errorf("kind must be knowledge or runbook")
	}
	shared, err := s.client.ShareEntry(in.Kind, in.ID, strings.TrimSpace(in.Org), !in.Unshare)
	if err != nil {
		return nil, nil, err
	}
	verb := "shared with"
	if in.Unshare {
		verb = "taken back from"
	}
	msg := fmt.Sprintf("%s #%d %s %s; now shared with: %s", in.Kind, in.ID, verb, in.Org, listOrNone(shared))
	if in.Kind == store.EntryKnowledge {
		if k, err := s.client.KnowledgeByID(in.ID); err == nil {
			return s.withCard(textResult(msg), s.builder().KnowledgeCard(k)), nil, nil
		}
	}
	return textResult(msg), nil, nil
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "nobody (private)"
	}
	return strings.Join(list, ", ")
}

func (s *Server) registerMachineTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "machine_get", Description: "Read a machine: who owns it (a person, or an organization for shared hosts such as build servers), how you stand to it, and which organizations it is shared with and with which role. Without a name it lists the machines that concern you. A machine you have no part in answers like one that does not exist. Entries shared with your organization are found with context_search machine=<name>. Sharing a machine with an organization is done by a person in the web UI; this tool only reads.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleMachineGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "machine_share_entry", Description: "Share one knowledge entry or runbook that has a machine scope with an organization, or take it back. Machine knowledge is private to the machine's owner until it is shared this way, and only into organizations a person has already shared the machine with (web UI). Only the owner of the machine (for organization machines a maintainer) may do it, and only for entries whose author may still vouch for the machine. To write an entry shared from the start, pass share_with_orgs to context_remember.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, IdempotentHint: true, OpenWorldHint: &closed}}, s.handleMachineShareEntry)
}
