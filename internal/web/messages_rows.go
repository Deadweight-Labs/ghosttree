package web

// Texte der Listenzeilen und der Listenfilter (UI-Durchsicht P5): was eine Liste
// sagt, wenn der Filter nichts übrig lässt, und die Einzahl der Zähler, die der
// Filter mitzählt. Sie kommen beim Start in beide Kataloge.

var messagesRows = map[string]string{
	"list.no_match":  "Nothing matches “%s”.",
	"list.clear":     "Clear the filter",
	"runbooks.n_one": "1 runbook",
	"docs.n_one":     "1 document",
}

var messagesRowsDE = map[string]string{
	"list.no_match":  "Nichts passt zu „%s“.",
	"list.clear":     "Filter zurücksetzen",
	"runbooks.n_one": "1 Ablauf",
	"docs.n_one":     "1 Dokument",
}

func init() {
	for k, v := range messagesRows {
		messages[k] = v
	}
	for k, v := range messagesRowsDE {
		messagesDE[k] = v
	}
}
