// Package links builds the canonical web addresses of ghosttree objects and the
// compact cards that go with them (REQ-603). Addresses follow section 3 of the
// document "oberflaeche-nach-github": the project is owner/repo, with the host
// as first segment for hosts other than github.com.
//
// An address carries no secret and no login. A browser that is not signed in is
// sent through the normal sign-in and returns to exactly this page.
package links

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Builder makes addresses for one server and one project. The zero value
// makes none; Project may be empty, then the addresses that need a project
// are empty.
type Builder struct {
	server  string
	project string
}

var segmentRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// New takes the server's base address (scheme and host, a path prefix is kept)
// and a project remote in any spelling. A server address with userinfo, query
// or fragment loses them: nothing that could carry a secret enters a link.
// A server that is not http(s) gives a Builder without addresses.
func New(server, project string) Builder {
	b := Builder{project: projectPath(project)}
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return b
	}
	b.server = u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/")
	return b
}

// projectPath turns a remote into the path segments of the interface:
// "git@github.com:Owner/Repo.git" gives "owner/repo", another host keeps its
// name as the first segment. Anything that is not host/owner/repo is empty.
func projectPath(remote string) string {
	n := scope.NormalizeRemote(remote)
	if n == "" {
		return ""
	}
	parts := strings.Split(n, "/")
	if len(parts) != 3 {
		return ""
	}
	for _, p := range parts {
		if !segmentRE.MatchString(p) {
			return ""
		}
	}
	if parts[0] == "github.com" {
		return parts[1] + "/" + parts[2]
	}
	return n
}

// Valid says whether the Builder can make addresses at all.
func (b Builder) Valid() bool { return b.server != "" }

// HasProject says whether project-bound addresses are available.
func (b Builder) HasProject() bool { return b.server != "" && b.project != "" }

// WithProject returns the Builder for another project on the same server. An
// empty or malformed project keeps the one the Builder has: an object without
// a project of its own (global knowledge) is shown in the project asked from.
func (b Builder) WithProject(project string) Builder {
	if p := projectPath(project); p != "" {
		b.project = p
	}
	return b
}

func (b Builder) root() string { return b.server + "/ui" }

func (b Builder) repo(rest string) string {
	if !b.HasProject() {
		return ""
	}
	return b.root() + "/" + b.project + rest
}

func esc(s string) string { return url.PathEscape(s) }

// escPath escapes each segment and keeps the slashes: branches and file paths.
func escPath(s string) string {
	parts := strings.Split(strings.Trim(s, "/"), "/")
	for i, p := range parts {
		parts[i] = esc(p)
	}
	return strings.Join(parts, "/")
}

func num(n int64) string { return strconv.FormatInt(n, 10) }

// Inbox is the inbox page.
func (b Builder) Inbox() string {
	if !b.Valid() {
		return ""
	}
	return b.root() + "/inbox"
}

// Repo is the project's start page (tab Code on the default branch).
func (b Builder) Repo() string { return b.repo("") }

// Search is the global search with the qualifier language of REQ-600.
func (b Builder) Search(q string) string {
	if !b.Valid() {
		return ""
	}
	return b.root() + "/search?q=" + url.QueryEscape(q)
}

// Issues is the issue list of the project; q is the search text, may be empty.
func (b Builder) Issues(q string) string {
	if q == "" {
		return b.repo("/issues")
	}
	return b.repo("/issues?q=" + url.QueryEscape(q))
}

// Issue is REQ-n.
func (b Builder) Issue(n int64) string { return b.repo("/issues/" + num(n)) }

// Criterion is one requirement of an issue, by its number within the issue.
func (b Builder) Criterion(requestID int64, number int) string {
	u := b.Issue(requestID)
	if u == "" {
		return ""
	}
	return u + "#criterion-" + strconv.Itoa(number)
}

// Task is TASK-n. It needs no project: the page leads to the anchor in the
// inbox or at the issue.
func (b Builder) Task(n int64) string {
	if !b.Valid() {
		return ""
	}
	return b.root() + "/tasks/" + num(n)
}

// Pain is PAIN-n.
func (b Builder) Pain(n int64) string { return b.repo("/pains/" + num(n)) }

// Idea is IDEA-n. The schema has no idea page yet; this is the address the
// interface is asked to serve.
func (b Builder) Idea(n int64) string { return b.repo("/ideas/" + num(n)) }

// Decision is DEC-n, an open decision of an idea or an issue. It has no page of
// its own in the schema yet; this is the address the interface is asked to
// serve (the page of its idea or issue, anchored at the decision).
func (b Builder) Decision(n int64) string { return b.repo("/decisions/" + num(n)) }

// Knowledge is the entry #id.
func (b Builder) Knowledge(id int64) string { return b.repo("/knowledge/" + num(id)) }

// Document is a document of the worktree, by slug. The schema has no document
// page yet; this is the address the interface is asked to serve.
func (b Builder) Document(slug string) string { return b.repo("/docs/" + esc(slug)) }

// Room is a room thread; a message id adds an anchor to the message.
func (b Builder) Room(thread, message int64) string {
	u := b.repo("/rooms/" + num(thread))
	if u == "" || message == 0 {
		return u
	}
	return u + "#message-" + num(message)
}

// RoomChat is a room by its key: the page that shows its messages. A room
// message has no page of its own; the link leads to the room it was said in.
func (b Builder) RoomChat(roomKey string) string {
	if !b.Valid() || strings.TrimSpace(roomKey) == "" {
		return ""
	}
	return b.root() + "/coord?room=" + url.QueryEscape(roomKey)
}

// Session is a session, by its public address or its number.
func (b Builder) Session(id string) string { return b.repo("/sessions/" + esc(id)) }

// Commit is a commit by (a prefix of) its hash.
func (b Builder) Commit(sha string) string { return b.repo("/commit/" + esc(sha)) }

// Pull is the pull request #n.
func (b Builder) Pull(n int64) string { return b.repo("/pulls/" + num(n)) }

// PullChecklist is the checklist tab of a pull request (the work package).
func (b Builder) PullChecklist(n int64) string { return b.repo("/pulls/" + num(n) + "/checkliste") }

// Package is the work package of a branch that has no pull request.
func (b Builder) Package(branch string) string { return b.repo("/package/" + escPath(branch)) }

// Runbook is a runbook by name.
func (b Builder) Runbook(name string) string { return b.repo("/runbooks/" + esc(name)) }

// Blob is a file on a branch; Tree is a folder.
func (b Builder) Blob(branch, path string) string {
	return b.repo("/blob/" + escPath(branch) + "/" + escPath(path))
}

func (b Builder) Tree(branch, path string) string {
	u := b.repo("/tree/" + escPath(branch))
	if strings.Trim(path, "/") == "" {
		return u
	}
	return u + "/" + escPath(path)
}

// PresetScope maps the stored scope (user, project, org) to the one of the
// address (me, repo, org). An unknown scope is empty.
func PresetScope(stored string) string {
	switch stored {
	case "user", "me":
		return "me"
	case "project", "repo", "builtin":
		// A built-in search belongs to no repository of its own; it runs in the
		// one it is opened from.
		return "repo"
	case "org":
		return "org"
	}
	return ""
}

// Preset is a saved search; scope is me, repo or org, args fill its slots by
// name ({pr}, {branch}, ...). The address names no repository for scope repo,
// so the project goes along as the query parameter project=owner/repo.
func (b Builder) Preset(scope, name string, args map[string]string) string {
	if !b.Valid() || PresetScope(scope) == "" {
		return ""
	}
	v := url.Values{}
	if PresetScope(scope) == "repo" && b.project != "" {
		v.Set("project", b.project)
	}
	for k, val := range args {
		v.Set(k, val)
	}
	u := b.root() + "/presets/" + PresetScope(scope) + "/" + esc(name)
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	return u
}
