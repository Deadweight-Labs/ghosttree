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
//
// A word is distinctive only if it passes two gates. The first is rarity among
// the entries, above. The second is rarity in the reader's own language,
// measured on the text of the reader's own sessions and on nobody else's
// (relevance_language.go): a word can be in few entries only because nobody
// files pitfalls about it — "dinge", "things" — and a sentence that merely says
// it is not asking for them.
//
// The reader should also meet an entry once. Delivery remembers, per account and
// harness session, what it handed over — here and in the start-up briefing — and
// does not hand it over again (context_deliveries.go). Staged entries and the
// gardener's notes are never delivered this way, and every entry names the word
// that brought it up, so the reader can tell a good reason from a bad one.
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

// veryRareEntries is how few entries may name a word anywhere, title or body,
// for the body to count as a place to look. Three hundred entries can all
// mention "server"; only one or two will ever mention a codename, and then the
// body is what says what the codename is. This is the same gate, tightened
// until a mention in the body is as telling as a mention in the title.
const veryRareEntries = 2

// RelevanceQuery is one sentence, asked of the archive on behalf of a reader who
// did not ask. Axes and Viewer say what the reader may see; Account and Session
// say what the reader has already been shown.
type RelevanceQuery struct {
	Text   string
	Axes   scope.Axes
	Limit  int
	Viewer []SessionViewer
	// Account and Session name the delivery memory. With no Session nothing is
	// remembered and nothing is held back, which is how delivery behaved before
	// it had one.
	Account, Session string
	// Record counts the delivery as a use of each entry. The replay of archived
	// prompts must not, or it would move the numbers it is later judged by.
	Record bool
}

// RelevantHit is an entry together with the word that brought it up, so the
// reader can see why it is in front of them and judge whether the reason holds.
type RelevantHit struct {
	Knowledge
	Term string
}

// HintTerm is a very rare word of the sentence for which a hint may be due: a
// word that passed both gates, that at most a couple of entries name, and that
// was not hinted at yet in this session. Known are the entries that name it and
// may be delivered, whether or not the session has seen them. If the reader may
// see one of those the word has its answer and needs no hint; if none, the
// archive's knowledge is silent on it, and its requests and sessions are the
// only place left to point to.
type HintTerm struct {
	Term  string
	Known []Knowledge
}

// Relevance is the answer to one sentence. Hits are entries to deliver, rarest
// reason first; Hints are the words that may deserve a pointer instead.
type Relevance struct {
	Hits  []RelevantHit
	Hints []HintTerm
}

// RelevantKnowledge returns entries that the given text gives a reason to
// deliver, most relevant first. It returns nothing far more often than
// something, which is the intended behaviour rather than a limitation.
func (s *Store) RelevantKnowledge(text string, ax scope.Axes, limit int, viewer ...SessionViewer) ([]Knowledge, error) {
	if s.reader != nil {
		return s.reader.RelevantKnowledge(text, ax, limit, viewer...)
	}
	return s.relevantKnowledge(text, ax, limit, true, viewer...)
}

// RelevantFor is RelevantKnowledge for a caller that has a session: entries the
// session was already shown are left out, every hit says which word brought it,
// and words that name no deliverable entry come back as candidates for a hint.
// It reads only. Remembering what was delivered is ClaimContextDeliveries, done
// after the caller has applied what the reader may see, so that an entry the
// reader could not have seen is not marked as shown.
func (s *Store) RelevantFor(q RelevanceQuery) (Relevance, error) {
	if s.reader != nil {
		return s.reader.RelevantFor(q)
	}
	return s.relevance(q)
}

func (s *Store) relevantKnowledge(text string, ax scope.Axes, limit int, record bool, viewer ...SessionViewer) ([]Knowledge, error) {
	res, err := s.relevance(RelevanceQuery{Text: text, Axes: ax, Limit: limit, Viewer: viewer, Record: record})
	if err != nil || len(res.Hits) == 0 {
		return nil, err
	}
	return hitKnowledge(res.Hits), nil
}

func hitKnowledge(hits []RelevantHit) []Knowledge {
	ks := make([]Knowledge, len(hits))
	for i, h := range hits {
		ks[i] = h.Knowledge
	}
	return ks
}

// deliverable is the condition, on a knowledge row aliased k, that an entry may
// be pushed at a reader who did not ask. Quarantined material is not
// deliverable anywhere. Staged is the unconfirmed state: the briefing keeps it
// out and so does this, because a guess nobody has accepted is the one thing
// that must not interrupt a sentence. The gardener's notes are suggestions about
// other entries ("is #1498 out of date?"), staged until a person decides, and by
// its title a word like "gardener" would fire them at every sentence that
// mentions the gardener. A note a person confirmed is still a note about an
// entry, not the entry, so the harness excludes it whatever its confidence.
const deliverable = `k.status = 'active' AND k.confidence NOT IN ('quarantined', 'staged') AND k.harness != 'gardener'`

// rareTerm is a word that got past both gates: df entries name it.
type rareTerm struct {
	term string
	df   int
}

func (s *Store) relevance(q RelevanceQuery) (Relevance, error) {
	var out Relevance
	limit := q.Limit
	if limit <= 0 {
		limit = 3
	}
	terms := distinctiveTerms(q.Text)
	if len(terms) == 0 {
		return out, nil
	}
	// The counts below see only what the viewer may read, so an entry of somebody
	// else neither displaces a result nor shifts how rare a term looks.
	scopeSQL, scopeArgs := withGlobalScope(`1=1`, nil, "knowledge", q.Viewer)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge
		WHERE status = 'active' AND confidence != 'quarantined' AND `+scopeSQL, scopeArgs...).Scan(&total); err != nil {
		return out, err
	}
	// At least one, so a young archive where every term is technically rare
	// still requires a term to point at a single entry rather than at several.
	ceiling := max(1, int(float64(total)*maxDocumentFrequency))
	// Never above the ceiling: in an archive of thirty entries a word in two of
	// them is a fifteenth of it, and the share above already said no.
	veryRare := min(veryRareEntries, ceiling)

	gate := s.newLanguageGate(q.Account)
	// First gate: rare among the entries. A word in none of them stays in, for
	// the hint; it can only fetch knowledge if at least one entry names it.
	// Second gate: unusual in the reader's own language. Only the words that got
	// past the first reach it, which is what keeps it cheap.
	var rare []rareTerm
	for _, term := range terms {
		n, err := s.documentFrequency(term, ceiling+1, q.Viewer...)
		if err != nil {
			return out, err
		}
		if n > ceiling {
			continue
		}
		ok, err := gate.unusual(term)
		if err != nil {
			return out, err
		}
		if ok {
			rare = append(rare, rareTerm{term: term, df: n})
		}
	}
	if len(rare) == 0 {
		return out, nil
	}

	// Only the rarest few ask the index. A long prompt turns up unusual words by
	// accident — a pasted stack trace, a list of plugin names — and any one of
	// them is enough to reach something if all of them are asked. Measured
	// against real archived prompts, using every rare term fired on four
	// sentences in five.
	var named []rareTerm
	for _, r := range rare {
		if r.df > 0 {
			named = append(named, r)
		}
	}
	// Stable, so words equally rare keep the order in which the sentence said
	// them. Nothing breaks the tie by how common a word is in the session text:
	// that count includes sessions the reader may not open, and an order that
	// followed it would let a guest compare how often two words occur there.
	sort.SliceStable(named, func(i, j int) bool { return named[i].df < named[j].df })
	if len(named) > maxRareTerms {
		named = named[:maxRareTerms]
	}

	where, args := q.Axes.UnionWhere()
	where, args = withGlobalScope(where, args, "k", q.Viewer)
	remembered := CleanSessionKey(q.Session) != ""
	seen := map[int64]bool{}
	// One question per word rather than one for all of them: an entry then comes
	// with the word that fetched it, and the rarest word's entries come first,
	// which is the order of the reasons and not of how long the sentence is.
	for _, r := range named {
		if len(out.Hits) >= limit {
			break
		}
		// Matched against the title, not the body. A term in the body means the
		// entry mentions the thing; a term in the title means the entry is
		// about it, and only the second is a reason to speak up unasked. What
		// this loses is still reachable by search, which is the right place for
		// a hunch. The exception is a word so rare that a mention anywhere is
		// the whole subject: a codename is rarely in a title, and what it is
		// stands in the one entry that names it.
		match := `title : "` + r.term + `"`
		if r.df <= veryRare {
			match = `"` + r.term + `"`
		}
		query := `SELECT ` + prefix(knowledgeCols, "k.") + `
			FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
			WHERE knowledge_fts MATCH ? AND ` + where + ` AND ` + deliverable
		qargs := append([]any{match}, args...)
		if remembered {
			query += ` AND NOT ` + deliveredToSessionSQL
			qargs = append(qargs, q.Account, q.Session)
		}
		rows, err := s.db.Query(query+` ORDER BY f.rank LIMIT ?`, append(qargs, limit)...)
		if err != nil {
			return out, err
		}
		ks, err := s.scanKnowledge(rows)
		if err != nil {
			return out, err
		}
		for _, k := range ks {
			if seen[k.ID] || len(out.Hits) >= limit {
				continue
			}
			seen[k.ID] = true
			out.Hits = append(out.Hits, RelevantHit{Knowledge: k, Term: r.term})
		}
	}
	// Being handed an entry because it fits the moment is the strongest form of
	// use there is, so it counts as one — this is the signal that tells a useful
	// entry from one nobody has ever needed.
	if q.Record && len(out.Hits) > 0 {
		s.recordKnowledgeSearchHit(hitKnowledge(out.Hits))
	}

	hints, err := s.hintTerms(q, rare, veryRare, where, args)
	if err != nil {
		return out, err
	}
	out.Hints = hints
	return out, nil
}

// hintTerms picks the words a hint may be due for: the very rare ones that this
// session was not already pointed at, in the order the sentence said them, each
// with the entries that name it. The order is the sentence's and not a measure of
// rarity for the same reason as above: rarity in the session text is not
// something a reader gets to compare.
func (s *Store) hintTerms(q RelevanceQuery, rare []rareTerm, veryRare int, where string, args []any) ([]HintTerm, error) {
	var hintable []rareTerm
	for _, r := range rare {
		if r.df <= veryRare {
			hintable = append(hintable, r)
		}
	}
	if CleanSessionKey(q.Session) != "" && len(hintable) > 0 {
		words := make([]string, len(hintable))
		for i, h := range hintable {
			words[i] = h.term
		}
		hinted, err := s.hintedTerms(q.Account, q.Session, words)
		if err != nil {
			return nil, err
		}
		kept := hintable[:0]
		for _, h := range hintable {
			if !hinted[h.term] {
				kept = append(kept, h)
			}
		}
		hintable = kept
	}
	if len(hintable) > maxRareTerms {
		hintable = hintable[:maxRareTerms]
	}
	var out []HintTerm
	for _, h := range hintable {
		hint := HintTerm{Term: h.term}
		if h.df > 0 {
			rows, err := s.db.Query(`SELECT `+prefix(knowledgeCols, "k.")+`
				FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
				WHERE knowledge_fts MATCH ? AND `+where+` AND `+deliverable+` ORDER BY f.rank LIMIT ?`,
				append(append([]any{`"` + h.term + `"`}, args...), veryRareEntries)...)
			if err != nil {
				return nil, err
			}
			if hint.Known, err = s.scanKnowledge(rows); err != nil {
				return nil, err
			}
		}
		out = append(out, hint)
	}
	return out, nil
}

// documentFrequency counts the active entries that name the term, up to upTo. The
// caller only ever compares the count with the ceiling, so a word that is in
// half the archive is ruled out after upTo rows instead of after reading them
// all, which on a keystroke path is most of the words of an ordinary sentence.
func (s *Store) documentFrequency(term string, upTo int, viewer ...SessionViewer) (int, error) {
	var n int
	scopeSQL, scopeArgs := withGlobalScope(`1=1`, nil, "k", viewer)
	err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND k.status = 'active' AND k.confidence != 'quarantined' AND `+scopeSQL+` LIMIT ?)`,
		append(append([]any{`"` + term + `"`}, scopeArgs...), upTo)...).Scan(&n)
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
