// Command sessexp is the measurement harness of REQ-538 part B: would hybrid
// session search profit from embedding the user prompts and the final answer of
// each turn? It is an experiment; nothing here is meant to be merged.
//
// Every subcommand reads and writes files or standard streams, so that the
// steps that need the production database (prep, fts) can run as the database
// user while the others run as the owner of the scratch directory.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: sessexp <command> [flags]

  prep-items    -db <path> [-min-runes 20]      all prompts and final answers, JSONL on stdout, aggregates on stderr
  prep-queries  -db <path> [-n 300] [-seed N]   knowledge titles with evidence sessions, JSONL on stdout
  prep-sessions -db <path>                      session metadata (id, harness, title), JSONL on stdout
  pick-own      -sessions f -items f [-n 70]    candidate sessions for hand-written questions
  plan          -items f -queries f -own f ...  the order in which sessions are embedded
  embed         -url u -items f -plan f -out f  embed documents and queries in small batches
  fts           -db <path> < queries            production session search, JSONL on stdout
  score         ...                             R@k and MRR@10 with bootstrap intervals
  latency       ...                             brute-force scan time over the projected stock
  space         ...                             bytes per vector in a scratch SQLite table
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "prep-items":
		err = cmdPrepItems(args)
	case "prep-queries":
		err = cmdPrepQueries(args)
	case "prep-sessions":
		err = cmdPrepSessions(args)
	case "pick-own":
		err = cmdPickOwn(args)
	case "stats":
		err = cmdStats(args)
	case "plan":
		err = cmdPlan(args)
	case "embed":
		err = cmdEmbed(args)
	case "fts":
		err = cmdFTS(args)
	case "score":
		err = cmdScore(args)
	case "latency":
		err = cmdLatency(args)
	case "space":
		err = cmdSpace(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessexp:", err)
		os.Exit(1)
	}
}
