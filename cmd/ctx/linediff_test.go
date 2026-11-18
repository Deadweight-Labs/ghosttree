package main

import (
	"bytes"
	"strings"
	"testing"
)

// N-F (counter-check of run 5): the difference shown after a refused push marks
// the lines that changed, not the whole file, and a file with Windows line ends
// does not differ from the same file with Unix line ends.
func TestLineDiffMarksOnlyTheChangedLine(t *testing.T) {
	base := "---\non: pr.merged\nmonth_usd: 5\nuses: ghosttree/echo\n---\n"
	server := "---\non: pr.merged\nmonth_usd: 10\nuses: ghosttree/echo\n---\n"
	var out bytes.Buffer
	printLineDiff(&out, "base (v4)", "server (v5)", base, server)
	got := out.String()
	for _, want := range []string{"-month_usd: 5\n", "+month_usd: 10\n", " on: pr.merged\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff lacks %q:\n%s", want, got)
		}
	}
	if n := marked(got); n != 2 {
		t.Fatalf("%d lines marked, want the 2 that changed:\n%s", n, got)
	}
	out.Reset()
	printLineDiff(&out, "a", "b", strings.ReplaceAll(base, "\n", "\r\n"), base)
	if n := marked(out.String()); n != 0 {
		t.Fatalf("a change of the line ends alone shows as a difference:\n%s", out.String())
	}
}

func TestLineDiffLeavesOutFarUnchangedLines(t *testing.T) {
	var a, b []string
	for i := 0; i < 30; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[15] = "changed"
	var out bytes.Buffer
	printLineDiff(&out, "a", "b", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	if !strings.Contains(out.String(), "...\n") || strings.Count(out.String(), "\n") > 14 {
		t.Fatalf("far unchanged lines are shown:\n%s", out.String())
	}
}

// marked counts the lines of a diff that start with - or +, without the two
// header lines.
func marked(diff string) int {
	n := 0
	for _, l := range strings.Split(diff, "\n")[2:] {
		if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
			n++
		}
	}
	return n
}
