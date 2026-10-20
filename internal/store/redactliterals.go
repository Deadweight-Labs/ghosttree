package store

import (
	"sort"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
)

func redactQuoteKey(table string, id int64) string { return table + "/" + strconv.FormatInt(id, 10) }

// literalQuotes replaces known values in every stored quote, also in those
// whose chunk is not (or no longer) there: requote only visits the quotes of
// chunks that changed in this run. The tables are small. It returns the rows
// changed (would change, in a dry run) per table column.
func (w *redactWork) literalQuotes() (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range []struct {
		table string
		on    bool
	}{{"knowledge_evidence", w.evidence}, {"request_sightings", w.sightings}} {
		if !t.on {
			continue
		}
		type row struct {
			id           int64
			quote, fixed string
		}
		var found []row
		rows, err := w.db.QueryContext(w.ctx, `SELECT id, quote FROM `+t.table+` WHERE quote != ''`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.quote); err != nil {
				rows.Close()
				return nil, err
			}
			if _, done := w.requoted[redactQuoteKey(t.table, r.id)]; done && !w.write {
				continue // a dry run has not written the quote that requote counted
			}
			if r.fixed = w.lits.Apply(r.quote, redact.Text, nil); r.fixed != r.quote {
				found = append(found, r)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			continue
		}
		if !w.write {
			out[t.table+".quote"] += int64(len(found))
			continue
		}
		tx, err := w.db.BeginTx(w.ctx, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			res, err := tx.ExecContext(w.ctx, `UPDATE `+t.table+` SET quote=? WHERE id=? AND quote=?`, r.fixed, r.id, r.quote)
			if err != nil {
				tx.Rollback()
				return nil, err
			}
			c, _ := res.RowsAffected()
			out[t.table+".quote"] += c
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// holders counts the texts that people and agents wrote and that hold a known
// value. They are not changed, see RedactSessions.
func (w *redactWork) holders() (map[string]RedactHolders, error) {
	out := map[string]RedactHolders{}
	for _, t := range []struct {
		table, owner string
		cols         [2]string
	}{
		{"knowledge", "id", [2]string{"title", "body"}},
		{"knowledge_versions", "knowledge_id", [2]string{"title", "body"}},
		{"requests", "id", [2]string{"title", "description"}},
	} {
		ok, err := w.has(t.table)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rows, err := w.db.QueryContext(w.ctx, `SELECT `+t.owner+`, `+t.cols[0]+`, `+t.cols[1]+` FROM `+t.table)
		if err != nil {
			return nil, err
		}
		var h RedactHolders
		seen := map[int64]struct{}{}
		for rows.Next() {
			var owner int64
			var a, b string
			if err := rows.Scan(&owner, &a, &b); err != nil {
				rows.Close()
				return nil, err
			}
			if !w.lits.Contains(a) && !w.lits.Contains(b) {
				continue
			}
			h.Rows++
			if _, dup := seen[owner]; !dup {
				seen[owner] = struct{}{}
				h.Owners = append(h.Owners, owner)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if h.Rows > 0 {
			sort.Slice(h.Owners, func(i, j int) bool { return h.Owners[i] < h.Owners[j] })
			out[t.table] = h
		}
	}
	return out, nil
}
