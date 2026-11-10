// Package ftssnippet makes FTS5 snippets for documents whose full-text index
// keeps no copy of the text (content=”). The few documents of one page of hits
// go into a small in-memory FTS5 table with the same columns and the default
// tokenizer, and snippet() runs there. snippet() picks its fragment from the
// text of the row and the expression alone, so the result is the one the
// stored index gave while it still held the text.
package ftssnippet

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// Index is an in-memory FTS5 table with fixed columns. It is safe for
// concurrent use; calls are serialized, each holds the table only while it runs.
type Index struct {
	cols []string
	mu   sync.Mutex
	db   *sql.DB
}

// New describes an index with these columns. Nothing is opened until the first call.
func New(cols ...string) *Index { return &Index{cols: cols} }

func (x *Index) open() (*sql.DB, error) {
	if x.db != nil {
		return x.db, nil
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	// Every connection would have a database of its own.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	x.db = db
	return db, nil
}

// Snippets returns, for every document the expression matches, snippet() of each
// column, with open and close around the matched words and at most tokens words
// (1 to 64) per fragment. Documents without a match are left out. docs maps an
// id to the values of the columns in the order given to New.
func (x *Index) Snippets(ctx context.Context, docs map[int64][]string, expr, open, close string, tokens int) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(docs) == 0 {
		return out, nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	db, err := x.open()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cols := strings.Join(x.cols, ", ")
	if _, err := conn.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS snip USING fts5(`+cols+`)`); err != nil {
		return nil, err
	}
	// Emptied again whatever happens, so the next call sees nothing of this one.
	defer conn.ExecContext(context.Background(), `DELETE FROM snip`)
	if _, err := conn.ExecContext(ctx, `DELETE FROM snip`); err != nil {
		return nil, err
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(x.cols)+1), ",")
	for id, vals := range docs {
		if len(vals) != len(x.cols) {
			return nil, fmt.Errorf("document %d has %d values for %d columns", id, len(vals), len(x.cols))
		}
		args := make([]any, 0, len(vals)+1)
		args = append(args, id)
		for _, v := range vals {
			args = append(args, v)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO snip(rowid, `+cols+`) VALUES(`+marks+`)`, args...); err != nil {
			return nil, err
		}
	}
	sel := make([]string, len(x.cols))
	for i := range x.cols {
		sel[i] = fmt.Sprintf("snippet(snip, %d, ?1, ?2, '…', ?3)", i)
	}
	rows, err := conn.QueryContext(ctx, `SELECT rowid, `+strings.Join(sel, ", ")+` FROM snip WHERE snip MATCH ?4`, open, close, tokens, expr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		vals := make([]string, len(x.cols))
		dest := []any{&id}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out[id] = vals
	}
	return out, rows.Err()
}
