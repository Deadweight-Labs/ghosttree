package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/ftssnippet"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Ein Suchpfad über Sessions (REQ-622): die Wörter eines Chunks stehen in
// chunk_fts, einem Volltextindex ohne gespeicherten Inhalt. Treffer, Rang und
// Zählungen kommen aus dem Index; ein Ausschnitt braucht den Text und wird für
// die wenigen Chunks einer Ergebnisseite aus raw neu abgeleitet.
//
// Ältere Zeilen (chunk_index.fts = 0) stehen noch in sess_fts mit ihrem Inhalt
// in chunk_index, bis ctx db slim-search sie umträgt. Jede Abfrage liest
// deshalb beide Indizes (wordHits); eine Zeile steht in genau einem.

// promptTextCol ist der Text eines Prompts in chunk_index: bei neuen Zeilen in
// prompt, bei älteren (fts = 0) noch in text.
const promptTextCol = "CASE WHEN fts = 1 THEN prompt ELSE text END"

// keptText ist der Text, den chunk_index selbst behält (Spalte prompt): der
// eines Prompts. Die Gliederung einer Session und die Auswahl der Texte für
// Vektoren lesen ihn (SessionOutline, sessionPicked, über promptTextCol); alles
// andere steht nur als Wörter im Index.
func keptText(f transcript.IndexFields) string {
	if f.Prompt {
		return f.Text
	}
	return ""
}

// wordHits ist die Abfrage der Chunks, die ein Volltextausdruck findet, als
// Zeilen (rid, r) aus chunk_fts und sess_fts. rank ist "" (r = 0), "rank" (bm25
// ohne Gewichte) oder die Gewichte von bm25. Der Ausdruck steht zweimal in der
// Abfrage: Argumente mit wordArgs.
func wordHits(rank string) string {
	col := func(table string) string {
		switch rank {
		case "":
			return "0"
		case "rank":
			return "rank"
		}
		return "bm25(" + table + ", " + rank + ")"
	}
	return `SELECT rowid AS rid, ` + col("chunk_fts") + ` AS r FROM chunk_fts WHERE chunk_fts MATCH ?
		UNION ALL SELECT rowid, ` + col("sess_fts") + ` FROM sess_fts WHERE sess_fts MATCH ?`
}

func wordArgs(expr string) []any { return []any{expr, expr} }

// sessionWeights sind die Gewichte von bm25 für die vier Spalten.
const sessionWeights = "2.0, 0.5, 1.0, 0.5"

// chunkSnippetIndex macht die Ausschnitte: ein kleiner Volltextindex im
// Speicher mit denselben Spalten und demselben Tokenizer wie chunk_fts, der nur
// die Chunks eines Aufrufs hält.
var chunkSnippetIndex = ftssnippet.New(ftsColumns...)

// chunkWords leitet die vier Felder der Chunks aus raw ab, wie indexChunks sie
// in den Index schreibt.
func (s *Store) chunkWords(ctx context.Context, ids []int64) (map[int64]transcript.IndexFields, error) {
	out := make(map[int64]transcript.IndexFields, len(ids))
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.seq, se.harness, c.raw FROM session_chunks c
			JOIN sessions se ON se.id = c.session_id WHERE c.id IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var seq int
			var harness string
			var v rawcodec.Value
			if err := rows.Scan(&id, &seq, &harness, &v); err != nil {
				return err
			}
			raw, err := s.rawLine(v)
			if err != nil {
				return fmt.Errorf("chunk %d: %w", id, err)
			}
			out[id] = transcript.Fields(transcript.Parse(harness, seq, raw))
		}
		return rows.Err()
	})
	return out, err
}

// chunkSnippets gibt für jeden der Chunks, den expr findet, snippet() der vier
// Spalten (Reihenfolge wie ftsColumns), mit open und close um die Treffer und
// höchstens tokens Wörtern je Ausschnitt. Chunks ohne Treffer fehlen.
func (s *Store) chunkSnippets(ctx context.Context, expr string, ids []int64, open, close string, tokens int) (map[int64][4]string, error) {
	out := map[int64][4]string{}
	if len(ids) == 0 {
		return out, nil
	}
	words, err := s.chunkWords(ctx, ids)
	if err != nil {
		return nil, err
	}
	docs := make(map[int64][]string, len(words))
	for id, f := range words {
		docs[id] = []string{f.Text, f.Thinking, f.ToolInput, f.ToolOutput}
	}
	snips, err := chunkSnippetIndex.Snippets(ctx, docs, expr, open, close, tokens)
	if err != nil {
		return nil, err
	}
	for id, c := range snips {
		out[id] = [4]string{c[0], c[1], c[2], c[3]}
	}
	return out, nil
}

// plainSnippet ist der Ausschnitt der ersten Spalte mit einem Treffer, ohne
// Markierung (open = \x02, close = \x03).
func plainSnippet(cols [4]string) string {
	for _, c := range cols {
		if strings.Contains(c, "\x02") {
			return strings.NewReplacer("\x02", "", "\x03", "").Replace(c)
		}
	}
	return ""
}

// ------------------------------------------------- Wartung (Schwärzen, Schlanken)

// indexRow ist eine Zeile von chunk_index, wie die Wartungsbefehle sie lesen:
// die vier Felder, die sie hält (bei fts = 1 leer), ob sie ein Prompt ist und
// wo ihre Wörter stehen.
type indexRow struct {
	fields indexFields
	prompt bool
	fts    int
}

func loadIndexRow(ctx context.Context, q maintQueryer, id int64) (indexRow, bool, error) {
	var r indexRow
	var prompt int
	err := q.QueryRowContext(ctx, `SELECT text, thinking, tool_input, tool_output, is_prompt, fts FROM chunk_index WHERE chunk_id=?`, id).
		Scan(&r.fields.text, &r.fields.thinking, &r.fields.toolInput, &r.fields.toolOutput, &prompt, &r.fts)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	r.prompt = prompt == 1
	return r, err == nil, err
}

// maintExec führt eine Änderung eines Wartungsbefehls aus (beim Probelauf nicht).
type maintExec func(q maintQueryer, query string, args ...any) (int64, error)

// dropWords trägt die Wörter eines Chunks aus dem Index aus, der sie hält. sess_fts
// hat seinen Inhalt in chunk_index und wird mit genau den gespeicherten Werten
// ausgetragen (#3273); chunk_fts löscht über die Nummer.
func dropWords(exec maintExec, q maintQueryer, id int64, old indexRow) error {
	if old.fts == 1 {
		_, err := exec(q, `DELETE FROM chunk_fts WHERE rowid=?`, id)
		return err
	}
	if old.fields.empty() {
		return nil
	}
	_, err := exec(q, `INSERT INTO sess_fts(sess_fts, rowid, text, thinking, tool_input, tool_output) VALUES('delete', ?, ?, ?, ?, ?)`,
		id, old.fields.text, old.fields.thinking, old.fields.toolInput, old.fields.toolOutput)
	return err
}

// rewriteWords ersetzt die Wörter eines Chunks durch f und bringt die Zeile von
// chunk_index in die Form einer neuen Zeile (fts = 1, die vier Felder leer, der
// Text eines Prompts in prompt). Eine Zeile aus sess_fts wandert dabei nach
// chunk_fts.
func rewriteWords(exec maintExec, q maintQueryer, id int64, old indexRow, f indexFields, prompt bool) error {
	if err := dropWords(exec, q, id, old); err != nil {
		return err
	}
	kept := ""
	if prompt {
		kept = f.text
	}
	if _, err := exec(q, `UPDATE chunk_index SET text='', thinking='', tool_input='', tool_output='', prompt=?, fts=1 WHERE chunk_id=?`, kept, id); err != nil {
		return err
	}
	if f.empty() {
		return nil
	}
	_, err := exec(q, `REPLACE INTO chunk_fts(rowid, text, thinking, tool_input, tool_output) VALUES(?, ?, ?, ?, ?)`,
		id, f.text, f.thinking, f.toolInput, f.toolOutput)
	return err
}

// chunksFTSLive sagt, ob chunks_fts noch gepflegt wird: solange der Auslöser
// chunks_ai besteht, trägt er jede neue Zeile ein, und Wartungsbefehle, die text
// ändern, müssen den Index mitziehen. ctx db slim-search entfernt beides.
func chunksFTSLive(ctx context.Context, q maintQueryer) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='chunks_ai'`).Scan(&n)
	return n > 0, err
}
