package store

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// The kinds of object that get vectors. Sessions are meant to be one more later:
// a kind is one entry in embedKinds (where its objects live, which of them count,
// how their text is made) plus a ranking in the search of its own; the storage,
// the background job and the status need nothing else.
const (
	KindKnowledge = "knowledge"
	KindRequest   = "request"
	KindPain      = "pain"
	KindDocument  = "document"
	KindGhost     = "ghost"
)

// EmbedKinds lists every kind that can be embedded, in the order the background
// job serves them: the small kinds first, so that the ghost descriptions (one
// per path, thousands) are never the reason a new pain waits.
func EmbedKinds() []string {
	return []string{KindKnowledge, KindRequest, KindPain, KindDocument, KindGhost}
}

// ParseEmbedKinds reads a comma-separated list of kinds, the way
// GHOSTTREE_EMBED_KINDS and GHOSTTREE_EMBED_SEARCH_KINDS give it. "all" means
// every kind; the plural forms and a few synonyms (files, docs) are accepted. The
// result is in job order without duplicates.
func ParseEmbedKinds(list string) ([]string, error) {
	want := map[string]bool{}
	for raw := range strings.SplitSeq(list, ",") {
		switch name := strings.ToLower(strings.TrimSpace(raw)); name {
		case "":
			continue
		case "all":
			for _, k := range EmbedKinds() {
				want[k] = true
			}
		default:
			kind, err := ParseEmbedKind(name)
			if err != nil {
				return nil, fmt.Errorf("%w, or all", err)
			}
			want[kind] = true
		}
	}
	var out []string
	for _, k := range EmbedKinds() {
		if want[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// ParseEmbedKind reads the name of one kind, in the singular or plural, with a
// few synonyms (files, docs).
func ParseEmbedKind(name string) (string, error) {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "requests":
		return KindRequest, nil
	case "pains":
		return KindPain, nil
	case "documents", "docs", "doc":
		return KindDocument, nil
	case "ghosts", "files", "file":
		return KindGhost, nil
	default:
		if slices.Contains(EmbedKinds(), n) {
			return n, nil
		}
	}
	return "", fmt.Errorf("unknown kind %q; the kinds are %s", name, strings.Join(EmbedKinds(), ", "))
}

// embedKind describes where the objects of a kind live and where their vectors
// are kept.
type embedKind struct {
	name string
	// emb and fail are the tables of vectors and of failures. A kind with tables of
	// its own (knowledge, which was first) keeps them, with its own id column; every
	// other kind shares embeddings and embedding_failures, told apart by kind.
	emb, fail string
	idCol     string
	shared    bool

	// from is the table of the objects with its alias, id the id on that alias.
	from, id string
	// active is a condition on the alias that says which objects get a vector:
	// the ones that can be a hit. Everything else has its vector ignored by the
	// search and removed by the job.
	active string
	// updated is the column that moves when an object is edited.
	updated string
	// alsoChanged is a further condition (it may use e.read_at) under which the text
	// may have changed although updated is old: parts of the text that live in
	// another table.
	alsoChanged string
	// texts returns the finished document text of the given objects.
	texts func(s *Store, ids []int64) (map[int64]string, error)
}

// vecJoin joins the vector table e to the objects.
func (k embedKind) vecJoin() string { return k.join("e", k.emb) }

// failJoin joins the failure table f to the objects.
func (k embedKind) failJoin() string { return k.join("f", k.fail) }

func (k embedKind) join(alias, table string) string {
	on := alias + "." + k.idCol + " = " + k.id
	if k.shared {
		on += " AND " + alias + ".kind = '" + k.name + "'"
	}
	return table + " " + alias + " ON " + on
}

// keyWhere restricts a query on the vector or failure table to this kind.
func (k embedKind) keyWhere(alias string) string {
	if k.shared {
		return alias + ".kind = '" + k.name + "' AND "
	}
	return ""
}

// idCols are the key columns of the vector and failure tables, idVals their
// placeholders, idArgs their values for one object.
func (k embedKind) idCols() string {
	if k.shared {
		return "kind, " + k.idCol
	}
	return k.idCol
}

func (k embedKind) idVals() string {
	if k.shared {
		return "?, ?"
	}
	return "?"
}

func (k embedKind) idArgs(id int64) []any {
	if k.shared {
		return []any{k.name, id}
	}
	return []any{id}
}

// knowledgeKind is the kind of the first version of hybrid search.
func knowledgeKind() embedKind {
	k, _ := embedKindOf(KindKnowledge)
	return k
}

func embedKindOf(name string) (embedKind, error) {
	for _, k := range embedKinds {
		if k.name == name {
			return k, nil
		}
	}
	return embedKind{}, fmt.Errorf("unknown embedding kind %q", name)
}

var embedKinds = []embedKind{
	{
		name: KindKnowledge, emb: "knowledge_embeddings", fail: "knowledge_embedding_failures", idCol: "knowledge_id",
		from: "knowledge k", id: "k.id", active: "k.status = 'active'", updated: "k.updated_at",
		texts: func(s *Store, ids []int64) (map[int64]string, error) {
			return s.textsOf(`SELECT id, title, body FROM knowledge WHERE id IN (`, ids, embed.DocumentText)
		},
	},
	{
		name: KindRequest, emb: "embeddings", fail: "embedding_failures", idCol: "object_id", shared: true,
		from: "requests r", id: "r.id", active: "1", updated: "r.updated_at",
		// A criterion is added without the request being touched.
		alsoChanged: "EXISTS (SELECT 1 FROM request_criteria c WHERE c.request_id = r.id AND c.updated_at >= e.read_at)",
		texts:       requestTexts,
	},
	{
		name: KindPain, emb: "embeddings", fail: "embedding_failures", idCol: "object_id", shared: true,
		from: "pains p", id: "p.id", active: "1", updated: "p.updated_at",
		texts: func(s *Store, ids []int64) (map[int64]string, error) {
			return s.textsOf(`SELECT id, title, body FROM pains WHERE id IN (`, ids, embed.DocumentText)
		},
	},
	{
		name: KindDocument, emb: "embeddings", fail: "embedding_failures", idCol: "object_id", shared: true,
		from: "documents d", id: "d.id", active: "d.status = 'active'", updated: "d.updated_at",
		texts: documentTexts,
	},
	{
		name: KindGhost, emb: "embeddings", fail: "embedding_failures", idCol: "object_id", shared: true,
		from: "ghost_files g", id: "g.id", active: "1", updated: "g.updated_at",
		texts: ghostTexts,
	},
}

// textsOf runs a query of the shape SELECT id, a, b FROM t WHERE id IN (... and
// makes the document text of every row with build.
func (s *Store) textsOf(prefixSQL string, ids []int64, build func(a, b string) string) (map[int64]string, error) {
	out := map[int64]string{}
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(prefixSQL+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var a, b string
			if err := rows.Scan(&id, &a, &b); err != nil {
				return err
			}
			out[id] = build(a, b)
		}
		return rows.Err()
	})
	return out, err
}

// embedTextChunk is how many objects a text query is asked for at a time.
const embedTextChunk = 400

func requestTexts(s *Store, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	type head struct{ title, description string }
	heads := map[int64]head{}
	if err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT id, title, description FROM requests WHERE id IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var h head
			if err := rows.Scan(&id, &h.title, &h.description); err != nil {
				return err
			}
			heads[id] = h
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	criteria := map[int64][]string{}
	if err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT request_id, description FROM request_criteria WHERE request_id IN (`+placeholders(len(part))+`) ORDER BY request_id, number`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var d string
			if err := rows.Scan(&id, &d); err != nil {
				return err
			}
			criteria[id] = append(criteria[id], d)
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	for id, h := range heads {
		out[id] = embed.RequestText(h.title, h.description, criteria[id])
	}
	return out, nil
}

func ghostTexts(s *Store, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT id, path, kind, description FROM ghost_files WHERE id IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var path, kind, description string
			if err := rows.Scan(&id, &path, &kind, &description); err != nil {
				return err
			}
			out[id] = embed.GhostText(path, kind, description)
		}
		return rows.Err()
	})
	return out, err
}

// documentTexts reads only the beginning of the head revision: the whole text of
// a long document is neither embedded nor worth loading for every look.
func documentTexts(s *Store, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT d.id, d.title, COALESCE(substr(r.body, 1, `+fmt.Sprint(embed.MaxDocumentHeadRunes*2)+`), '')
			FROM documents d LEFT JOIN document_revisions r ON r.document_id = d.id AND r.revision = d.head_revision
			WHERE d.id IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var title, body string
			if err := rows.Scan(&id, &title, &body); err != nil {
				return err
			}
			out[id] = embed.DocumentHeadText(title, body)
		}
		return rows.Err()
	})
	return out, err
}

// eachChunk calls fn with the ids as query arguments, embedTextChunk at a time.
func (s *Store) eachChunk(ids []int64, fn func(part []any) error) error {
	for start := 0; start < len(ids); start += embedTextChunk {
		part := ids[start:min(start+embedTextChunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		if err := fn(args); err != nil {
			return err
		}
	}
	return nil
}
