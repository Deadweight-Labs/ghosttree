package web

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A text of a person that is shown as a line (a row of the inbox, a card, a list, a quote) is
// plain text (REQ-684, REQ-705): what the reader would see on the page, without the marks of
// Markdown. stripMarkdown is the one function for it; plainSnippet and previewText cut its
// result. A picture reads "Image: alt", a link its words, a code block its lines, a tag
// nothing, a table its cells, and an @name the name of whom it means. The page of the room or
// issue shows the real thing through ui-md.js; the row only says what it is about.

var (
	previewImage = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	previewLink  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	previewFence = regexp.MustCompile("(?s)(?:```|~~~)[^\\n]*\\n?(.*?)(?:```|~~~|$)")
	previewTag   = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	previewHead  = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	previewQuote = regexp.MustCompile(`(?m)^[ \t]*(?:>[ \t]?)+`)
	// A note box is "> [!NOTE]" and a task list item "- [x]". Both are also taken out in the
	// middle of a line: the snippet of a text stored before the lines were kept is one line.
	previewCallout = regexp.MustCompile(`(?:>[ \t]*)?\[![A-Za-z]+\][ \t]*(?:>[ \t]*)?`)
	previewTask    = regexp.MustCompile(`(^|\s)[-*+][ \t]+\[[ xX]\][ \t]+`)
	previewItem    = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+(?:\[[ xX]\][ \t]+)?`)
	previewCode    = regexp.MustCompile("`([^`]*)`")
	previewAuto    = regexp.MustCompile(`<(https?://[^>\s]+)>`)
	previewRule    = regexp.MustCompile(`(?m)^[ \t]{0,3}(?:[-*_][ \t]*){3,}$`)
	previewSep     = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)+\|?[ \t]*$`)
	previewRow     = regexp.MustCompile(`(?m)^[ \t]*\|(.*?)\|?[ \t]*$`)
	previewEscape  = regexp.MustCompile("\\\\([\\\\`*_\\[\\]()#>@!~|<+.-])")
	// previewEscaped are the characters a backslash makes plain; each stands in as a private-use character
	// while the marks are taken out, so that "\*" is not read as the start of an emphasis.
	previewEscaped = "\\`*_[]()#>@!~|<+.-"
	previewMention = regexp.MustCompile(`@[\p{L}\p{N}][\p{L}\p{N}_-]*(?:[.:][\p{L}\p{N}][\p{L}\p{N}_-]*)*`)
)

// previewMarks are the emphasis marks, each only around a word group and never inside a word
// (snake_case_word, 2*3*4 stay as they are).
var previewMarks = func() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, m := range []string{`**`, `__`, `~~`, `*`, `_`} {
		q := regexp.QuoteMeta(m)
		out = append(out, regexp.MustCompile(`(^|[^\p{L}\p{N}])`+q+`([^\s`+regexp.QuoteMeta(m[:1])+`](?:[^\n]*?[^\s`+regexp.QuoteMeta(m[:1])+`])?)`+q+`([^\p{L}\p{N}]|$)`))
	}
	return out
}()

// mentionHandles are the handles an @name in the texts stands for, without the @.
func mentionHandles(texts ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range previewMention.FindAllString(t, -1) {
			if h := m[1:]; !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// mentionNamesOf asks for the full names of the handles in texts, as the viewer may see them
// (nothing for a project without a room the viewer may read).
func mentionNamesOf(pa *store.ProjectAccess, project string, texts ...string) map[string]string {
	if project == "" || pa == nil {
		return nil
	}
	handles := mentionHandles(texts...)
	if len(handles) == 0 {
		return nil
	}
	if len(handles) > maxMentionHandles {
		handles = handles[:maxMentionHandles]
	}
	out := map[string]string{}
	for h, n := range pa.MentionNames(project, handles) {
		out[h] = n.Name
	}
	return out
}

// stripMarkdown strips Markdown from s. names maps the handle of an @name to a full name.
func stripMarkdown(l lang, s string, names map[string]string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = previewEscape.ReplaceAllStringFunc(s, func(m string) string {
		return string(rune(0xE000 + strings.IndexByte(previewEscaped, m[1])))
	})
	s = previewFence.ReplaceAllString(s, "$1")
	s = previewImage.ReplaceAllStringFunc(s, func(m string) string {
		alt := strings.TrimSpace(previewImage.FindStringSubmatch(m)[1])
		if alt == "" {
			return l.msg("inbox.preview.image_plain")
		}
		return l.msg("inbox.preview.image", alt)
	})
	s = previewLink.ReplaceAllString(s, "$1")
	s = previewAuto.ReplaceAllString(s, "$1")
	s = previewTag.ReplaceAllString(s, " ")
	s = previewSep.ReplaceAllString(s, "")
	s = previewRow.ReplaceAllStringFunc(s, func(row string) string {
		cells := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
		for i, c := range cells {
			cells[i] = strings.TrimSpace(c)
		}
		return strings.Join(cells, " · ")
	})
	s = previewRule.ReplaceAllString(s, "")
	s = previewHead.ReplaceAllString(s, "")
	s = previewQuote.ReplaceAllString(s, "")
	s = previewCallout.ReplaceAllString(s, "")
	s = previewItem.ReplaceAllString(s, "")
	s = previewTask.ReplaceAllString(s, "$1")
	s = previewCode.ReplaceAllString(s, "$1")
	for _, mark := range previewMarks {
		s = mark.ReplaceAllString(s, "$1$2$3")
	}
	if len(names) > 0 {
		s = nameMentions(s, names)
	}
	s = strings.Map(func(r rune) rune {
		if r >= 0xE000 && r < 0xE000+rune(len(previewEscaped)) {
			return rune(previewEscaped[r-0xE000])
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// plainSnippet is a text of a person as one plain line of at most limit characters, cut at the end
// of a word: the line a card, a list row or a quote shows. The same plain text as the inbox row.
func plainSnippet(l lang, s string, limit int) string {
	return shortWords(stripMarkdown(l, s, nil), limit)
}

// previewText is the line a row of the inbox shows for a text: plain, and cut at limit.
func previewText(l lang, s string, limit int, names map[string]string) string {
	return questionTitle(stripMarkdown(l, s, names), limit)
}

// fullIfCut is the whole plain text of s when title shows less of it, so a row can unfold it; "" when
// the title says everything.
func fullIfCut(l lang, s, title string, names map[string]string) string {
	full := strings.Join(strings.Fields(stripMarkdown(l, s, names)), " ")
	if full == title {
		return ""
	}
	return full
}

// roomProject is the project of a project room ("project:github.com/x/y"), "" for any other room.
func roomProject(key string) string {
	p, _ := strings.CutPrefix(key, "project:")
	if p == key {
		return ""
	}
	return p
}

// nameMentions writes the full name for each @handle that names somebody. The name a person typed
// out after the handle ("@Mara Keller") is the rest of that name and is said once: the longest
// run of the following words that continues the name goes with the handle.
func nameMentions(s string, names map[string]string) string {
	var out strings.Builder
	at := 0
	for _, m := range previewMention.FindAllStringIndex(s, -1) {
		if m[0] < at {
			continue
		}
		handle := s[m[0]+1 : m[1]]
		name := names[handle]
		if name == "" {
			continue
		}
		out.WriteString(s[at:m[0]])
		out.WriteString(name)
		at = m[1] + typedRestOfName(s[m[1]:], handle, name)
	}
	out.WriteString(s[at:])
	return out.String()
}

// typedRestOfName is how many bytes at the start of text spell the words of name that come after
// handle ("Keller (Tech-Lead)" for "Mara" and "Mara Keller (Tech-Lead)"), as far as they match.
func typedRestOfName(text, handle, name string) int {
	if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(handle)) {
		return 0
	}
	used := 0
	for _, word := range strings.Fields(name[len(handle):]) {
		rest := text[used:]
		trimmed := strings.TrimLeft(rest, " \t")
		if len(trimmed) == len(rest) || len(trimmed) < len(word) || !strings.EqualFold(trimmed[:len(word)], word) {
			break
		}
		if next := trimmed[len(word):]; next != "" {
			if r, _ := utf8.DecodeRuneInString(next); unicode.IsLetter(r) || unicode.IsDigit(r) {
				break
			}
		}
		used += len(rest) - len(trimmed) + len(word)
	}
	return used
}

// withoutAddressee is the text of a question without the @name it starts with: the row says who
// it is for in its head and the title says what is asked. Only a name that resolves goes, and only
// when something is left.
func withoutAddressee(s string, names map[string]string) string {
	trimmed := strings.TrimLeft(s, " \t\n")
	loc := previewMention.FindStringIndex(trimmed)
	if loc == nil || loc[0] != 0 {
		return s
	}
	handle := trimmed[1:loc[1]]
	name := names[handle]
	if name == "" {
		return s
	}
	rest := trimmed[loc[1]:]
	rest = rest[typedRestOfName(rest, handle, name):]
	rest = strings.TrimLeft(rest, " \t\n,:;-")
	if rest == "" {
		return s
	}
	return rest
}
