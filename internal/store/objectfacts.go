package store

import (
	"fmt"
	"strings"
)

// ObjectFacts sind die Eigenschaften eines Objekts, nach denen die Suchsprache
// filtert und die an keinem Treffer selbst stehen (REQ-600): Labels, die
// Git-Anker aller seiner Schreibvorgänge, Arbeitspakete, Beziehungen und die
// Bereiche seines Entstehungskontexts. Eine Abfrage für eine ganze Trefferliste
// statt je eine pro Treffer.
type ObjectFacts struct {
	Labels   []string
	Branches []string
	Commits  []string
	PRs      []int64
	Packages []int64
	Areas    []string
	Parents  []int64
	Blocks   []int64
	// Features sind die Features, auf die sich das Objekt bezieht (REQ-612).
	Features []int64
	// OriginAt ist der Zeitpunkt des Entstehungskontexts, leer ohne.
	OriginAt string
	// RequestID ist bei einem Kriterium und einer Aufgabe der Request dahinter.
	RequestID int64
}

func (f *ObjectFacts) addBranch(b string) {
	if b = strings.TrimSpace(b); b != "" && !hasString(f.Branches, b) {
		f.Branches = append(f.Branches, b)
	}
}

func (f *ObjectFacts) addCommit(c string) {
	if c != "" && !hasString(f.Commits, c) {
		f.Commits = append(f.Commits, c)
	}
}

func (f *ObjectFacts) addPR(n int64) {
	if n <= 0 {
		return
	}
	for _, v := range f.PRs {
		if v == n {
			return
		}
	}
	f.PRs = append(f.PRs, n)
}

func hasString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func factIDArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// chunked ruft fn mit Teilen von höchstens 500 Ids.
func chunked(ids []int64, fn func(part []int64) error) error {
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		if err := fn(part); err != nil {
			return err
		}
	}
	return nil
}

// ObjectFacts liefert die Eigenschaften der Objekte einer Art (request, pain,
// idea, knowledge, criterion, task, message), nach Objekt-Id. Ohne Zugriffsprüfung: der
// Aufrufer ruft es nur für Treffer, die der Betrachter schon lesen darf.
func (s *Store) ObjectFacts(kind string, ids []int64) (map[int64]ObjectFacts, error) {
	if s.reader != nil {
		return s.reader.ObjectFacts(kind, ids)
	}
	out := map[int64]ObjectFacts{}
	if len(ids) == 0 {
		return out, nil
	}
	switch kind {
	case "request":
		return s.withFeatureLinks(kind, ids)(s.requestFacts(ids))
	case "criterion", "task":
		return s.derivedFacts(kind, ids)
	case "pain", "knowledge", "message":
		return s.plainFacts(kind, ids)
	case "idea":
		return s.withFeatureLinks(kind, ids)(s.plainFacts(kind, ids))
	case "feature":
		return s.featureFacts(ids)
	}
	return nil, fmt.Errorf("no facts for %q", kind)
}

// withFeatureLinks ergänzt die Features, auf die sich Ideen und Issues beziehen
// (REQ-612): feature:N trifft sie.
func (s *Store) withFeatureLinks(kind string, ids []int64) func(map[int64]ObjectFacts, error) (map[int64]ObjectFacts, error) {
	return func(out map[int64]ObjectFacts, err error) (map[int64]ObjectFacts, error) {
		if err != nil {
			return out, err
		}
		err = chunked(ids, func(part []int64) error {
			rows, err := s.db.Query(`SELECT ref_id, feature_id FROM feature_links WHERE kind=? AND ref_id IN (`+placeholders(len(part))+`) ORDER BY feature_id`,
				append([]any{kind}, factIDArgs(part)...)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ref, fid int64
				if err := rows.Scan(&ref, &fid); err != nil {
					return err
				}
				f := out[ref]
				dup := false
				for _, x := range f.Features {
					dup = dup || x == fid
				}
				if !dup {
					f.Features = append(f.Features, fid)
				}
				out[ref] = f
			}
			return rows.Err()
		})
		return out, err
	}
}

// featureFacts: Bereiche der jüngsten Fassung, Anker und PR-Nummern aller Fassungen.
func (s *Store) featureFacts(ids []int64) (map[int64]ObjectFacts, error) {
	out := map[int64]ObjectFacts{}
	for _, id := range ids {
		out[id] = ObjectFacts{}
	}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT v.feature_id, v.revision, v.areas, v.pr, v.created_at, a.commit_sha, a.branch, a.pr
			FROM feature_versions v LEFT JOIN git_anchors a ON a.id=v.anchor_id
			WHERE v.feature_id IN (`+placeholders(len(part))+`) ORDER BY v.revision`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var fid int64
			var rev int
			var areas, created string
			var pr int64
			var commit, branch *string
			var apr *int64
			if err := rows.Scan(&fid, &rev, &areas, &pr, &created, &commit, &branch, &apr); err != nil {
				return err
			}
			f := out[fid]
			// Die Bereiche der jüngsten Fassung (die Zeilen kommen nach Fassung
			// aufsteigend, die letzte gewinnt).
			f.Areas = decodeAreas(areas)
			if f.OriginAt == "" {
				f.OriginAt = created
			}
			if commit != nil {
				f.addCommit(*commit)
			}
			if branch != nil {
				f.addBranch(*branch)
			}
			f.addPR(pr)
			if apr != nil {
				f.addPR(*apr)
			}
			out[fid] = f
		}
		return rows.Err()
	})
	return out, err
}

// applyOrigins füllt Bereiche und Zeitpunkt aus den Kontexten der Zeilen einer
// Tabelle; rowOwner bildet Zeilen-Id auf Objekt-Id ab.
func (s *Store) applyOrigins(table string, rowIDs []int64, rowOwner map[int64]int64, out map[int64]ObjectFacts) error {
	origins, err := s.originsFor(table, rowIDs)
	if err != nil {
		return err
	}
	for row, c := range origins {
		owner := rowOwner[row]
		f := out[owner]
		for _, a := range c.Areas {
			if !hasString(f.Areas, a.Path) {
				f.Areas = append(f.Areas, a.Path)
			}
		}
		if f.OriginAt == "" || (c.CapturedAt != "" && c.CapturedAt < f.OriginAt) {
			f.OriginAt = c.CapturedAt
		}
		out[owner] = f
	}
	return nil
}

func (s *Store) applyLabels(kind string, ids []int64, out map[int64]ObjectFacts) error {
	labels, err := s.LabelsFor(kind, ids)
	if err != nil {
		return err
	}
	for id, ls := range labels {
		f := out[id]
		for _, l := range ls {
			f.Labels = append(f.Labels, l.Name)
		}
		out[id] = f
	}
	return nil
}

// plainFacts: Pains, Ideen und Wissen. Anker und Kontext aller Schreibvorgänge
// des Objekts (bei Pains die Ereignisse, sonst die Zeile selbst).
func (s *Store) plainFacts(kind string, ids []int64) (map[int64]ObjectFacts, error) {
	out := map[int64]ObjectFacts{}
	for _, id := range ids {
		out[id] = ObjectFacts{}
	}
	table, ownerCol := kind, "id"
	switch kind {
	case "pain":
		table, ownerCol = "pain_events", "pain_id"
	case "idea":
		table = "ideas"
	case "message":
		// Raum-Nachrichten tragen Anker und Kontext, aber keine Labels.
		table = "coord_messages"
	}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT t.`+ownerCol+`, t.id, a.commit_sha, a.branch, a.pr FROM `+table+` t
			JOIN git_anchors a ON a.id=t.anchor_id WHERE t.`+ownerCol+` IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var owner, row int64
			var commit, branch string
			var pr *int64
			if err := rows.Scan(&owner, &row, &commit, &branch, &pr); err != nil {
				return err
			}
			f := out[owner]
			f.addCommit(commit)
			f.addBranch(branch)
			if pr != nil {
				f.addPR(*pr)
			}
			out[owner] = f
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		// Kontexte der Zeilen dieser Objekte.
		rows2, err := s.db.Query(`SELECT t.id, t.`+ownerCol+` FROM `+table+` t WHERE t.origin_id IS NOT NULL AND t.`+ownerCol+` IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		var rowIDs []int64
		owner := map[int64]int64{}
		for rows2.Next() {
			var row, o int64
			if err := rows2.Scan(&row, &o); err != nil {
				rows2.Close()
				return err
			}
			rowIDs = append(rowIDs, row)
			owner[row] = o
		}
		err = rows2.Err()
		rows2.Close()
		if err != nil {
			return err
		}
		return s.applyOrigins(table, rowIDs, owner, out)
	})
	if err != nil {
		return nil, err
	}
	if kind == "message" {
		return out, nil
	}
	if err := s.applyLabels(kind, ids, out); err != nil {
		return nil, err
	}
	return out, nil
}

// requestFacts: Anker aus Anlegen, Ereignissen und Nachweisen, Arbeitspakete,
// die Branch-Relationen, Eltern und Blockaden, Labels und der Kontext des
// Anlegens.
func (s *Store) requestFacts(ids []int64) (map[int64]ObjectFacts, error) {
	out := map[int64]ObjectFacts{}
	for _, id := range ids {
		out[id] = ObjectFacts{}
	}
	err := chunked(ids, func(part []int64) error {
		in := placeholders(len(part))
		args := factIDArgs(part)
		// Der Branch, auf dem der Request angelegt wurde.
		rows, err := s.db.Query(`SELECT id, branch FROM requests WHERE id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var branch string
			if err := rows.Scan(&id, &branch); err != nil {
				rows.Close()
				return err
			}
			f := out[id]
			f.addBranch(branch)
			out[id] = f
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, table := range []string{"request_activity", "request_evidence"} {
			rows, err := s.db.Query(`SELECT t.request_id, a.commit_sha, a.branch, a.pr FROM `+table+` t JOIN git_anchors a ON a.id=t.anchor_id
				WHERE t.request_id IN (`+in+`)`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id int64
				var commit, branch string
				var pr *int64
				if err := rows.Scan(&id, &commit, &branch, &pr); err != nil {
					rows.Close()
					return err
				}
				f := out[id]
				f.addCommit(commit)
				f.addBranch(branch)
				if pr != nil {
					f.addPR(*pr)
				}
				out[id] = f
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		rows, err = s.db.Query(`SELECT m.request_id, p.id, p.branch, COALESCE(p.pr,0) FROM work_package_requests m JOIN work_packages p ON p.id=m.package_id
			WHERE m.request_id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, pkg, pr int64
			var branch string
			if err := rows.Scan(&id, &pkg, &branch, &pr); err != nil {
				rows.Close()
				return err
			}
			f := out[id]
			f.Packages = append(f.Packages, pkg)
			f.addBranch(branch)
			f.addPR(pr)
			out[id] = f
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = s.db.Query(`SELECT request_id, kind, COALESCE(other_request_id,0), external_ref FROM request_relations
			WHERE request_id IN (`+in+`) AND kind IN ('parent','blocks','external')`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, other int64
			var kind, ext string
			if err := rows.Scan(&id, &kind, &other, &ext); err != nil {
				rows.Close()
				return err
			}
			f := out[id]
			switch kind {
			case "parent":
				f.Parents = append(f.Parents, other)
			case "blocks":
				f.Blocks = append(f.Blocks, other)
			case "external":
				// branch:<projekt>@<branch>, die alte Form der Branch-Zuordnung.
				if rest, ok := strings.CutPrefix(ext, "branch:"); ok {
					if i := strings.LastIndex(rest, "@"); i >= 0 {
						f.addBranch(rest[i+1:])
					}
				}
			}
			out[id] = f
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = s.db.Query(`SELECT id, request_id FROM request_activity WHERE kind='request.created' AND origin_id IS NOT NULL AND request_id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		var actIDs []int64
		owner := map[int64]int64{}
		for rows.Next() {
			var act, req int64
			if err := rows.Scan(&act, &req); err != nil {
				rows.Close()
				return err
			}
			actIDs = append(actIDs, act)
			owner[act] = req
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		return s.applyOrigins("request_activity", actIDs, owner, out)
	})
	if err != nil {
		return nil, err
	}
	if err := s.applyLabels("request", ids, out); err != nil {
		return nil, err
	}
	return out, nil
}

// derivedFacts: Kriterien und Aufgaben erben, was am Request hängt (Branch,
// Paket, Bereiche; keine Beziehungen und keine Labels); beim Kriterium kommen die Anker
// seiner Nachweise dazu, damit "erfüllt in PR 12" gilt. Eine Aufgabe erbt auch
// PR und Commit des Requests.
func (s *Store) derivedFacts(kind string, ids []int64) (map[int64]ObjectFacts, error) {
	out := map[int64]ObjectFacts{}
	owner := map[int64]int64{}
	var reqIDs []int64
	table := "request_criteria"
	if kind == "task" {
		table = "human_tasks"
	}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT id, request_id FROM `+table+` WHERE id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, req int64
			if err := rows.Scan(&id, &req); err != nil {
				return err
			}
			owner[id] = req
			reqIDs = append(reqIDs, req)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	base, err := s.requestFacts(dedupe(reqIDs))
	if err != nil {
		return nil, err
	}
	for id, req := range owner {
		f := base[req]
		f.RequestID = req
		// Beziehungen und Labels gehören dem Request: ein Kriterium oder eine
		// Aufgabe erschiene sonst unter jedem label: und parent: ihres Requests.
		f.Parents, f.Blocks, f.Labels = nil, nil, nil
		if kind == "criterion" {
			// Der PR und der Commit eines Kriteriums sind die seiner Nachweise,
			// nicht die des ganzen Requests.
			f.PRs, f.Commits = nil, nil
		}
		out[id] = f
	}
	if kind == "criterion" {
		err := chunked(ids, func(part []int64) error {
			rows, err := s.db.Query(`SELECT e.criterion_id, a.commit_sha, a.branch, a.pr FROM request_evidence e JOIN git_anchors a ON a.id=e.anchor_id
				WHERE e.criterion_id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var commit, branch string
				var pr *int64
				if err := rows.Scan(&id, &commit, &branch, &pr); err != nil {
					return err
				}
				f := out[id]
				f.addCommit(commit)
				f.addBranch(branch)
				if pr != nil {
					f.addPR(*pr)
				}
				out[id] = f
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func dedupe(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
