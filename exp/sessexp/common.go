package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

// Item is one text that is embedded: a user prompt or the final answer of a turn.
type Item struct {
	Idx   int    `json:"i"`
	SID   int64  `json:"sid"`
	Chunk int64  `json:"chunk"`
	Seq   int    `json:"seq"`
	Kind  string `json:"kind"` // prompt | final
	Turn  int    `json:"turn"`
	// Tools: the turn ended on a tool call after this answer text, so it is the
	// last text of the turn but not a closing message.
	Tools bool `json:"tools,omitempty"`
	// Full is the length in runes before the cut to the document budget.
	Full int `json:"full"`
	// Doc is the document text exactly as the embedding server gets it.
	Doc string `json:"doc"`
}

// Query is one search question and the sessions that count as a hit.
type Query struct {
	ID      string  `json:"id"`
	Set     string  `json:"set"` // title | own
	Text    string  `json:"text"`
	Targets []int64 `json:"targets"`
	// Self: for title queries, the target session is the one that wrote the
	// knowledge entry (the entry's session_ref is the session's external id).
	Self bool `json:"self,omitempty"`
}

type SessionMeta struct {
	SID     int64  `json:"sid"`
	Harness string `json:"harness"`
	Title   string `json:"title"`
	Msgs    int    `json:"msgs"`
	Started string `json:"started"`
	Prompts int    `json:"prompts"`
	Finals  int    `json:"finals"`
}

func openRO(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: abs}).String() + "?mode=ro&_pragma=busy_timeout(10000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// readJSONL decodes every line of r into a new T.
func readJSONL[T any](r io.Reader) ([]T, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var out []T
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func readJSONLFile[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readJSONL[T](f)
}

func writeJSONL[T any](w io.Writer, vs []T) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, v := range vs {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

// shuffled returns the ids in a seeded random order.
func shuffled[T any](in []T, seed uint64) []T {
	out := append([]T(nil), in...)
	r := newRand(seed)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func percentiles(xs []float64, ps ...float64) []float64 {
	if len(xs) == 0 {
		return make([]float64, len(ps))
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	out := make([]float64, len(ps))
	for i, p := range ps {
		k := int(p * float64(len(s)-1))
		out[i] = s[k]
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
