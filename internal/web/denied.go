package web

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A project the viewer may not open answers in two ways. For a stranger it is as
// unknown as a project that does not exist: nothing on the page tells them it is
// there. For a member of the organization the project belongs to, hiding it would
// only make them guess, so the page says which project it is, why they cannot
// open it and whom to ask (403). The handlers keep answering 404; the page writer
// (errorpages.go) turns the answer into the 403 page when the address names such a
// project.

type denialKey struct{}

// denial is what the 403 page of a project says.
type denial struct {
	Project string
	// Name is the project as people read it: owner/name, without the host of GitHub.
	Name   string
	Org    string
	OrgURL string
	Shared bool
	Owners []string
}

func denialFrom(r *http.Request) *denial {
	d, _ := r.Context().Value(denialKey{}).(*denial)
	return d
}

// deniedProjectName is how a project is called to people: owner/name, github.com left out.
func deniedProjectName(remote string) string {
	rest, _ := strings.CutPrefix(remote, repoHost+"/")
	return rest
}

// denialFor explains why the viewer may not open the project, or returns nil when
// the project is not the viewer's to know about: unknown, of another organization,
// or one they may open after all. Only a full member of the owning organization
// gets an answer; a guest sees only the project it was invited to.
func (a *app) denialFor(r *http.Request, remote string) *denial {
	p, ok := a.store.ProjectByRemote(remote)
	if !ok {
		return nil
	}
	kind := a.store.OrgViewerKind(p.OrgID, browserPrincipal(r).ID)
	if kind != store.OrgViewerOwner && kind != store.OrgViewerMember {
		return nil
	}
	if a.access(r).Role(p.Remote).Role != "" {
		return nil
	}
	d := &denial{Project: p.Remote, Name: deniedProjectName(p.Remote), Org: p.Org, OrgURL: "/ui/" + pathEscapeAll(p.Org), Shared: p.Shared}
	if org, err := a.store.OrgByRef(p.Org); err == nil {
		if members, err := a.store.ListOrgMembers(org.ID); err == nil {
			for _, m := range members {
				if store.IsOrgOwnerRole(m.Role) && !m.Guest && m.Kind != store.AccountKindService {
					d.Owners = append(d.Owners, m.Account)
				}
			}
		}
		if org.Name != "" {
			d.Org = org.Name
		}
	}
	return d
}

// addressProject is the project an address names: /ui/{org}/{repo}/…, the
// host form, the form without a host, or a room (/ui/coord?room=project:…). The
// candidates come in the order the address reads them.
func addressProjects(r *http.Request) []string {
	if strings.HasPrefix(r.URL.Path, "/ui/coord") {
		if rest, ok := strings.CutPrefix(r.URL.Query().Get("room"), "project:"); ok && rest != "" {
			return []string{scope.NormalizeRemote(rest)}
		}
		return nil
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/ui/")
	if !ok {
		return nil
	}
	segs := strings.Split(strings.Trim(rest, "/"), "/")
	if reservedSegment(segs[0]) {
		return nil
	}
	remote, _, ok := parseRepoPath(segs)
	if !ok {
		return nil
	}
	out := []string{remote}
	if id := hostlessFallback(segs); id != "" {
		out = append(out, id)
	}
	return out
}

// deniedProject is the denial for the project an address names, nil when there is
// none to explain.
func (a *app) deniedProject(r *http.Request) *denial {
	opts, _ := a.projectOptions(r, a.access(r))
	candidates := addressProjects(r)
	// A project the viewer can open is the one the address means.
	for _, c := range candidates {
		if slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == c }) {
			return nil
		}
	}
	for _, c := range candidates {
		if d := a.denialFor(r, c); d != nil {
			return d
		}
	}
	return nil
}

func withDenial(r *http.Request, d *denial) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), denialKey{}, d))
}

// deniedView words the 403 page of a project.
func (a *app) deniedView(r *http.Request, d *denial) *errorView {
	l := a.langFor(r)
	reason := l.msg("err.denied.private", d.Org)
	if d.Shared {
		reason = l.msg("err.denied.shared", d.Org)
	}
	text := reason
	if len(d.Owners) > 0 {
		text += " " + l.msg("err.denied.owners", strings.Join(d.Owners, ", "))
	}
	v := &errorView{Code: codeText(http.StatusForbidden), Icon: "lock", Title: l.msg("err.denied.title", d.Name), Text: text,
		BackHref: "/ui/inbox", BackLabel: l.msg("err.back_inbox"), AltHref: d.OrgURL, AltLabel: l.msg("err.denied.org", d.Org)}
	return v
}

// deniedText is the one line a form or a room shows when a write is refused for
// the same reason.
func (a *app) deniedText(r *http.Request, d *denial) string {
	l := a.langFor(r)
	text := l.msg("err.denied.title", d.Name) + ". " + l.msg("err.denied.private", d.Org)
	if d.Shared {
		text = l.msg("err.denied.title", d.Name) + ". " + l.msg("err.denied.shared", d.Org)
	}
	if len(d.Owners) > 0 {
		text += " " + l.msg("err.denied.owners", strings.Join(d.Owners, ", "))
	}
	return text
}

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("err.denied.title", "No access to %s", "Kein Zugriff auf %s")
	add("err.denied.private", "The project is private. You belong to %s, but you have no role in this project.", "Das Projekt ist privat. Du gehörst zu %s, hast aber keine Rolle in diesem Projekt.")
	add("err.denied.shared", "The project is shared with %s, but your account may not use it.", "Das Projekt ist mit %s geteilt, dein Konto darf es aber nicht nutzen.")
	add("err.denied.owners", "Ask %s for a role.", "Frag %s nach einer Rolle.")
	add("err.denied.org", "To %s", "Zu %s")
}

// projectShort is the short name of a project in a list: its last segment, with the
// owner in front when two projects the viewer can see end alike (the project of an
// organization and a repository on GitHub that are both called deploy-hook).
func (a *app) projectShort(r *http.Request, remote string) string {
	for _, p := range a.shellBaseFor(r).projects {
		if p.Remote == remote {
			return p.Label
		}
	}
	return lastSegment(remote)
}
