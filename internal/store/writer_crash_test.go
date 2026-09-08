package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestWriterCrashChild(t *testing.T) {
	phase := os.Getenv("GHOSTTREE_TEST_CRASH_PHASE")
	if phase == "" {
		return
	}
	s, err := OpenRuntime(os.Getenv("GHOSTTREE_TEST_CRASH_DB"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids := writeCrashACKs(t, s)
	if err := json.NewEncoder(os.Stdout).Encode(ids); err != nil {
		t.Fatal(err)
	}

	pause := func() {
		fmt.Println("ready", phase)
		time.Sleep(30 * time.Second)
		t.Fatal("parent did not crash child at the requested boundary")
	}
	in := snapshotCreateInput()
	switch phase {
	case "chunk_committed_before_response":
		if err := s.AppendChunks(ids.Session, []Chunk{{Seq: 2, Role: "assistant", Text: "ambiguous chunk", Raw: "raw2"}}); err != nil {
			t.Fatal(err)
		}
		pause()
	case "revision_committed_before_response":
		if _, err := s.PushRevision(ids.Document, 1, "ambiguous revision", "second", "crash"); err != nil {
			t.Fatal(err)
		}
		pause()
	case "before_admission":
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.CreateContextSnapshot(ctx, in, snapshot.DefaultLimits(), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled admission: %v", err)
		}
		pause()
	case "pending":
		started := make(chan struct{})
		if _, err := s.writer.admit(context.Background(), 0, func() error {
			close(started)
			time.Sleep(30 * time.Second)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		<-started
		returned := make(chan error, 1)
		go func() {
			_, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
			returned <- err
		}()
		deadline := time.Now().Add(5 * time.Second)
		for s.writer.stats().OutstandingOperations != 2 {
			select {
			case err := <-returned:
				t.Fatalf("pending snapshot returned early: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("snapshot not admitted")
			}
			time.Sleep(time.Millisecond)
		}
		pause()
	case "in_transaction":
		s.snapshotFault = func(at string) error {
			if at == "before_commit" {
				pause()
			}
			return nil
		}
		if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil); err != nil {
			t.Fatal(err)
		}
		t.Fatal("snapshot escaped transaction crash boundary")
	case "committed_before_response", "acknowledged":
		result, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
		if err != nil || result.Snapshot.State != "sealed" {
			t.Fatalf("snapshot commit: %+v %v", result, err)
		}
		if phase == "acknowledged" {
			fmt.Println("ack", result.Snapshot.Name)
		}
		pause()
	default:
		t.Fatal("unknown crash phase")
	}
}

func TestRuntimeWriterCrashRestartMatrix(t *testing.T) {
	for _, phase := range []string{"before_admission", "pending", "in_transaction", "committed_before_response", "acknowledged", "chunk_committed_before_response", "revision_committed_before_response"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			ids := crashWriterAt(t, path, phase)

			s, err := OpenRuntime(path, DefaultWriterConfig())
			if err != nil {
				t.Fatalf("restart after %s: %v", phase, err)
			}
			defer s.Close()
			committed := phase == "committed_before_response" || phase == "acknowledged"
			wantCount := 0
			if committed {
				wantCount = 1
			}
			var count int
			if err := s.reader.db.QueryRow("SELECT count(*) FROM context_snapshots").Scan(&count); err != nil || count != wantCount {
				t.Fatalf("snapshot state after crash: %d want %d: %v", count, wantCount, err)
			}
			in := snapshotCreateInput()
			first, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
			if err != nil || first.Created == committed {
				t.Fatalf("restart snapshot retry: %+v %v", first, err)
			}
			retry, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
			if err != nil || retry.Created || first.Snapshot.ID != retry.Snapshot.ID || first.Snapshot.ContentDigest != retry.Snapshot.ContentDigest {
				t.Fatalf("duplicate snapshot on retry: %+v %v", retry, err)
			}
			chunks, err := s.ReadSession(ids.Session, 0, 10)
			wantChunks := 1
			if phase == "chunk_committed_before_response" {
				wantChunks = 2
			}
			if err != nil || len(chunks) != wantChunks || chunks[0].Text != "acknowledged before crash" || chunks[0].Raw != "raw" {
				t.Fatalf("acknowledged chunk lost: %+v %v", chunks, err)
			}
			if err := s.AppendChunks(ids.Session, []Chunk{{Seq: 1, Role: "user", Text: "acknowledged before crash", Raw: "raw"}, {Seq: 2, Role: "assistant", Text: "ambiguous chunk", Raw: "raw2"}}); err != nil {
				t.Fatal(err)
			}
			chunks, err = s.ReadSession(ids.Session, 0, 10)
			if err != nil || len(chunks) != 2 || chunks[1].Text != "ambiguous chunk" {
				t.Fatalf("chunk retry duplicated data: %+v %v", chunks, err)
			}
			detail, err := s.CreateRequest(crashRequestInput())
			if err != nil || detail.Request.ID != ids.Request {
				t.Fatalf("request retry changed identity: %+v %v", detail, err)
			}
			rev, err := s.DocumentRevision(ids.Document, 1)
			if err != nil || rev.Body != "original" {
				t.Fatalf("acknowledged revision lost: %+v %v", rev, err)
			}
			_, pushErr := s.PushRevision(ids.Document, 1, "ambiguous revision", "second", "crash")
			if phase == "revision_committed_before_response" {
				if pushErr == nil {
					t.Fatal("ambiguous revision replay did not respect CAS")
				}
			} else if pushErr != nil {
				t.Fatal(pushErr)
			}
			after, err := s.DocumentRevision(ids.Document, 2)
			if err != nil || after.Body != "ambiguous revision" {
				t.Fatalf("revision retry state: %+v %v", after, err)
			}
			if _, err := s.PushRevision(ids.Document, 1, "conflicting retry", "stale", "crash"); err == nil {
				t.Fatal("stale revision retry overwrote committed revision")
			}
			var integrity string
			if err := s.reader.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity: %s %v", integrity, err)
			}
			rows, err := s.reader.db.Query("PRAGMA foreign_key_check")
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Error("foreign key violation after crash and retry")
			}
			if err := rows.Err(); err != nil {
				t.Error(err)
			}
			rows.Close()
		})
	}
}

type crashACKs struct{ Session, Document, Request int64 }

func crashRequestInput() requestdomain.CreateInput {
	return requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "Stable"}, IdempotencyKey: "crash-stable"}
}

func writeCrashACKs(t *testing.T, s *Store) crashACKs {
	t.Helper()
	sid, err := s.UpsertSession(Session{Harness: "crash", ExternalID: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(sid, []Chunk{{Seq: 1, Role: "user", Text: "acknowledged before crash", Raw: "raw"}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateDocument(Document{Project: "p", Slug: "stable", Kind: "spec", Title: "Stable"}, "original", "first")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.CreateRequest(crashRequestInput())
	if err != nil {
		t.Fatal(err)
	}
	return crashACKs{sid, doc.ID, req.Request.ID}
}

func crashWriterAt(t *testing.T, path, phase string) crashACKs {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWriterCrashChild$")
	cmd.Env = append(os.Environ(), "GHOSTTREE_TEST_CRASH_PHASE="+phase, "GHOSTTREE_TEST_CRASH_DB="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	var ids crashACKs
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if !scanner.Scan() {
			ready <- fmt.Errorf("missing domain ACKs: %v", scanner.Err())
			return
		}
		if err := json.Unmarshal(scanner.Bytes(), &ids); err != nil {
			ready <- err
			return
		}
		if ids.Session <= 0 || ids.Document <= 0 || ids.Request <= 0 {
			ready <- fmt.Errorf("invalid domain ACKs: %+v", ids)
			return
		}
		if !scanner.Scan() {
			ready <- fmt.Errorf("child exited before boundary: %v", scanner.Err())
			return
		}
		if phase == "acknowledged" {
			if scanner.Text() != "ack "+snapshotCreateInput().Name {
				ready <- fmt.Errorf("missing durable ACK: %s", scanner.Text())
				return
			}
			scanner.Scan()
		}
		if scanner.Text() != "ready "+phase {
			ready <- fmt.Errorf("wrong crash boundary: %s", scanner.Text())
			return
		}
		ready <- nil
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not reach crash boundary")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited normally instead of crashing")
	}
	return ids
}
