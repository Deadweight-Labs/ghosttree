package web

// Texte der offenen Oberflächen-Kriterien (REQ-599, REQ-628): Paketfilter der
// Listen, Qualifier-Vorschläge im Suchfeld, Schnellsuche mit Strg+K. Sie
// kommen beim Start in beide Kataloge; die Schlüssel sind dieselben.

var messagesOpenAC = map[string]string{
	"filter.package":     "Package",
	"filter.any_package": "Any package",
}

var messagesOpenACDE = map[string]string{
	"filter.package":     "Paket",
	"filter.any_package": "Jedes Paket",
}

func init() {
	for k, v := range messagesOpenAC {
		messages[k] = v
	}
	for k, v := range messagesOpenACDE {
		messagesDE[k] = v
	}
}
