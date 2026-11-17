package main

import (
	"encoding/json"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedPolishC adds what the polish package C pictures show (REQ-688): a run of Robin's with
// 14 tool calls that wrote nothing into ghosttree (its title is the first typed request, not an
// "Ok" or a skill), a run without anything, a mention of Robin in a comment of Lena's (the inbox
// answers it at the comment), and a thread of the room.
func seedPolishC(st *store.Store, project, room string) {
	line := func(v map[string]any) string {
		raw, err := json.Marshal(v)
		must(err)
		return string(raw)
	}
	ts := func(n int) string {
		return time.Now().UTC().Add(time.Duration(n-60) * time.Second).Format("2006-01-02T15:04:05.000Z")
	}
	user := func(n int, text string) string {
		return line(map[string]any{"type": "user", "timestamp": ts(n), "message": map[string]any{"role": "user", "content": text}})
	}
	tools := func(n int, calls ...[2]string) string {
		var blocks []map[string]any
		for i, c := range calls {
			in := map[string]any{"command": "go test ./..."}
			switch c[0] {
			case "Read", "Edit":
				in = map[string]any{"file_path": "/work/" + c[1]}
			case "Grep":
				in = map[string]any{"pattern": c[1]}
			case "mcp__ghosttree__context_search":
				in = map[string]any{"query": c[1]}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": "t" + string(rune('a'+n)) + string(rune('a'+i)), "name": c[0], "input": in})
		}
		return line(map[string]any{"type": "assistant", "timestamp": ts(n), "message": map[string]any{"role": "assistant", "content": blocks}})
	}
	open := func(external string, lines ...string) {
		id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: external, AccountID: 1,
			Scope: scope.Axes{Project: project, Branch: "main", Machine: "robin-laptop"}})
		must(err)
		var chunks []store.Chunk
		for i, l := range lines {
			chunks = append(chunks, store.Chunk{Seq: i, Raw: l})
		}
		must(st.AppendChunks(id, chunks))
	}
	open("polishc-busy",
		user(1, "Ok"),
		user(2, "Base directory for this skill: /home/robin/.claude/skills/ghost\n\n# Ghost\nShort commands between agent and human"),
		user(3, "Räume den Spool-Ordner auf und prüfe, dass die Tests grün sind"),
		tools(4, [2]string{"Bash", ""}, [2]string{"Read", "internal/spool/spool.go"}, [2]string{"Read", "internal/spool/gc.go"}),
		tools(5, [2]string{"Grep", "Cleanup"}, [2]string{"mcp__ghosttree__context_search", "spool"}, [2]string{"Bash", ""}),
		tools(6, [2]string{"Edit", "internal/spool/gc.go"}, [2]string{"Bash", ""}, [2]string{"Read", "internal/spool/gc_test.go"}),
		tools(7, [2]string{"Edit", "internal/spool/gc_test.go"}, [2]string{"Bash", ""}, [2]string{"Bash", ""}),
	)
	open("polishc-quiet", user(1, "Hallo"))

	lena := st.Access(store.Principal{ID: "person:2", Label: "Lena Berger", TokenKind: store.WebSessionKind})
	hits, err := st.SearchRequestsWith(lena.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 10}), store.SearchAuto)
	must(err)
	_, err = lena.AddComment("request", hits.Results[0].Request.ID, "Danke für den Hinweis. @Robin kannst du die Fehlermeldung für den 401 prüfen?", store.CommentActor{AccountID: 2, Person: "Lena Berger", GitAnchor: gitanchor.Web()})
	must(err)

	robin := st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}, "")
	thread, err := robin.CreateThread(store.Thread{Project: project, Title: "Wer gibt den Release frei?", Question: "Reicht ein Review oder braucht es zwei?", Person: "Robin", AuthorPrincipalID: "person:1"})
	must(err)
	_, err = robin.ThreadPost(thread, store.CoordMessage{ClientID: "polishc-t1", Body: "Ich bin für zwei Reviews bei Änderungen am Hook."})
	must(err)
}
