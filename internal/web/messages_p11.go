package web

// Texte des Pakets P11 (Fehlerseiten, Formulargrenzen, Rechte). Die Schlüssel sind in
// beiden Katalogen dieselben.

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("err.form.title", "Not saved", "Nicht gespeichert")
	add("err.form.back", "Back to the form", "Zurück zum Formular")
	add("err.form.kept", "Your text", "Dein Text")
	add("err.form.invalid", "That input does not fit. Check the form and send it again.", "Die Eingabe passt nicht. Prüf das Formular und schick es noch einmal ab.")
	add("err.form.required", "Something is missing. Fill in the fields that are marked and send it again.", "Es fehlt eine Angabe. Füll die Pflichtfelder aus und schick es noch einmal ab.")
	add("err.form.expired", "The page was open too long. Your text is back in the form, send it again.", "Die Seite war zu lange offen. Dein Text steht wieder im Formular, schick ihn noch einmal ab.")
	add("err.form.forbidden", "Your role may not do this.", "Das darfst du mit deiner Rolle nicht.")
	add("err.form.gone", "That is gone, or you may not see it.", "Das gibt es nicht mehr, oder du darfst es nicht sehen.")
	add("err.form.conflict", "Something changed in the meantime. Reload the page and check whether the change is still needed.", "Inzwischen hat sich etwas geändert. Lad die Seite neu und prüf, ob die Änderung noch nötig ist.")
	add("err.form.nochange", "There was nothing to change.", "Es gab nichts zu ändern.")
	add("err.form.busy", "Too many requests. Wait a moment and try again.", "Zu viele Anfragen. Warte einen Moment und versuch es noch einmal.")
	add("err.form.failed", "Saving did not work. Your text is still here, try again in a moment.", "Das Speichern hat nicht geklappt. Dein Text steht noch da, versuch es gleich noch einmal.")
	add("err.form.toolarge", "The form is too large. A text may have up to %s characters.", "Das Formular ist zu groß. Ein Text darf höchstens %s Zeichen haben.")
	add("err.form.toolong", "The text is too long: %[2]s characters, at most %[1]s.", "Der Text ist zu lang: %[2]s Zeichen, erlaubt sind höchstens %[1]s.")
	add("runbooks.form.err.name", "The name may use lowercase letters, digits, \".\", \"_\" and \"-\" only, at most 64 characters, and starts with a letter or a digit.", "Der Name darf nur kleine Buchstaben, Ziffern, „.“, „_“ und „-“ enthalten, höchstens 64 Zeichen, und beginnt mit einem Buchstaben oder einer Ziffer.")
	add("runbooks.form.err.step_title", "A step needs a title.", "Ein Schritt braucht einen Titel.")
	add("runbooks.form.err.back_title", "A rollback step needs a title.", "Ein Rückweg-Schritt braucht einen Titel.")
	add("runbooks.form.err.long_title", "The title of a step is too long.", "Der Titel eines Schritts ist zu lang.")
	add("runbooks.form.err.param_name", "The name of a parameter starts with a lowercase letter and has lowercase letters, digits and \"_\" only.", "Der Name eines Parameters beginnt mit einem kleinen Buchstaben und hat nur kleine Buchstaben, Ziffern und „_“.")
	add("runbooks.form.err.param_twice", "Two parameters have the same name.", "Zwei Parameter heißen gleich.")
	add("runbooks.form.err.enum_values", "A choice needs 1 to 20 values, separated by commas.", "Eine Auswahl braucht 1 bis 20 Werte, durch Komma getrennt.")
	add("runbooks.form.err.default", "The default does not fit the type of its parameter. Letters, digits and . _ / : @ + , = - are allowed, up to 200 characters, and no \"-\" at the start.", "Der Standardwert passt nicht zum Typ seines Parameters. Erlaubt sind Buchstaben, Ziffern und . _ / : @ + , = -, bis 200 Zeichen, und kein „-“ am Anfang.")
	add("runbooks.form.err.undeclared", "A step uses {{%s}}, which is not a parameter of this runbook.", "Ein Schritt nutzt {{%s}}, das ist kein Parameter dieses Ablaufs.")
	add("runbooks.form.err.pitfalls", "Pitfalls are numbers of knowledge entries, for example 4294, 4293.", "Fallen sind Nummern von Wissenseinträgen, zum Beispiel 4294, 4293.")
	add("runbooks.form.err.keyword", "A keyword needs at least 3 characters.", "Ein Stichwort braucht mindestens 3 Zeichen.")
}
