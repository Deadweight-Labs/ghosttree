package web

// Texts of stage E5 of the workflows (REQ-698): the tool calls of an own prompt
// on the page of its run.

func init() {
	for _, m := range [][3]string{
		{"wf.tools", "Tool calls", "Werkzeugaufrufe"},
		{"wf.tool.round", "Round %d", "Runde %d"},
		{"wf.tool.ok", "Done", "Ausgeführt"},
		{"wf.tool.failed", "Failed", "Fehlgeschlagen"},
		{"wf.tool.refused", "Refused", "Abgewiesen"},
		{"wf.tool.why.not_declared", "Not declared in the workflow", "Im Workflow nicht erklärt"},
		{"wf.tool.why.per_round", "Too many calls in one round", "Zu viele Aufrufe in einer Runde"},
		{"wf.tool.input", "Input", "Eingabe"},
		{"wf.tool.result", "Result", "Ergebnis"},
		{"wf.tool.answer", "Handed in", "Abgegeben"},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
