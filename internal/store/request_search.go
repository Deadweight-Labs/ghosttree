package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// hybridCursorPrefix marks a cursor that is a position in a ranking and not a
// request id. The list of requests is ordered by id, and its cursor is the last
// id of a page. A hybrid search is ordered by rank, which no id can resume, so
// its cursor is the number of hits already shown, written ~N.
const hybridCursorPrefix = "~"

// HybridCursor is the cursor of the page that starts after offset hits of a
// ranked request search.
func HybridCursor(offset int) string { return hybridCursorPrefix + strconv.Itoa(offset) }

func parseHybridCursor(cursor string) (offset int, ok bool) {
	n, found := strings.CutPrefix(cursor, hybridCursorPrefix)
	if !found {
		return 0, false
	}
	offset, err := strconv.Atoi(n)
	if err != nil || offset < 0 {
		return 0, false
	}
	return offset, true
}

// ValidRequestCursor says whether a cursor of a request search is one this
// package hands out: the id of a request, or a position in a ranking.
func ValidRequestCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	if _, ok := parseHybridCursor(cursor); ok {
		return true
	}
	_, err := strconv.ParseInt(cursor, 10, 64)
	return err == nil
}

func requestLimit(limit int) int {
	if limit <= 0 {
		limit = 10
	}
	return min(limit, 25)
}

// SearchRequests lists requests, newest first: the filter, and with a query the
// requests whose words match it, in the order of their ids. It does not use
// embeddings; SearchRequestsWith does.
func (s *Store) SearchRequests(filter requestdomain.SearchFilter) (requestdomain.SearchPage, error) {
	if s.reader != nil {
		return s.reader.SearchRequests(filter)
	}
	return s.requestsByList(filter)
}

// SearchRequestsWith is SearchRequests that, with an embedding server configured
// and requests switched on for search, ranks a query by words and by meaning:
// the full-text ranking and the ranking by the cosine of the stored vectors are
// fused by reciprocal rank, and what comes back is in that order, not in the
// order of the ids.
//
// A ranking has no id to resume after, so its pages are counted: NextCursor is
// ~N, the number of hits already shown, and a cursor of that form is read as a
// position. Pages add up: each is a slice of one fusion, the same for every page,
// because both rankings are read to a depth that does not depend on the page
// (requestPool rows each, ties broken by id) and the hits that only the embedding
// found (marked Match "semantic", at most five in all) are admitted the same way
// for every page. The fusion ends where the rankings do; when they were cut at
// their depth, the last page says so (Truncated) and has no cursor.
//
// Which mode a cursor belongs to is the cursor's business and not the search's:
// a cursor that is an id always continues the list by ids, a cursor ~N always
// continues a ranking. So a page that was answered from the list because the
// embedding server failed does not make the next page start the ranking over, and
// a ranked cursor that reaches a search that cannot rank is read as "skip N" in
// the list (the order differs from the ranking, but it keeps going forward).
// Without a query, with a query that asks for the list of the backlog, and
// wherever the embedding is not used, the answer is the list SearchRequests gives,
// with its cursor of ids. The visibility of the filter is the same SQL for both
// rankings, so a request the reader may not see is not compared and moves no
// other.
//
// mode is SearchAuto in production; the others are for the quality measurement,
// which asks for the rankings one at a time, never answered by the fallback.
func (s *Store) SearchRequestsWith(filter requestdomain.SearchFilter, mode SearchMode) (requestdomain.SearchPage, error) {
	if s.reader != nil {
		return s.reader.SearchRequestsWith(filter, mode)
	}
	_, ranked := parseHybridCursor(filter.Cursor)
	idCursor := filter.Cursor != "" && !ranked
	if filter.Query != "" && !idCursor && (mode.strict() || ClassifySearch(filter.Query) == SearchFullText) {
		qvec, cfg, err := s.queryVector(filter.Query, mode, KindRequest)
		if err != nil {
			return requestdomain.SearchPage{}, err
		}
		if qvec != nil {
			page, err := s.hybridRequests(filter, mode, qvec, cfg)
			switch {
			case err == nil:
				s.embedShared().hybrid.Add(1)
				return page, nil
			case mode.strict():
				return requestdomain.SearchPage{}, err
			}
			s.embedShared().logFallback(cfg, err)
		}
	}
	return s.requestsByList(filter)
}

// requestAttrWhere is the part of a request query that every ranking shares: the
// scope and visibility of the reader and the filters of the page.
func requestAttrWhere(filter requestdomain.SearchFilter) ([]string, []any) {
	where, args := requestScopeWhere(filter)
	if filter.State != "" {
		where = append(where, `r.state=?`)
		args = append(args, filter.State)
	}
	if filter.Type != "" {
		where = append(where, `r.type=?`)
		args = append(args, filter.Type)
	}
	if filter.Priority != "" {
		where = append(where, `r.priority=?`)
		args = append(args, filter.Priority)
	}
	return where, args
}

// requestHitSelect is the head of a query for the hits of a search.
func requestHitSelect(filter requestdomain.SearchFilter) string {
	// substr, not the full column: a search page is a list, and a list shows a
	// snippet. Sending every description in full made twenty-four requests
	// exceed what a tool result may return, and it is no better for a card or a
	// terminal row. GetRequest answers with the whole text.
	description := "substr(r.description,1," + strconv.Itoa(snippetChars) + ")"
	if filter.FullDescription {
		description = "r.description"
	}
	return `SELECT r.id,r.type,r.title,` + description + `,r.state,r.priority,r.project,r.branch,r.machine,r.origin,r.person,r.session_ref,r.created_at,r.updated_at,
		(SELECT COUNT(*) FROM request_criteria c WHERE c.request_id=r.id AND c.state='open'),
		COALESCE((SELECT w.summary FROM request_work w WHERE w.request_id=r.id AND w.summary!='' ORDER BY w.id DESC LIMIT 1),''),
		(SELECT COUNT(DISTINCT g.session_id) FROM request_sightings g WHERE g.request_id=r.id)
		FROM requests r`
}

func scanRequestHits(rows *sql.Rows) ([]requestdomain.SearchHit, error) {
	defer rows.Close()
	var out []requestdomain.SearchHit
	for rows.Next() {
		var hit requestdomain.SearchHit
		r := &hit.Request
		if err := rows.Scan(&r.ID, &r.Type, &r.Title, &r.Description, &r.State, &r.Priority, &r.Scope.Project, &r.Scope.Branch, &r.Scope.Machine, &r.Origin, &r.Person, &r.SessionRef, &r.CreatedAt, &r.UpdatedAt, &hit.OpenCriteria, &hit.LatestHandoff, &hit.Sightings); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

// requestsByList is the list of requests as it has always been answered: ordered
// by id, a query a filter on the full-text index, the cursor the last id of a
// page (or, when a ranked search has fallen back to it, ~N: skip that many).
func (s *Store) requestsByList(filter requestdomain.SearchFilter) (requestdomain.SearchPage, error) {
	limit := requestLimit(filter.Limit)
	query := requestHitSelect(filter)
	var where []string
	var args []any
	// A query of nothing but ordinary words asks to see the backlog, not to
	// find one entry in it. Matching it would answer "was ist noch zu tun" with
	// whichever request happens to contain all five words — measured on
	// 2026-08-24: one of twenty-four, effectively at random.
	if ClassifySearch(filter.Query) == SearchFullText {
		query += ` JOIN search_documents d ON d.kind='request' AND d.domain_id=r.id JOIN search_documents_fts f ON f.rowid=d.id`
		where = append(where, `search_documents_fts MATCH ?`)
		args = append(args, ftsQuery(filter.Query))
	}
	attrWhere, attrArgs := requestAttrWhere(filter)
	where = append(where, attrWhere...)
	args = append(args, attrArgs...)
	offset, ranked := parseHybridCursor(filter.Cursor)
	if filter.Cursor != "" && !ranked {
		where = append(where, `r.id<?`)
		args = append(args, filter.Cursor)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit+1)
	if ranked && offset > 0 {
		query += ` OFFSET ?`
		args = append(args, offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	results, err := scanRequestHits(rows)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	page := requestdomain.SearchPage{Results: results}
	if filter.Query != "" {
		for i := range page.Results {
			page.Results[i].MatchReason = "full-text match"
		}
	}
	if len(page.Results) > limit {
		if ranked {
			page.NextCursor = HybridCursor(offset + limit)
		} else {
			page.NextCursor = fmt.Sprintf("%d", page.Results[limit-1].Request.ID)
		}
		page.Results = page.Results[:limit]
	}
	return page, nil
}

// requestPool is how many requests each ranking of a ranked search contributes
// to the fusion. It is a number of the search and not of its page: the fusion
// that page 4 slices must be the one that page 1 sliced, or hits fall between
// the pages and others show twice. It is the depth the row rule of the other
// kinds is willing to scan (keepScanCap); a variable only so that a test can
// lower it.
var requestPool = keepScanCap

// fuseRequests is the whole fusion of a ranked request search: the requests in
// order, which of them only the embedding found, and whether either ranking was
// cut at requestPool, so that more requests match than the fusion holds. It does
// not depend on the cursor, only on the query, the filter and the page size (the
// room for hits of meaning alone is a share of one page).
func (s *Store) fuseRequests(filter requestdomain.SearchFilter, mode SearchMode, qvec []float32, cfg *EmbedSearchConfig) (fused []fusedHit, truncated bool, err error) {
	limit := requestLimit(filter.Limit)
	pool := requestPool
	where, args := requestAttrWhere(filter)
	cond := strings.Join(where, " AND ")

	var textIDs []int64
	if mode != SearchVectorOnly {
		query := `SELECT r.id FROM requests r
			JOIN search_documents d ON d.kind='request' AND d.domain_id=r.id
			JOIN search_documents_fts f ON f.rowid=d.id
			WHERE search_documents_fts MATCH ?`
		if cond != "" {
			query += ` AND ` + cond
		}
		// The id decides a tie, so that the same query always reads the same rows.
		query += ` ORDER BY f.rank, r.id LIMIT ?`
		rows, err := s.db.Query(query, append(append([]any{ftsQuery(filter.Query)}, args...), pool+1)...)
		if err != nil {
			return nil, false, err
		}
		if textIDs, err = scanIDs(rows); err != nil {
			return nil, false, err
		}
	}
	k, err := embedKindOf(KindRequest)
	if err != nil {
		return nil, false, err
	}
	vecIDs, err := s.vectorIDs(k, cfg, qvec, cond, args)
	if err != nil {
		return nil, false, err
	}
	// The text query asked for one row more than the pool, so that a ranking that
	// has exactly pool rows is not taken for a cut one; the vector ranking holds
	// every candidate and is cut here.
	//
	// Which list counts depends on what the search is for. Fused, the words decide:
	// a request that matches them is in the text ranking if that ranking is whole,
	// and of the neighbours only the best few join the fusion whatever the depth of
	// the vector ranking, so a vector ranking that was cut loses no hit. Only the
	// ranking by meaning alone (the measurement) is cut by its own depth.
	if mode == SearchVectorOnly {
		truncated = len(vecIDs) > pool
	} else {
		truncated = len(textIDs) > pool
	}
	textIDs, vecIDs = textIDs[:min(len(textIDs), pool)], vecIDs[:min(len(vecIDs), pool)]
	room := maxSemanticOnly(limit)
	if mode == SearchVectorOnly {
		room = len(vecIDs)
	}
	return fuseRankings(textIDs, vecIDs, len(textIDs)+len(vecIDs), room), truncated, nil
}

// hybridRequests answers a query with the page of its fusion that the cursor
// names.
func (s *Store) hybridRequests(filter requestdomain.SearchFilter, mode SearchMode, qvec []float32, cfg *EmbedSearchConfig) (requestdomain.SearchPage, error) {
	limit := requestLimit(filter.Limit)
	offset, _ := parseHybridCursor(filter.Cursor)
	fused, truncated, err := s.fuseRequests(filter, mode, qvec, cfg)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	var page requestdomain.SearchPage
	if offset >= len(fused) {
		page.Truncated = truncated
		return page, nil
	}
	shown := fused[offset:min(offset+limit, len(fused))]
	if len(fused) > offset+limit {
		page.NextCursor = HybridCursor(offset + limit)
	} else {
		// The last page. If the rankings were cut there is more than it, out of
		// reach of any cursor; say so instead of ending as if that were all.
		page.Truncated = truncated
	}
	ids := make([]int64, len(shown))
	inArgs := make([]any, len(shown))
	for i, f := range shown {
		ids[i], inArgs[i] = f.ID, f.ID
	}
	rows, err := s.db.Query(requestHitSelect(filter)+` WHERE r.id IN (`+placeholders(len(ids))+`)`, inArgs...)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	loaded, err := scanRequestHits(rows)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	byID := make(map[int64]requestdomain.SearchHit, len(loaded))
	for _, h := range loaded {
		byID[h.Request.ID] = h
	}
	for _, f := range shown {
		h, ok := byID[f.ID]
		if !ok {
			continue
		}
		h.MatchReason = "full-text match"
		if f.Semantic {
			h.Match, h.MatchReason = MatchSemantic, "semantic match"
		}
		page.Results = append(page.Results, h)
	}
	return page, nil
}
