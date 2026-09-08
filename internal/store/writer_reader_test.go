package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestRuntimeCoreReadsContinueDuringWriteTransaction(t *testing.T) {
	s := runtimeDomainStore(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "seed.go", Description: "committed"}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateDocument(Document{Project: "p", Slug: "seed", Kind: "spec", Title: "Seed"}, "committed", "first")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "committed", Scope: scope.Axes{Project: "p"}}, IdempotencyKey: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := s.UpsertSession(Session{Harness: "test", ExternalID: "read", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(sid, []Chunk{{Seq: 1, Text: "committed", Raw: "raw"}}); err != nil {
		t.Fatal(err)
	}
	commit := holdRuntimeTransaction(t, s, `UPDATE ghost_files SET description='pending' WHERE project='p' AND path='seed.go'`)
	for name, read := range map[string]func() error{
		"ghost": func() error {
			g, e := s.GhostFileByPath("p", "seed.go")
			if e == nil && g.Description != "committed" {
				return fmt.Errorf("uncommitted ghost: %s", g.Description)
			}
			return e
		},
		"ghost_fts": func() error {
			gs, e := s.SearchGhostFiles("committed", "p", 10)
			if e == nil && len(gs) != 1 {
				return fmt.Errorf("FTS count=%d", len(gs))
			}
			return e
		},
		"archive_preview": func() error { _, e := s.PrepareGhostArchive("p", "seed.go"); return e },
		"document_revision": func() error {
			r, e := s.DocumentRevision(doc.ID, 1)
			if e == nil && r.Body != "committed" {
				return fmt.Errorf("revision=%s", r.Body)
			}
			return e
		},
		"request": func() error {
			r, e := s.RequestByID(req.Request.ID)
			if e == nil && r.Request.Title != "committed" {
				return fmt.Errorf("request=%s", r.Request.Title)
			}
			return e
		},
		"session": func() error {
			chunks, e := s.ReadSession(sid, 0, 10)
			if e == nil && (len(chunks) != 1 || chunks[0].Text != "committed") {
				return fmt.Errorf("chunks=%+v", chunks)
			}
			return e
		},
	} {
		t.Run(name, func(t *testing.T) { assertRuntimeReadPrompt(t, read) })
	}
	commit()
	g, err := s.GhostFileByPath("p", "seed.go")
	if err != nil || g.Description != "pending" {
		t.Fatalf("new commit invisible: %+v %v", g, err)
	}
}

func assertRuntimeReadPrompt(t *testing.T, read func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("read blocked behind writable connection")
	}
}

func holdRuntimeTransaction(t *testing.T, s *Store, sql string) func() {
	t.Helper()
	entered := make(chan error, 1)
	release := make(chan struct{})
	r, err := s.writer.admit(context.Background(), 0, func() error {
		tx, err := s.db.Begin()
		if err != nil {
			entered <- err
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(sql); err != nil {
			entered <- err
			return err
		}
		entered <- nil
		<-release
		return tx.Commit()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-entered; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	commit := func() {
		once.Do(func() {
			close(release)
			if err := <-r.done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(commit)
	return commit
}

func TestRuntimeRemainingPureReadsContinueDuringWriteTransaction(t *testing.T) {
	s := runtimeDomainStore(t)
	axes := scope.Axes{Project: "p"}
	kid, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "committed", Body: "body", Scope: axes})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.AddPerson("reader")
	if err != nil {
		t.Fatal(err)
	}
	in := snapshotCreateInput()
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil); err != nil {
		t.Fatal(err)
	}
	commit := holdRuntimeTransaction(t, s, `UPDATE knowledge SET title='pending' WHERE project='p'`)
	for name, read := range map[string]func() error{
		"knowledge": func() error {
			k, e := s.KnowledgeByID(kid)
			if e == nil && k.Title != "committed" {
				return fmt.Errorf("title=%s", k.Title)
			}
			return e
		},
		"operator_fts": func() error {
			ks, e := s.SearchAllKnowledge("committed", axes, 10)
			if e == nil && len(ks) != 1 {
				return fmt.Errorf("FTS count=%d", len(ks))
			}
			return e
		},
		"preview": func() error { _, e := s.KnowledgeForActivatedPreview(axes, activation.Context{}); return e },
		"usage": func() error {
			hits, _, e := s.KnowledgeUsage(kid)
			if e == nil && hits != 0 {
				return fmt.Errorf("preview counted use=%d", hits)
			}
			return e
		},
		"authentication": func() error {
			name, ok := s.Authenticate(token)
			if !ok || name != "reader" {
				return fmt.Errorf("authentication=%q %v", name, ok)
			}
			return nil
		},
		"snapshot":     func() error { _, _, e := s.ContextSnapshot(context.Background(), "p", in.Name); return e },
		"migration":    func() error { _, e := s.CompletedMigrationArtifacts("p"); return e },
		"distillation": func() error { _, e := s.OpenDistillBatches(); return e },
		"cost":         func() error { _, e := s.BilledTranscriptChars(""); return e },
		"evidence":     func() error { _, e := s.EvidenceFor(kid); return e },
		"regression":   func() error { _, _, e := s.RegressionGaps(axes); return e },
	} {
		t.Run(name, func(t *testing.T) { assertRuntimeReadPrompt(t, read) })
	}
	commit()
}
