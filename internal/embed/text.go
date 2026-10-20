// Package embed is the client side of hybrid search: the texts the embedding
// model is given, the HTTP call to the server that runs it, and the vector
// arithmetic that needs no model (normalising, int8 quantisation, cosine,
// rank fusion). It depends on the standard library only; ghosttree stays a
// cgo-free single binary and talks to the model over loopback HTTP.
package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultModel names the model the prefixes below belong to. It is the model
	// id sent in the request and stored with every vector.
	DefaultModel = "embeddinggemma-2-q8_0"

	// PrefixVersion identifies the query and document formats below. The model
	// card makes the prefixes part of the model: a vector made with another
	// format is not comparable, and forgetting the prefix yields worse hits and
	// no error. Change the formats, change this string; every stored vector then
	// counts as stale and is recomputed.
	PrefixVersion = "gemma2-v1"

	// MaxDocumentRunes caps the document text, prefix included. The server runs
	// with a context of 2048 tokens and a document that does not fit is refused.
	// There is no tokenizer in this binary, so the cap is counted in characters
	// and chosen so that even a text with one token per character (hashes, paths,
	// digits) stays below the context. For German and English prose it is about
	// 500 to 700 tokens, close to the 512 tokens the model was measured with.
	MaxDocumentRunes = 2000
	// MaxTitleRunes keeps a pathological title from eating the body's share.
	MaxTitleRunes = 200
	// MaxQueryRunes bounds the latency of a search that was given a whole
	// paragraph.
	MaxQueryRunes = 1000
	// MaxRequestDescriptionRunes is how much of a request's description is
	// embedded; what is left of MaxDocumentRunes goes to its criteria, which say
	// what the request is for and are often shorter than the story before them.
	MaxRequestDescriptionRunes = 1200
	// MaxDocumentHeadRunes is how much of a long-form document is embedded: its
	// beginning, where a title, a summary or the question it answers stand. The
	// whole text is not embedded; it would not fit and would blur what the
	// document is about.
	MaxDocumentHeadRunes = 1500

	// DefaultMinSimilarity is the cosine below which an entry is not a neighbour
	// of a query. Without a floor the embedding ranking has a nearest neighbour
	// for any query, however far away, and the fusion fills a result list with
	// them. For EmbeddingGemma 2 (768 dimensions) the best neighbour of each of
	// 398 evidence-quote queries measured on the project's own entries had a
	// cosine of at least 0.66, and of about 0.78 for the median query; 0.55 sits
	// below that, so it cuts the tail of unrelated entries and not the answers.
	// Quotes are longer than what people ask and score higher, so this is a
	// starting point: measure it on the server with the --embed-min-similarity
	// option of the quality measurement. The scale belongs to a model; set the
	// value again when the model changes.
	DefaultMinSimilarity = 0.55

	// DefaultQueryTimeout is how long a search waits for the query vector before
	// it answers with full-text results alone.
	DefaultQueryTimeout = 1500 * time.Millisecond
	// DefaultBatchTimeout bounds one batch request of the background job.
	DefaultBatchTimeout = 2 * time.Minute
)

// QueryText is the text the model gets for a search query.
func QueryText(q string) string {
	return "task: search result | query: " + truncRunes(oneLine(q), MaxQueryRunes)
}

// DocumentText is the text the model gets for a knowledge entry. The same
// string is hashed, so a change of title, body, prefix or cut is a change of
// hash.
func DocumentText(title, body string) string {
	t := truncRunes(oneLine(title), MaxTitleRunes)
	if t == "" {
		t = "none"
	}
	head := "title: " + t + " | text: "
	budget := MaxDocumentRunes - utf8.RuneCountInString(head)
	text := truncRunes(strings.TrimSpace(strings.ToValidUTF8(body, "�")), budget)
	return strings.TrimRight(head+text, " \t\r\n")
}

// RequestText is the text the model gets for a request: title, description and
// the acceptance criteria. The state of a criterion is not part of it, so ticking
// one off does not make the vector stale.
func RequestText(title, description string, criteria []string) string {
	body := truncRunes(strings.TrimSpace(strings.ToValidUTF8(description, "�")), MaxRequestDescriptionRunes)
	var list []string
	for _, c := range criteria {
		if c = oneLine(c); c != "" {
			list = append(list, "- "+c)
		}
	}
	if len(list) > 0 {
		if body != "" {
			body += "\n\n"
		}
		body += strings.Join(list, "\n")
	}
	return DocumentText(title, body)
}

// GhostText is the text the model gets for the description of a path: the path
// stands where a title does, a directory with a closing slash.
func GhostText(path, kind, description string) string {
	title := strings.TrimSpace(path)
	switch {
	case title == "":
		title = "repository root"
	case kind == "dir":
		title += "/"
	}
	return DocumentText(title, description)
}

// DocumentHeadText is the text the model gets for a long-form document: its
// title and the first MaxDocumentHeadRunes characters of the text.
func DocumentHeadText(title, body string) string {
	return DocumentText(title, truncRunes(strings.TrimSpace(strings.ToValidUTF8(body, "�")), MaxDocumentHeadRunes))
}

// TextHash identifies a document text.
func TextHash(document string) string {
	sum := sha256.Sum256([]byte(document))
	return hex.EncodeToString(sum[:])
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(s, "�")), " ")
}

// truncRunes cuts at a character boundary.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
