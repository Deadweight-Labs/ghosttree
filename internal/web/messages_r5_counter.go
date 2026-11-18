package web

// Texts of the rest of the counter-check of run 5 (REQ-713): the summary of a
// run's results as they stand now, the fix link of an invalid workflow, and the
// note on a run that is still under way.

func init() {
	for _, m := range [][3]string{
		{"wf.sum.proposed", "%d waiting for review", "%d warten auf Review"},
		{"wf.sum.accepted", "%d accepted", "%d angenommen"},
		{"wf.sum.auto", "%d applied automatically", "%d automatisch übernommen"},
		{"wf.sum.undone", "%d undone", "%d rückgängig gemacht"},
		{"wf.sum.linked", "%d already open", "%d schon offen"},
		{"wf.sum.rejected", "%d refused", "%d abgelehnt"},
		{"workshop.ask.close_questions", "The concept still lists open questions. Close the discussion anyway?", "Das Konzept führt noch offene Fragen auf. Diskussion trotzdem beenden?"},
		{"workshop.answered", "Answered", "Beantwortet"},
		{"wf.fix.workflow", "Fix the workflow", "Workflow korrigieren"},
		{"wf.fix.workflow_view", "Open the workflow", "Workflow ansehen"},
		{"wf.active.queued", "Waiting for its turn. This page updates by itself.", "Wartet, bis er dran ist. Die Seite aktualisiert sich selbst."},
		{"wf.active.running", "Running. A run with a model takes one to three minutes; this page updates by itself.", "Läuft. Ein Lauf mit Modell dauert ein bis drei Minuten; die Seite aktualisiert sich selbst."},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
