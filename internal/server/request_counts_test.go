package server

import (
	"encoding/json"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestRequestCountAndPackageQueriesRespectAccess(t *testing.T) {
	f := accessAPI(t, true)
	mia, robin := f.id["r-mia"], f.id["r-robin"]
	f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/c", "requests": []map[string]any{{"request_id": mia}, {"request_id": robin, "role": "optional"}}})

	var n struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(f.expect(t, "gus", 200, "GET", "/api/requests/count?project="+accProject, nil)), &n); err != nil || n.Count != 2 {
		t.Fatalf("count = %+v %v", n, err)
	}
	// Auf einem Branch zählt der Request ohne Branch mit, wie in der Liste.
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", "/api/requests/count?project="+accProject+"&branch=feat/c", nil)), &n); err != nil || n.Count != 2 {
		t.Fatalf("count on a branch = %+v %v", n, err)
	}
	f.expect(t, "mia", 400, "GET", "/api/requests/count?project="+accProject+"&state=bogus", nil)
	f.expect(t, "nora", 404, "GET", "/api/requests/count?project="+accProject, nil)

	pk := decode[[]requestdomain.PackageOfRequest](t, f.expect(t, "gus", 200, "GET", idPath("/api/requests/%d/packages", robin), nil))
	if len(pk) != 1 || pk[0].Role != "optional" || pk[0].Branch != "" || !pk[0].NameHidden {
		t.Fatalf("packages of a request: %+v", pk)
	}
	// Unbekannt und fremd antworten gleich.
	f.expect(t, "nora", 404, "GET", idPath("/api/requests/%d/packages", robin), nil)
	f.expect(t, "nora", 404, "GET", idPath("/api/requests/%d/packages", robin+9999), nil)

	list := decode[[]requestdomain.PackageSummary](t, f.expect(t, "gus", 200, "GET", "/api/packages/list?project="+accProject, nil))
	if len(list) != 1 || list[0].Requests != 2 {
		t.Fatalf("package list: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/packages/list?project="+accProject, nil)
	f.expect(t, "mia", 400, "GET", "/api/packages/list", nil)
}
