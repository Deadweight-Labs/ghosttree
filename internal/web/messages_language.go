package web

// Texte zur Sprachwahl (REQ-715): der Umschalter DE | EN im Kontomenü und auf den
// Anmeldeseiten und der Abschnitt im Profil. Sie kommen beim Start in beide Kataloge.

var messagesLanguage = map[string]string{
	"shell.language":             "Language",
	"profile.language":           "Language",
	"profile.language_interface": "Interface language",
	"lang.short.de":              "DE",
	"lang.short.en":              "EN",
	"lang.name.de":               "Deutsch",
	"lang.name.en":               "English",
	"ui.filter_menu":             "Filter …",
	"wf.err.too_large":           "The file is too large.",
	"wf.err.edit_invalid":        "The edit could not be read.",
}

var messagesLanguageDE = map[string]string{
	"shell.language":             "Sprache",
	"profile.language":           "Sprache",
	"profile.language_interface": "Sprache der Oberfläche",
	"lang.short.de":              "DE",
	"lang.short.en":              "EN",
	"lang.name.de":               "Deutsch",
	"lang.name.en":               "English",
	"ui.filter_menu":             "Filter …",
	"wf.err.too_large":           "Die Datei ist zu groß.",
	"wf.err.edit_invalid":        "Die Änderung ließ sich nicht lesen.",
}

func init() {
	for k, v := range messagesLanguage {
		messages[k] = v
	}
	for k, v := range messagesLanguageDE {
		messagesDE[k] = v
	}
}
