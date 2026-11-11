package store

import "strings"

// Mehrere Wörter: zuerst UND, ODER nur als Rückfall (IDEA-24).
//
// ftsQuery verbindet die Wörter einer Anfrage mit ODER, damit eine Anfrage nicht
// leer bleibt, weil ihre Wörter in zwei Einträgen stehen. Der Preis: Jedes weitere
// Wort vergrößert die Treffermenge, und die Zeit einer Suche mit Rang ist linear
// in den Treffern. „go test“ traf lokal 53.808 Chunks (81 ms), mit UND 13.192.
// Außerdem ist UND meist das, was gemeint ist.
//
// Deshalb läuft eine Suche zuerst mit UND. Liefert sie genug Zeilen, bleibt es
// dabei. Liefert sie zu wenige, kommt die Suche mit ODER dazu und füllt auf: Die
// Treffer mit allen Wörtern stehen vorn, danach die übrigen in der Reihenfolge,
// die der Rang gibt, ohne eine Zeile doppelt.

// sessionAndEnough ist, wie viele Chunk-Treffer die UND-Suche über Sessions
// liefern muss, damit ODER entfällt. Eine Seite zeigt höchstens so viele
// Sessions; die Suche mit ODER über den ganzen Bestand ist der teure Teil.
const sessionAndEnough = 30

// ftsExprs sind die beiden Ausdrücke einer Anfrage: and verlangt jedes Wort, or
// jedes beliebige. and ist leer, wenn die Anfrage höchstens ein Wort hat; dann
// gibt es nichts zu verschärfen und or ist die ganze Suche.
func ftsExprs(q string) (and, or string) {
	terms := searchTerms(q)
	if len(terms) < 2 {
		return "", ftsQuery(q)
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " AND "), strings.Join(quoted, " OR ")
}

// andThenOr führt run zuerst mit dem UND-Ausdruck von q aus. Kommen weniger als
// enough Zeilen zurück, führt es run mit dem ODER-Ausdruck aus und hängt an, was
// noch nicht dabei ist (key sagt, wann zwei Zeilen dieselbe sind). Mehr als limit
// Zeilen gibt es nie. Mit höchstens einem Wort läuft run einmal, mit ODER, wie
// vorher.
//
// run bekommt einen FTS5-Ausdruck und liefert die Zeilen in der Reihenfolge des
// Rangs; ein Fehler bricht ab, auch der des Rückfalls: eine halbe Antwort wäre
// nicht von einer vollständigen zu unterscheiden.
func andThenOr[T any, K comparable](q string, enough, limit int, key func(T) K, run func(expr string) ([]T, error)) ([]T, error) {
	and, or := ftsExprs(q)
	if and == "" {
		return run(or)
	}
	hits, err := run(and)
	if err != nil || len(hits) >= enough {
		return hits, err
	}
	more, err := run(or)
	if err != nil {
		return nil, err
	}
	seen := make(map[K]bool, len(hits))
	for _, h := range hits {
		seen[key(h)] = true
	}
	for _, h := range more {
		if len(hits) >= limit {
			break
		}
		if k := key(h); !seen[k] {
			seen[k] = true
			hits = append(hits, h)
		}
	}
	return hits, nil
}

func idKey(id int64) int64 { return id }
