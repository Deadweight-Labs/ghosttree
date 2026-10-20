package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

func float32bits(f float32) uint32     { return mathFloat32bits(f) }
func float32frombits(b uint32) float32 { return mathFloat32frombits(b) }

type scan struct {
	data  [][]byte
	scale []float32
	sess  []int32 // dense session number per vector
	nsess int
}

// brute is the vector ranking of the production, for sessions: the cosine of
// every stored vector, the maximum per session, the sessions above the floor in
// order. It is what SearchSessions would do per query, minus the database.
func (s *scan) brute(q []float32, floor float32) []int32 {
	best := make([]float32, s.nsess)
	for i := range best {
		best[i] = -2
	}
	for i, raw := range s.data {
		c := embed.Cosine(q, raw, s.scale[i])
		if c > best[s.sess[i]] {
			best[s.sess[i]] = c
		}
	}
	type ss struct {
		id int32
		s  float32
	}
	all := make([]ss, 0, 256)
	for id, c := range best {
		if c >= floor {
			all = append(all, ss{int32(id), c})
		}
	}
	slices.SortFunc(all, func(a, b ss) int {
		switch {
		case a.s > b.s:
			return -1
		case a.s < b.s:
			return 1
		}
		return int(a.id - b.id)
	})
	out := make([]int32, min(100, len(all)))
	for i := range out {
		out[i] = all[i].id
	}
	return out
}

type latRow struct {
	Vectors  int     `json:"vectors"`
	Sessions int     `json:"sessions"`
	MemP50   float64 `json:"mem_p50_ms"`
	MemP95   float64 `json:"mem_p95_ms"`
	MemMax   float64 `json:"mem_max_ms"`
	DBP50    float64 `json:"db_p50_ms"`
	DBP95    float64 `json:"db_p95_ms"`
	DBMax    float64 `json:"db_max_ms"`
	DBBytes  int64   `json:"db_file_bytes"`
	PerRow   float64 `json:"db_bytes_per_row"`
	Queries  int     `json:"queries_timed_db"`
}

func cmdLatency(args []string) error {
	fs := flag.NewFlagSet("latency", flag.ExitOnError)
	itemsPath := fs.String("items", "", "items")
	vecsPath := fs.String("vecs", "", "vectors")
	qvecsPath := fs.String("qvecs", "", "query vectors")
	dir := fs.String("dir", "", "scratch directory for the SQLite files")
	stock := fs.Int("stock", 0, "vectors in the stock today (items of all sessions)")
	scalesArg := fs.String("scales", "1,2,5", "multiples of the stock")
	dbQueries := fs.Int("db-queries", 100, "queries timed against SQLite")
	out := fs.String("out", "", "json result file")
	fs.Parse(args)

	items, err := readJSONLFile[Item](*itemsPath)
	if err != nil {
		return err
	}
	vecs, err := readVecs(*vecsPath)
	if err != nil {
		return err
	}
	qv, err := readJSONLFile[queryVec](*qvecsPath)
	if err != nil {
		return err
	}
	type base struct {
		raw   []byte
		scale float32
		sid   int64
		kind  string
		seq   int
	}
	var bases []base
	for _, it := range items {
		if v, ok := vecs[it.Idx]; ok {
			raw := make([]byte, dim)
			for i, x := range v.Q {
				raw[i] = byte(x)
			}
			bases = append(bases, base{raw, v.Scale, it.SID, it.Kind, it.Seq})
		}
	}
	if len(bases) == 0 {
		return fmt.Errorf("no vectors")
	}
	logf("%d real vectors, tiled to the stock", len(bases))

	var rows []latRow
	for _, ss := range strings.Split(*scalesArg, ",") {
		mult, err := strconv.Atoi(strings.TrimSpace(ss))
		if err != nil {
			return err
		}
		m := *stock * mult
		sc := &scan{}
		sessNo := map[string]int32{}
		type rec struct {
			sid  int64
			kind string
			seq  int
			raw  []byte
			sc   float32
		}
		recs := make([]rec, m)
		for i := 0; i < m; i++ {
			b := bases[i%len(bases)]
			round := i / len(bases)
			sid := b.sid + int64(round)*1_000_000 // each tiling round counts as new sessions
			key := strconv.FormatInt(sid, 10)
			n, ok := sessNo[key]
			if !ok {
				n = int32(len(sessNo))
				sessNo[key] = n
			}
			sc.data = append(sc.data, b.raw)
			sc.scale = append(sc.scale, b.scale)
			sc.sess = append(sc.sess, n)
			recs[i] = rec{sid, b.kind, b.seq, b.raw, b.scale}
		}
		sc.nsess = len(sessNo)
		row := latRow{Vectors: m, Sessions: sc.nsess}
		// in memory
		var ms []float64
		for _, q := range qv {
			t0 := time.Now()
			_ = sc.brute(q.V, 0.55)
			ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
		}
		p := percentiles(ms, 0.5, 0.95, 1)
		row.MemP50, row.MemP95, row.MemMax = p[0], p[1], p[2]

		// SQLite: the same vectors in a table shaped like a production one.
		path := filepath.Join(*dir, fmt.Sprintf("scratch-%dx.db", mult))
		os.Remove(path)
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(OFF)")
		if err != nil {
			return err
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`CREATE TABLE sessions(id INTEGER PRIMARY KEY, visibility TEXT NOT NULL DEFAULT 'private', account_id INTEGER NOT NULL DEFAULT 0);
			CREATE TABLE session_embeddings(
			  session_id INTEGER NOT NULL, chunk_seq INTEGER NOT NULL, kind TEXT NOT NULL,
			  model TEXT NOT NULL, prefix_version TEXT NOT NULL, dim INTEGER NOT NULL,
			  text_hash TEXT NOT NULL, scale REAL NOT NULL, vec BLOB NOT NULL,
			  read_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			  PRIMARY KEY(session_id, chunk_seq));`); err != nil {
			return err
		}
		tx, _ := db.Begin()
		ins, _ := tx.Prepare(`INSERT OR IGNORE INTO sessions(id) VALUES(?)`)
		insE, _ := tx.Prepare(`INSERT OR REPLACE INTO session_embeddings VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
		for i, r := range recs {
			ins.Exec(r.sid)
			h := sha256.Sum256([]byte(strconv.Itoa(i)))
			insE.Exec(r.sid, r.seq*1000+i%1000, r.kind, embed.DefaultModel, embed.PrefixVersion, dim,
				hex.EncodeToString(h[:]), float64(r.sc), r.raw, "2026-10-20T18:00:00Z", "2026-10-20T18:00:00Z")
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		db.Exec(`VACUUM`)
		if fi, err := os.Stat(path); err == nil {
			row.DBBytes = fi.Size()
			var cnt int
			db.QueryRow(`SELECT count(*) FROM session_embeddings`).Scan(&cnt)
			row.PerRow = float64(fi.Size()) / float64(max(cnt, 1))
		}
		var dms []float64
		for qi, q := range qv {
			if qi >= *dbQueries {
				break
			}
			t0 := time.Now()
			rows, err := db.Query(`SELECT e.session_id, e.scale, e.vec FROM session_embeddings e
				JOIN sessions s ON s.id = e.session_id WHERE e.model = ? AND e.prefix_version = ?`, embed.DefaultModel, embed.PrefixVersion)
			if err != nil {
				return err
			}
			best := map[int64]float32{}
			for rows.Next() {
				var sid int64
				var scale float64
				var raw sql.RawBytes
				if err := rows.Scan(&sid, &scale, &raw); err != nil {
					return err
				}
				if len(raw) != len(q.V) {
					continue
				}
				if c := embed.Cosine(q.V, raw, float32(scale)); c >= 0.55 {
					if cur, ok := best[sid]; !ok || c > cur {
						best[sid] = c
					}
				}
			}
			rows.Close()
			type ss struct {
				id int64
				s  float32
			}
			all := make([]ss, 0, len(best))
			for id, c := range best {
				all = append(all, ss{id, c})
			}
			slices.SortFunc(all, func(a, b ss) int {
				switch {
				case a.s > b.s:
					return -1
				case a.s < b.s:
					return 1
				}
				return int(a.id - b.id)
			})
			dms = append(dms, float64(time.Since(t0).Microseconds())/1000)
		}
		row.Queries = len(dms)
		p = percentiles(dms, 0.5, 0.95, 1)
		row.DBP50, row.DBP95, row.DBMax = p[0], p[1], p[2]
		db.Close()
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
		rows = append(rows, row)
		logf("%dx: %+v", mult, row)
	}
	b, _ := json.MarshalIndent(rows, "", " ")
	if *out != "" {
		return os.WriteFile(*out, b, 0o600)
	}
	fmt.Println(string(b))
	return nil
}

func cmdSpace(args []string) error { return cmdLatency(args) }
