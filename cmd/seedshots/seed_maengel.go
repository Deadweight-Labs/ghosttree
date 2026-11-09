package main

import (
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedMaengel legt an, was die Sichtprüfung der Weboberfläche brauchte und im
// Seed fehlte: einen Thread im Projektraum (Seite 24), einen Ablauf mit Schritten
// (Seite 31), einen offenen Einladungscode für /join/<code> und ein Konto ohne
// Maschine, dessen Startseite die Einrichtung zeigt. Der Thread und der Ablauf
// sind Daten, die der Server selbst führt; der Code kommt in out["invite"], das
// Konto in out["fresh"].
func seedMaengel(st *store.Store, project, room string, orgID int64, out map[string]string) {
	robin := st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}, "")
	threadID, err := robin.CreateTaskThreadInRoom(room, "Wer gibt den Release frei?", "Brauchen wir dafür zwei Augenpaare oder reicht eine Person?", "")
	must(err)
	out["thread"] = strconv.FormatInt(threadID, 10)

	spec := runbook.Spec{
		Purpose:       "Den Release bauen, prüfen und signieren.",
		Params:        []runbook.Param{{Name: "tag", Type: runbook.TypeString, Description: "Versionsmarke, zum Beispiel v0.14.0"}},
		Preconditions: []string{"Der Branch ist gemergt", "Alle Prüfungen sind grün"},
		Steps: []runbook.Step{
			{Title: "Bauen", Command: "make build", Check: "Exit-Code 0"},
			{Title: "Prüfen", Command: "make check", Check: "keine Fehler"},
			{Title: "Signieren", Command: "make sign TAG=$tag"},
		},
		Rollback: []runbook.Step{{Title: "Marke zurücknehmen", Command: "git tag -d $tag"}},
	}
	_, err = st.WriteRunbook(store.RunbookWrite{Project: project, Name: "release-bauen", Spec: spec, Person: "Robin", AuthorID: 1, Approve: true})
	must(err)

	// Vorschläge, die auf Prüfung warten (Seite 21).
	for _, k := range []store.Knowledge{
		{Type: "pitfall", Title: "Qualifier-Filter müssen Branch-Vererbung berücksichtigen", Body: "Ein Filter auf feat/git-native zeigt Treffer von dev nicht.", Person: "claude"},
		{Type: "decision", Title: "Check „gemeinsam/checkliste“ blockiert erst nach zwei grünen Wochen", Body: "Bis dahin zeigt der Check nur an.", Person: "Robin"},
		{Type: "note", Title: "Die Weboberfläche ist eingebettet und braucht keinen Build-Schritt", Body: "Dateien liegen in internal/web/static.", Person: "claude"},
	} {
		k.Scope, k.Confidence = scope.Axes{Project: project}, "staged"
		_, err = st.InsertKnowledge(k)
		must(err)
	}

	code, _, err := st.CreateProjectInvitation("person:1", orgID, project, store.RoleMember, 0)
	must(err)
	out["invite"] = code

	_, err = st.AddAccount("Frisch", "", false)
	must(err)
	c, _, err := st.CreateAccountCode(store.CodeLogin, "Frisch")
	must(err)
	out["fresh"] = c
}
