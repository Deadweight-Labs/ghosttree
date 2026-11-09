package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ui-md.js reads texts that anyone in a room can write, in the browser of every
// reader. testdata/uimd_test.js feeds it the inputs that once held a tab for
// seconds to minutes and holds each to a time limit, and checks that the text
// of the sample with every element is drawn as it was before the fix
// (testdata/uimd_sample.html). It needs node and is skipped without it.
func TestUiMdStaysFastAndUnchanged(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "ui-md.js")
	if err := os.WriteFile(script, mustReadEmbedded(t, "static/ui-md.js"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join(dir, "uimd_test.js"), script,
		filepath.Join(dir, "uimd_sample.md"), filepath.Join(dir, "uimd_sample.html")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
