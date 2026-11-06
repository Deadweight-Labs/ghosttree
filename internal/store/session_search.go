package store

import (
	"database/sql"
	"errors"
	"slices"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Hybrid search over sessions: the full-text ranking of the sessions (in the order
// of their first hit) and a ranking by the vectors of their texts are fused by
// reciprocal rank, the vectors with the weight EmbedSearchConfig.VectorWeights
// gives (2 by default), at most five of the ten hits only by meaning. The score
// of a session in the vector ranking is the cosine of the text that is closest to
// the query.
//
// What a reader may see is settled before either ranking is made: both read only
// the sessions of readableSessions, so a session that is hidden from the reader is
// neither compared nor given a rank, and cannot move one that is shown (#2447).
// What the caller applies afterwards (CanSeeTranscript, MetaView) is the rule it
// always applied. A vector never leaves the server; a hit shows text the reader may
// read.

// SessionSearch describes one search over sessions, the way a caller already asks
// SearchSessionsVisible: the filter, the session to leave out (the one that is
// asking), how many hits, and what the reader may see (Keep is the row rule, Prefilter
// the part of it that is SQL).
type SessionSearch struct {
	Filter         scope.Axes
	ExcludeSession string
	Limit          int
	Keep           func(Session) bool
	Prefilter      SessionPrefilter
	// Mode is SearchAuto in production, and see SearchMode for the others. Without a
	// vector ranking (not configured, not switched on for sessions, a query of
	// ordinary words, the embedding server failing) the answer is that of
	// SearchSessionsVisible: one row for every chunk the words found.
	Mode SearchMode
}

// SearchSessionsWith is SearchSessionsVisible that, with an embedding server
// configured and sessions switched on for search, ranks the query by words and by
// meaning. What comes back is then a list of sessions, one hit each (the first
// the words found in it, or for a session that only the embedding found the text
// that came closest, marked Match "semantic"), not of chunks.
func (s *Store) SearchSessionsWith(q string, o SessionSearch) ([]SessionHit, error) {
	if s.reader != nil {
		return s.reader.SearchSessionsWith(q, o)
	}
	qvec, cfg, err := s.queryVector(q, o.Mode, KindSession)
	if err != nil {
		return nil, err
	}
	if qvec != nil {
		hits, err := s.hybridSessions(q, o, qvec, cfg)
		switch {
		case err == nil:
			s.embedShared().hybrid.Add(1)
			return hits, nil
		case o.Mode.strict():
			return nil, err
		}
		s.embedShared().logFallback(cfg, err)
	}
	return s.SearchSessionsVisible(q, o.Filter, o.ExcludeSession, o.Limit, o.Keep, o.Prefilter)
}

// sessionBest is the text of a session that is closest to a query.
type sessionBest struct {
	session int64
	chunk   int64
	seq     int
	score   float32
}

// sessionVectorRanking ranks the sessions of r by the cosine of their best text
// and the query vector, best first, ties by session number; a session whose best
// text is below the minimum similarity is left out. Only the vectors of the
// configured model and text format are read, and of the sessions of r (the
// asking session excepted): nothing outside is compared.
func (s *Store) sessionVectorRanking(cfg *EmbedSearchConfig, qvec []float32, exclude string, r readableSessions) ([]sessionBest, error) {
	rows, err := s.db.Query(`SELECT c.session_id, c.id, c.seq, e.scale, e.vec
		FROM session_embeddings e
		JOIN session_chunks c ON c.id = e.chunk_id
		JOIN sessions se ON se.id = c.session_id
		WHERE e.model = ? AND e.prefix_version = ? AND (? = '' OR se.external_id != ?) AND `+r.where,
		append([]any{cfg.Embedder.Model(), cfg.Embedder.PrefixVersion(), exclude, exclude}, r.args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	best := map[int64]sessionBest{}
	mismatched := 0
	for rows.Next() {
		var b sessionBest
		var scale float64
		var raw sql.RawBytes
		if err := rows.Scan(&b.session, &b.chunk, &b.seq, &scale, &raw); err != nil {
			return nil, err
		}
		if len(raw) != len(qvec) {
			mismatched++
			continue
		}
		b.score = embed.Cosine(qvec, raw, float32(scale))
		if cfg.MinSimilarity > 0 && b.score < cfg.MinSimilarity {
			continue
		}
		// The first of two texts with the same score wins, the one of the earlier chunk.
		if cur, ok := best[b.session]; !ok || b.score > cur.score || (b.score == cur.score && b.chunk < cur.chunk) {
			best[b.session] = b
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if mismatched > 0 {
		s.embedShared().warnDimension(cfg, mismatched, len(qvec))
	}
	out := make([]sessionBest, 0, len(best))
	for _, b := range best {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b sessionBest) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		case a.session < b.session:
			return -1
		case a.session > b.session:
			return 1
		}
		return 0
	})
	return out, nil
}

// semanticSnippetRunes is how much of the text that came closest a hit shows.
const semanticSnippetRunes = 200

// semanticText is the beginning of the text of a chunk as a hit shows it, made
// by the chain that made the text for the embedding, and the kind of hit it is
// ("user" or "assistant", as the web view says it).
func (s *Store) semanticText(harness string, b sessionBest) (snippet, kind string, err error) {
	raws, err := s.chunkRaws([]int64{b.chunk})
	if err != nil {
		return "", "", err
	}
	snippet = transcript.OneLine(cleanSessionText(harness, b.seq, raws[b.chunk]), semanticSnippetRunes)
	kind = "assistant"
	var role string
	if err := s.db.QueryRow(`SELECT role FROM chunk_index WHERE chunk_id = ?`, b.chunk).Scan(&role); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if role == "user" {
		kind = "user"
	}
	return snippet, kind, nil
}

// hybridSessions answers a query for sessions from the words and the vectors.
func (s *Store) hybridSessions(q string, o SessionSearch, qvec []float32, cfg *EmbedSearchConfig) ([]SessionHit, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 20
	}
	r, err := s.readableSessions(o.Filter, o.Keep, o.Prefilter)
	if err != nil {
		return nil, err
	}
	if r.none {
		return []SessionHit{}, nil
	}
	pool := max(hybridPool, limit)

	// The words: the chunks they were found in, in the order of the full-text
	// ranking, as the sessions they belong to in the order of their first hit. The
	// chunk of that first hit is the hit that is shown.
	var textIDs []int64
	first := map[int64]int{}
	var rowsOf []SessionHit
	var cids []int64
	if o.Mode != SearchVectorOnly {
		if rowsOf, cids, err = s.ftsSessionHits(q, o.ExcludeSession, r, o.Prefilter, keepScanCap); err != nil {
			return nil, err
		}
		for i, h := range rowsOf {
			if _, seen := first[h.Session.ID]; !seen {
				first[h.Session.ID] = i
				textIDs = append(textIDs, h.Session.ID)
			}
		}
	}
	best, err := s.sessionVectorRanking(cfg, qvec, o.ExcludeSession, r)
	if err != nil {
		return nil, err
	}
	closest := make(map[int64]sessionBest, len(best))
	var vecIDs []int64
	for i, b := range best {
		closest[b.session] = b
		if i < pool {
			vecIDs = append(vecIDs, b.session)
		}
	}
	room := maxSemanticOnly(limit)
	if o.Mode == SearchVectorOnly {
		room = limit
	}
	fused := fuseRankingsWeighted(textIDs, vecIDs, limit, room, cfg.vectorWeight(KindSession))

	// The sessions that only the embedding found are read from the table.
	var onlyVec []int64
	for _, f := range fused {
		if _, ok := first[f.ID]; !ok {
			onlyVec = append(onlyVec, f.ID)
		}
	}
	vecSessions := map[int64]Session{}
	if len(onlyVec) > 0 {
		args := make([]any, len(onlyVec))
		for i, id := range onlyVec {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE id IN (`+placeholders(len(args))+`)`, args...)
		if err != nil {
			return nil, err
		}
		found, err := scanSessions(rows)
		if err != nil {
			return nil, err
		}
		for _, se := range found {
			vecSessions[se.ID] = se
		}
	}
	out := make([]SessionHit, 0, len(fused))
	var outCids []int64
	for _, f := range fused {
		if i, ok := first[f.ID]; ok {
			out = append(out, rowsOf[i])
			outCids = append(outCids, cids[i])
			continue
		}
		se, ok := vecSessions[f.ID]
		if !ok {
			continue
		}
		snippet, _, err := s.semanticText(se.Harness, closest[f.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, SessionHit{Session: se, Seq: closest[f.ID].seq, Snippet: snippet, Match: MatchSemantic})
		outCids = append(outCids, 0)
	}
	// The text of a hit the words found, with its snippet; the others have theirs.
	var withWords []SessionHit
	var withWordsCids []int64
	var at []int
	for i, h := range out {
		if h.Match == "" {
			withWords = append(withWords, h)
			withWordsCids = append(withWordsCids, outCids[i])
			at = append(at, i)
		}
	}
	if err := s.sessionSnippets(q, withWords, withWordsCids); err != nil {
		return nil, err
	}
	for j, i := range at {
		out[i] = withWords[j]
	}
	if err := s.fillHitOwners(out); err != nil {
		return nil, err
	}
	return out, nil
}

// transcriptKindUsesVectors says whether the search of the web view for a kind of
// text (see SearchQuery.Kind) uses the vectors. They are of messages, what was
// typed and what was answered: the kinds that look only at the output, the
// commands or the thinking are what they say and are answered by words.
func transcriptKindUsesVectors(kind string) bool {
	return kind == "" || kind == "messages" || kind == "all"
}

// transcriptVectors ranks the readable sessions of a web search by meaning, when
// the search uses the vectors: the order is by relevance (the sort by time is
// what it says), the kind of text is one of messages, sessions are switched on
// for search, and the query is not one of ordinary words. It returns nil for a
// search that is answered by words alone. A failure of the vectors is logged, and
// the search goes on without them.
func (s *Store) transcriptVectors(q SearchQuery, readable []Session) ([]sessionBest, *EmbedSearchConfig) {
	if q.Sort == "newest" || !transcriptKindUsesVectors(q.Kind) {
		return nil, nil
	}
	qvec, cfg, _ := s.queryVector(q.Q, SearchAuto, KindSession)
	if qvec == nil {
		return nil, nil
	}
	best, err := s.sessionVectorRanking(cfg, qvec, "", readableSessions{
		where: `(1=1) AND se.id IN (SELECT value FROM json_each(?))`, args: []any{idsJSON(readable)}, restricted: true})
	if err != nil {
		s.embedShared().logFallback(cfg, err)
		return nil, nil
	}
	s.embedShared().hybrid.Add(1)
	return best, cfg
}

// fuseTranscripts fuses the sessions the words found (kept, in the order of the
// full-text ranking, after the filters) with those the vectors rank. The vector
// ranking is cut to the sessions that pass the filters before its depth is
// taken, and fused with the weight of sessions; the sessions that only the vectors
// have get a row of their own, marked semantic, with the closest text as their
// hit. At most five sessions of the whole list, whatever its pages, are there
// by meaning alone (or half of a short page). The result does not depend on the
// page asked for, so that pages add up.
func (a *ProjectAccess) fuseTranscripts(kept []matchedSession, vec []sessionBest, q SearchQuery, byID map[int64]Session, cfg *EmbedSearchConfig) []matchedSession {
	textIDs := make([]int64, len(kept))
	byText := make(map[int64]matchedSession, len(kept))
	for i, m := range kept {
		textIDs[i] = m.sess.ID
		byText[m.sess.ID] = m
	}
	var vecIDs []int64
	closest := map[int64]sessionBest{}
	for _, b := range vec {
		sess, ok := byID[b.session]
		if !ok || !a.matchFilter(sess, q.Filter, dimAll) {
			continue
		}
		vecIDs = append(vecIDs, b.session)
		closest[b.session] = b
		if len(vecIDs) >= hybridPool {
			break
		}
	}
	fused := fuseRankingsWeighted(textIDs, vecIDs, len(textIDs)+len(vecIDs), maxSemanticOnly(q.Limit), cfg.vectorWeight(KindSession))
	out := make([]matchedSession, 0, len(fused))
	for _, f := range fused {
		if m, ok := byText[f.ID]; ok {
			out = append(out, m)
			continue
		}
		b := closest[f.ID]
		out = append(out, matchedSession{sess: byID[f.ID], semantic: true, closest: b})
	}
	return out
}
