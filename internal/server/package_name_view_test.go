package server

import (
	"strings"
	"testing"
)

// Der Name eines Pakets ist oft der Branch-Name; ein Gast bekommt ihn nicht, auch
// nicht über die Paket-Chips an einem Request und nicht über die Projektliste.
func TestPackageNameIsNoBranchLeakForGuests(t *testing.T) {
	f := accessAPI(t, true)
	named := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/secret-a", "requests": []map[string]any{{"request_id": f.id["r-mia"]}}}))
	custom := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/secret-b", "name": "Geheimprojekt", "requests": []map[string]any{{"request_id": f.id["r-robin"]}}}))
	for _, p := range []string{idPath("/api/packages/%d", named.ID), idPath("/api/packages/%d", custom.ID),
		idPath("/api/packages/%d/review", named.ID), "/api/packages/list?project=" + accProject, idPath("/api/requests/%d/packages", f.id["r-mia"]), idPath("/api/requests/%d/packages", f.id["r-robin"])} {
		body := f.expect(t, "gus", 200, "GET", p, nil)
		for _, leak := range []string{"secret-a", "Geheimprojekt"} {
			if strings.Contains(body, leak) {
				t.Errorf("guest at %s sees %q: %s", p, leak, body)
			}
		}
	}
	got := pkgJSON(t, f.expect(t, "gus", 200, "GET", idPath("/api/packages/%d", named.ID), nil))
	if got.Name != "Package #"+i64(named.ID) || !got.NameHidden {
		t.Errorf("guest gets the name %q (hidden=%v), want the neutral one", got.Name, got.NameHidden)
	}
	// Ein Mitglied sieht beide Namen.
	if m := pkgJSON(t, f.expect(t, "mia", 200, "GET", idPath("/api/packages/%d", custom.ID), nil)); m.Name != "Geheimprojekt" || m.NameHidden {
		t.Errorf("member sees %q", m.Name)
	}
	if m := pkgJSON(t, f.expect(t, "mia", 200, "GET", idPath("/api/packages/%d", named.ID), nil)); m.Name != "feat/secret-a" {
		t.Errorf("member sees %q", m.Name)
	}
}
