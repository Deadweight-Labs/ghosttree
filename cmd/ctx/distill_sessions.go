package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func cmdDistillSessions(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("distill-sessions", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to sqlite database")
	idle := fs.Duration("idle", time.Hour, "minimum session idle time")
	limit := fs.Int("limit", 20, "maximum sessions per run")
	if fs.Parse(args) != nil || *idle <= 0 || *limit <= 0 {
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	cfg, err := llm.LoadConfig()
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	model, err := llm.New(cfg)
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	sessions, err := st.ListSessions(scope.Axes{}, *limit*5)
	if err != nil {
		fmt.Fprintf(stdout, "list sessions: %v\n", err)
		return 1
	}
	cutoff := time.Now().Add(-*idle)
	processed, inserted := 0, 0
	for _, session := range sessions {
		seen, err := time.Parse(time.RFC3339Nano, session.LastSeenAt)
		if err != nil || seen.After(cutoff) {
			continue
		}
		chunks, err := st.ReadSession(session.ID, 0, 5000)
		if err != nil || len(chunks) == 0 {
			continue
		}
		digest := sessiondistill.Digest(chunks)
		exists, err := st.SessionDistillationExists(session.ID, digest)
		if err != nil || exists {
			continue
		}
		existing, err := st.KnowledgeForProject(session.Scope.Project)
		if err != nil {
			fmt.Fprintf(stdout, "session %d knowledge: %v\n", session.ID, err)
			return 1
		}
		titles := make([]string, 0, len(existing))
		for _, k := range existing {
			titles = append(titles, k.Title)
		}
		items, err := sessiondistill.Distill(context.Background(), model, chunks, titles)
		if err != nil {
			fmt.Fprintf(stdout, "session %d distill: %v\n", session.ID, err)
			continue
		}
		n, err := st.ApplySessionDistillation(session.ID, digest, session.Scope, items)
		if err != nil {
			fmt.Fprintf(stdout, "session %d persist: %v\n", session.ID, err)
			continue
		}
		processed++
		inserted += n
		if processed >= *limit {
			break
		}
	}
	fmt.Fprintf(stdout, "distilled %d sessions into %d quarantined knowledge items\n", processed, inserted)
	return 0
}
