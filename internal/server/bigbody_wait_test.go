package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Waiting for a slot in bigBodies must not use up the server's ReadTimeout:
// the clock for reading the body starts once the slot is held.
func TestWaitingForAnUploadSlotDoesNotCountAgainstTheReadTimeout(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	srv := httptest.NewUnstartedServer(New(st))
	srv.Config.ReadTimeout = 500 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	var made struct {
		ID int64 `json:"id"`
	}
	resp := req(t, "POST", srv.URL+"/api/sessions", token, map[string]any{"harness": "claude-code", "external_id": "slow"})
	if err := json.NewDecoder(resp.Body).Decode(&made); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for i := 0; i < cap(bigBodies); i++ {
		bigBodies <- struct{}{}
	}
	go func() {
		time.Sleep(900 * time.Millisecond)
		for i := 0; i < cap(bigBodies); i++ {
			<-bigBodies
		}
	}()
	body, _ := json.Marshal(map[string]any{"chunks": []store.Chunk{{Seq: 0, Role: "user", Text: "x", Raw: strings.Repeat("y", smallBody+1)}}})
	rq, _ := http.NewRequest("POST", srv.URL+idPath("/api/sessions/%d/chunks", made.ID), bytes.NewReader(body))
	rq.Header.Set("Authorization", "Bearer "+token)
	out, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	if out.StatusCode != 204 {
		t.Errorf("upload after waiting for a slot = %d, want 204", out.StatusCode)
	}
}
