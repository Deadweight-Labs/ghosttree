package web

// Texts of the page of a workflow run that say how it ended (S02, S12, S28 of
// usability run 5): the reason in words, where it is fixed and by whom, the
// rerun, the proposals waiting in review, and how long the run took.

func init() {
	for _, m := range [][3]string{
		{"wf.why", "What happened", "Was passiert ist"},
		{"wf.why.raw", "Message in full", "Meldung im Wortlaut"},
		{"wf.why.no_proxy", "This server gives workflows no access to a model.", "Dieser Server gibt Workflows keinen Zugang zu einem Modell."},
		{"wf.why.model_unavailable", "The model this workflow names is not released for this repository.", "Das Modell, das dieser Workflow nennt, ist für dieses Repo nicht freigegeben."},
		{"wf.why.key_unusable", "The key of the connection cannot be read on the server.", "Der Schlüssel der Anbindung lässt sich auf dem Server nicht lesen."},
		{"wf.why.address_not_allowed", "The address of the connection is not allowed: it needs https and a public host.", "Die Adresse der Anbindung ist nicht erlaubt: Sie braucht https und einen öffentlichen Rechner."},
		{"wf.why.no_answer", "The provider did not answer.", "Der Anbieter hat nicht geantwortet."},
		{"wf.why.no_tools", "The model of this connection cannot use tools, and an own prompt needs them.", "Das Modell dieser Anbindung kann keine Werkzeuge benutzen, ein eigener Prompt braucht sie aber."},
		{"wf.why.answer_unusable", "The model answered, but not in the form the workflow expects. Running it again often helps, a larger model more so.", "Das Modell hat geantwortet, aber nicht in der Form, die der Workflow erwartet. Erneut ausführen hilft oft, ein größeres Modell eher."},
		{"wf.why.http404", "No model answers at the address of the connection (HTTP 404). Usually the base address is wrong.", "Unter der Adresse der Anbindung antwortet kein Modell (HTTP 404). Meist stimmt die Basisadresse nicht."},
		{"wf.why.key_refused", "The provider refused the key of the connection.", "Der Anbieter hat den Schlüssel der Anbindung abgelehnt."},
		{"wf.why.rate", "The provider is holding back requests right now. Run it again later.", "Der Anbieter bremst gerade die Anfragen. Später erneut ausführen."},
		{"wf.why.bad_request", "The provider refused the request (HTTP 400). Often the model name does not fit the connection.", "Der Anbieter hat die Anfrage abgelehnt (HTTP 400). Oft passt der Modellname nicht zur Anbindung."},
		{"wf.why.provider_down", "The provider had a fault. Run it again later.", "Beim Anbieter ist ein Fehler aufgetreten. Später erneut ausführen."},
		{"wf.why.http", "The provider answered with an error.", "Der Anbieter hat mit einem Fehler geantwortet."},
		{"wf.why.other", "The run broke off with an error.", "Der Lauf ist mit einem Fehler abgebrochen."},
		{"wf.fix.connection", "AI connections", "KI-Anbindungen"},
		{"wf.fix.policy", "Workflow policy", "Workflow-Richtlinie"},
		{"wf.who.ai", "Connections and caps are kept by the owners of %s.", "Anbindungen und Deckel pflegen die Owner von %s."},
		{"wf.who.policy", "The policy is set by the owners of %s.", "Die Richtlinie legen die Owner von %s fest."},
		{"wf.rerun", "Run again", "Erneut ausführen"},
		{"wf.output.none", "No output: the run ended before a model answered.", "Keine Ausgabe: Der Lauf endete, bevor ein Modell geantwortet hat."},
		{"wf.duration", "Duration", "Dauer"},
		{"wf.duration.under_second", "under a second", "unter einer Sekunde"},
		{"wf.duration.seconds", "%d s", "%d s"},
		{"wf.duration.minutes", "%d min", "%d min"},
		{"wf.cost.none", "No model call", "Kein Modellaufruf"},
		{"wf.review.waiting", "%d proposals wait in review", "%d Vorschläge warten im Review"},
		{"wf.review.waiting_one", "1 proposal waits in review", "1 Vorschlag wartet im Review"},
		{"wf.review.open", "Review", "Prüfen"},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
