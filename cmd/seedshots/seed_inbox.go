package main

import (
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedInbox adds what the inbox shows besides tasks: a direct message, a group
// message that answers one of Robin's own, an agent's question to Robin and a
// mention in the project room.
func seedInbox(st *store.Store, room string) {
	robin := st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}, "")
	lena := st.CoordinationFor(store.Principal{ID: "person:2", Label: "Lena Berger", TokenKind: store.WebSessionKind}, "")
	direct := store.RoomKeyForDirect([]string{"person:1", "person:2"})
	must(robin.EnsureDirect(store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"person:1", "person:2"}}))
	_, err := lena.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: direct, ClientID: "i-dm1", Body: "Hast du kurz Zeit für die Rollen beim öffentlichen Lesen? Ich hänge an der Org-Freigabe."})
	must(err)
	group, err := robin.CreateGroup(store.GroupInput{Label: "Release-Team", Members: []string{"person:1", "person:2"}})
	must(err)
	asked, err := robin.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: group.Key, ClientID: "i-g1", Body: "Gibst du den Release-PR frei, sobald alle Prüfungen grün sind?"})
	must(err)
	_, err = lena.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: group.Key, ClientID: "i-g2", ReplyTo: asked, Body: "Release 0.14.0 steht, fehlt nur noch deine Freigabe am PR."})
	must(err)
	_, err = lena.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "i-m1", Body: "@Robin Öffentliches Lesen nur für Repos mit Freigabe „Öffentlich“, nicht für die ganze Org?", Mentions: []string{"person:1"}})
	must(err)
}

// seedLists adds what the issue, idea and pain lists show: labels in several
// colours on the requests and pains, two ideas and a saved search.
func seedLists(st *store.Store, project string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin"})
	mk := func(name, color string) store.Label {
		l, err := robin.CreateLabel(store.LabelInput{Project: project, Name: name, Color: color, By: "Robin"})
		must(err)
		return l
	}
	ui, git, ux := mk("ui", "0969da"), mk("git", "1a7f37"), mk("agenten-ux", "f9d0c4")
	hits, err := st.SearchRequestsWith(robin.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 10}), store.SearchAuto)
	must(err)
	for i, h := range hits.Results {
		names := []string{ui.Name}
		if i%2 == 1 {
			names = []string{git.Name, ux.Name}
		}
		_, err := robin.SetLabels("request", h.Request.ID, names, nil)
		must(err)
	}
	actor := idea.Actor{AccountID: 1, Person: "Robin"}
	for _, in := range []store.CreateIdeaInput{
		{Project: project, Title: "Weekly look back per repo", Body: "Every Monday one page: what got done, what was left, which knowledge is new.", Actor: actor},
		{Project: project, Title: "Voice control in the inbox", Body: "Answer questions by voice when the hands are not free.", Actor: actor},
	} {
		i, err := robin.CreateIdea(in)
		must(err)
		_, err = robin.SetLabels("idea", i.ID, []string{ux.Name}, nil)
		must(err)
	}
	_, err = robin.SavePreset(project, "user", "my-open-issues", "", []string{"is:issue is:open"})
	must(err)
}

// seedCode adds a small ghost tree and a few knowledge entries for the code pages.
func seedCode(st *store.Store, project string) {
	put := func(path, kind, desc string) {
		_, err := st.PutGhostFile(store.GhostFile{Project: project, Path: path, Kind: kind, Description: desc, Person: "Robin", Harness: "claude-code"})
		must(err)
	}
	put("", "dir", "Shows what is not in the code: pitfalls, decisions and notes, plus one description per file and folder.\n\nThe server code is in `internal/`, the client and hooks in `cmd/ctx`.")
	put("internal", "dir", "All the server code: store, server, web.")
	put("cmd", "dir", "Entry points: ctx and the tools around it.")
	put("docs", "dir", "Operations and product documentation, one topic per file.")
	put("README.md", "file", "Product page: what it is, how to install it.")
	put("Makefile", "file", "Build, test and lint; `make check` runs like CI.")
	put("internal/store/ledger.go", "file", "The request ledger: requests, criteria, proofs.")
	for i, k := range []struct{ typ, title, path string }{
		{"pitfall", "Migrations run without a way back", "internal/store/ledger.go"},
		{"note", "The web UI is embedded and needs no build step", "internal/web/web.go"},
		{"decision", "Numbers stay in the tab, not beside it", "docs/web.md"},
	} {
		_ = i
		_, err := st.InsertKnowledge(store.Knowledge{Type: k.typ, Title: k.title, Body: "b", Scope: scope.Axes{Project: project}, Person: "Robin", Confidence: "trusted",
			OriginContext: &originctx.Context{Paths: []originctx.Path{{Path: k.path, Kind: originctx.KindNamed, Weight: 10}}}})
		must(err)
	}
}
