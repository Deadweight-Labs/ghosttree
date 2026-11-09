package web

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

var boldRE = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)

// renderDescription macht aus dem Markdown einer Beschreibung HTML. Es kennt
// nur, was Beschreibungen brauchen: Absätze, Überschriften, Listen, Zitate,
// Code in Backticks und Fettdruck. Alles andere bleibt Text. Der Text wird
// zuerst maskiert und erst danach ausgezeichnet, eine Beschreibung kann also
// weder Markup noch Links einschleusen (die Seite lädt nur von 'self').
func renderDescription(src string) template.HTML {
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	for _, block := range splitBlocks(src) {
		lines := strings.Split(block, "\n")
		switch {
		case strings.HasPrefix(lines[0], "# ") || strings.HasPrefix(lines[0], "## ") || strings.HasPrefix(lines[0], "### "):
			tag := "h2"
			if !strings.HasPrefix(lines[0], "# ") {
				tag = "h3"
			}
			text := strings.TrimSpace(strings.TrimLeft(lines[0], "# "))
			b.WriteString("<" + tag + ">" + inlineMD(text) + "</" + tag + ">")
			if len(lines) > 1 {
				b.WriteString("<p>" + inlineMD(strings.Join(lines[1:], " ")) + "</p>")
			}
		case allPrefixed(lines, "> ", ">"):
			var text []string
			for _, l := range lines {
				text = append(text, strings.TrimSpace(strings.TrimPrefix(l, ">")))
			}
			b.WriteString("<blockquote><p>" + inlineMD(strings.Join(text, " ")) + "</p></blockquote>")
		case allPrefixed(lines, "- ", "* "):
			b.WriteString("<ul>")
			for _, l := range lines {
				b.WriteString("<li>" + inlineMD(strings.TrimSpace(l[2:])) + "</li>")
			}
			b.WriteString("</ul>")
		default:
			b.WriteString("<p>" + inlineMD(strings.Join(lines, " ")) + "</p>")
		}
	}
	return template.HTML(b.String())
}

// splitBlocks trennt an Leerzeilen.
func splitBlocks(s string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, strings.TrimRight(l, " \t"))
	}
	flush()
	return out
}

func allPrefixed(lines []string, prefixes ...string) bool {
	for _, l := range lines {
		ok := false
		for _, p := range prefixes {
			if strings.HasPrefix(l, p) && len(l) >= 2 {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// inlineMD zeichnet Code und Fettdruck aus. Ein Backtick ohne Gegenstück bleibt
// ein Zeichen.
func inlineMD(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		// Ungerade Zahl von Backticks: der letzte hat kein Gegenstück.
		parts = []string{strings.ReplaceAll(s, "`", "\x00")}
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + html.EscapeString(p) + "</code>")
			continue
		}
		esc := html.EscapeString(p)
		esc = boldRE.ReplaceAllString(esc, "<strong>$1</strong>")
		b.WriteString(strings.ReplaceAll(esc, "\x00", "`"))
	}
	return b.String()
}

// plainLine ist eine Beschreibung als eine Zeile ohne Auszeichnung, für die
// Spalte der Tabelle.
func plainLine(s string, limit int) string {
	s = strings.NewReplacer("`", "", "**", "", "\n> ", " ", "\n- ", " ").Replace(s)
	s = strings.TrimLeft(s, "#> -*")
	return leadSentence(s, limit)
}
