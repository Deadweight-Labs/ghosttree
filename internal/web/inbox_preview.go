package web

import (
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The preview of a message or a text in the inbox is plain text (REQ-684): what the reader
// would see on the page, without the marks of Markdown. A picture reads "[Image: alt]", a
// link its words, a code block its lines, a tag nothing, and an @name the name of whom it
// means. The page of the room or issue shows the real thing; the row only says what it is about.

var (
	previewImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	previewLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	previewFence   = regexp.MustCompile("(?s)(?:```|~~~)[^\\n]*\\n?(.*?)(?:```|~~~|$)")
	previewTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	previewHead    = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	previewQuote   = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	previewItem    = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+(?:\[[ xX]\][ \t]+)?`)
	previewCode    = regexp.MustCompile("`([^`]*)`")
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
	s = previewFence.ReplaceAllString(s, "$1")
	s = previewImage.ReplaceAllStringFunc(s, func(m string) string {
		alt := strings.TrimSpace(previewImage.FindStringSubmatch(m)[1])
		if alt == "" {
			return l.msg("inbox.preview.image_plain")
		}
		return l.msg("inbox.preview.image", alt)
	})
	s = previewLink.ReplaceAllString(s, "$1")
	s = previewTag.ReplaceAllString(s, " ")
	s = previewHead.ReplaceAllString(s, "")
	s = previewQuote.ReplaceAllString(s, "")
	s = previewItem.ReplaceAllString(s, "")
	s = previewCode.ReplaceAllString(s, "$1")
	for _, mark := range previewMarks {
		s = mark.ReplaceAllString(s, "$1$2$3")
	}
	if len(names) > 0 {
		s = previewMention.ReplaceAllStringFunc(s, func(m string) string {
			if n := names[m[1:]]; n != "" {
				return n
			}
			return m
		})
	}
	return strings.Join(strings.Fields(s), " ")
}

// previewText is the line a row of the inbox shows for a text: plain, and cut at limit.
func previewText(l lang, s string, limit int, names map[string]string) string {
	return questionTitle(stripMarkdown(l, s, names), limit)
}

// roomProject is the project of a project room ("project:github.com/x/y"), "" for any other room.
func roomProject(key string) string {
	p, _ := strings.CutPrefix(key, "project:")
	if p == key {
		return ""
	}
	return p
}
