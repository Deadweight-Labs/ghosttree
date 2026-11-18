package web

import (
	"net/http"
	"strings"
	"testing"
)

// REQ-711 (WP-R5-9): the small corrections that hang on one component each.

func TestAreaBelowNamesOnlyWhatAddsToThePathOfTheBox(t *testing.T) {
	for _, c := range []struct{ area, path, want string }{
		{"internal/config", "", ""},                                   // under the description of the whole repository (S13)
		{".", "README.md", ""},                                        // "Area: ." at a file
		{"internal/config", "internal/config", ""},                    // the path says it already
		{"internal/config", "cmd", ""},                                // elsewhere
		{"internal/config/x", "internal/config", "internal/config/x"}, // a part of the folder
		{"/internal/config/x/", "internal/config", "internal/config/x"},
	} {
		if got := areaBelow(c.area, c.path); got != c.want {
			t.Errorf("areaBelow(%q, %q) = %q, want %q", c.area, c.path, got, c.want)
		}
	}
}

func TestSearchHitOfAFinishedIssueDoesNotCarryTheSignOfAnOpenOne(t *testing.T) {
	for state, want := range map[string]string{"open": "issue", "done": "done", "dropped": "drop"} {
		if got := requestStateMark(state); got != want {
			t.Errorf("requestStateMark(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWaitDotIsAMarkWithoutFaces(t *testing.T) {
	got := string(waitDot(langEN, []string{"Robin", "Mara"}))
	if !strings.Contains(got, `class="wdot"`) || !strings.Contains(got, "Robin, Mara") || strings.Contains(got, "gav") {
		t.Errorf("the mark in the tab: %s", got)
	}
	if waitDot(langEN, nil) != "" {
		t.Error("nobody waits: nothing is drawn")
	}
}

func TestHostOnlyAndLoopbackAreNotShownInTheAccountMenu(t *testing.T) {
	a := &app{}
	for host, want := range map[string]string{"127.0.0.1:8474": "", "localhost:3000": "", "[::1]:8474": "", "gt.example.test": "gt.example.test", "10.0.0.5:80": "10.0.0.5:80"} {
		r := httptestRequest(host)
		if got := a.publicHost(r); got != want {
			t.Errorf("publicHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func httptestRequest(host string) *http.Request {
	r, _ := http.NewRequest("GET", "http://"+host+"/ui/", nil)
	r.Host = host
	return r
}
