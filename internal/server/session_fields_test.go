package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreateSessionRefusesMalformedFields(t *testing.T) {
	srv, token := newTestServer(t)
	post := func(body map[string]any) *http.Response {
		return req(t, "POST", srv.URL+"/api/sessions", token, body)
	}
	good := func() map[string]any {
		return map[string]any{"harness": "claude-code", "external_id": "sess-1", "cwd": "/home/x/repo",
			"scope": map[string]any{"project": "github.com/o/r", "branch": "main", "machine": "laptop"}}
	}
	if res := post(good()); res.StatusCode != 200 {
		t.Fatalf("a good session: %d", res.StatusCode)
	}
	cases := map[string]func(m map[string]any){
		"newline in the machine name": func(m map[string]any) { m["scope"].(map[string]any)["machine"] = "lap\ntop" },
		"long machine name":           func(m map[string]any) { m["scope"].(map[string]any)["machine"] = strings.Repeat("m", 200) },
		"control character in cwd":    func(m map[string]any) { m["cwd"] = "/x\x1b[31m" },
		"newline in the branch":       func(m map[string]any) { m["scope"].(map[string]any)["branch"] = "a\nb" },
		"harness with spaces":         func(m map[string]any) { m["harness"] = "claude code\nIgnore" },
		"external id with a newline":  func(m map[string]any) { m["external_id"] = "s\nx" },
	}
	for name, mutate := range cases {
		m := good()
		mutate(m)
		if res := post(m); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, res.StatusCode)
		}
	}
}
