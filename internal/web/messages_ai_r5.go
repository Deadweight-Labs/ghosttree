package web

// Texts of usability run 5, package WP-R5-5 (REQ-707): the refusals of the
// connection form at the field, the model rows, the test of a connection, the
// release of the server's connections with one cap per organization, and the
// policy of the workflows.

func init() {
	for _, m := range [][3]string{
		// Form of a connection: the reason at the field.
		{"ai.f.name.missing", "A name is missing.", "Es fehlt ein Name."},
		{"ai.f.name.long", "At most 40 characters.", "Höchstens 40 Zeichen."},
		{"ai.f.name.chars", "Letters, digits, dot, dash and underscore only.", "Nur Buchstaben, Ziffern, Punkt, Bindestrich und Unterstrich."},
		{"ai.f.format", "Choose anthropic or openai.", "Wähle anthropic oder openai."},
		{"ai.f.url.missing", "An address is missing, for example https://api.anthropic.com", "Es fehlt eine Adresse, zum Beispiel https://api.anthropic.com"},
		{"ai.f.url.invalid", "Not an address. It starts with https://, for example https://api.anthropic.com", "Das ist keine Adresse. Sie beginnt mit https://, zum Beispiel https://api.anthropic.com"},
		{"ai.f.url.parts", "The address has no user, no query and no fragment.", "Die Adresse hat keinen Benutzer, keine Abfrage und kein Fragment."},
		{"ai.f.url.https", "Only https addresses on the public internet. http is not accepted here.", "Nur https-Adressen im öffentlichen Netz. http wird hier nicht angenommen."},
		{"ai.f.url.https_server", "https, or http only to this machine (localhost, 127.0.0.1).", "https, oder http nur zu diesem Rechner (localhost, 127.0.0.1)."},
		{"ai.f.key.missing", "A new connection needs a key.", "Eine neue Anbindung braucht einen Schlüssel."},
		{"ai.f.key.moved", "A new address or format needs the key again; the stored key never goes to an address it was not entered for.", "Bei neuer Adresse oder neuem Format den Schlüssel noch einmal eingeben: der gespeicherte geht nie an eine Adresse, für die er nicht eingegeben wurde."},
		{"ai.f.key.again", "Enter the key again. It is never shown after saving, also not here.", "Den Schlüssel noch einmal eingeben. Er wird nach dem Speichern nie wieder angezeigt, auch hier nicht."},
		{"ai.f.models.empty", "At least one model with its two prices.", "Mindestens ein Modell mit seinen zwei Preisen."},
		{"ai.f.models.many", "At most 20 models.", "Höchstens 20 Modelle."},
		{"ai.f.models.rows", "Some rows need a look.", "Einige Zeilen brauchen einen zweiten Blick."},
		{"ai.f.model.name_missing", "The model has no name.", "Das Modell hat keinen Namen."},
		{"ai.f.model.name", "A model name has no spaces and no slash, at most 100 characters.", "Ein Modellname hat keine Leerzeichen und keinen Schrägstrich, höchstens 100 Zeichen."},
		{"ai.f.model.dup", "This model is listed twice.", "Dieses Modell steht doppelt da."},
		{"ai.f.model.price", "Both prices are numbers from 0 to 10000, for example 0.10 and 0.50.", "Beide Preise sind Zahlen von 0 bis 10000, zum Beispiel 0.10 und 0.50."},
		{"ai.f.cap", "A number from 0 to 1,000,000, or empty for no cap.", "Eine Zahl von 0 bis 1.000.000, oder leer für keinen Deckel."},
		{"ai.f.form", "The connection could not be saved. Check the fields.", "Die Anbindung konnte nicht gespeichert werden. Prüfe die Felder."},
		{"ai.err.alias", "An alias has letters, digits, dash and underscore, and names a model of the connection.", "Ein Alias besteht aus Buchstaben, Ziffern, Bindestrich und Unterstrich und nennt ein Modell der Anbindung."},
		{"ai.err.loan", "A loan needs a cap above 0.", "Eine Leihgabe braucht einen Deckel über 0."},
		{"ai.err.test_off", "This server cannot test connections.", "Dieser Server kann Anbindungen nicht testen."},
		// The model rows of the form.
		{"ai.model.name", "Model", "Modell"},
		{"ai.model.in", "USD per million tokens in", "USD je Million Tokens ein"},
		{"ai.model.out", "out", "aus"},
		{"ai.model.add", "Add model", "Modell hinzufügen"},
		{"ai.model.remove", "Remove model", "Modell entfernen"},
		{"ai.key.unchanged", "unchanged", "unverändert"},
		// Costs and the server's release on the page of an organization.
		{"ai.test.workflow", "Connection test", "Verbindungstest"},
		{"ai.released_cap", "Released by the server, cap %s per month", "Vom Server freigegeben, Deckel %s im Monat"},
		{"ai.released_nocap", "Released by the server, no cap", "Vom Server freigegeben, ohne Deckel"},
		{"ai.released.label", "Released by the server", "Vom Server freigegeben"},
		{"ai.released.per_month", "%s per month", "%s im Monat"},
		// Test connection.
		{"ai.test.button", "Test connection", "Verbindung testen"},
		{"ai.test.title", "One tiny call, booked as cost", "Ein winziger Aufruf, wird als Kosten gebucht"},
		{"ai.test.ok", "The connection answers (model %[1]s, %[2]d ms). The test cost %[3]s.", "Die Anbindung antwortet (Modell %[1]s, %[2]d ms). Der Test hat %[3]s gekostet."},
		{"ai.test.key", "The provider refuses the key (HTTP %s). Enter the key again.", "Der Anbieter lehnt den Schlüssel ab (HTTP %s). Gib den Schlüssel neu ein."},
		{"ai.test.not_found", "%[1]s does not know this address or the model %[2]s (HTTP 404). Check the address and the model name.", "%[1]s kennt diese Adresse oder das Modell %[2]s nicht (HTTP 404). Prüfe Adresse und Modellname."},
		{"ai.test.request", "The provider refused the request (HTTP %[1]s). Check the model name %[2]s.", "Der Anbieter hat die Anfrage abgelehnt (HTTP %[1]s). Prüfe den Modellnamen %[2]s."},
		{"ai.test.payment", "The provider asks for payment or credit (HTTP 402).", "Der Anbieter verlangt Zahlung oder Guthaben (HTTP 402)."},
		{"ai.test.rate", "The provider is slowing calls down (HTTP 429). Try again in a minute.", "Der Anbieter bremst gerade (HTTP 429). Versuche es in einer Minute noch einmal."},
		{"ai.test.server", "The provider reports an error (HTTP %s). Try again later.", "Der Anbieter meldet einen Fehler (HTTP %s). Versuche es später noch einmal."},
		{"ai.test.timeout", "No answer from %[1]s within %[2]d seconds.", "Keine Antwort von %[1]s innerhalb von %[2]d Sekunden."},
		{"ai.test.unreachable", "%s cannot be reached: the name does not resolve, or the connection is refused.", "%s ist nicht erreichbar: Der Name lässt sich nicht auflösen, oder die Verbindung wird verweigert."},
		{"ai.test.tls", "The certificate of %s does not fit this address.", "Das Zertifikat von %s passt nicht zu dieser Adresse."},
		{"ai.test.address", "This address is not allowed for a connection: only https to public addresses.", "Diese Adresse ist für eine Anbindung nicht erlaubt: nur https zu öffentlichen Adressen."},
		{"ai.test.no_key", "The stored key cannot be opened here. Enter the key again.", "Der gespeicherte Schlüssel lässt sich hier nicht öffnen. Gib den Schlüssel neu ein."},
		{"ai.test.cap", "A cap stops the test: %s.", "Ein Deckel stoppt den Test: %s."},
		{"ai.test.other", "Unexpected answer (HTTP %s).", "Unerwartete Antwort (HTTP %s)."},
		{"ai.test.rate_limit", "Too many tests. Try again in %d seconds.", "Zu viele Tests. Versuche es in %d Sekunden noch einmal."},
		// Release of the server's connections.
		{"ai.release.cap", "Standard cap per organization and month", "Standard-Deckel je Organisation und Monat"},
		{"ai.grant.col_org", "Organization", "Organisation"},
		{"ai.grant.col_cap", "Cap per month", "Deckel im Monat"},
		{"ai.grant.col_used", "This month", "Dieser Monat"},
		{"ai.grant.default", "Standard", "Standard"},
		{"ai.grant.add", "Make available", "Freigeben"},
		{"ai.grant.set", "Set cap", "Deckel festlegen"},
		{"ai.grant.save", "Save", "Speichern"},
		{"ai.grant.none", "No organization released yet", "Noch keine Organisation freigegeben"},
		{"ai.grant.no_exceptions", "No exceptions: every organization has the standard cap", "Keine Ausnahmen: jede Organisation hat den Standard-Deckel"},
		{"ai.grant.org", "Organization", "Organisation"},
		{"ai.grant.none_off", "No organization may use the server's connections", "Keine Organisation darf die Anbindungen des Servers nutzen"},
		// Policy of the workflows.
		{"wf.retention.title", "Retention", "Aufbewahrung"},
		{"wf.policy.title", "Policy", "Richtlinie"},
		{"wf.policy.saved", "Policy saved", "Richtlinie gespeichert"},
		{"wf.policy.forbid", "Forbidden abilities", "Verbotene Fähigkeiten"},
		{"wf.policy.forbid_kinds", "Forbidden results", "Verbotene Ergebnisse"},
		{"wf.policy.auto", "Apply without review", "Ohne Prüfung übernehmen"},
		{"wf.policy.instruction", "Blocks with an instruction of the team", "Bausteine mit Anweisung des Teams"},
		{"wf.policy.prompt", "Free prompts with tools", "Freie Prompts mit Werkzeugen"},
		{"wf.policy.research", "Research on the web", "Recherche im Netz"},
		{"wf.policy.affects", "affects: %s", "betrifft: %s"},
		{"wf.policy.auto_members", "This applies only when the quotes come from text written by members; text from pull requests or web pages always waits for a person.", "Das gilt nur, wenn die Belege aus Text von Mitgliedern stammen; Text aus Pull Requests oder dem Netz wartet immer auf eine Person."},
		{"wf.policy.auto_warn", "Results of these kinds count at once and can only be undone afterwards.", "Ergebnisse dieser Arten gelten sofort und lassen sich nur nachträglich zurücknehmen."},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
