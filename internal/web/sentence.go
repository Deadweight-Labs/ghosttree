package web

import (
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"
)

// sentenceOf fills the placeholders of a catalog message with markup. The message
// is escaped, the arguments are template.HTML: markup that is already safe, so a
// plain string cannot be passed by mistake (the compiler refuses it). Text of a
// person goes through escHTML, a number through numHTML. A %d in the message is
// filled like a %s, with the number as text.
func sentenceOf(l lang, key string, args ...template.HTML) template.HTML {
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = string(a)
	}
	msg := strings.ReplaceAll(html.EscapeString(l.msg(key)), "%d", "%s")
	return template.HTML(fmt.Sprintf(msg, vals...))
}

// escHTML is a text of a person as markup: escaped.
func escHTML(s string) template.HTML { return template.HTML(html.EscapeString(s)) }

// numHTML is a number as markup.
func numHTML(n int) template.HTML { return template.HTML(strconv.Itoa(n)) }
