package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestServeShutdownProcess(t *testing.T) {
	if os.Getenv("GHOSTTREE_TEST_SERVE_CHILD") != "1" {
		return
	}
	os.Exit(cmdServe([]string{"--db", os.Getenv("GHOSTTREE_TEST_SERVE_DB"), "--listen", os.Getenv("GHOSTTREE_TEST_SERVE_LISTEN")}, os.Stdout))
}

func TestServeSIGTERMDrainsAcceptedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shutdown.db")
	seed, err := store.OpenWithOptions(path, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	token, err := seed.AddPerson("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestServeShutdownProcess$")
	cmd.Env = append(os.Environ(), "GHOSTTREE_TEST_SERVE_CHILD=1", "GHOSTTREE_TEST_SERVE_DB="+path, "GHOSTTREE_TEST_SERVE_LISTEN="+addr)
	log, err := os.Create(filepath.Join(t.TempDir(), "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	client := &http.Client{Timeout: 10 * time.Second}
	url := "http://" + addr
	ready := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get(url + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(ready) {
			t.Fatal("child server did not become healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	locker, err := store.OpenWithOptions(path, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, err := locker.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	responses := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			req, err := http.NewRequest("POST", url+"/api/sessions", strings.NewReader(fmt.Sprintf(`{"harness":"shutdown","external_id":"accepted-%d"}`, i)))
			if err != nil {
				responses <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			response, err := client.Do(req)
			if err == nil {
				response.Body.Close()
				if response.StatusCode != 200 {
					err = fmt.Errorf("accepted write response=%d", response.StatusCode)
				}
			}
			responses <- err
		}(i)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		accepted := int64(0)
		for line := range strings.SplitSeq(string(raw), "\n") {
			if strings.HasPrefix(line, "ghosttree_writer_outstanding_operations ") {
				accepted, _ = strconv.ParseInt(strings.TrimPrefix(line, "ghosttree_writer_outstanding_operations "), 10, 64)
			}
		}
		if accepted == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writes did not reach writer admission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("server exited before accepted writes completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-responses; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("SIGTERM exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not finish draining")
	}
	var count int
	if err := conn.QueryRowContext(context.Background(), "SELECT count(*) FROM sessions WHERE harness='shutdown'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("drained writes=%d %v", count, err)
	}
}
