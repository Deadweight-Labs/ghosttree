package tui

import (
	"context"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// Backend is everything the TUI asks of the rest of ctx. cmd/ctx implements
// it with the same functions the subcommands use; tests use a fake. The TUI
// itself never reads the config file, never talks HTTP and never sees a token.
type Backend interface {
	// Local is the state on this machine; it must not wait for the network.
	Local() Local
	// CheckServer asks one server whether it is reachable, which version it
	// runs and who the stored login belongs to.
	CheckServer(ctx context.Context, url string) Server

	Resolve(ctx context.Context, input string) (links.Card, error)
	Search(ctx context.Context, line string) ([]Item, error)
	// List is one list of a mode: the project lists answer for the current
	// folder's project, the inbox lists across every visible project.
	List(ctx context.Context, kind ListKind) ([]Item, error)

	// Open opens a web address in the browser of this machine when that is
	// possible (no ssh, a display); why says what stood in the way.
	Open(url string) (opened bool, why string)

	SetChannel(channel string) error
	Doctor(ctx context.Context, fix bool) (DoctorReport, error)
	RestartCollector(ctx context.Context) (string, error)
	Logout(ctx context.Context, server string) error
	StartLogin(ctx context.Context, server, machine string) (LoginStart, error)
	WaitLogin(ctx context.Context, start LoginStart) (LoginResult, error)

	// CheckProject says whether the server knows a project the caller may see.
	CheckProject(ctx context.Context, project string) (bool, error)
	// Projects lists the projects the caller may see.
	Projects(ctx context.Context) ([]ProjectInfo, error)
	// ProjectPreview is the open requests and the caller's open tasks of one
	// project.
	ProjectPreview(ctx context.Context, project string) (requests, tasks []Item, err error)
	// ClaimPlan says which remote would become a project and in which
	// organizations; Claim makes it one (ctx project claim).
	ClaimPlan(ctx context.Context) (ClaimPlan, error)
	Claim(ctx context.Context, remote, org string) (ProjectInfo, error)
	// RecentDirs are folders of this machine where sessions ran, newest first,
	// that still exist; CompleteDir completes a typed path to folders; ChangeDir
	// makes dir the folder the TUI works in.
	RecentDirs(ctx context.Context) ([]string, error)
	CompleteDir(input string) []string
	ChangeDir(dir string) error
}

// ProjectInfo is one project as the server lists it.
type ProjectInfo struct {
	ID    string // remote or <org>/<name>
	Org   string
	Named bool
}

// OrgChoice is an organization a project could be put into.
type OrgChoice struct {
	Slug    string
	Name    string
	Role    string
	Default bool
}

// ClaimPlan is what "create project from this folder" would do.
type ClaimPlan struct {
	Remote string
	Orgs   []OrgChoice
}

// Problem is the kind of trouble a load ran into, in words a person can act
// on, instead of an HTTP status.
type Problem int

const (
	ProblemOther     Problem = iota
	ProblemNoProject         // the folder has no project, or the server does not know it
	ProblemForbidden         // the project exists, the account has no right to it
	ProblemOffline           // the server did not answer
	ProblemSignedOut         // no login, or the server refused it
)

// ProblemError carries a Problem; the backend wraps every failed call in it.
// Detail is a cleaned short text for ProblemOther.
type ProblemError struct {
	Kind   Problem
	Detail string
}

func (e *ProblemError) Error() string {
	switch e.Kind {
	case ProblemNoProject:
		return "no project"
	case ProblemForbidden:
		return "forbidden"
	case ProblemOffline:
		return "server unreachable"
	case ProblemSignedOut:
		return "not signed in"
	}
	return e.Detail
}

// ListKind names a list of the home screen.
type ListKind int

const (
	// Project mode: everything about the current folder's project.
	ListRequests     ListKind = iota // open requests
	ListPains                        // open pains
	ListProjectTasks                 // open human tasks of the project
	ListRoom                         // unread messages in the project's room
	// Inbox mode: what waits for the caller, across projects.
	ListMyTasks     // human tasks for the caller
	ListQuestions   // decisions and questions or approvals for people
	ListUnread      // unread messages in the rooms of visible projects
	ListSuggestions // gardener suggestions waiting for review
	listKindCount
)

// Item is one row of a list or of search results. Ref is a short form that
// Resolve understands (REQ-12, TASK-3, #4295, room:7 ...).
type Item struct {
	Ref   string
	Kind  string
	Title string
	Meta  string
	// Card, when set, is shown as is instead of resolving Ref (messages
	// have no short form of their own).
	Card *links.Card
}

// TokenState says what is known about the stored login, never the token.
type TokenState int

const (
	TokenNone     TokenState = iota // nothing stored
	TokenStored                     // stored, not checked yet
	TokenValid                      // the server accepted it
	TokenRejected                   // the server refused it
)

// Server is one entry of the server list. Today there is one; with server
// profiles (REQ-610) there will be several, and the list takes them as is.
type Server struct {
	Name      string
	URL       string
	Default   bool
	Token     TokenState
	Account   string
	Machine   string
	Checked   bool
	Reachable bool
	Version   string
	Channel   string // the server's own build channel
	Err       string
}

// Collector is the state of the session collector service.
type Collector struct {
	Running bool
	State   string // running, stopped, missing, unknown
	Detail  string
}

// Local is the client-side picture: config, servers, machine, collector.
type Local struct {
	Configured    bool
	ConfigPath    string
	Servers       []Server
	Channel       string // update channel: stable or dev
	Machine       string
	ClientVersion string
	Project       string
	Branch        string
	// Dir is the folder the TUI works in; Git says whether it lies in a git
	// repository, Remote is its normalized origin ("" without one), Linked
	// that the project comes from ctx project link.
	Dir        string
	Git        bool
	Remote     string
	Linked     bool
	Collector  Collector
	ProfileURL string
}

// DefaultServer is the server marked default, or the first one.
func (l Local) DefaultServer() (Server, bool) {
	for _, s := range l.Servers {
		if s.Default {
			return s, true
		}
	}
	if len(l.Servers) > 0 {
		return l.Servers[0], true
	}
	return Server{}, false
}

// DoctorLine is one check of ctx doctor.
type DoctorLine struct {
	Name   string
	Status string // OK, FAIL, UNVERIFIED, or a note
	Detail string
	Fix    string
}

// DoctorReport is the result of ctx doctor (or ctx doctor --fix).
type DoctorReport struct {
	OK    bool
	Lines []DoctorLine
}

// Counts returns how many checks passed, failed and stayed unverified.
func (r DoctorReport) Counts() (ok, fail, unverified int) {
	for _, l := range r.Lines {
		switch l.Status {
		case "OK":
			ok++
		case "FAIL":
			fail++
		case "UNVERIFIED":
			unverified++
		}
	}
	return
}

// LoginStart is a running device login: what the person has to see.
type LoginStart struct {
	Server      string
	Machine     string
	URL         string
	CompleteURL string
	Code        string
	Expires     time.Time
	// Handle belongs to the backend (device code, client); the TUI passes it
	// back to WaitLogin untouched.
	Handle any
}

// LoginResult is a finished login.
type LoginResult struct {
	Account string
	Machine string
	// Note and Warning report what happened to the login on a previous
	// server: revoked, or not confirmed.
	Note    string
	Warning string
}

// MachineTakenError is returned by WaitLogin when the machine name belongs to
// another account; Suggestion is a free name to try.
type MachineTakenError struct {
	Machine    string
	Suggestion string
}

func (e *MachineTakenError) Error() string {
	return "machine name " + e.Machine + " belongs to another account"
}
