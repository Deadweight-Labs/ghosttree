package web

import (
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The text of a person (description, note, reason, message, document) reaches
// the page as its source. static/ui-md.js is the one renderer: it reads the
// source from data-md and builds DOM nodes, the same for a preview, a comment,
// a note and a room message. This file writes what a reader sees without the
// script: the blocks of the text (paragraph, heading, list, quote, table, code,
// rule) as plain HTML of escaped text, and the source for the script.

type bodyBlock struct {
	Code bool
	Text string
	// Parts zerlegt einen Absatz an `Rückwärtsstrichen` in Text und Code.
	Parts []inlinePart
	// Kind is "" for a paragraph, else "code", "h", "list", "quote", "table"
	// or "hr". Level is the level of a heading.
	Kind  string
	Level int
	// Src is the block as it stood in the text; the blocks joined by a blank
	// line are the source the script renders.
	Src string
}

type inlinePart struct {
	Code bool
	Text string
}

func inlineParts(text string) []inlinePart {
	var out []inlinePart
	for i, seg := range strings.Split(text, "`") {
		if seg != "" {
			out = append(out, inlinePart{Code: i%2 == 1, Text: seg})
		}
	}
	return out
}

var (
	mdFence = regexp.MustCompile("^ {0,3}(`{3,})[ \\t]*([\\w+#.\\-]*)[^`]*$")
	mdHead  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	mdQuote = regexp.MustCompile(`^ {0,3}>`)
	mdItem  = regexp.MustCompile(`^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$`)
	mdSep   = regexp.MustCompile(`^ {0,3}\|? *:?-+:? *(?:\| *:?-+:? *)*\|? *$`)
)

// mdRule is a line of three or more of one of - * _ (spaces between are fine).
func mdRule(l string) bool {
	s := strings.ReplaceAll(strings.TrimSpace(l), " ", "")
	if len(s) < 3 || mdIndent(l) > 3 || !strings.ContainsAny(s[:1], "-*_") {
		return false
	}
	return strings.Trim(s, s[:1]) == ""
}

// mdTableAt is true when line i is the head of a table: a line with a bar and a
// separator line under it with as many cells.
func mdTableAt(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") || !strings.Contains(lines[i+1], "|") || !mdSep.MatchString(lines[i+1]) {
		return false
	}
	return len(mdCells(lines[i])) == len(mdCells(lines[i+1]))
}

// mdCells splits a table row at its bars (a \| is a bar in the text).
func mdCells(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	if strings.HasSuffix(l, "|") && !strings.HasSuffix(l, `\|`) {
		l = l[:len(l)-1]
	}
	var out []string
	var cur strings.Builder
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case l[i] == '|':
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	return append(out, strings.TrimSpace(cur.String()))
}

// mdInterrupts is true for a line that ends a paragraph.
func mdInterrupts(l string) bool {
	if mdFence.MatchString(l) || mdHead.MatchString(l) || mdQuote.MatchString(l) || mdRule(l) {
		return true
	}
	m := mdItem.FindStringSubmatch(l)
	return m != nil && m[3] != "" && (m[2][0] < '0' || m[2][0] > '9' || m[2] == "1." || m[2] == "1)")
}

func mdIndent(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// fenceFor is the fence that holds text: longer than any run of backticks in it.
func fenceFor(text string) string {
	n, run := 3, 0
	for _, r := range text {
		if r == '`' {
			run++
			n = max(n, run+1)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", n)
}

// mdSplit reads the text line by line into its blocks.
func mdSplit(s string) []bodyBlock {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\n")
	var out []bodyBlock
	for i := 0; i < len(lines); {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" {
			i++
			continue
		}
		if m := mdFence.FindStringSubmatch(ln); m != nil {
			start, n := i, len(m[1])
			i++
			var code []string
			for i < len(lines) {
				t := strings.TrimRight(lines[i], " \t")
				if mdIndent(t) <= 3 && len(strings.TrimLeft(t, " ")) >= n && strings.Trim(strings.TrimLeft(t, " "), "`") == "" {
					break
				}
				code = append(code, lines[i])
				i++
			}
			closed := i < len(lines)
			i++
			src := strings.Join(lines[start:min(i, len(lines))], "\n")
			if !closed {
				src += "\n" + m[1]
			}
			out = append(out, bodyBlock{Kind: "code", Code: true, Text: strings.Trim(strings.Join(code, "\n"), "\n"), Src: src})
			continue
		}
		if m := mdHead.FindStringSubmatch(ln); m != nil {
			out = append(out, bodyBlock{Kind: "h", Level: len(m[1]), Text: strings.TrimSpace(m[2]), Src: ln})
			i++
			continue
		}
		if mdRule(ln) {
			out = append(out, bodyBlock{Kind: "hr", Src: ln})
			i++
			continue
		}
		if mdQuote.MatchString(ln) {
			start := i
			for i < len(lines) && mdQuote.MatchString(lines[i]) {
				i++
			}
			out = append(out, bodyBlock{Kind: "quote", Text: strings.Join(lines[start:i], "\n"), Src: strings.Join(lines[start:i], "\n")})
			continue
		}
		if mdTableAt(lines, i) {
			start := i
			i += 2
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && strings.Contains(lines[i], "|") {
				i++
			}
			out = append(out, bodyBlock{Kind: "table", Text: strings.Join(lines[start:i], "\n"), Src: strings.Join(lines[start:i], "\n")})
			continue
		}
		if m := mdItem.FindStringSubmatch(ln); m != nil {
			start := i
			i++
			for i < len(lines) {
				l2 := lines[i]
				if strings.TrimSpace(l2) == "" {
					k := i + 1
					for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
						k++
					}
					if k >= len(lines) || !(mdIndent(lines[k]) >= 2 || mdItem.MatchString(lines[k])) || mdRule(lines[k]) {
						break
					}
					i = k
					continue
				}
				if mdItem.MatchString(l2) && !mdRule(l2) || mdIndent(l2) >= 2 {
					i++
					continue
				}
				if mdInterrupts(l2) || mdTableAt(lines, i) {
					break
				}
				i++
			}
			text := strings.Join(lines[start:i], "\n")
			out = append(out, bodyBlock{Kind: "list", Text: text, Src: text})
			continue
		}
		start := i
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !mdInterrupts(lines[i]) && !mdTableAt(lines, i) {
			i++
		}
		text := strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		out = append(out, bodyBlock{Text: text, Src: strings.Join(lines[start:i], "\n")})
	}
	return out
}

// bodyBlocks teilt einen Text in seine Blöcke (Absatz, Überschrift, Liste, Zitat,
// Tabelle, Code, Linie). limit kürzt auf so viele Zeichen und hängt "…" an
// (0 = nicht kürzen).
func bodyBlocks(s string, limit int) []bodyBlock {
	var out []bodyBlock
	budget := limit
	for _, b := range mdSplit(s) {
		if strings.TrimSpace(b.Text) == "" && b.Kind != "hr" {
			continue
		}
		if limit > 0 && b.Kind != "hr" {
			n := utf8.RuneCountInString(b.Text)
			if n > budget {
				b.Text = strings.TrimRight(string([]rune(b.Text)[:max(budget, 0)]), " \n") + "…"
				b.Src = b.Text
				if b.Kind == "code" {
					b.Src = "```\n" + b.Text + "\n```"
				} else if b.Kind != "" && b.Kind != "h" {
					b.Kind = ""
				}
				b.Parts = inlineParts(b.Text)
				out = append(out, b)
				return out
			}
			budget -= n
		}
		if b.Kind == "" || b.Kind == "h" || b.Kind == "quote" {
			b.Parts = inlineParts(b.Text)
		}
		out = append(out, b)
	}
	return out
}

// mdSource is the text the blocks stood in: what the script renders.
func mdSource(blocks []bodyBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Src)
	}
	return strings.Join(parts, "\n\n")
}

// mdBlocks writes blocks for the reader without the script. The box carries the
// source in data-md; the script replaces what stands inside with the same text
// as DOM nodes. Every text is escaped, nothing a person wrote is markup.
func mdBlocks(blocks []bodyBlock) template.HTML { return mdBlocksOpt(blocks, mdOptions{}) }

// mdOptions: Anchors gives the headings an address (the documents); the script
// does the same and builds the outline from them.
type mdOptions struct {
	Anchors bool
}

func mdBlocksOpt(blocks []bodyBlock, o mdOptions) template.HTML {
	var b strings.Builder
	b.WriteString(`<div class="kn-body prose" data-md="`)
	b.WriteString(html.EscapeString(mdSource(blocks)))
	b.WriteString(`"`)
	if o.Anchors {
		b.WriteString(` data-md-anchors`)
	}
	b.WriteString(`>`)
	used := map[string]bool{}
	for _, bl := range blocks {
		switch bl.Kind {
		case "code":
			b.WriteString(`<pre class="kn-code"><code>` + html.EscapeString(bl.Text) + `</code></pre>`)
		case "h":
			tag := "h" + string(rune('0'+bl.Level))
			b.WriteString("<" + tag)
			if o.Anchors {
				b.WriteString(` id="` + html.EscapeString(uniqueAnchor(headingText(bl.Text), used)) + `"`)
			}
			b.WriteString(">" + partsHTML(bl.Parts) + "</" + tag + ">")
		case "hr":
			b.WriteString("<hr>")
		case "quote":
			b.WriteString("<blockquote><p>" + partsHTML(inlineParts(quoteText(bl.Text))) + "</p></blockquote>")
		case "list":
			b.WriteString(listHTML(bl.Text))
		case "table":
			b.WriteString(tableHTML(bl.Text))
		default:
			b.WriteString("<p>" + partsHTML(bl.Parts) + "</p>")
		}
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// mdText is the text of a person written for a page: one box of blocks.
func mdText(s string) template.HTML { return mdBlocks(bodyBlocks(s, 0)) }

func partsHTML(parts []inlinePart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Code {
			b.WriteString("<code>" + html.EscapeString(p.Text) + "</code>")
		} else {
			b.WriteString(mentionPattern.ReplaceAllString(html.EscapeString(p.Text), `$1<span class="mention">$2</span>`))
		}
	}
	return b.String()
}

func quoteText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimLeft(l, " ")
		l = strings.TrimPrefix(l, ">")
		lines[i] = strings.TrimPrefix(l, " ")
	}
	return strings.Join(lines, "\n")
}

// listHTML writes a list as nested ul/ol of the indentation of its lines. A
// line that is no item belongs to the item above it.
func listHTML(text string) string {
	type item struct {
		indent  int
		ordered bool
		text    []string
	}
	var items []*item
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if m := mdItem.FindStringSubmatch(ln); m != nil && !mdRule(ln) {
			items = append(items, &item{indent: len(m[1]), ordered: m[2][0] >= '0' && m[2][0] <= '9', text: []string{m[3]}})
			continue
		}
		if len(items) > 0 {
			last := items[len(items)-1]
			last.text = append(last.text, strings.TrimSpace(ln))
		}
	}
	var b strings.Builder
	type open struct {
		indent  int
		ordered bool
	}
	var stack []open
	closeTo := func(indent int) {
		for len(stack) > 0 && stack[len(stack)-1].indent > indent {
			t := "ul"
			if stack[len(stack)-1].ordered {
				t = "ol"
			}
			b.WriteString("</li></" + t + ">")
			stack = stack[:len(stack)-1]
		}
	}
	for _, it := range items {
		// one step of less than two spaces is no step down
		if n := len(stack); n > 0 && it.indent > stack[n-1].indent && it.indent < stack[n-1].indent+2 {
			it.indent = stack[n-1].indent
		}
		closeTo(it.indent)
		if n := len(stack); n > 0 && stack[n-1].indent == it.indent && stack[n-1].ordered != it.ordered {
			// the kind of list changes at the same step: a new list
			t := "ul"
			if stack[n-1].ordered {
				t = "ol"
			}
			b.WriteString("</li></" + t + ">")
			stack = stack[:n-1]
		}
		switch {
		case len(stack) > 0 && stack[len(stack)-1].indent == it.indent:
			b.WriteString("</li>")
		default:
			t := "ul"
			if it.ordered {
				t = "ol"
			}
			b.WriteString("<" + t + ">")
			stack = append(stack, open{it.indent, it.ordered})
		}
		txt := strings.Join(it.text, "\n")
		switch {
		case strings.HasPrefix(txt, "[ ] "):
			txt = "☐ " + txt[4:]
		case strings.HasPrefix(txt, "[x] "), strings.HasPrefix(txt, "[X] "):
			txt = "☑ " + txt[4:]
		}
		b.WriteString("<li>" + partsHTML(inlineParts(txt)))
	}
	closeTo(-1)
	return b.String()
}

// tableHTML writes a table; the head row and the separator line make the head.
func tableHTML(text string) string {
	lines := strings.Split(text, "\n")
	cols := len(mdCells(lines[0]))
	var b strings.Builder
	b.WriteString(`<div class="tbl"><table>`)
	for i, ln := range lines {
		if i == 1 {
			continue
		}
		tag := "td"
		if i == 0 {
			tag = "th"
			b.WriteString("<thead>")
		} else if i == 2 {
			b.WriteString("<tbody>")
		}
		b.WriteString("<tr>")
		cells := mdCells(ln)
		for k := 0; k < cols; k++ {
			c := ""
			if k < len(cells) {
				c = cells[k]
			}
			b.WriteString("<" + tag + ">" + partsHTML(inlineParts(c)) + "</" + tag + ">")
		}
		b.WriteString("</tr>")
		if i == 0 {
			b.WriteString("</thead>")
		}
	}
	if len(lines) > 2 {
		b.WriteString("</tbody>")
	}
	b.WriteString(`</table></div>`)
	return b.String()
}

var mdInlineMarks = regexp.MustCompile("[*_`~]|\\[([^\\]]*)\\]\\([^)]*\\)")

// headingText is what a reader reads of a heading: no marks of the inline text.
func headingText(s string) string {
	return strings.TrimSpace(mdInlineMarks.ReplaceAllString(s, "$1"))
}

// anchorSlug is the part of a heading's address after "h-": the letters and
// digits of the text, in lower case, words joined by a hyphen.
func anchorSlug(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '-' || r == '_':
			dash = true
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// uniqueAnchor is "h-" and the slug; when the address is taken, a number after it
// counts up until one is free. ui-md.js does the same in the browser.
func uniqueAnchor(text string, used map[string]bool) string {
	base := "h-" + anchorSlug(text)
	id := base
	for n := 2; used[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	used[id] = true
	return id
}

// excerptMarks are what a line of Markdown starts with: heading, quote, list, task.
var excerptMarks = regexp.MustCompile(`^\s*(?:#{1,6}\s+|>\s?|(?:[-*+]|\d{1,9}[.)])\s+(?:\[[ xX]\]\s+)?)`)

// mdPlain reads a line of Markdown as the words it says.
func mdPlain(l string) string {
	l = excerptMarks.ReplaceAllString(l, "")
	l = strings.NewReplacer("**", "", "__", "", "~~", "", "`", "").Replace(l)
	return strings.TrimSpace(l)
}

// withoutTitleHeading drops the first line of a document when it is a heading
// of the first level that says what the title of the page says: the page shows
// the title once.
func withoutTitleHeading(src, title string) string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		m := mdHead.FindStringSubmatch(ln)
		if m == nil || len(m[1]) != 1 || !strings.EqualFold(headingText(m[2]), strings.TrimSpace(title)) {
			return src
		}
		return strings.TrimLeft(strings.Join(lines[i+1:], "\n"), "\n")
	}
	return src
}
