package main

import (
	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedReactions adds what the last functions of the web interface show:
// reactions on an issue, an idea and the comments of the issue (Robin and Lena,
// the two who may react in the demo project; Gus is a guest and only reads), an agent's question to Robin with proposed answers, a task
// with proposed answers, and a task with an explanation and steps. It runs after
// seedComments, whose issue and comments it reacts to.
func seedReactions(st *store.Store, project, room string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	lena := st.Access(store.Principal{ID: "person:2", Label: "Lena Berger", TokenKind: store.WebSessionKind})
	react := func(pa *store.ProjectAccess, kind string, id int64, keys ...string) {
		for _, k := range keys {
			_, err := pa.React(kind, id, k)
			must(err)
		}
	}

	hits, err := st.SearchRequestsWith(robin.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 30}), store.SearchAuto)
	must(err)
	var voice int64
	for _, h := range hits.Results {
		if h.Request.Title == "Answer inbox questions by voice" {
			voice = h.Request.ID
		}
	}
	if voice != 0 {
		react(robin, store.ReactOnRequest, voice, "+1")
		react(lena, store.ReactOnRequest, voice, "+1", "rocket")
		comments, err := robin.Comments(store.CommentOnRequest, voice)
		must(err)
		for _, c := range comments {
			switch {
			case c.Deleted:
			case c.Person == "Lena Berger":
				react(robin, store.ReactOnComment, c.ID, "+1", "heart")
			default:
				react(lena, store.ReactOnComment, c.ID, "hooray")
			}
		}
	}
	ideas, err := robin.SearchIdeas(idea.Filter{Project: project, State: "open", Limit: 5})
	must(err)
	for _, i := range ideas {
		// The idea with a workshop stays without reactions; the others are plain wishes.
		if i.Title != "Rohideen aus Sprachnachrichten zu Konzepten ausarbeiten" {
			react(robin, store.ReactOnIdea, i.ID, "+1")
			react(lena, store.ReactOnIdea, i.ID, "+1", "heart")
		}
	}

	// An agent of Lena's asks Robin something and proposes two answers.
	agent := "claude:lena-laptop:22222222-aaaa-4bbb-8ccc-000000000002"
	asked, err := st.CoordinationFor(store.Principal{ID: "person:2", Label: "Lena Berger"}, agent).Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: agent, ClientID: "r-q1", Body: "Git-Anker und Arbeitspakete in einer Migration oder getrennt?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}})
	must(err)
	must(st.SaveAnswerOptions(store.AnswerOnMessage, asked, []string{"Getrennt", "Eine Migration"}))

	actor := humantask.Actor{AccountID: 1, Person: "Robin"}
	task := func(in humantask.CreateInput) humantask.Task {
		in.Project = project
		t, err := robin.CreateHumanTask(in, actor)
		must(err)
		return t
	}
	task(humantask.CreateInput{RequestID: voice, Kind: "input", Title: "Welche Testphase soll es für neue Teams geben?", Options: []string{"14 Tage kostenlos", "30 Tage kostenlos", "Keine Testphase"}})
	connect := task(humantask.CreateInput{RequestID: voice, Kind: "action", Title: "GitHub-App für die Organisation installieren",
		Steps: []humantask.Step{
			{Text: "Die Seite der GitHub-App öffnen", URL: "https://github.com/apps"},
			{Text: "„Install“ wählen und die Organisation Robins Werkstatt auswählen"},
			{Text: "Nur die Repositories freigeben, die ghosttree lesen soll"},
			{Text: "Hier „Erledigt“ klicken"},
		}})
	must(st.SaveAgentExplanation(explain.KindTask, connect.ID, "Ohne die App sieht ghosttree keine Pull Requests und keine Commits deiner Repositories. Du installierst sie einmal für die Organisation, danach läuft alles von selbst."))
	short := task(humantask.CreateInput{RequestID: voice, Kind: "input", Title: "Den Namen der Ideen-Werkstatt festlegen"})
	must(st.SaveAgentExplanation(explain.KindTask, short.ID, "Der Name steht in der Navigation und in jeder Einladung."))
}
