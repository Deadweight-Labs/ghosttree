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
	MyTasks(ctx context.Context) ([]Item, error)
	OpenRequests(ctx context.Context) ([]Item, error)
	Messages(ctx context.Context) ([]Item, error)

	// Open opens a web address in the browser of this machine when that is
	// possible (no ssh, a display); why says what stood in the way.
	Open(url string) (opened bool, why string)

	SetChannel(channel string) error
	Doctor(ctx context.Context, fix bool) (DoctorReport, error)
	RestartCollector(ctx context.Context) (string, error)
	Logout(ctx context.Context, server string) error
	StartLogin(ctx context.Context, server, machine string) (LoginStart, error)
	WaitLogin(ctx context.Context, start LoginStart) (LoginResult, error)
}

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
	Collector     Collector
	ProfileURL    string
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
