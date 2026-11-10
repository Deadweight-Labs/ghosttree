package web

// Texte des Pakets P13b (Session-Einzelseite mit ghosttree-Ansicht, Kopf von Pain
// und Idee). Die Schlüssel sind in beiden Katalogen dieselben.

func init() {
	for key, text := range map[string][2]string{
		"sessions.ghost_view":            {"ghosttree view", "ghosttree-Ansicht"},
		"sessions.ev.started":            {"started the session", "hat die Session gestartet"},
		"sessions.ev.criterion_met":      {"met %[1]s of %[2]s", "hat %[1]s an %[2]s erfüllt"},
		"sessions.ev.criterion_waived":   {"waived %[1]s of %[2]s", "hat auf %[1]s an %[2]s verzichtet"},
		"sessions.ev.files":              {"touched %d files", "hat %d Dateien berührt"},
		"sessions.ev.files_one":          {"touched 1 file", "hat 1 Datei berührt"},
		"sessions.ev.more_files":         {"%d more files", "%d weitere Dateien"},
		"sessions.ev.knowledge":          {"wrote knowledge", "hat Wissen angelegt"},
		"sessions.ev.question":           {"asks in %s", "fragt in %s"},
		"sessions.ev.request":            {"opened %s", "hat %s angelegt"},
		"sessions.side.machine":          {"Machine", "Maschine"},
		"sessions.side.started":          {"started", "gestartet"},
		"sessions.side.areas":            {"Touched areas", "Berührte Bereiche"},
		"sessions.side.issues":           {"Issues", "Issues"},
		"sessions.side.made":             {"Made", "Entstanden"},
		"sessions.made.knowledge":        {"%d knowledge entries", "%d Wissenseinträge"},
		"sessions.made.knowledge_one":    {"1 knowledge entry", "1 Wissenseintrag"},
		"sessions.made.criteria":         {"%d requirements", "%d Anforderungen"},
		"sessions.made.criteria_one":     {"1 requirement", "1 Anforderung"},
		"sessions.made.commits":          {"%d commits", "%d Commits"},
		"sessions.made.commits_one":      {"1 commit", "1 Commit"},
		"sessions.made.files":            {"%d files", "%d Dateien"},
		"sessions.made.files_one":        {"1 file", "1 Datei"},
		"sessions.made.tasks":            {"%d tasks", "%d Aufgaben"},
		"sessions.made.tasks_one":        {"1 task", "1 Aufgabe"},
		"sessions.made.questions":        {"%d room questions", "%d Raumfragen"},
		"sessions.made.questions_one":    {"1 room question", "1 Raumfrage"},
		"pains.action.issue":             {"Issue from this", "Issue daraus"},
		"pains.result.corrected":         {"Saved.", "Gespeichert."},
		"pains.result.reason_required":   {"Say why this changes.", "Schreib, warum sich das ändert."},
		"pains.result.title_required":    {"The pain needs a title.", "Der Pain braucht einen Titel."},
		"pains.result.nothing_to_change": {"Nothing was changed.", "Es hat sich nichts geändert."},
		"ideas.result.corrected":         {"Saved.", "Gespeichert."},
		"ideas.result.correct_reason":    {"Say why this changes.", "Schreib, warum sich das ändert."},
		"ideas.result.correct_title":     {"The title is missing or too long.", "Der Titel fehlt oder ist zu lang."},
		"ideas.result.correct_body":      {"The text is too long.", "Der Text ist zu lang."},
		"ideas.result.correct_nothing":   {"Nothing was changed.", "Es hat sich nichts geändert."},
		"ideas.result.correct_changed":   {"The idea changed in the meantime. Open it again.", "Die Idee hat sich inzwischen geändert. Öffne sie neu."},
		"ideas.result.correct_failed":    {"Could not save.", "Konnte nicht speichern."},
		"ideas.ev.corrected_title":       {"corrected the title", "hat den Titel korrigiert"},
		"ideas.ev.corrected_body":        {"corrected the text", "hat den Text korrigiert"},
		"ideas.ev.corrected_both":        {"corrected title and text", "hat Titel und Text korrigiert"},
	} {
		messages[key] = text[0]
		messagesDE[key] = text[1]
	}
}
