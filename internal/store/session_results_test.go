package store

import (
	"strings"
	"testing"
	"time"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// A session is credited with what its stamp says it wrote, and only that: the
// requirement it met (with the newest commit proof), not the one another session
// met.
func TestSessionResultsListWhatTheStampedSessionWrote(t *testing.T) {
	st := openTest(t)
	mine, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "mine", Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "other", Scope: scope.Axes{Project: "github.com/x/y"}})
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "T", Scope: scope.Axes{Project: "github.com/x/y"}, Person: "a"}, Criteria: []string{"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, sid := range []int64{mine, other} {
		c := d.Criteria[i]
		if err := st.SetCriterionState(c.ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "abcdef1234567890", Person: "a"}); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordActor(actordomain.Criterion, c.ID, actordomain.Actor{Kind: actordomain.Agent, Harness: "claude-code", SessionID: sid}); err != nil {
			t.Fatal(err)
		}
	}
	sess, _ := st.SessionByID(mine)
	res, err := st.SessionResults(sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Criteria) != 1 || res.Criteria[0].Number != 1 || res.Criteria[0].RequestID != d.Request.ID || !strings.HasPrefix(res.Criteria[0].Commit, "abcdef1") {
		t.Fatalf("criteria of the session = %+v", res.Criteria)
	}
	if len(res.Knowledge)+len(res.Tasks)+len(res.Questions)+len(res.Requests) != 0 {
		t.Errorf("nothing else was written: %+v", res)
	}
}

// An entry stamped before the session was uploaded names the session by its
// external id; it counts for the session of the same account only.
func TestSessionResultsResolveAStampThatKnewOnlyTheExternalID(t *testing.T) {
	st := openTest(t)
	id, _ := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "late", Scope: scope.Axes{Project: "github.com/x/y"}})
	k, err := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Late", Body: "b", Scope: scope.Axes{Project: "github.com/x/y"}, Person: "a", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActor(actordomain.Knowledge, k, actordomain.Actor{Kind: actordomain.Agent, Harness: "claude-code", SessionRef: "late"}); err != nil {
		t.Fatal(err)
	}
	sess, _ := st.SessionByID(id)
	res, err := st.SessionResults(sess)
	if err != nil || len(res.Knowledge) != 1 || res.Knowledge[0].Title != "Late" {
		t.Fatalf("knowledge = %+v %v", res.Knowledge, err)
	}
}

// The files a session changed come from the path activity; the lines they gained
// and lost from the edits in the transcript, only when asked for.
func TestSessionFilesCountTheLinesOfTheEditsOnRequest(t *testing.T) {
	st := openTest(t)
	id, _ := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "files", Scope: scope.Axes{Project: "github.com/x/y"}})
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.RecordPathActivity([]PathActivity{
		{SessionExternalID: "files", Tool: "Edit", Path: "/w/a.go", Checkout: "/w", Writes: true, Quality: ActivityObserved, At: now},
		{SessionExternalID: "files", Tool: "Read", Path: "/w/b.go", Checkout: "/w", Quality: ActivityIntent, At: now},
	}); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/w/a.go","old_string":"x\ny","new_string":"x\nz\nw"}}]}}`
	if err := st.AppendChunks(id, []Chunk{{Seq: 0, Raw: line}}); err != nil {
		t.Fatal(err)
	}
	sess, _ := st.SessionByID(id)
	with, err := st.SessionFiles(sess, true)
	if err != nil || len(with) != 1 || with[0].Path != "a.go" {
		t.Fatalf("files = %+v %v", with, err)
	}
	if !with[0].HasStat || with[0].Add != 3 || with[0].Del != 2 {
		t.Errorf("lines = +%d -%d (stat %v), want +3 -2", with[0].Add, with[0].Del, with[0].HasStat)
	}
	without, _ := st.SessionFiles(sess, false)
	if len(without) != 1 || without[0].HasStat {
		t.Errorf("without lines: %+v", without)
	}
}

// The read of what a session wrote must not walk every stamp of a kind: both
// branches (by session id, and by external id for a session stamped before its
// upload) are looked up in an index of entry_actors. A plain OR over the two
// conditions does not do that, which is why stampedBy is two selects.
func TestSessionResultsReadTheStampsThroughTheirIndexes(t *testing.T) {
	st := openTest(t)
	for _, kind := range []string{actordomain.Criterion, actordomain.Knowledge, actordomain.Task, actordomain.Message, actordomain.Request} {
		cte, args := stampedBy(Session{ID: 7, ExternalID: "ext", AccountID: 1}, 1, kind)
		rows, err := st.db.Query(`EXPLAIN QUERY PLAN `+cte+`SELECT ea.at, ea.entry_id FROM ea ORDER BY ea.at DESC`, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		all := strings.Join(plan, "\n")
		if !strings.Contains(all, "entry_actors_session") || !strings.Contains(all, "entry_actors_pending") {
			t.Errorf("%s: the plan does not use both indexes:\n%s", kind, all)
		}
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN") || strings.Contains(line, "USING PRIMARY KEY") && strings.Contains(line, "entry_actors") {
				t.Errorf("%s: the plan reads all stamps of a kind: %q\n%s", kind, line, all)
			}
		}
	}
	// A session without an external id has only the first branch.
	cte, _ := stampedBy(Session{ID: 7}, 1, actordomain.Task)
	if strings.Contains(cte, "UNION") {
		t.Errorf("a session without external id looks for pending stamps: %s", cte)
	}
}
