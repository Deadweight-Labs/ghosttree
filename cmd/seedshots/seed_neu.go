package main

import (
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func sp(s string) *string { return &s }

// seedNeu adds what the pages of features, machines and the idea workshop show:
// features with versions, ideas and pull requests; machines of Robin and of the
// organisation with knowledge, agents and sessions; and ideas that agents have worked
// out into a concept and open questions with variants, one of them already decided.
func seedNeu(st *store.Store, project string) {
	seedFeatures(st, project)
	seedMachines(st, project)
	seedWorkshop(st, project)
}

func seedFeatures(st *store.Store, project string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	actor := feature.Actor{AccountID: 1, Person: "Robin"}
	agent := feature.Actor{AccountID: 1, Person: "claude-mainex", Agent: true}
	mk := func(in store.CreateFeatureInput) feature.Feature {
		in.Project, in.Actor = project, actor
		f, err := robin.CreateFeature(in)
		must(err)
		return f
	}
	inbox := mk(store.CreateFeatureInput{Message: "Erste Beschreibung nach dem Umbau", Content: feature.Patch{
		Name: sp("Posteingang"), Summary: sp("Ein Ort für alles, was auf dich wartet: Fragen von Agenten, Aufgaben, Erwähnungen und Entscheidungen."),
		Behavior: sp("Der Posteingang listet, was an dich gerichtet ist, das Neueste oben. Eine Zeile führt zur Nachricht, „Antworten“ antwortet gleich in der Zeile, der Haken hakt ab.\n\nFilter: Alle, Direkt, Fragen, Aufgaben, Issues, Gärtner."),
		Limits:   sp("Antworten per Sprache gibt es noch nicht. Entscheidungen aus Ideen erscheinen erst, wenn ein Mensch sie treffen muss."),
		CLI:      sp("`ctx inbox` zeigt dieselbe Liste im Terminal."), MCP: sp("`coord_inbox` liest, `coord_send` antwortet."),
		API: sp("`GET /api/coord/inbox`"), UI: sp("Die Glocke im Kopf führt hierher."), Areas: &[]string{"internal/web", "internal/store/coordinbox"}}})
	_, _, err := robin.UpdateFeature(inbox.ID, store.UpdateFeatureInput{Message: "Filter für Gärtner-Hinweise ergänzt", PR: 181, Actor: agent,
		Patch: feature.Patch{Behavior: sp("Der Posteingang listet, was an dich gerichtet ist, das Neueste oben. Eine Zeile führt zur Nachricht, „Antworten“ antwortet gleich in der Zeile, der Haken hakt ab.\n\nFilter: Alle, Direkt, Fragen, Aufgaben, Issues, Gärtner.\n\nHinweise des Gärtners stehen in einem eigenen Filter und stören die Fragen nicht.")}})
	must(err)
	_, err = st.CreatePackage(requestdomain.PackageInput{Project: project, Branch: "feat/inbox-gardener", Name: "Gärtner im Posteingang", PR: 181, Person: "Robin"})
	must(err)
	rooms := mk(store.CreateFeatureInput{Message: "Räume, Teams und Direkt", Content: feature.Patch{
		Name: sp("Räume"), Summary: sp("Gespräche zwischen Menschen und Agenten, geordnet in Räume, Teams und Direktnachrichten."),
		Behavior: sp("Jedes Repo hat einen Raum „Allgemein“. Teams sind Gruppen von Menschen, Direkt ist eins zu eins. Threads haben eine eigene Ansicht."),
		UI:       sp("Der Reiter „Räume“ eines Repos und die Seite unter /ui/coord."), Areas: &[]string{"internal/web", "internal/store"}}})
	werkstatt := mk(store.CreateFeatureInput{Message: "Beschreibung zur Werkstatt", Content: feature.Patch{
		Name: sp("Ideen-Werkstatt"), Summary: sp("Aus einer Rohidee wird ein Konzept mit offenen Fragen, Varianten und einer Freigabe durch einen Menschen."),
		Behavior: sp("Agenten legen Kurzfassung und Konzept an und stellen Fragen mit Varianten A, B, C. Menschen stimmen ab, wählen und geben das Konzept frei."),
		Limits:   sp("Bilder an Varianten werden in der Oberfläche noch nicht gezeigt."), MCP: sp("`idea_text`, `idea_concept`, `decision_open`."), Areas: &[]string{"internal/idea", "internal/store"}}})
	old := mk(store.CreateFeatureInput{Message: "Übersicht alt", Content: feature.Patch{
		Name: sp("Alte Übersicht"), Summary: sp("Die Startseite vor dem Posteingang."), Behavior: sp("Zählte offene Aufträge und Wissen in Kacheln."), Areas: &[]string{"internal/web/overview.go"}}})
	_, err = robin.SetFeatureState(old.ID, feature.Retired, "Der Posteingang hat sie ersetzt.")
	must(err)
	_ = rooms
	// Ideas that change a feature.
	i, err := robin.CreateIdea(store.CreateIdeaInput{Project: project, Title: "Antworten im Posteingang per Sprache", Body: "Fragen laut beantworten, wenn die Hände nicht frei sind.", Actor: idea.Actor{AccountID: 1, Person: "Robin"}})
	must(err)
	_, err = robin.LinkFeature(inbox.ID, feature.KindIdea, i.ID, feature.RelAffects, "Robin")
	must(err)
	_ = werkstatt
}

func seedMachines(st *store.Store, project string) {
	robinWeb := store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}
	for _, m := range []struct{ account, name string }{{"person:1", "mainex"}, {"person:1", "build-1"}, {"person:2", "lena-laptop"}} {
		_, _, err := st.CreateDeviceToken(m.account, m.name)
		must(err)
	}
	_, err := st.ShareMachine(robinWeb, "build-1", "werkstatt", store.ShareWrite, "builder", "/srv/build")
	must(err)
	robin := st.Access(robinWeb)
	var shared []int64
	for _, k := range []struct{ title, body string }{
		{"Der Build-Cache liegt unter /var/cache/ghosttree", "Läuft er voll, brechen Builds ohne Fehlermeldung ab. `make clean-cache` räumt ihn auf."},
		{"Deploy nur über systemd-run mit User ghosttree", "Direktes sudo -u scheitert an der Umgebung des Dienstes."},
	} {
		id, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: k.title, Body: k.body, Scope: scope.Axes{Machine: "build-1"}, Person: "Robin", AuthorID: 1, Confidence: "trusted"})
		must(err)
		shared = append(shared, id)
	}
	must(st.ShareEntry(robinWeb, store.EntryKnowledge, shared[0], "werkstatt"))
	_, err = st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Die Platte von mainex füllt sich montags mit Sitzungsprotokollen", Body: "Montags rotiert nichts, bis der Timer läuft.", Scope: scope.Axes{Machine: "mainex"}, Person: "Robin", AuthorID: 1, Confidence: "trusted"})
	must(err)
	_, err = st.InsertKnowledge(store.Knowledge{Type: "decision", Title: "Releases laufen auf build-1, nicht lokal", Body: "Nur dort stimmen Go-Version und Signierschlüssel.", Scope: scope.Axes{Project: project, Machine: "build-1"}, Person: "Robin", AuthorID: 1, Confidence: "trusted"})
	must(err)
	_ = robin
	room := store.RoomKeyForProject(project)
	for _, a := range []struct{ id, name, machine, principal string }{
		{"claude:mainex:44444444-aaaa-4bbb-8ccc-000000000004", "claude-mainex", "mainex", "person:1"},
		{"claude:build-1:55555555-aaaa-4bbb-8ccc-000000000005", "claude-build", "build-1", "person:1"},
	} {
		_, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: a.id, Provider: "claude", RoomKey: room, PrincipalID: a.principal, DisplayName: a.name, Machine: a.machine, Branch: "feat/git-native"})
		must(err)
	}
	for _, s := range []struct{ ext, machine, title string }{
		{"seed-mainex-1", "mainex", "Wissen der Maschine bereinigen"}, {"seed-mainex-2", "mainex", "Posteingang: Filter für Gärtner"},
		{"seed-build-1", "build-1", "Release 0.14.0 bauen und signieren"},
	} {
		id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: s.ext, AccountID: 1, Scope: scope.Axes{Project: project, Machine: s.machine, Branch: "feat/git-native"}})
		must(err)
		must(st.AppendChunks(id, []store.Chunk{{Seq: 0, Raw: `{"type":"user","message":{"role":"user","content":"` + s.title + `"}}`}, {Seq: 1, Raw: `{"type":"ai-title","aiTitle":"` + s.title + `"}`}}))
	}
}

func seedWorkshop(st *store.Store, project string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	agent := idea.Actor{AccountID: 1, Person: "claude-mainex", Agent: true}
	lena := idea.Actor{AccountID: 2, Person: "Lena Berger"}
	human := idea.Actor{AccountID: 1, Person: "Robin"}
	i, err := robin.CreateIdea(store.CreateIdeaInput{Project: project, Title: "Rohideen aus Sprachnachrichten zu Konzepten ausarbeiten",
		Body: "Ich spreche eine Idee auf, ein Agent macht daraus ein Konzept, und wir entscheiden gemeinsam, wie es gebaut wird.", Actor: human})
	must(err)
	_, _, err = robin.PutIdeaText(i.ID, store.PutIdeaTextInput{Part: idea.PartRaw, Source: "Sprachnachricht 8.11.", Actor: agent,
		Body: "Also, ich hätte gern, dass ich eine Idee einfach aufspreche und ein Agent daraus erst mal eine Zusammenfassung macht. Dann soll er Fragen stellen, wo es mehrere Wege gibt."})
	must(err)
	_, _, err = robin.PutIdeaText(i.ID, store.PutIdeaTextInput{Part: idea.PartSummary, Actor: agent,
		Body: "Eine Rohidee wird zu Kurzfassung und Konzept. Wo es mehrere Wege gibt, stellt der Agent eine Frage mit Varianten, und ein Mensch wählt."})
	must(err)
	_, _, err = robin.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: agent, Message: "Erster Entwurf", Patch: idea.ConceptPatch{
		About: sp("Der Weg von der Sprachnachricht zur Freigabe."), What: sp("Die Idee-Seite zeigt Kurzfassung, Konzept und offene Fragen. Varianten stehen nebeneinander, ein Mensch wählt eine und sagt warum."),
		How: sp("Agenten legen die Texte und Fragen über MCP an. Die Auswahl und die Freigabe laufen nur in der Oberfläche."), Not: sp("Kein automatisches Entscheiden bei Sicherheit, Kosten oder Produktrichtung."),
		Questions:    sp("Wie lange liegt eine offene Frage, bevor sie im Posteingang erinnert?"),
		Requirements: &[]string{"Varianten stehen nebeneinander und lassen sich vergleichen", "Eine Entscheidung nennt immer einen Grund", "Nur ein Mensch gibt das Konzept frei"}}})
	must(err)
	d, err := robin.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Wo wird die Sprachnachricht in Text verwandelt?",
		Detail: "Das bestimmt Datenschutz, Kosten und Qualität.", Aspects: []string{idea.AspectCost, idea.AspectSecurity}, Actor: agent,
		Variants: []idea.VariantInput{
			{Title: "Im Browser", Text: "Die Spracherkennung des Browsers schreibt mit.", Pro: "Nichts verlässt den Rechner.", Contra: "Qualität hängt vom Browser ab."},
			{Title: "Auf dem Server", Text: "Ein Modell auf dem ghosttree-Server schreibt mit.", Pro: "Überall gleiche Qualität.", Contra: "Braucht Rechenzeit und Platz auf dem Server."},
			{Title: "Beim Anbieter", Text: "Ein Dienst im Netz schreibt mit.", Pro: "Beste Qualität ohne eigenen Betrieb.", Contra: "Die Aufnahme geht an Dritte, und es kostet je Minute."}}})
	must(err)
	_, err = robin.VoteDecision(d.ID, "B", "Gleiche Qualität für alle und nichts an Dritte", lena)
	must(err)
	_, err = robin.VoteDecision(d.ID, "A", "Am sparsamsten, solange die Qualität reicht", agent)
	must(err)
	d2, err := robin.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Wer darf das Konzept freigeben?",
		Aspects: []string{idea.AspectPeople}, Actor: agent, Variants: []idea.VariantInput{
			{Title: "Nur der Besitzer der Idee", Pro: "Klare Verantwortung."}, {Title: "Jeder Maintainer", Pro: "Niemand wartet auf eine Person.", Contra: "Mehrere Köpfe, mehr Streit."}}})
	must(err)
	_, err = robin.CloseDecision(store.CloseDecisionInput{ID: d2.ID, Outcome: idea.DecisionDecided, Variant: "B", Reason: "Der Besitzer ist im Urlaub nicht erreichbar", Actor: human})
	must(err)
	_, err = robin.StartIdeaDiscussion(i.ID)
	must(err)
	if inbox, err := robin.SearchFeatures(feature.Filter{Project: project, Query: "Werkstatt", Limit: 5}); err == nil && len(inbox) > 0 {
		_, err = robin.LinkFeature(inbox[0].ID, feature.KindIdea, i.ID, feature.RelAffects, "Robin")
		must(err)
	}
}
