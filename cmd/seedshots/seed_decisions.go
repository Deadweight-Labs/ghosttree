package main

import (
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedDecisions adds open decisions that wait for Robin besides the one of the
// workshop idea: two on issues (one with three variants, one with a long text and
// two), asked by an agent of Robin's, and one on an idea without variants, which
// the inbox shows without buttons.
func seedDecisions(st *store.Store, project string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	agent := idea.Actor{AccountID: 1, Person: "claude-mainex", Agent: true, SessionRef: "seed-mainex-1"}
	hits, err := st.SearchRequestsWith(robin.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 20}), store.SearchAuto)
	must(err)
	byTitle := map[string]int64{}
	for _, h := range hits.Results {
		byTitle[h.Request.Title] = h.Request.ID
	}
	ask := func(subject string, id int64, in store.OpenDecisionInput) {
		in.Subject, in.SubjectID, in.Actor, in.Way = subject, id, agent, idea.WayHuman
		_, err := robin.OpenDecision(in)
		must(err)
	}
	if id := byTitle["Connect Stripe for billing"]; id != 0 {
		ask(idea.SubjectRequest, id, store.OpenDecisionInput{
			Question: "Wie sollen Rechnungen an Kunden gehen?",
			Detail:   "Stripe kann Rechnungen selbst verschicken oder nur die Daten liefern. Das bestimmt, wer Mails mit Rechnungen in Händen hält und wie viel Betrieb wir übernehmen.",
			Aspects:  []string{idea.AspectCost, idea.AspectSecurity},
			Variants: []idea.VariantInput{
				{Title: "Stripe verschickt", Pro: "Kein eigener Mailversand.", Contra: "Layout und Absender sind begrenzt."},
				{Title: "Wir verschicken", Pro: "Eigenes Layout, eigener Absender.", Contra: "Wir betreiben den Versand."},
				{Title: "Erst nur im Konto", Pro: "Der kleinste Schritt.", Contra: "Kunden müssen selbst nachsehen."}}})
	}
	if id := byTitle["Migrate the schema to the new store"]; id != 0 {
		ask(idea.SubjectRequest, id, store.OpenDecisionInput{
			Question: "Läuft die Migration vor dem nächsten Release oder danach?",
			Detail:   "Die Migration berührt jede Tabelle des Ledgers. Läuft sie vorher, hängt der Release an ihr; läuft sie danach, bleibt ein Release mit dem alten Schema draußen.",
			Aspects:  []string{idea.AspectProduct},
			Variants: []idea.VariantInput{{Title: "Vor dem Release", Pro: "Ein Schema für alle."}, {Title: "Nach dem Release", Pro: "Der Release bleibt klein."}}})
	}
	ideas, err := robin.SearchIdeas(idea.Filter{Project: project, State: "open", Limit: 10})
	must(err)
	for _, i := range ideas {
		if i.Title == "Voice control in the inbox" {
			ask(idea.SubjectIdea, i.ID, store.OpenDecisionInput{Question: "Soll die Spracheingabe im Posteingang zuerst nur am Handy erscheinen?", Aspects: []string{idea.AspectProduct}})
		}
	}
}
