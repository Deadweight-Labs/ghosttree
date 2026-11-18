package web

import (
	"fmt"
	"html"
	"html/template"
)

// htmlSentence setzt die Argumente in den Katalogtext von key ein und liefert
// HTML. Der Text und jedes string-Argument werden escaped; was schon HTML ist
// (eine Pill, ein Link), muss als template.HTML ankommen. So kann ein
// Aufrufer, der einen Namen oder Titel durchreicht, kein Markup einschleusen.
func htmlSentence(l lang, key string, args ...any) template.HTML {
	safe := make([]any, len(args))
	for i, a := range args {
		if s, ok := a.(string); ok {
			a = html.EscapeString(s)
		}
		safe[i] = a
	}
	return template.HTML(fmt.Sprintf(html.EscapeString(l.msg(key)), safe...))
}
