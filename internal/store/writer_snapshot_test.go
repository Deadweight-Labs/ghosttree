package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestRuntimeSnapshotUsesAdmissionAndCommitsBeforeReturn(t *testing.T) {
	s := runtimeDomainStore(t)
	in := snapshotCreateInput()
	release := holdRuntimeWriter(t, s.writer, 0)
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("snapshot bypassed queue: %v", err)
	}
	release()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM context_snapshots").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected snapshot heads=%d %v", n, err)
	}
	entered, commit := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		result, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { close(entered); <-commit; return in.Git, nil })
		if err == nil && (!result.Created || result.Snapshot.State != "sealed") {
			err = errors.New("unsealed ACK")
		}
		done <- err
	}()
	<-entered
	select {
	case err := <-done:
		t.Errorf("returned before commit: %v", err)
	default:
	}
	if err := s.reader.db.QueryRow("SELECT count(*) FROM context_snapshots").Scan(&n); err != nil || n != 0 {
		t.Errorf("partial snapshot visible: %d %v", n, err)
	}
	close(commit)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
	if err != nil || retry.Created || retry.Snapshot.State != "sealed" {
		t.Fatalf("retry=%+v %v", retry, err)
	}
}

func TestRuntimeSnapshotReservesCaptureBeforeAdmission(t *testing.T) {
	s := runtimeDomainStore(t)
	s.writer.mu.Lock()
	s.writer.cfg.MaxOperations = 4
	maxBytes := s.writer.cfg.MaxBytes
	s.writer.mu.Unlock()
	limits := snapshot.DefaultLimits()
	release := holdRuntimeWriter(t, s.writer, maxBytes-limits.MaxSnapshotLogicalBytes+1)
	if _, err := s.CreateContextSnapshot(context.Background(), snapshotCreateInput(), limits, nil); !errors.Is(err, ErrWriterBytesFull) {
		t.Errorf("capture bypassed byte reserve: %v", err)
	}
	release()
	if _, err := s.CreateContextSnapshot(context.Background(), snapshotCreateInput(), limits, nil); err != nil {
		t.Fatalf("default capture budget does not fit: %v", err)
	}
}

func TestRuntimeSnapshotQueuedCancellationDoesNotCapture(t *testing.T) {
	s := runtimeDomainStore(t)
	s.writer.mu.Lock()
	s.writer.cfg.MaxOperations = 4
	s.writer.mu.Unlock()
	release := holdRuntimeWriter(t, s.writer, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.CreateContextSnapshot(ctx, snapshotCreateInput(), snapshot.DefaultLimits(), nil)
		done <- err
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		s.writer.mu.Lock()
		accepted := s.writer.operations == 2
		s.writer.mu.Unlock()
		if accepted {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("snapshot did not wait for writer: %v", err)
		case <-deadline.C:
			t.Fatal("snapshot never admitted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	release()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM context_snapshots").Scan(&n); err != nil || n != 0 {
		t.Fatalf("canceled capture persisted: %d %v", n, err)
	}
}

func TestRuntimeSnapshotReservationRejectsUnboundedOrOverflowingLimits(t *testing.T) {
	for _, mutate := range []func(*snapshot.Limits){
		func(l *snapshot.Limits) { l.MaxSnapshotLogicalBytes = -1 },
		func(l *snapshot.Limits) { l.MaxEntriesPerSnapshot = 1<<63 - 1 },
		func(l *snapshot.Limits) { l.MaxEntryPayloadBytes = 1<<63 - 1 },
		func(l *snapshot.Limits) { l.MaxSnapshotPayloadBytes = 1<<63 - 1 },
	} {
		limits := snapshot.DefaultLimits()
		mutate(&limits)
		s := runtimeDomainStore(t)
		_, err := s.CreateContextSnapshot(context.Background(), snapshotCreateInput(), limits, nil)
		if !errors.Is(err, ErrWriterInvalidPayload) {
			t.Fatalf("invalid capture reservation: %v", err)
		}
	}
}

func TestRuntimeSnapshotUsesOriginalContextAndRollsBackCaptureFailure(t *testing.T) {
	s := runtimeDomainStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CreateContextSnapshot(ctx, snapshotCreateInput(), snapshot.DefaultLimits(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
	expected := errors.New("capture fault")
	s.snapshotFault = func(phase string) error {
		if phase == "after_entries" {
			return expected
		}
		return nil
	}
	if _, err := s.CreateContextSnapshot(context.Background(), snapshotCreateInput(), snapshot.DefaultLimits(), nil); !errors.Is(err, expected) {
		t.Fatalf("fault lost on direct view: %v", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM context_snapshots").Scan(&n); err != nil || n != 0 {
		t.Fatalf("fault left partial snapshot: %d %v", n, err)
	}
}
