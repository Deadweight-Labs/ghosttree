package embedjob

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window is a span of the clock of a day in which the job may embed the sessions,
// in the local time of the server: the texts of all the sessions take three
// nights to catch up on and the embedding server has two cores, which are then
// the only thing the machine does. A window may cross midnight (22:00-04:00).
// Outside it the job leaves the sessions alone; every other kind is not tied to
// it.
type Window struct {
	// Start and End are minutes after midnight, End up to 24:00 (1440). A window
	// with Start after End runs through midnight.
	Start, End int
	// Always is a window that never closes.
	Always bool
}

// DefaultSessionWindow is the window of the sessions when none is given: the
// night, from midnight to six.
var DefaultSessionWindow = Window{Start: 0, End: 6 * 60}

// ParseWindow reads "HH:MM-HH:MM" (24 hours, local time) or "always". The end
// may be 24:00. A window that begins where it ends is not one: it would be empty
// or always, and the one is as unlikely to be wanted as the other is easy to say.
func ParseWindow(s string) (Window, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "always" {
		return Window{Always: true}, nil
	}
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return Window{}, fmt.Errorf("a window is HH:MM-HH:MM (the local time of the server, may cross midnight) or always, got %q", s)
	}
	start, err := parseClock(from, false)
	if err != nil {
		return Window{}, err
	}
	end, err := parseClock(to, true)
	if err != nil {
		return Window{}, err
	}
	if start == end {
		return Window{}, errors.New("a window that begins where it ends is empty; to let the sessions be embedded at any time say always")
	}
	return Window{Start: start, End: end}, nil
}

func parseClock(s string, allow24 bool) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hours, errH := strconv.Atoi(h)
	minutes, errM := strconv.Atoi(m)
	if !ok || errH != nil || errM != nil || hours < 0 || minutes < 0 || minutes > 59 || len(m) != 2 {
		return 0, fmt.Errorf("%q is not a time of day as HH:MM", s)
	}
	if mins := hours*60 + minutes; mins < 24*60 || (allow24 && mins == 24*60) {
		return mins, nil
	}
	return 0, fmt.Errorf("%q is not a time of day as HH:MM", s)
}

// String is the window as ParseWindow reads it.
func (w Window) String() string {
	if w.Always {
		return "always"
	}
	clock := func(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }
	return clock(w.Start) + "-" + clock(w.End)
}

// Contains says whether the clock of t, as it is given (no zone is converted:
// the job passes the local time), is in the window.
func (w Window) Contains(t time.Time) bool {
	if w.Always {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.Start < w.End {
		return m >= w.Start && m < w.End
	}
	return m >= w.Start || m < w.End
}

// Opens is how long it takes from t until the window opens: zero while it is
// open.
func (w Window) Opens(t time.Time) time.Duration {
	if w.Contains(t) {
		return 0
	}
	m := t.Hour()*60 + t.Minute()
	wait := (w.Start - m + 24*60) % (24 * 60)
	return time.Duration(wait)*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond())
}
