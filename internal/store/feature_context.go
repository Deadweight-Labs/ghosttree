package store

import (
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Der kurze Kontext (REQ-612, Kriterium 4). Ein Agent, der in einem Bereich
// arbeitet, bekommt die Features, deren Bereiche seine Pfade enthalten, in
// Kurzform und mit Budget. Mit Session-Schlüssel kommt jede Fassung eines
// Features höchstens einmal je Session; ohne (ein ausdrücklicher Aufruf) kommt
// alles, und es wird nichts vermerkt.

const (
	// featureDeliveryRetention: so lange merkt sich der Server, was eine Session
	// als Kontext bekam (wie context_deliveries).
	featureDeliveryRetention = 7 * 24 * time.Hour
	// maxFeatureDeliveryRows begrenzt die Zeilen je Konto: wer immer neue
	// Schlüssel schickt, füllt die Tabelle sonst ohne Grenze. Darüber wird nichts
	// mehr vermerkt, die Session bekommt den Kontext dann erneut.
	maxFeatureDeliveryRows = 5000
)

// featureSessionKey bindet den Schlüssel des Clients an das Konto. Der Teil vor
// dem Schrägstrich ist die Konto-Id und kommt nie vom Client: ein anderes Konto
// erreicht damit die Zustellungen dieser Session nicht.
func featureSessionKey(account, key string) string {
	key = CleanSessionKey(key)
	if key == "" || account == "" {
		return ""
	}
	return account + "/" + key
}

// FeatureContext ist die Antwort: die Kurzformen, der fertige Text und wie viele
// Zeilen darin stehen.
type FeatureContext struct {
	Features []feature.Brief `json:"features"`
	Text     string          `json:"text"`
	Shown    int             `json:"shown"`
}

// FeaturesForPaths liefert die Features für Pfade, soweit der Betrachter sie lesen
// darf. Die Treffer stehen nach Nummer; ein Feature, dessen Bereich mehrere Pfade
// enthält, steht einmal.
func (a *ProjectAccess) FeaturesForPaths(project string, paths []string, sessionKey string, budget int) (FeatureContext, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	sessionKey = featureSessionKey(a.principal.ID, sessionKey)
	out := FeatureContext{Features: []feature.Brief{}}
	if project == "" || len(paths) == 0 {
		return out, nil
	}
	// Ohne Leserecht kommt eine leere Antwort und es wird nichts vermerkt (wie bei
	// den Ghost-Beschreibungen): ein Hook darf daran nicht scheitern.
	if !a.Allow(project, ResFeature, ActRead, Object{}) {
		return out, nil
	}
	if len(paths) > 20 {
		paths = paths[:20]
	}
	var clean []string
	for _, p := range paths {
		if c, ok := feature.CleanPath(p); ok {
			clean = append(clean, c)
		}
	}
	list, err := a.st.searchFeatures(feature.Filter{Project: project, Limit: maxChecklistFeatures})
	if err != nil {
		return out, err
	}
	var briefs []feature.Brief
	revOf := map[int64]int{}
	for _, f := range list {
		if !a.CanDeliverFeature(f) {
			continue
		}
		for _, p := range clean {
			if f.Content.Covers(p) {
				briefs = append(briefs, feature.BriefOf(f, p))
				revOf[f.ID] = f.Revision
				break
			}
		}
	}
	sort.Slice(briefs, func(i, j int) bool { return briefs[i].ID < briefs[j].ID })
	if sessionKey != "" && len(briefs) > 0 {
		ids := make([]int64, len(briefs))
		for i, b := range briefs {
			ids[i] = b.ID
		}
		done, err := a.st.deliveredFeatures(sessionKey, ids)
		if err != nil {
			return out, err
		}
		fresh := briefs[:0]
		for _, b := range briefs {
			if !done[b.ID][b.Revision] {
				fresh = append(fresh, b)
			}
		}
		briefs = fresh
	}
	out.Text, out.Shown = feature.RenderBriefs(briefs, budget)
	out.Features = briefs
	if out.Features == nil {
		out.Features = []feature.Brief{}
	}
	if sessionKey != "" && out.Shown > 0 {
		if err := a.st.recordFeatureDeliveries(a.principal.ID, sessionKey, briefs[:out.Shown]); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Store) deliveredFeatures(sessionKey string, ids []int64) (map[int64]map[int]bool, error) {
	if s.reader != nil {
		return s.reader.deliveredFeatures(sessionKey, ids)
	}
	out := map[int64]map[int]bool{}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT feature_id, revision FROM feature_deliveries WHERE session_key=? AND feature_id IN (`+placeholders(len(part))+`)`,
			append([]any{sessionKey}, factIDArgs(part)...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var rev int
			if err := rows.Scan(&id, &rev); err != nil {
				return err
			}
			if out[id] == nil {
				out[id] = map[int]bool{}
			}
			out[id][rev] = true
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) recordFeatureDeliveries(account, sessionKey string, shown []feature.Brief) error {
	if s.writer != nil {
		return queueWrite(s, []any{account, sessionKey, shown}, func(d *Store, p []any) error {
			return d.recordFeatureDeliveries(p[0].(string), p[1].(string), p[2].([]feature.Brief))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Altes räumt dort auf, wo geschrieben wird, wie bei context_deliveries.
	cutoff := time.Now().UTC().Add(-featureDeliveryRetention).Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM feature_deliveries WHERE at < ?`, cutoff); err != nil {
		return err
	}
	// Die Zeilen eines Kontos liegen im Bereich "<Konto>/" bis "<Konto>0" des
	// Primärschlüssels ('0' folgt auf '/').
	var rows int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM feature_deliveries WHERE session_key >= ? AND session_key < ?`,
		account+"/", account+"0").Scan(&rows); err != nil {
		return err
	}
	if rows >= maxFeatureDeliveryRows {
		return tx.Commit() // das Aufräumen bleibt, vermerkt wird nichts
	}
	ts := now()
	for _, b := range shown {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feature_deliveries(session_key,feature_id,revision,at) VALUES(?,?,?,?)`, sessionKey, b.ID, b.Revision, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}
