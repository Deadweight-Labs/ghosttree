package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
)

const exportUsage = `usage: ctx export <session-id> [-o file]

Write a session's original JSONL transcript to stdout or a file. The harnesses
expire their own transcripts, so this is the long-term copy.`

// splitLeadingOperand removes the first non-flag argument and returns the
// remaining arguments together with it.
func splitLeadingOperand(args []string) ([]string, string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return append(append([]string{}, args[:i]...), args[i+1:]...), a
		}
	}
	return args, ""
}

func cmdExport(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stdout)
	out := fs.String("o", "", "write to this file instead of stdout")
	fs.Usage = func() { fmt.Fprintln(stdout, exportUsage) }
	// flag stops at the first positional argument, so pull the session id out
	// first and let the flags follow it, the way the usage line reads.
	rest, idArg := splitLeadingOperand(args)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if idArg == "" || fs.NArg() != 0 {
		fmt.Fprintln(stdout, exportUsage)
		return 2
	}
	id, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil {
		fmt.Fprintf(stdout, "not a session id: %q\n%s\n", idArg, exportUsage)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	raw, err := client.New(cfg).RawSession(id)
	if err != nil {
		fmt.Fprintf(stdout, "export failed: %v\n", err)
		return 1
	}
	if raw == "" {
		fmt.Fprintf(stdout, "session %d has no stored lines\n", id)
		return 1
	}
	if *out == "" {
		fmt.Fprint(stdout, raw)
		return 0
	}
	if err := privatefile.Write(*out, []byte(raw)); err != nil {
		fmt.Fprintf(stdout, "write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}
