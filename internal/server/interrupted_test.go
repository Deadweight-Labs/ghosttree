package server

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// interruptedFixture legt einen angefangenen Faden an, dessen Sitzung vor
// idleFor still geworden ist.
func interruptedFixture(t *testing.T, st *store.Store, title, externalID string, idleFor time.Duration, handoff string) int64 {
	t.Helper()
	sessionID, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: externalID,
		Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: title, Scope: scope.Axes{Project: "github.com/x/y"}}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := st.StartRequestWork(created.Request.ID, sessionID, "primary", "robin")
	if err != nil {
		t.Fatal(err)
	}
	if handoff != "" {
		if _, err := st.FinishRequestWork(work.ID, "paused", handoff, "robin"); err != nil {
			t.Fatal(err)
		}
	}
	silent := time.Now().UTC().Add(-idleFor).Format(time.RFC3339)
	if _, err := st.DB().Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, silent, sessionID); err != nil {
		t.Fatal(err)
	}
	if handoff != "" {
		if _, err := st.DB().Exec(`UPDATE request_work SET ended_at=? WHERE id=?`, silent, work.ID); err != nil {
			t.Fatal(err)
		}
	}
	return created.Request.ID
}

func bootstrapBody(t *testing.T, st *store.Store, query string) string {
	t.Helper()
	token, _ := st.AddPerson("test")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?project=github.com/x/y"+query, token, nil)
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// Der Fall, für den REQ-177 geschrieben wurde: die Übergabe existiert und wird
// nur gefunden, wenn jemand im Prompt danach fragt.
func TestBootstrapNamesAnInterruptedThreadAndThePathToItsHandoff(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	id := interruptedFixture(t, st, "Ghost-Dateien", "gone", 40*time.Minute, "Schema steht, Rollout fehlt")

	out := bootstrapBody(t, st, "")
	if !strings.Contains(out, fmt.Sprintf("REQ-%d", id)) || !strings.Contains(out, "Ghost-Dateien") {
		t.Fatalf("bootstrap does not name the interrupted thread:\n%s", out)
	}
	if !strings.Contains(out, "request_get") {
		t.Fatalf("bootstrap does not say how to reach the handoff:\n%s", out)
	}
	if !strings.Contains(out, "40 minutes ago") {
		t.Fatalf("bootstrap does not say when it stopped:\n%s", out)
	}
}

// Das Fehlen sichtbar machen ist der wertvollere Fall: er erzeugt den Anreiz,
// den die Disziplin allein nicht erzeugt.
func TestBootstrapSaysWhenNoHandoffWasLeft(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	interruptedFixture(t, st, "Fakt abgelöst", "silent", 3*time.Hour, "")

	out := bootstrapBody(t, st, "")
	if !strings.Contains(out, "no handoff") {
		t.Fatalf("a thread without a handoff must say so:\n%s", out)
	}
	if strings.Contains(out, "request_get") {
		t.Fatalf("there is no handoff to fetch:\n%s", out)
	}
}

// Eine Sitzung, die gerade arbeitet, ist kein unterbrochener Faden — auch dann
// nicht, wenn sie session-start ein zweites Mal feuert.
func TestBootstrapSkipsTheAskingSessionsOwnThread(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	interruptedFixture(t, st, "meine eigene Arbeit", "self", 3*time.Hour, "")

	out := bootstrapBody(t, st, "&session=self")
	if strings.Contains(out, "meine eigene Arbeit") {
		t.Fatalf("the asking session reported itself as interrupted:\n%s", out)
	}
}

// Ohne unterbrochene Arbeit bleibt der Abschnitt weg: eine Überschrift über
// einer leeren Liste kostet Zeichen und sagt nichts.
func TestBootstrapStaysSilentWithoutInterruptedWork(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	_, _ = st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "nie angefasst", Scope: scope.Axes{Project: "github.com/x/y"}}})

	out := bootstrapBody(t, st, "")
	if strings.Contains(out, "Interrupted work") {
		t.Fatalf("bootstrap announces interrupted work that does not exist:\n%s", out)
	}
}
