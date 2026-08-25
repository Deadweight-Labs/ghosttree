package store

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Relevance delivery answers a different question from search. Search is asked;
// this is not. Something has to decide, from a sentence nobody wrote for it,
// whether the archive holds anything worth interrupting with — and the cost of
// being wrong is asymmetric. A missed entry is what the situation was already
// like; a wrong one spends context and teaches the reader to skim past the next.
//
// So the rule is deliberately narrow: an entry surfaces only through a term that
// is rare in the corpus. "Ollama" appears in one entry of three hundred and says
// what the sentence is about. "Server" appears in a third of them and says
// nothing. This is the idf half of bm25, applied as a gate instead of as a
// weight, because as a weight a long sentence can outvote it: five ordinary
// words matching one entry will outrank one rare word matching another, which is
// exactly backwards for deciding whether to speak at all.
const (
	// maxDocumentFrequency is the share of the corpus a term may appear in and
	// still count as distinctive. It is deliberately low. A looser share reads
	// fine in the abstract and fails in a real archive: a fifteenth of three
	// hundred entries is forty-five, and a project's own subject matter —
	// "sqlite" in this repository — occurs in far more than one entry while
	// carrying no intent at all. Anything above this is what the project is
	// about, not what this sentence is about.
	maxDocumentFrequency = 0.05
	// maxRareTerms is how many of the rarest terms actually get to ask the
	// index. Three is enough for a sentence with a subject and narrow enough
	// that a wall of pasted text cannot reach everything at once.
	maxRareTerms = 3
	// maxTerms bounds the work a single prompt can cause. This runs on the
	// keystroke path, and a pasted stack trace is a prompt too.
	maxTerms = 24
	// minTermLen keeps single letters and most inflection out. Tool names are
	// what matter here, and the shortest real ones are three characters: ctx,
	// ufw, npm.
	minTermLen = 3
)

// RelevantKnowledge returns entries that the given text gives a reason to
// deliver, most relevant first. It returns nothing far more often than
// something, which is the intended behaviour rather than a limitation.
func (s *Store) RelevantKnowledge(text string, ax scope.Axes, limit int) ([]Knowledge, error) {
	return s.relevantKnowledge(text, ax, limit, true)
}

func (s *Store) relevantKnowledge(text string, ax scope.Axes, limit int, record bool) ([]Knowledge, error) {
	if limit <= 0 {
		limit = 3
	}
	terms := distinctiveTerms(text)
	if len(terms) == 0 {
		return nil, nil
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge
		WHERE status = 'active' AND confidence != 'quarantined'`).Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	// At least one, so a young archive where every term is technically rare
	// still requires a term to point at a single entry rather than at several.
	ceiling := max(1, int(float64(total)*maxDocumentFrequency))

	type scored struct {
		term string
		df   int
	}
	var rare []scored
	for _, term := range terms {
		n, err := s.documentFrequency(term)
		if err != nil {
			return nil, err
		}
		if n > 0 && n <= ceiling {
			rare = append(rare, scored{term, n})
		}
	}
	if len(rare) == 0 {
		return nil, nil
	}
	// Only the rarest few. A long prompt turns up unusual words by accident —
	// a pasted stack trace, a list of plugin names — and any one of them is
	// enough to reach something if all of them are asked. Measured against real
	// archived prompts, using every rare term fired on four sentences in five.
	sort.Slice(rare, func(i, j int) bool { return rare[i].df < rare[j].df })
	if len(rare) > maxRareTerms {
		rare = rare[:maxRareTerms]
	}
	// Matched against the title, not the body. A term in the body means the
	// entry mentions the thing; a term in the title means the entry is about it,
	// and only the second is a reason to speak up unasked. What this loses is
	// still reachable by search, which is the right place for a hunch.
	match := make([]string, len(rare))
	for i, r := range rare {
		match[i] = `title : "` + r.term + `"`
	}

	where, args := ax.UnionWhere()
	args = append([]any{strings.Join(match, " OR ")}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(knowledgeCols, "k.")+`
		FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND `+where+`
		  AND k.status = 'active' AND k.confidence != 'quarantined'
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ks, err := s.scanKnowledge(rows)
	if err != nil {
		return nil, err
	}
	// Being handed an entry because it fits the moment is the strongest form of
	// use there is, so it counts as one — this is the signal that tells a useful
	// entry from one nobody has ever needed.
	if record {
		s.recordKnowledgeSearchHit(ks)
	}
	return ks, nil
}

func (s *Store) documentFrequency(term string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND k.status = 'active' AND k.confidence != 'quarantined'`,
		`"`+term+`"`).Scan(&n)
	return n, err
}

// distinctiveTerms splits a sentence the way the index splits a document, so a
// term that matches here is a term that can match there. Stopwords are not the
// mechanism — the frequency gate removes ordinary words on its own, and it does
// so per corpus rather than per language, which matters in an archive that is
// half German and half English. They are dropped only to keep the frequency
// queries down on the keystroke path.
func distinctiveTerms(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(field) < minTermLen || seen[field] || commonWords[field] {
			continue
		}
		seen[field] = true
		if out = append(out, field); len(out) == maxTerms {
			break
		}
	}
	return out
}

// commonWords are the words frequent enough in either language that asking the
// index about them is wasted work. The list is short on purpose: anything not
// caught here is still caught by the frequency gate.
var commonWords = map[string]bool{
	"aber": true, "alle": true, "als": true, "also": true, "auch": true, "auf": true,
	"aus": true, "bei": true, "bis": true, "das": true, "dass": true, "dem": true,
	"den": true, "der": true, "des": true, "die": true, "doch": true, "dort": true,
	"ein": true, "eine": true, "einen": true, "einer": true, "etwas": true, "für": true,
	"ganz": true, "gibt": true, "hab": true, "habe": true, "haben": true, "hat": true,
	"ich": true, "ist": true, "kann": true, "mal": true, "man": true, "mehr": true,
	"mich": true, "mir": true, "mit": true, "nach": true, "nicht": true, "noch": true,
	"nur": true, "oder": true, "schon": true, "sein": true, "sich": true, "sind": true,
	"soll": true, "über": true, "und": true, "uns": true, "vom": true, "von": true,
	"war": true, "was": true, "wenn": true, "werden": true, "wie": true, "wir": true,
	"wird": true, "zum": true, "zur": true,
	// Light verbs and question words. They carry no subject matter in any
	// corpus, and without them "was ist noch zu tun" has no content word left —
	// which is the signal that it asks to see everything rather than to find
	// something. A corpus-frequency gate would derive these instead of listing
	// them; RelevantKnowledge does exactly that and is the better long-term
	// answer, but it needs the database and this does not.
	"tun": true, "zu": true, "machen": true, "macht": true, "mach": true,
	"gemacht": true, "geht": true, "gehen": true, "muss": true, "müssen": true,
	"sollen": true, "sollte": true, "wollen": true, "bitte": true,
	"brauche": true, "brauchen": true, "steht": true, "gerne": true, "einfach": true,
	"do": true, "does": true, "need": true, "want": true, "must": true, "left": true,
	"about": true, "after": true, "all": true, "and": true, "any": true, "are": true,
	"but": true, "can": true, "for": true, "from": true, "get": true, "has": true,
	"have": true, "how": true, "into": true, "is": true, "its": true, "just": true, "like": true,
	"make": true, "more": true, "not": true, "now": true, "one": true, "only": true,
	"out": true, "please": true, "should": true, "some": true, "than": true, "that": true,
	"the": true, "their": true, "them": true, "then": true, "there": true, "these": true,
	"they": true, "this": true, "were": true, "what": true, "when": true,
	"which": true, "will": true, "with": true, "would": true, "you": true, "your": true,
	"to": true,
}
