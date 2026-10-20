package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const embedUsage = `usage: ctx embed status --db <path> [--embed-url <url>] [--embed-model <id>]

Hybrid search keeps one vector per active knowledge entry. The server computes
them in the background once it is started with --embed-url (env
GHOSTTREE_EMBED_URL); see "Embeddings for hybrid search" in deploy/README.md.

  status  how many entries have a current vector, how many are due, how the
          background job last fared, and whether the embedding server answers
          (the check embeds one short text)

status reads the database without writing to it, so the server may keep
running. A vector counts as current when it was made by the configured model,
with the current text formats, from the entry's text as it is now.`

func cmdEmbed(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, embedUsage)
		return 2
	}
	switch args[0] {
	case "status":
		return cmdEmbedStatus(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, embedUsage)
		return 0
	}
	fmt.Fprintf(stdout, "unknown embed command %q\n%s\n", args[0], embedUsage)
	return 2
}

func cmdEmbedStatus(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("embed status", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	embedFlagSet := addEmbedFlags(fs, false)
	fs.Usage = func() { fmt.Fprintln(stdout, embedUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 {
		fmt.Fprintln(stdout, embedUsage)
		return 2
	}
	cfg, err := embedFlagSet.resolve()
	if err != nil {
		fmt.Fprintf(stdout, "embed status: %v\n", err)
		return 2
	}
	st, err := store.OpenReadOnly(*dbPath, 1)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	model := store.EmbedModel{Model: cfg.Model, PrefixVersion: embed.PrefixVersion}
	status, err := st.KnowledgeEmbeddingStatus(model)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			fmt.Fprintln(stdout, "The database has no embedding tables yet. Start the server once with this version; it creates them.")
			return 1
		}
		fmt.Fprintf(stdout, "embed status: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "model            %s (text formats %s)\n", model.Model, model.PrefixVersion)
	fmt.Fprintf(stdout, "active entries   %d\n", status.Active)
	fmt.Fprintf(stdout, "embedded         %d\n", status.Current)
	due := fmt.Sprintf("%d", status.Due)
	if status.OtherModel > 0 {
		due += fmt.Sprintf(" (%d have a vector of another model or text format)", status.OtherModel)
	}
	if status.Parked > 0 {
		due += fmt.Sprintf(" (%d parked: the embedding server fails on or refuses their text, see embed_entry_parked in the log)", status.Parked)
	}
	fmt.Fprintf(stdout, "due              %s\n", due)
	fmt.Fprintf(stdout, "background job   %s\n", describeJob(status))

	if !cfg.enabled() {
		fmt.Fprintln(stdout, "embedding server not configured (--embed-url or GHOSTTREE_EMBED_URL); this command does not know which server the running ctx serve uses")
		return 0
	}
	fmt.Fprintf(stdout, "embedding server %s: %s\n", cfg.URL, probe(cfg))
	return 0
}

func describeJob(s store.EmbedStatus) string {
	j := s.Job
	switch {
	case j.Failures > 0:
		out := fmt.Sprintf("failing since %s, %d in a row: %s", orNever(j.LastErrorAt), j.Failures, j.LastError)
		if j.LastOKAt != "" {
			out += "; last progress " + j.LastOKAt
		}
		return out
	case j.LastOKAt != "":
		return "last progress " + j.LastOKAt
	case s.Current == 0 && s.Due == 0:
		return "nothing to do"
	}
	return "no progress recorded yet"
}

func orNever(s string) string {
	if s == "" {
		return "an unknown time"
	}
	return s
}

// probe checks the server the way a search would use it: health, then one real
// query embedding, timed.
func probe(cfg embedSettings) string {
	c, err := cfg.client()
	if err != nil {
		return err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Health(ctx); err != nil {
		return "not ready: " + err.Error()
	}
	started := time.Now()
	v, err := c.Query(ctx, "status check")
	if err != nil {
		return "ready, but embedding failed: " + err.Error()
	}
	return fmt.Sprintf("ready, one query took %d ms, %d dimensions", time.Since(started).Milliseconds(), len(v))
}
