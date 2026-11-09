package web

// Zeichen und Verweisarten der drei Seitenbereiche aus REQ-599 (Features,
// Maschinen, Ideen-Werkstatt). Sie kommen beim Start in die gemeinsamen Tabellen,
// damit icons16.go und identity.go unberührt bleiben.

func init() {
	for name, body := range map[string]string{
		// feature: ein Würfel, das fertige Stück des Produkts
		"feature": `<path d="M8 1.9 13.6 5v6L8 14.1 2.4 11V5z"/><path d="M2.6 5.1 8 8.2l5.4-3.1M8 8.2v5.8"/>`,
		// server: zwei Einschübe, die Maschine
		"server": `<rect x="2" y="2.5" width="12" height="4.5" rx="1.2"/><rect x="2" y="9" width="12" height="4.5" rx="1.2"/><path d="M4.6 4.75h.01M4.6 11.25h.01"/>`,
		// flask: der Kolben der Werkstatt
		"flask": `<path d="M6.2 2h3.6M6.7 2v4.2L3 12.6a1.1 1.1 0 0 0 1 1.7h8a1.1 1.1 0 0 0 1-1.7L9.3 6.2V2"/><path d="M4.6 10.2h6.8"/>`,
		// cols: zwei Spalten, Varianten nebeneinander
		"cols": `<rect x="2" y="3" width="5" height="10" rx="1.2"/><rect x="9" y="3" width="5" height="10" rx="1.2"/>`,
	} {
		mockIcons[name] = body
	}
	refKinds["feature"] = refKind{"FEAT", "k-feat", "ref.kind.feature", mockIcons["feature"]}
	repoTabIcons["features"] = "feature"
	pageAssets["features"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css", "ui-features.css"}}
	pageAssets["feature"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css", "ui-features.css"}}
	pageAssets["machine"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css", "ui-sessions.css", "ui-agents.css", "ui-machines.css"}}
	pageAssets["orgmachines"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-machines.css"}}
	// Die Werkstatt hängt an der Idee-Seite und lädt ihre Regeln dort mit.
	idea := pageAssets["idea"]
	pageAssets["idea"] = struct{ CSS, JS []string }{CSS: append(append([]string{}, idea.CSS...), "ui-workshop.css"), JS: idea.JS}
}
