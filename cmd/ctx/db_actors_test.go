package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestDBBackfillActorsReportsAndHasADryRun(t *testing.T) {
	db := filepath.Join(t.TempDir(), "g.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"agent", "human", "distilled"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "bug", Title: "r " + origin,
			Scope: scope.Axes{Project: "github.com/x/y"}, Origin: origin, Person: "robin"}, AuthorID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Ältere Einträge: jüngere überlässt der Nachtrag dem Live-Stempel.
	if _, err := st.DB().Exec(`UPDATE requests SET created_at='2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := cmdDB(append([]string{"backfill-actors", "--db", db}, args...), &out); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		return out.String()
	}
	dry := run("--dry-run", "--kind", "request")
	if !strings.Contains(dry, "dry run: nothing was written") || !strings.Contains(dry, "recent") {
		t.Fatalf("dry run: %s", dry)
	}
	var rep store.ActorBackfillReport
	if err := json.Unmarshal([]byte(run("--json", "--batch", "1")), &rep); err != nil {
		t.Fatal(err)
	}
	for _, k := range rep.Kinds {
		// origin human ohne Web-Herkunft ist kein Beweis, distilled nie.
		if k.Kind == "request" && (k.Candidates != 3 || k.Agent != 1 || k.Human != 0 || k.Unknown != 2) {
			t.Errorf("requests: %+v", k)
		}
	}
	again := run()
	if !strings.Contains(again, "model history: 0 sessions") {
		t.Errorf("second run: %s", again)
	}
	var out bytes.Buffer
	if code := cmdDB([]string{"backfill-actors"}, &out); code != 2 {
		t.Errorf("without --db: exit %d", code)
	}
}
