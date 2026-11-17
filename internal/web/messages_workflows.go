package web

// Texts of the tab "Workflows" (REQ-693): the runs of a repository, the page of
// a run, the block at the pull request and the retention in the organization's
// settings. They go into both catalogs at start.

var messagesWorkflows = map[string]string{
	"repo.tab.workflows":              "Workflows",
	"wf.empty":                        "No workflow has run yet",
	"wf.n_one":                        "1 run",
	"wf.n":                            "%d runs",
	"wf.state.queued":                 "Queued",
	"wf.state.running":                "Running",
	"wf.state.done":                   "Done",
	"wf.state.failed":                 "Failed",
	"wf.state.budget":                 "Budget",
	"wf.state.skipped":                "Skipped",
	"wf.event.pr.merged":              "Merged",
	"wf.event.idea.created":           "Idea created",
	"wf.event.idea.discussion_closed": "Discussion closed",
	"wf.event.schedule":               "Schedule",
	"wf.reason.chain":                 "Caused by another run",
	"wf.reason.chain_depth":           "Chain longer than two runs",
	"wf.reason.unknown_workflow":      "Unknown workflow",
	"wf.reason.interrupted":           "Interrupted by a restart",
	"wf.reason.timeout":               "Took longer than the time limit",
	"wf.reason.rate_limit":            "Too many runs in the last hour",
	"wf.result":                       "Result",
	"wf.reason":                       "Reason",
	"wf.inputs":                       "Input",
	"wf.output":                       "Output",
	"wf.purged":                       "Full text deleted on %s",
	"wf.side":                         "About this run",
	"wf.cost":                         "Cost",
	"wf.version":                      "Version",
	"wf.model":                        "Model",
	"wf.started":                      "Started",
	"wf.finished":                     "Finished",
	"wf.chain":                        "Chain",
	"wf.chain_n":                      "Run %d of the chain",
	"wf.retention":                    "Keep the full text of workflow runs",
	"wf.retention.days":               "days",
	"wf.retention.save":               "Save",
	"wf.retention.saved":              "Retention saved",
}

var messagesWorkflowsDE = map[string]string{
	"repo.tab.workflows":              "Workflows",
	"wf.empty":                        "Noch kein Workflow gelaufen",
	"wf.n_one":                        "1 Lauf",
	"wf.n":                            "%d Läufe",
	"wf.state.queued":                 "Wartet",
	"wf.state.running":                "Läuft",
	"wf.state.done":                   "Fertig",
	"wf.state.failed":                 "Fehlgeschlagen",
	"wf.state.budget":                 "Budget",
	"wf.state.skipped":                "Übersprungen",
	"wf.event.pr.merged":              "Gemergt",
	"wf.event.idea.created":           "Idee angelegt",
	"wf.event.idea.discussion_closed": "Diskussion beendet",
	"wf.event.schedule":               "Zeitplan",
	"wf.reason.chain":                 "Von einem anderen Lauf ausgelöst",
	"wf.reason.chain_depth":           "Kette länger als zwei Läufe",
	"wf.reason.unknown_workflow":      "Unbekannter Workflow",
	"wf.reason.interrupted":           "Durch einen Neustart unterbrochen",
	"wf.reason.timeout":               "Hat die Zeitgrenze überschritten",
	"wf.reason.rate_limit":            "Zu viele Läufe in der letzten Stunde",
	"wf.result":                       "Ergebnis",
	"wf.reason":                       "Grund",
	"wf.inputs":                       "Eingabe",
	"wf.output":                       "Ausgabe",
	"wf.purged":                       "Volltext gelöscht am %s",
	"wf.side":                         "Zu diesem Lauf",
	"wf.cost":                         "Kosten",
	"wf.version":                      "Version",
	"wf.model":                        "Modell",
	"wf.started":                      "Gestartet",
	"wf.finished":                     "Beendet",
	"wf.chain":                        "Kette",
	"wf.chain_n":                      "Lauf %d der Kette",
	"wf.retention":                    "Volltext von Workflow-Läufen behalten",
	"wf.retention.days":               "Tage",
	"wf.retention.save":               "Speichern",
	"wf.retention.saved":              "Aufbewahrung gespeichert",
}

func init() {
	for k, v := range messagesWorkflows {
		messages[k] = v
	}
	for k, v := range messagesWorkflowsDE {
		messagesDE[k] = v
	}
}
