package store

import (
	"sort"
)

// ContextUseLine zählt zugestellte Teile einer Art (und eines Wegs oder Profils)
// nach ihrem Urteil. Es sind Summen: keine Nummern, Pfade oder Sitzungen.
type ContextUseLine struct {
	Via, Kind, Profile string
	Parts              int // alle zugestellten Teile im Fenster
	Judged             int // davon beurteilt (used 0, 1 oder 2)
	PickedUp           int // aufgegriffen, aber nicht gefolgt (used 1)
	Followed           int // gefolgt (used 2)
	// Pending sind beurteilbare Teile, die noch auf ihre Auswertung warten.
	Pending int
	// Chars sind die Zeichen aller Teile, JudgedChars die der beurteilten,
	// UsedChars die der aufgegriffenen oder befolgten: die Budgetwirkung.
	Chars, JudgedChars, UsedChars int
}

// ContextUseReport ist die Nutzung des zugestellten Kontexts seit einem Tag.
type ContextUseReport struct {
	Since string
	// Lines sind die Teile je Weg und Art, Profiles die beurteilbaren Arten je
	// Profil der Sitzung.
	Lines    []ContextUseLine
	Profiles []ContextUseLine
}

// useColumns zählt in einem Zug: alle Teile, beurteilte, aufgegriffene,
// befolgte, wartende (beurteilbar, offen, vom Agenten selbst und nicht
// zurückgehalten) und die Zeichen dazu.
const useColumns = `COUNT(*),
	SUM(used >= 0), SUM(used = 1), SUM(used = 2),
	SUM(used = -1 AND judged_at = '' AND subagent = 0 AND held_out = 0 AND kind IN ('knowledge','runbook')),
	COALESCE(SUM(chars), 0), COALESCE(SUM(CASE WHEN used >= 0 THEN chars END), 0), COALESCE(SUM(CASE WHEN used >= 1 THEN chars END), 0)`

func scanUseLine(sc interface{ Scan(...any) error }, l *ContextUseLine, lead ...any) error {
	var judged, picked, followed, pending *int
	dest := append(lead, &l.Parts, &judged, &picked, &followed, &pending, &l.Chars, &l.JudgedChars, &l.UsedChars)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	for _, p := range []struct {
		from *int
		to   *int
	}{{judged, &l.Judged}, {picked, &l.PickedUp}, {followed, &l.Followed}, {pending, &l.Pending}} {
		if p.from != nil {
			*p.to = *p.from
		}
	}
	return nil
}

// ContextUseReport liest die Nutzung seit einem Tag über diese Projekte. Wer sie
// sehen darf, entscheidet der Aufrufer (ProjectAccess.ContextUseReport). Es
// kommen nur Summen heraus, kein Text, keine Nummer, kein Pfad.
func (s *Store) ContextUseReport(projects []string, since string) (ContextUseReport, error) {
	if s.reader != nil {
		return s.reader.ContextUseReport(projects, since)
	}
	rep := ContextUseReport{Since: since}
	if len(projects) == 0 {
		return rep, nil
	}
	in, args := inList(projects)
	q := append(append([]any{}, args...), since)
	rows, err := s.db.Query(`SELECT via, kind, `+useColumns+`
		FROM context_delivery_log WHERE project IN (`+in+`) AND delivered_at >= ? AND kind != 'frame' GROUP BY via, kind`, q...)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var l ContextUseLine
		if err := scanUseLine(rows, &l, &l.Via, &l.Kind); err != nil {
			rows.Close()
			return rep, err
		}
		rep.Lines = append(rep.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	prows, err := s.db.Query(`SELECT profile, kind, `+useColumns+`
		FROM context_delivery_log WHERE project IN (`+in+`) AND delivered_at >= ? AND kind IN ('knowledge','runbook') AND subagent = 0 GROUP BY profile, kind`, q...)
	if err != nil {
		return rep, err
	}
	defer prows.Close()
	for prows.Next() {
		var l ContextUseLine
		if err := scanUseLine(prows, &l, &l.Profile, &l.Kind); err != nil {
			return rep, err
		}
		rep.Profiles = append(rep.Profiles, l)
	}
	if err := prows.Err(); err != nil {
		return rep, err
	}
	sort.Slice(rep.Lines, func(i, j int) bool {
		a, b := rep.Lines[i], rep.Lines[j]
		if a.Chars != b.Chars {
			return a.Chars > b.Chars
		}
		return a.Via+a.Kind < b.Via+b.Kind
	})
	sort.Slice(rep.Profiles, func(i, j int) bool {
		a, b := rep.Profiles[i], rep.Profiles[j]
		if a.Profile != b.Profile {
			return a.Profile < b.Profile
		}
		return a.Kind < b.Kind
	})
	return rep, nil
}

// ContextUseReport liest über die Projekte, die dieser Betrachter vollständig
// sehen darf: dieselbe Auswahl wie DeliveryReport (statsProjects).
func (a *ProjectAccess) ContextUseReport(project, since string) (ContextUseReport, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return ContextUseReport{}, err
	}
	return a.st.ContextUseReport(projects, since)
}
