// Package explain holds the "Kurz erklärt" block: a short, plain-language
// orientation for one object (knowledge entry, request, pain, human task, room
// question). The text is either written by the agent that created the object or
// generated afterwards from the object's own project data.
package explain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// Kinds of explained object.
const (
	KindKnowledge = "knowledge"
	KindRequest   = "request"
	KindPain      = "pain"
	KindTask      = "task"
	KindQuestion  = "question"
)

// Who wrote an explanation.
const (
	SourceAgent     = "agent"
	SourceGenerated = "generated"
)

const (
	// MaxTextRunes bounds an explanation: two to four sentences fit well below it.
	MaxTextRunes = 900
	minTextRunes = 20
	// MaxInputRunes bounds what is sent to a model per object.
	MaxInputRunes = 6000
)

var (
	// ErrNotExplainable means the object does not exist or does not belong to a
	// project, so nothing is generated for it.
	ErrNotExplainable = errors.New("object cannot be explained")
	// ErrDisabled means no model is configured.
	ErrDisabled = errors.New("explanations are not generated: no model configured")
	// ErrBatchTaken means an object of a batch about to be sent already waits in another one.
	ErrBatchTaken = errors.New("object is already part of an open batch")
)

// ValidKind reports whether kind is one of the explained object kinds.
func ValidKind(kind string) bool {
	switch kind {
	case KindKnowledge, KindRequest, KindPain, KindTask, KindQuestion:
		return true
	}
	return false
}

// Explanation is the stored block of one object.
type Explanation struct {
	Kind        string `json:"kind"`
	ObjectID    int64  `json:"object_id"`
	Project     string `json:"project,omitempty"`
	Text        string `json:"text"`
	Language    string `json:"language,omitempty"`
	Source      string `json:"source"`
	Model       string `json:"model,omitempty"`
	GeneratedAt string `json:"generated_at"`
	ContentHash string `json:"content_hash"`
}

// Source is what is known about an object for explaining it. Context lines
// come from data that belongs to the same project (linked request titles,
// code paths named in the text); never from session transcripts.
type Source struct {
	Kind    string
	ID      int64
	Project string
	// Label is the kind of the object as the reader knows it (pitfall, bug, ...).
	Label   string
	Title   string
	Body    string
	Context []string
	// Hash identifies the text that is explained. An explanation with another
	// hash was written for an earlier text.
	Hash string
}

// HashOf hashes the parts of an object that its explanation depends on.
func HashOf(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(p)))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

var (
	markup  = regexp.MustCompile("[`*]|^#+\\s*")
	tags    = regexp.MustCompile(`<[^>]*>`)
	urls    = regexp.MustCompile(`https?://\S+`)
	spaces  = regexp.MustCompile(`\s+`)
	reqRef  = regexp.MustCompile(`\bREQ-(\d+)\b`)
	pathRef = regexp.MustCompile(`\b(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.(?:go|md|html|js|ts|tsx|css|sql|yaml|yml|json|sh|py|rs|toml)\b`)
	prRef   = regexp.MustCompile(`(?i)\b(?:PR|pull request)\s*#(\d+)\b`)
	prURL   = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/(\d+)`)
)

// Clean turns model or agent output into plain text the page may show: no
// markup, links or tags, one paragraph, at most MaxTextRunes. Text that looks
// like it carries a secret is refused rather than shortened.
func Clean(text string) (string, error) {
	text = strings.ToValidUTF8(text, "")
	text = tags.ReplaceAllString(text, " ")
	text = urls.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "-•> ")
		lines[i] = markup.ReplaceAllString(l, "")
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' {
			return -1
		}
		return r
	}, strings.Join(lines, " "))
	text = strings.TrimSpace(spaces.ReplaceAllString(text, " "))
	if utf8.RuneCountInString(text) < minTextRunes {
		return "", fmt.Errorf("explanation is too short")
	}
	if err := humantask.CheckText("explanation", text); err != nil {
		return "", errors.New(err.Message)
	}
	return cut(text, MaxTextRunes), nil
}

// cut shortens text to at most n runes, preferably at the end of a sentence.
func cut(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	runes := []rune(text)[:n]
	for i := len(runes) - 1; i > n/2; i-- {
		if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' {
			return string(runes[:i+1])
		}
	}
	return strings.TrimSpace(string(runes[:n-1])) + "…"
}

// Refs are the pointers a text carries to where it comes from.
type Refs struct {
	Requests []int64
	Paths    []string
	PRs      []PullRef
}

// PullRef is a pull request named in a text. URL is empty when only the
// number is known.
type PullRef struct {
	Number int
	URL    string
}

// References finds request numbers, code paths and pull requests named in text.
// At most three of each: this orients a reader, it is not an index.
func References(text string) Refs {
	var out Refs
	seenReq := map[int64]bool{}
	for _, m := range reqRef.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && n > 0 && !seenReq[n] && len(out.Requests) < 3 {
			seenReq[n] = true
			out.Requests = append(out.Requests, n)
		}
	}
	seenPath := map[string]bool{}
	for _, m := range pathRef.FindAllString(text, -1) {
		m = strings.TrimPrefix(m, "./")
		if strings.Contains(m, "..") || strings.HasPrefix(m, "/") || seenPath[m] || len(out.Paths) >= 3 {
			continue
		}
		seenPath[m] = true
		out.Paths = append(out.Paths, m)
	}
	seenPR := map[int]bool{}
	for _, m := range prURL.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		if n > 0 && !seenPR[n] && len(out.PRs) < 3 {
			seenPR[n] = true
			out.PRs = append(out.PRs, PullRef{Number: n, URL: "https://github.com/" + m[1] + "/pull/" + m[2]})
		}
	}
	for _, m := range prRef.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		if n > 0 && !seenPR[n] && len(out.PRs) < 3 {
			seenPR[n] = true
			out.PRs = append(out.PRs, PullRef{Number: n})
		}
	}
	return out
}
