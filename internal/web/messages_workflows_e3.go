package web

// Texts of stage E3 of the workflows (REQ-696): the closed discussion in the
// idea workshop, the templates in the tab Workflows, the new kinds of result.

func init() {
	for _, m := range [][3]string{
		{"workshop.phase.discussed", "Discussion closed", "Diskussion beendet"},
		{"workshop.close", "Close the discussion", "Diskussion abschließen"},
		{"workshop.reopen_discussion", "Reopen the discussion", "Diskussion wieder öffnen"},
		{"workshop.done.close", "The discussion is closed.", "Die Diskussion ist beendet."},
		{"workshop.done.reopen_discussion", "The discussion is open again.", "Die Diskussion ist wieder offen."},
		{"workshop.closed_by", "Closed by", "Beendet von"},
		{"workshop.closed_agent", "%s’s agent", "Agent von %s"},
		{"wf.tpl.choose", "Template", "Vorlage"},
		{"wf.tpl.create", "From template", "Aus Vorlage"},
		{"wf.tpl.wissen_nach_merge", "Knowledge after a merge", "Wissen nach Merge"},
		{"wf.tpl.wuensche_im_pr", "Wishes in the pull request", "Wünsche im PR"},
		{"wf.tpl.idee_abgleich", "Match a new idea", "Idee-Abgleich"},
		{"wf.tpl.kriterien_aus_diskussion", "Criteria from a discussion", "Kriterien aus Diskussion"},
		{"wf.kind.requirement", "Requirement", "Anforderung"},
		{"wf.kind.comment", "Comment", "Kommentar"},
		{"wf.result.linked", "Already open", "Schon offen"},
		{"wf.result.why.exists", "The wish is in this issue", "Der Wunsch steht in diesem Issue"},
	} {
		messages[m[0]] = m[1]
		messagesDE[m[0]] = m[2]
	}
}
