package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRequestCLIJSONSearchWritesOneDocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: token}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "ledger search", Scope: requestProjectAxes("")}})

	var out bytes.Buffer
	if code := run([]string{"request", "search", "ledger", "--json"}, &out); code != 0 {
		t.Fatalf("code = %d, output = %s", code, out.String())
	}
	var envelope struct {
		OK   bool                     `json:"ok"`
		Data requestdomain.SearchPage `json:"data"`
	}
	dec := json.NewDecoder(&out)
	if err := dec.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || len(envelope.Data.Results) != 1 {
		t.Fatalf("envelope = %+v", envelope)
	}
	var extra any
	if dec.Decode(&extra) == nil {
		t.Fatal("stdout contains more than one JSON document")
	}
}

func TestRequestCLIRejectsUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"request", "bogus"}, &out); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestRequestCLIWorkLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	_ = config.Save(config.Config{ServerURL: srv.URL, Token: token})
	sessionID, _ := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "cli-work"})

	var out bytes.Buffer
	if code := run([]string{"request", "create", "--type", "feature", "--title", "CLI flow", "--ac", "works", "--json"}, &out); code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	var created struct {
		Data requestdomain.Detail `json:"data"`
	}
	_ = json.Unmarshal(out.Bytes(), &created)
	out.Reset()
	if code := run([]string{"request", "start", fmt.Sprint(created.Data.Request.ID), "--session", fmt.Sprint(sessionID), "--json"}, &out); code != 0 {
		t.Fatalf("start: %s", out.String())
	}
	var started struct {
		Data requestdomain.Work `json:"data"`
	}
	_ = json.Unmarshal(out.Bytes(), &started)
	out.Reset()
	if code := run([]string{"request", "ac", "met", fmt.Sprint(created.Data.Request.ID), fmt.Sprint(created.Data.Criteria[0].ID), "--evidence-kind", "test", "--evidence-ref", "go test", "--json"}, &out); code != 0 {
		t.Fatalf("ac met: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"request", "pause", fmt.Sprint(started.Data.ID), "--summary", "CLI verified", "--json"}, &out); code != 0 {
		t.Fatalf("pause: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"request", "done", fmt.Sprint(created.Data.Request.ID), "--evidence-kind", "commit", "--evidence-ref", "abc", "--json"}, &out); code != 0 {
		t.Fatalf("done: %s", out.String())
	}
}
