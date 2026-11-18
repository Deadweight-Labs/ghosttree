package web

// Texts of the idea workshop in the browser (usability run 5, REQ-709): the forms that write a raw text, a summary,
// a concept and a question with variants, one button per step of the way, and the questions before a step that has a consequence.

func init() {
	for _, m := range [][3]string{
		{"workshop.tool.raw", "Add raw text", "Rohtext hinzufügen"},
		{"workshop.tool.summary", "Write a summary", "Kurzfassung schreiben"},
		{"workshop.tool.summary_edit", "Edit the summary", "Kurzfassung ändern"},
		{"workshop.tool.concept", "Write the concept", "Konzept schreiben"},
		{"workshop.tool.concept_edit", "Edit the concept", "Konzept bearbeiten"},
		{"workshop.tool.decision", "Open a decision", "Entscheidung öffnen"},
		{"workshop.empty", "Nothing worked out yet.", "Noch nichts ausgearbeitet."},
		{"workshop.save_text", "Save", "Speichern"},
		{"workshop.save_concept", "Save as a new version", "Als neue Fassung speichern"},
		{"workshop.ask_decision", "Open the decision", "Entscheidung öffnen"},
		{"workshop.cancel", "Cancel", "Abbrechen"},
		{"workshop.panel.raw", "Raw text", "Rohtext"},
		{"workshop.panel.source", "Source (voice message, meeting …)", "Quelle (Sprachnachricht, Meeting …)"},
		{"workshop.panel.requirements", "Requirements, one per line", "Anforderungen, eine pro Zeile"},
		{"workshop.panel.note", "Note on this version", "Notiz zur Fassung"},
		{"workshop.panel.question", "Question", "Frage"},
		{"workshop.panel.detail", "Details", "Einzelheiten"},
		{"workshop.panel.variant", "Variant %s", "Variante %s"},
		{"workshop.panel.variant_title", "Title", "Titel"},
		{"workshop.panel.variant_text", "Text", "Text"},
		{"workshop.panel.more", "More variants", "Weitere Varianten"},
		{"workshop.add_variant", "Add a variant", "Variante hinzufügen"},
		{"workshop.add_variant_submit", "Add", "Hinzufügen"},
		{"workshop.stand.done_one", "The decision is made", "Die Entscheidung ist getroffen"},
		{"workshop.ask.close", "%d decisions are still open. Close the discussion anyway?", "%d Entscheidungen sind noch offen. Diskussion trotzdem beenden?"},
		{"workshop.ask.approve", "The concept has no requirements, so no criteria will come out of it. Approve anyway?", "Das Konzept hat keine Anforderungen, daraus entstehen keine Kriterien. Trotzdem freigeben?"},
		{"workshop.ask.close_yes", "Close anyway", "Trotzdem beenden"},
		{"workshop.ask.approve_yes", "Approve anyway", "Trotzdem freigeben"},
		{"workshop.done.text_raw", "Raw text saved.", "Rohtext gespeichert."},
		{"workshop.done.text_summary", "Summary saved.", "Kurzfassung gespeichert."},
		{"workshop.done.concept_saved", "New version of the concept saved.", "Neue Fassung des Konzepts gespeichert."},
		{"workshop.done.concept_same", "Nothing changed in the concept.", "Am Konzept hat sich nichts geändert."},
		{"workshop.done.asked", "Decision opened.", "Entscheidung geöffnet."},
		{"workshop.done.variant", "Variant added.", "Variante hinzugefügt."},
		{"workshop.fail.raw", "The raw text is empty or has characters that cannot be kept.", "Der Rohtext ist leer oder enthält Zeichen, die sich nicht speichern lassen."},
		{"workshop.fail.raw_long", "The raw text is too long. Save it in parts; each part is its own version.", "Der Rohtext ist zu lang. Speichere ihn in Teilen; jeder Teil ist eine eigene Fassung."},
		{"workshop.fail.summary", "The summary is empty.", "Die Kurzfassung ist leer."},
		{"workshop.fail.section", "A section is too long or has characters that cannot be kept.", "Ein Abschnitt ist zu lang oder enthält Zeichen, die sich nicht speichern lassen."},
		{"workshop.fail.requirement", "A requirement is one line, with at most 500 characters, and there are at most 30.", "Eine Anforderung ist eine Zeile mit höchstens 500 Zeichen, und es sind höchstens 30."},
		{"workshop.fail.note", "The note is one line.", "Die Notiz ist eine Zeile."},
		{"workshop.fail.source", "The source is one line.", "Die Quelle ist eine Zeile."},
		{"workshop.fail.question", "The question is missing, or it is more than one line.", "Die Frage fehlt oder ist länger als eine Zeile."},
		{"workshop.fail.detail", "The details are too long or have characters that cannot be kept.", "Die Einzelheiten sind zu lang oder enthalten Zeichen, die sich nicht speichern lassen."},
		{"workshop.fail.variant_title", "Every variant needs a title of one line.", "Jede Variante braucht einen Titel in einer Zeile."},
		{"workshop.fail.variant_long", "A variant text is too long or has characters that cannot be kept.", "Ein Text der Variante ist zu lang oder enthält Zeichen, die sich nicht speichern lassen."},
		{"workshop.fail.variants_few", "A choice needs at least two variants.", "Eine Auswahl braucht mindestens zwei Varianten."},
		{"workshop.fail.variants_many", "There are already as many variants as a choice can have.", "Es gibt schon so viele Varianten, wie eine Auswahl haben kann."},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
