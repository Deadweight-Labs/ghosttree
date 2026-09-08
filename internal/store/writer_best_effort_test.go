package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestRuntimeKnowledgeReadsDoNotWaitForBookkeeping(t *testing.T) {
	s := runtimeDomainStore(t)
	axes := scope.Axes{Project: "p"}
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "Spectrometer calibration", Body: "body", Scope: axes})
	if err != nil {
		t.Fatal(err)
	}
	commit := holdRuntimeTransaction(t, s, `UPDATE knowledge SET body='pending' WHERE project='p'`)
	for name, read := range map[string]func() error{
		"delivery": func() error {
			ks, e := s.KnowledgeForContext(axes)
			if e == nil && len(ks) != 1 {
				return fmt.Errorf("delivery count=%d", len(ks))
			}
			return e
		},
		"activated_delivery": func() error { _, e := s.KnowledgeForActivatedContext(axes, activation.Context{}); return e },
		"search": func() error {
			ks, e := s.SearchKnowledge("Spectrometer", axes, 10)
			if e == nil && len(ks) != 1 {
				return fmt.Errorf("search count=%d", len(ks))
			}
			return e
		},
		"context_search": func() error { _, e := s.SearchKnowledgeForContext("Spectrometer", axes, 10); return e },
		"relevance":      func() error { _, e := s.RelevantKnowledge("Spectrometer", axes, 10); return e },
		"machine":        func() error { s.TouchMachine("reader"); return nil },
	} {
		t.Run(name, func(t *testing.T) { assertRuntimeReadPrompt(t, read) })
	}
	commit()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithOptions(s.path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	hits, _, err := reopened.KnowledgeUsage(id)
	if err != nil || hits < 4 {
		t.Fatalf("drained usage=%d %v", hits, err)
	}
	var machines int
	if err := reopened.db.QueryRow("SELECT count(*) FROM machines WHERE hostname='reader'").Scan(&machines); err != nil || machines != 1 {
		t.Fatalf("drained machine=%d %v", machines, err)
	}
}

func TestRuntimeBookkeepingCoalescesBehindDomainWork(t *testing.T) {
	s := runtimeDomainStore(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "seed", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	for range 3 {
		s.recordKnowledgeUse([]Knowledge{{ID: id}, {ID: id}})
	}
	for range 2 {
		s.recordKnowledgeSearchHit([]Knowledge{{ID: id}})
	}
	s.TouchMachine("same")
	s.TouchMachine("same")
	var before int
	if err := s.db.QueryRow("SELECT hit_count FROM knowledge WHERE id=?", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Errorf("bookkeeping bypassed writer: hits=%d", before)
	}
	s.writer.mu.Lock()
	s.writer.cfg.MaxOperations = 2
	s.writer.mu.Unlock()
	domain, err := s.writer.admit(context.Background(), 0, func() error {
		var hits int
		if err := s.db.QueryRow("SELECT hit_count FROM knowledge WHERE id=?", id).Scan(&hits); err != nil {
			return err
		}
		if hits != 0 {
			return fmt.Errorf("bookkeeping ran before queued domain work: %d", hits)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-domain.done; err != nil {
		t.Error(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithOptions(s.path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var hits, searches int
	if err := reopened.db.QueryRow("SELECT hit_count,search_hits FROM knowledge WHERE id=?", id).Scan(&hits, &searches); err != nil || hits != 5 || searches != 2 {
		t.Fatalf("coalesced counts=%d/%d %v", hits, searches, err)
	}
}

func TestRuntimeBookkeepingBoundsKeysBytesAndCountsDrops(t *testing.T) {
	s := runtimeDomainStore(t)
	release := holdRuntimeWriter(t, s.writer, 0)
	body := strings.Repeat("body", 2<<20)
	for i := 0; i < bestEffortMaxKeys+3; i++ {
		s.recordKnowledgeUse([]Knowledge{{ID: int64(i + 1), Body: body}})
	}
	for i := 0; i < 2048; i++ {
		s.TouchMachine(fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 1024)))
	}
	s.writer.mu.Lock()
	usage := s.writer.bestEffort[bestEffortDelivery]
	machines := s.writer.bestEffort[bestEffortMachine]
	var drops [bestEffortKinds]uint64
	for kind := range bestEffortKinds {
		drops[kind] = s.writer.bestEffortDrops[kind].Load()
	}
	operations, bytes := s.writer.operations, s.writer.bytes
	s.writer.mu.Unlock()
	if len(usage.usage) != bestEffortMaxKeys || usage.bytes > bestEffortMaxBytes || drops[bestEffortDelivery] != 3 {
		t.Fatalf("usage keys=%d bytes=%d drops=%d", len(usage.usage), usage.bytes, drops[bestEffortDelivery])
	}
	if len(machines.machines) == 0 || len(machines.machines) > bestEffortMaxKeys || machines.bytes > bestEffortMaxBytes || drops[bestEffortMachine] == 0 {
		t.Fatalf("machine keys=%d bytes=%d drops=%d", len(machines.machines), machines.bytes, drops[bestEffortMachine])
	}
	if operations != 1 || bytes != 0 {
		t.Fatalf("bookkeeping consumed domain admission: %d/%d", operations, bytes)
	}
	release()
}

func TestRuntimeBookkeepingOwnsCompactMachineNames(t *testing.T) {
	s := runtimeDomainStore(t)
	release := holdRuntimeWriter(t, s.writer, 0)
	large := strings.Repeat("hostname", 1<<20)
	short := large[:8]
	s.TouchMachine(short)
	s.writer.mu.Lock()
	for hostname := range s.writer.bestEffort[bestEffortMachine].machines {
		if unsafe.StringData(hostname) == unsafe.StringData(short) {
			t.Error("pending hostname retains original huge allocation")
		}
	}
	s.writer.mu.Unlock()
	before := s.writer.bestEffortDrops[bestEffortMachine].Load()
	s.TouchMachine(large)
	s.writer.mu.Lock()
	after := s.writer.bestEffortDrops[bestEffortMachine].Load()
	s.writer.mu.Unlock()
	if after != before+1 {
		t.Fatal("oversize hostname drop not counted")
	}
	release()
}

func TestRuntimeBookkeepingFailureRollsBackAndCountsLostUpdates(t *testing.T) {
	s := runtimeDomainStore(t)
	first, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "first", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "second", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_usage BEFORE UPDATE OF hit_count ON knowledge WHEN new.id=%d BEGIN SELECT RAISE(FAIL,'test usage failure'); END`, second)); err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	s.recordKnowledgeUse([]Knowledge{{ID: first}, {ID: second}})
	release()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.writer.mu.Lock()
	drops := s.writer.bestEffortDrops[bestEffortDelivery].Load()
	s.writer.mu.Unlock()
	if drops != 2 {
		t.Fatalf("failed batch drops=%d", drops)
	}
	direct, err := OpenWithOptions(s.path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	for _, id := range []int64{first, second} {
		hits, _, err := direct.KnowledgeUsage(id)
		if err != nil || hits != 0 {
			t.Fatalf("partial best-effort transaction id=%d hits=%d %v", id, hits, err)
		}
	}
}

func TestRuntimeBookkeepingDoesNotWaitForPayloadPreparation(t *testing.T) {
	s := runtimeDomainStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		r, err := s.writer.admitPrepared(context.Background(), 0, func(r *writerRequest) { close(entered); <-release; r.run = func() error { return nil } })
		if err == nil {
			err = <-r.done
		}
		done <- err
	}()
	<-entered
	for name, record := range map[string]func(){"usage": func() { s.recordKnowledgeUse([]Knowledge{{ID: 1}}) }, "machine": func() { s.TouchMachine("reader") }} {
		t.Run(name, func(t *testing.T) { assertRuntimeReadPrompt(t, func() error { record(); return nil }) })
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []int{bestEffortDelivery, bestEffortMachine} {
		if got := s.writer.bestEffortDrops[kind].Load(); got != 1 {
			t.Fatalf("contention drops kind=%d: %d", kind, got)
		}
	}
}
