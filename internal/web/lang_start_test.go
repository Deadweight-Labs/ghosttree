package web

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// ctx is one binary: every hook and every command loads this package, and only
// the server shows a page. Parsing the templates of both languages while the
// package loads took 11 of the 14.5 ms a ctx process needed to start, with
// about 172,000 allocations; without it the package makes about 11,000.
//
// The runtime reports what the initialisation of a package allocated, so this
// starts the test binary once more without running a test and reads that line.
// The count does not depend on the machine, the time would.
func TestLoadingThePackageParsesNoTemplate(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start the test binary: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^init \S+/internal/web @.*, (\d+) allocs$`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("the runtime did not report the initialisation of the package:\n%s", out)
	}
	allocs, _ := strconv.Atoi(string(m[1]))
	if allocs > 50000 {
		t.Fatalf("loading the package made %d allocations; it parses templates or builds something else a hook never uses", allocs)
	}
	if langEN.page() == nil || langDE.page() == nil {
		t.Fatal("a language has no pages")
	}
}
