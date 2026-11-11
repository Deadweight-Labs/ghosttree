package main

import (
	"encoding/json"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedSessionLinks hangs sessions on the commits of the open pull request 184 so
// the pages show every level of "Built in": Robin's own session (his GitHub login
// is verified, so the assignment is firm), Lena's session shared with the project
// and matched to a commit by a bot author (nothing to check it against: weak), and
// Ben's session, private to him, which Robin and the guest see only as a number.
func seedSessionLinks(st *store.Store, project string) {
	must(st.SetAccountGitHub("person:1", "Robin", 1001))
	at := func(back time.Duration) time.Time { return time.Now().UTC().Add(-back).Truncate(time.Second) }
	stamp := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z") }
	session := func(account int64, external, title string) int64 {
		id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: external, AccountID: account,
			Scope: scope.Axes{Project: project, Branch: "feat/599-pulls", Machine: "build-1"}})
		must(err)
		raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": title}})
		must(st.AppendChunks(id, []store.Chunk{{Seq: 1, Role: "user", Text: title, Raw: string(raw)}}))
		return id
	}
	commit := func(account int64, external string, seq int, when time.Time) {
		_, err := st.RecordSessionGitEvents(account, "", []store.SessionGitEvent{{Harness: "claude-code", SessionExternalID: external, Seq: seq, Kind: "commit", Op: "commit",
			StartedAt: stamp(when.Add(-200 * time.Millisecond)), EndedAt: stamp(when.Add(200 * time.Millisecond)), OK: true, Branch: "feat/599-pulls"}})
		must(err)
	}
	session(1, "seed-robin", "Liste der Pull Requests mit Zustandsreitern bauen")
	lena := session(2, "seed-lena", "Seite eines PRs mit Unterhaltung und Checkliste")
	session(3, "seed-ben", "Reiter Commits mit Tagesgruppen, privat")
	must(st.SetSessionShared(lena, "person:2", true))
	commit(1, "seed-robin", 4, at(9*time.Hour))
	commit(2, "seed-lena", 4, at(7*time.Hour))
	commit(3, "seed-ben", 4, at(6*time.Hour))
	_, err := st.RecordSessionGitEvents(1, "", []store.SessionGitEvent{{Harness: "claude-code", SessionExternalID: "seed-robin", Seq: 9, Kind: "pr_create",
		StartedAt: stamp(at(8 * time.Hour)), EndedAt: stamp(at(8*time.Hour - 3*time.Second)), OK: true, PR: 184, PRRemote: project}})
	must(err)
}
