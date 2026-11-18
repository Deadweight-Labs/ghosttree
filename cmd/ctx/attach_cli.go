package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// attachToObject is ctx pain attach and ctx request attach (REQ-671): the image
// goes to the pain or the issue itself, the same way ctx idea attach sends one to an
// idea. rest is "<id> <file> [--alt TEXT]"; prefix is the key of the number (PAIN-,
// REQ-), subject and owner are the same word (pain, request). It returns false when
// the arguments do not fit, for the caller to print its usage.
func attachToObject(c *client.Client, cfg config.Config, stdout io.Writer, jsonMode bool, subject, prefix string, rest []string) (int, bool) {
	pos, flags := splitLeading(rest)
	if len(pos) != 2 {
		return 0, false
	}
	id, ok := pain.ParseNumberRef(pos[0], prefix)
	if !ok {
		return 0, false
	}
	fs := flag.NewFlagSet(subject+" attach", flag.ContinueOnError)
	fs.SetOutput(stdout)
	alt := fs.String("alt", "", "what the image shows")
	if fs.Parse(flags) != nil || fs.NArg() != 0 {
		return 0, false
	}
	data, err := readImageArg(pos[1])
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err), true
	}
	m, err := c.Attach(client.AttachRequest{Subject: subject, ID: id, Owner: subject, Data: data, Alt: *alt, SessionID: cliSessionID(c, cfg)})
	if err != nil || jsonMode {
		return writeRequestCLIResult(stdout, jsonMode, m, nil, err), true
	}
	fmt.Fprintf(stdout, "ATT-%d %s %dx%d %s\n", m.ID, m.Mime, m.Width, m.Height, m.URL)
	return 0, true
}
