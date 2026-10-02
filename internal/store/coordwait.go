package store

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Gegenseitiges Warten (REQ-360, Spec §A8). Aus den offenen Fragen, Freigaben
// und Blockern, die Presence schon auswertet (loadWaitRows), entsteht ein
// Graph: A -> B heißt "A wartet auf B". Ein Kreis darin (A <-> B, A -> B -> C
// -> A) wird gemeldet, nie aufgelöst: kein Abbruch, kein Schließen.
//
// Die Meldung geht an drei Orte: in die Presence der Beteiligten (Cycle), in
// die Anzeige von coord_peers und Web, und einmal je Kreis als Nachricht in
// den Raum (reconcileWaitCyclesTx). Ein Handoff ist keine Wartekante: wer
// übergibt, wartet nicht.

const (
	// WaitReviewAfter ist der abgeleitete Review-Zeitpunkt einer Wartekante
	// ohne eigenes Ablaufdatum: Startwert, nicht gemessen.
	WaitReviewAfter = 30 * time.Minute

	// IntentAttention markiert die Systemmeldung über einen Wartekreis. Sie ist
	// bewusst KEIN Intent mit Attention-Eintrag (question, approval, blocker,
	// handoff): die Meldung darf selbst keine Wartekante erzeugen.
	IntentAttention = "attention"

	// WaitCycleSender ist der Absender der Systemmeldung.
	WaitCycleSender = "system:wait-cycle"

	// waitCycleMask steht als rohe Erwähnung an der Meldung. Ein Gast sieht
	// nur rohe Erwähnungen; ohne diese Marke würde er an den erwähnten Agenten
	// ablesen, wer im Kreis wartet.
	waitCycleMask = "*"

	maxOrderedCycle = 10
)

// WaitEdge: Waiter wartet auf Awaited.
type WaitEdge struct {
	Waiter, Awaited, Reason string
	Since                   string
	// ReviewAt ist das Ablaufdatum des Eintrags, sonst Since + WaitReviewAfter
	// (ReviewDerived). Danach ist die Wartekante überfällig, nicht geschlossen.
	ReviewAt      string
	ReviewDerived bool
	Overdue       bool
}

// WaitCycle ist ein Kreis gegenseitigen Wartens (eine stark zusammenhängende
// Gruppe von mindestens zwei Agenten).
type WaitCycle struct {
	// Members stehen in Kreisreihenfolge, wenn es einen Kreis durch alle gibt
	// (Ordered), sonst sortiert.
	Members []string `json:"members"`
	Ordered bool     `json:"ordered"`
	// Since: seit wann der Kreis besteht, die jüngste Kante darin.
	Since string `json:"since"`
	// ReviewAt: die früheste Review-Frist einer Kante des Kreises.
	ReviewAt      string `json:"review_at"`
	ReviewDerived bool   `json:"review_derived,omitempty"`
	Overdue       bool   `json:"overdue,omitempty"`
}

// Key ist die Identität des Kreises: die Mitglieder, sortiert.
func (c WaitCycle) Key() string {
	m := append([]string(nil), c.Members...)
	sort.Strings(m)
	return strings.Join(m, "\x1f")
}

// Describe ist die Zeile für Menschen und Agenten; label setzt Anzeigenamen
// (leer oder nil: die Kennung).
func (c WaitCycle) Describe(label func(string) string) string {
	name := func(id string) string {
		if label != nil {
			if l := label(id); l != "" {
				return l
			}
		}
		return id
	}
	names := make([]string, len(c.Members))
	for i, m := range c.Members {
		names[i] = name(m)
	}
	var chain string
	switch {
	case len(names) == 2:
		chain = names[0] + " ↔ " + names[1]
	case c.Ordered:
		chain = strings.Join(names, " → ") + " → " + names[0]
	default:
		chain = strings.Join(names, " ↔ ")
	}
	out := "gegenseitiges Warten: " + chain + " (seit " + c.Since + ")"
	if c.Overdue {
		out += ", Review seit " + c.ReviewAt + " überfällig"
	} else if c.ReviewAt != "" {
		out += ", Review bis " + c.ReviewAt
	}
	return out
}

// DetectWaitCycles findet die Kreise im Wartegraph. Rein, ohne Uhr: Review und
// Überfälligkeit stehen schon an den Kanten. Eigenwartet (A -> A) zählt nicht.
func DetectWaitCycles(edges []WaitEdge) []WaitCycle {
	adj := map[string]map[string]bool{}
	nodes := map[string]bool{}
	for _, e := range edges {
		if e.Waiter == e.Awaited || e.Waiter == "" || e.Awaited == "" {
			continue
		}
		if adj[e.Waiter] == nil {
			adj[e.Waiter] = map[string]bool{}
		}
		adj[e.Waiter][e.Awaited] = true
		nodes[e.Waiter], nodes[e.Awaited] = true, true
	}
	order := make([]string, 0, len(nodes))
	for n := range nodes {
		order = append(order, n)
	}
	sort.Strings(order)
	neighbours := func(n string) []string {
		var out []string
		for m := range adj[n] {
			out = append(out, m)
		}
		sort.Strings(out)
		return out
	}

	// Tarjan: stark zusammenhängende Gruppen.
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var groups [][]string
	next := 0
	var visit func(string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range neighbours(v) {
			if _, seen := index[w]; !seen {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] == index[v] {
			var g []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				g = append(g, w)
				if w == v {
					break
				}
			}
			if len(g) >= 2 {
				sort.Strings(g)
				groups = append(groups, g)
			}
		}
	}
	for _, n := range order {
		if _, seen := index[n]; !seen {
			visit(n)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })

	var out []WaitCycle
	for _, g := range groups {
		in := map[string]bool{}
		for _, m := range g {
			in[m] = true
		}
		c := WaitCycle{Members: g}
		if path, ok := hamiltonCycle(g, adj); ok {
			c.Members, c.Ordered = path, true
		}
		first := true
		var reviewAt time.Time
		for _, e := range edges {
			if e.Waiter == e.Awaited || !in[e.Waiter] || !in[e.Awaited] {
				continue
			}
			if laterThan(e.Since, c.Since) {
				c.Since = e.Since
			}
			if t, ok := parseAt(e.ReviewAt); ok && (first || t.Before(reviewAt)) {
				first, reviewAt = false, t
				c.ReviewAt, c.ReviewDerived = e.ReviewAt, e.ReviewDerived
			}
			if e.Overdue {
				c.Overdue = true
			}
		}
		out = append(out, c)
	}
	return out
}

// hamiltonCycle sucht eine Reihenfolge, in der jeder auf den nächsten wartet
// und der letzte auf den ersten, damit "A → B → C → A" so stehen kann. Die
// Gruppen sind klein; über maxOrderedCycle wird nicht gesucht.
func hamiltonCycle(group []string, adj map[string]map[string]bool) ([]string, bool) {
	n := len(group)
	if n > maxOrderedCycle {
		return nil, false
	}
	path := []string{group[0]}
	used := map[string]bool{group[0]: true}
	var walk func() bool
	walk = func() bool {
		last := path[len(path)-1]
		if len(path) == n {
			return adj[last][group[0]]
		}
		for _, m := range group[1:] {
			if used[m] || !adj[last][m] {
				continue
			}
			used[m] = true
			path = append(path, m)
			if walk() {
				return true
			}
			path = path[:len(path)-1]
			used[m] = false
		}
		return false
	}
	if walk() {
		return path, true
	}
	return nil, false
}

// waitRow ist ein offener Wartepunkt: ein Eintrag mit Absender und Empfänger.
type waitRow struct {
	Sender, Recipient, Reason, At, Expires string
	// Kind: "user" (Empfänger ist eine Person), "peer" (ein Agent), sonst "".
	Kind string
}

// edge macht aus einer Zeile eine Wartekante, wenn beide Enden Agenten sind.
func (r waitRow) edge(ref time.Time) (WaitEdge, bool) {
	if r.Kind != "peer" {
		return WaitEdge{}, false
	}
	e := WaitEdge{Waiter: r.Sender, Awaited: r.Recipient, Reason: r.Reason, Since: r.At}
	if _, ok := parseAt(r.Expires); ok {
		e.ReviewAt = r.Expires
	} else if t, ok := parseAt(r.At); ok {
		e.ReviewAt, e.ReviewDerived = t.Add(WaitReviewAfter).Format(time.RFC3339), true
	}
	if t, ok := parseAt(e.ReviewAt); ok && !t.After(ref) {
		e.Overdue = true
	}
	return e, true
}

// loadWaitRows ist die eine Wartequelle: offene, nicht abgelaufene Fragen,
// Freigaben und Blocker der Absender, nur aus diesem Raum (und Threads, die
// dort zuhause sind, soweit sie nicht auf Teilnehmer beschränkt sind). Zwei
// Abfragen, unabhängig von der Zahl der Agenten.
func loadWaitRows(db presenceDB, ref time.Time, roomKey string, senders []any) ([]waitRow, error) {
	if len(senders) == 0 {
		return nil, nil
	}
	refText := ref.Format(time.RFC3339)
	wr, err := db.Query(presenceWaitsSQL(len(senders)),
		append(append([]any{}, senders...), AttentionOpen, AttentionQuestion, AttentionApproval, AttentionBlocker,
			DestinationRoom, roomKey, DestinationDiscussion, roomKey)...)
	if err != nil {
		return nil, err
	}
	var rows []waitRow
	recipients := map[string]bool{}
	for wr.Next() {
		var r waitRow
		if wr.Scan(&r.Sender, &r.Recipient, &r.Reason, &r.At, &r.Expires) == nil && !expiredAt(r.Expires, refText) {
			rows = append(rows, r)
			if _, perr := parsePersonPrincipalID(r.Recipient); perr != nil {
				recipients[r.Recipient] = true
			}
		}
	}
	wr.Close()
	isAgent := map[string]bool{}
	if len(recipients) > 0 {
		list := make([]any, 0, len(recipients))
		for r := range recipients {
			list = append(list, r)
		}
		ar, err := db.Query(`SELECT external_id FROM coord_agents WHERE external_id IN (`+placeholders(len(list))+`)`, list...)
		if err != nil {
			return nil, err
		}
		for ar.Next() {
			var id string
			if ar.Scan(&id) == nil {
				isAgent[id] = true
			}
		}
		ar.Close()
	}
	for i := range rows {
		if _, perr := parsePersonPrincipalID(rows[i].Recipient); perr == nil {
			rows[i].Kind = "user"
		} else if isAgent[rows[i].Recipient] {
			rows[i].Kind = "peer"
		}
	}
	return rows, nil
}

func ensureCoordWaitCycles(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS coord_wait_cycles(
		id INTEGER PRIMARY KEY,
		room_key TEXT NOT NULL,
		cycle_key TEXT NOT NULL,
		formed_at TEXT NOT NULL,
		notified_message_id INTEGER NOT NULL DEFAULT 0,
		dissolved_at TEXT NOT NULL DEFAULT '');
		CREATE UNIQUE INDEX IF NOT EXISTS coord_wait_cycles_active
			ON coord_wait_cycles(room_key,cycle_key) WHERE dissolved_at=''`)
	return err
}

// reconcileWaitCyclesTx gleicht die Kreise des Raums mit dem Merkbuch ab: ein
// Kreis, den es nicht mehr gibt, gilt als aufgelöst; ein neuer bekommt GENAU
// EINE Nachricht an alle Beteiligten. Solange er besteht, ändert sich nichts,
// auch wenn eine Kante dazukommt oder ausläuft. Löst er sich auf und bildet
// sich später neu, ist das ein neuer Kreis mit neuer Meldung.
//
// Aufgerufen, wo sich der Graph ändert: beim Senden einer Frage, Freigabe oder
// eines Blockers und beim Schließen eines Eintrags. Die Meldung geht durch die
// gewöhnliche Weckregel (ShouldWake: Erwähnung im Projektraum, immer im Direkt-
// und Gruppenraum), es gibt keinen eigenen Weckpfad. Sie erzeugt keine
// Wartekante und beantwortet nichts, kann also keine Schleife anstoßen.
func reconcileWaitCyclesTx(tx *sql.Tx, roomKey string, ref time.Time) error {
	if roomKey == "" {
		return nil
	}
	mr, err := tx.Query(`SELECT a.external_id FROM coord_agents a
		JOIN coord_room_memberships m ON m.principal_id=a.external_id
		WHERE m.room_key=? AND m.left_at=''`, roomKey)
	if err != nil {
		return err
	}
	var members []any
	for mr.Next() {
		var id string
		if err := mr.Scan(&id); err != nil {
			mr.Close()
			return err
		}
		members = append(members, id)
	}
	if err := mr.Err(); err != nil {
		mr.Close()
		return err
	}
	mr.Close()

	var cycles []WaitCycle
	if len(members) > 0 {
		rows, err := loadWaitRows(tx, ref, roomKey, members)
		if err != nil {
			return err
		}
		var edges []WaitEdge
		for _, r := range rows {
			if e, ok := r.edge(ref); ok {
				edges = append(edges, e)
			}
		}
		cycles = DetectWaitCycles(edges)
	}
	current := map[string]WaitCycle{}
	for _, c := range cycles {
		current[c.Key()] = c
	}

	ar, err := tx.Query(`SELECT id,cycle_key FROM coord_wait_cycles WHERE room_key=? AND dissolved_at=''`, roomKey)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	var gone []any
	for ar.Next() {
		var id int64
		var key string
		if err := ar.Scan(&id, &key); err != nil {
			ar.Close()
			return err
		}
		if _, still := current[key]; still {
			active[key] = true
		} else {
			gone = append(gone, id)
		}
	}
	if err := ar.Err(); err != nil {
		ar.Close()
		return err
	}
	ar.Close()
	refText := ref.Format(time.RFC3339)
	for _, id := range gone {
		if _, err := tx.Exec(`UPDATE coord_wait_cycles SET dissolved_at=? WHERE id=?`, refText, id); err != nil {
			return err
		}
	}
	for _, c := range cycles {
		key := c.Key()
		if active[key] {
			continue
		}
		res, err := tx.Exec(`INSERT INTO coord_wait_cycles(room_key,cycle_key,formed_at) VALUES(?,?,?)`, roomKey, key, refText)
		if err != nil {
			return err
		}
		rowID, _ := res.LastInsertId()
		mentions := normalizeMembers(c.Members)
		msgID, err := appendCoordMessageTx(tx, CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: roomKey,
			SenderExternalID: WaitCycleSender, AuthorPrincipalID: "system", AuthorKind: AuthorSystem,
			ClientID: "wait-cycle:" + strconv.FormatInt(rowID, 10),
			Intent:   IntentAttention, Priority: "high", Mentions: mentions, CreatedAt: refText,
			// Kein Name im Text: der Raum kann Gäste haben, die Nachrichten
			// lesen. Wer im Kreis steht, sagen die Erwähnungen, die nur
			// Mitglieder sehen, und coord_peers.
			Body: "Mutual wait: the agents mentioned here are waiting on each other, so none of them will move until one of you answers, withdraws or asks the human. Nothing was resolved automatically. See coord_peers for who waits on whom and the review date.",
		})
		if err != nil {
			return err
		}
		if err := insertRawMentionsTx(tx, msgID, []string{waitCycleMask}); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE coord_wait_cycles SET notified_message_id=? WHERE id=?`, msgID, rowID); err != nil {
			return err
		}
	}
	return nil
}
