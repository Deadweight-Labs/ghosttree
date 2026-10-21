package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const embedUsage = `usage: ctx embed status --db <path> [--embed-url <url>] [--embed-query-url <url>]
                       [--embed-model <id>] [--embed-kinds <kinds>]
                       [--embed-search-kinds <kinds>]
                       [--embed-session-window <HH:MM-HH:MM>]

Hybrid search keeps one vector per active knowledge entry and, for the kinds
named in --embed-kinds (env GHOSTTREE_EMBED_KINDS), per request, pain, document,
description of a path and, for sessions, per prompt and per final answer of a
turn. The server computes them in the background once it is started with
--embed-url (env GHOSTTREE_EMBED_URL); see "Embeddings for hybrid search" in
deploy/README.md. The sessions are embedded only in a span of the night
(--embed-session-window, env GHOSTTREE_EMBED_SESSION_WINDOW) and after every
other kind. With --embed-query-url (env GHOSTTREE_EMBED_QUERY_URL) search queries
are embedded by a second server, which must serve the same model.

  status  for each kind how many objects have a current vector, how many are
          due and how many of those are parked, how the background job last
          fared, and whether the embedding server answers (the check embeds one
          short text); with a second server for queries also whether it answers
          and whether both serve the same model. With --embed-kinds and
          --embed-search-kinds (or their environment variables) it also says
          which kind that configuration embeds and searches by. The sessions
          are counted only when --embed-kinds names them: counting them reads
          every text that has no vector yet, which the other kinds do not need.

status reads the database without writing to it, so the server may keep
running. A vector counts as current when it was made by the configured model,
with the current text formats, from the object's text as it is now.`

// embedNow is the clock of the status of the session window; a test replaces it.
var embedNow = time.Now

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
	embedFlagSet.addKinds(fs)
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

	// The other kinds, one line each: the objects that count, how many have a
	// current vector, how many are due, how many of those are parked.
	total := status
	for _, kind := range store.EmbedKinds() {
		if kind == store.KindKnowledge {
			continue
		}
		if kind == store.KindSession && !slices.Contains(cfg.embedKinds(), store.KindSession) {
			// Counting the sessions reads and cleans the line of every text that has no
			// vector yet: a command that took a second before would take a long time on an
			// installation that does not embed sessions at all. They are counted when the
			// configuration names them, and are not part of the total before.
			fmt.Fprintf(stdout, "%-16s not counted: the sessions are not among the embedded kinds (name them in --embed-kinds, for example knowledge,session)%s\n",
				kind, describeSwitches(cfg, kind))
			continue
		}
		ks, err := st.EmbeddingStatus(kind, model)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				// A database that the server has not opened with this version yet
				// (it creates the tables of these kinds when it starts): knowledge
				// above is still right, and each kind says so instead of the whole
				// command failing.
				fmt.Fprintf(stdout, "%-16s tables not created yet; start the server once with this version%s\n", kind, describeSwitches(cfg, kind)+describeWindow(cfg, kind))
				continue
			}
			fmt.Fprintf(stdout, "embed status: %s: %v\n", kind, err)
			return 1
		}
		total.Active += ks.Active
		total.Current += ks.Current
		total.Due += ks.Due
		fmt.Fprintf(stdout, "%-16s %s%s\n", kind, describeKind(ks), describeSwitches(cfg, kind)+describeWindow(cfg, kind))
	}
	if cfg.kindsGiven {
		fmt.Fprintf(stdout, "%-16s %s%s\n", store.KindKnowledge, "(counted above)", describeSwitches(cfg, store.KindKnowledge))
	}
	fmt.Fprintf(stdout, "background job   %s\n", describeJob(total))

	if !cfg.enabled() {
		fmt.Fprintln(stdout, "embedding server not configured (--embed-url or GHOSTTREE_EMBED_URL); this command does not know which server the running ctx serve uses")
		return 0
	}
	if !cfg.separateQuery() {
		fmt.Fprintf(stdout, "embedding server %s: %s\n", cfg.URL, probe(cfg, cfg.URL))
		return 0
	}
	fmt.Fprintf(stdout, "job server       %s: %s\n", cfg.URL, probe(cfg, cfg.URL))
	fmt.Fprintf(stdout, "query server     %s: %s\n", cfg.QueryURL, probe(cfg, cfg.QueryURL))
	fmt.Fprintf(stdout, "same model       %s\n", describeAgreement(cfg))
	return 0
}

// describeKind is the line of one kind: embedded of active, due, parked.
func describeKind(s store.EmbedStatus) string {
	out := fmt.Sprintf("embedded %d of %d, due %d", s.Current, s.Active, s.Due)
	var notes []string
	if s.Parked > 0 {
		notes = append(notes, fmt.Sprintf("%d parked", s.Parked))
	}
	if s.OtherModel > 0 {
		notes = append(notes, fmt.Sprintf("%d of another model or text format", s.OtherModel))
	}
	if len(notes) > 0 {
		out += " (" + strings.Join(notes, ", ") + ")"
	}
	return out
}

// describeSwitches says whether this invocation's configuration embeds a kind
// and uses its vectors in the search; it says nothing when the configuration was
// not given, since the command does not know which one the running server has.
func describeSwitches(cfg embedSettings, kind string) string {
	if !cfg.kindsGiven {
		return ""
	}
	on := func(kinds []string) string {
		if slices.Contains(kinds, kind) {
			return "on"
		}
		return "off"
	}
	return fmt.Sprintf("  [embedding %s, search %s]", on(cfg.embedKinds()), on(cfg.searchKinds()))
}

// describeWindow is the span of the clock in which the sessions are embedded, and
// whether it is open: the sessions are not embedded at any hour, and a count that
// does not move at noon is not a fault. Said for the sessions only.
func describeWindow(cfg embedSettings, kind string) string {
	if kind != store.KindSession {
		return ""
	}
	w := cfg.Job.SessionWindow
	if w == (embedjob.Window{}) {
		w = embedjob.DefaultSessionWindow
	}
	now := embedNow()
	state := "open now"
	if wait := w.Opens(now); wait > 0 {
		state = "opens in " + wait.Round(time.Minute).String()
	}
	return fmt.Sprintf("  [window %s local time of the server, %s]", w, state)
}

// describeAgreement says whether the query server and the job server serve the
// same model, as far as one probe text shows.
func describeAgreement(cfg embedSettings) string {
	ag, err := verifyServers(cfg)
	var diff *embed.DisagreementError
	switch {
	case err == nil:
		return fmt.Sprintf("yes (the same text has a cosine of %.5f, %d dimensions)", ag.Cosine, ag.Dimensions)
	case errors.As(err, &diff):
		return "NO: " + diff.Error() + "; searches answer from full text until the servers agree"
	}
	return "not checked: " + err.Error()
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

// probe checks the server at url the way a search would use it: health, then
// one real query embedding, timed.
func probe(cfg embedSettings, url string) string {
	c, err := embed.New(embed.Config{URL: url, Model: cfg.Model})
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
