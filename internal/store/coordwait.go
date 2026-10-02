package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
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

	// waitChunk begrenzt die IN-Listen einer Abfrage; SQLite kennt eine
	// Obergrenze für Variablen, und große Räume sollen sie nicht erreichen.
	waitChunk = 500
)

// waitNoteInterval: höchstens eine Systemmeldung über einen Wartekreis je Raum
// in diesem Abstand. Ein weiterer Kreis wird im Merkbuch festgehalten und
// erscheint in coord_peers, wird aber nicht gesendet. Variable für Tests.
var waitNoteInterval = 10 * time.Minute

// WaitEdge: Waiter wartet auf Awaited.
type WaitEdge struct {
	Waiter, Awaited, Reason string
	Since                   string
	// ReviewAt ist das Ablaufdatum des Eintrags, sonst Since + WaitReviewAfter
	// (ReviewDerived). Danach ist die Wartekante überfällig, nicht geschlossen.
	// Ein Eintrag mit eigenem Ablauf wird nie überfällig: abgelaufen ist er
	// keine gegenwärtige Wartekante mehr (wie bei der Weckregel), er fällt aus
	// dem Graphen, statt als überfällig weiterzugelten.
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
// dort zuhause sind, soweit sie nicht auf Teilnehmer beschränkt sind). Die
// Absenderliste wird in Stücken zu waitChunk gefragt; sonst zwei Abfragen
// (plus eine für die Empfänger), unabhängig von der Zahl der Agenten.
func loadWaitRows(db presenceDB, ref time.Time, roomKey string, senders []any) ([]waitRow, error) {
	refText := ref.Format(time.RFC3339)
	var rows []waitRow
	recipients := map[string]bool{}
	for _, part := range chunkArgs(senders, waitChunk) {
		wr, err := db.Query(presenceWaitsSQL(len(part)),
			append([]any{roomKey, AttentionQuestion, AttentionApproval, AttentionBlocker}, part...)...)
		if err != nil {
			return nil, err
		}
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
	}
	isAgent := map[string]bool{}
	list := make([]any, 0, len(recipients))
	for r := range recipients {
		list = append(list, r)
	}
	for _, part := range chunkArgs(list, waitChunk) {
		ar, err := db.Query(`SELECT external_id FROM coord_agents WHERE external_id IN (`+placeholders(len(part))+`)`, part...)
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

func chunkArgs(list []any, n int) [][]any {
	var out [][]any
	for len(list) > 0 {
		k := n
		if len(list) < k {
			k = len(list)
		}
		out = append(out, list[:k])
		list = list[k:]
	}
	return out
}

func ensureCoordWaitCycles(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS coord_wait_cycles(
		id INTEGER PRIMARY KEY,
		room_key TEXT NOT NULL,
		cycle_key TEXT NOT NULL,
		formed_at TEXT NOT NULL,
		notified_message_id INTEGER NOT NULL DEFAULT 0,
		notified_at TEXT NOT NULL DEFAULT '',
		held INTEGER NOT NULL DEFAULT 0,
		dissolved_at TEXT NOT NULL DEFAULT '');
		CREATE UNIQUE INDEX IF NOT EXISTS coord_wait_cycles_active
			ON coord_wait_cycles(room_key,cycle_key) WHERE dissolved_at='';
		CREATE INDEX IF NOT EXISTS coord_wait_cycles_set ON coord_wait_cycles(room_key,cycle_key,notified_at)`); err != nil {
		return err
	}
	for _, col := range []struct{ table, name, ddl string }{
		{"coord_wait_cycles", "notified_at", `TEXT NOT NULL DEFAULT ''`},
		{"coord_wait_cycles", "held", `INTEGER NOT NULL DEFAULT 0`},
		{"coord_attention", "room_key", `TEXT NOT NULL DEFAULT ''`},
	} {
		if err := ensureColumn(db, col.table, col.name, col.ddl); err != nil {
			return err
		}
	}
	// room_key an coord_attention: der Raum der Nachricht, damit die
	// Wartepunkt-Abfrage die offenen Einträge EINES Raums findet. Altbestand
	// wird beim Öffnen nachgetragen (nur offene Einträge zählen).
	if _, err := db.Exec(`DROP INDEX IF EXISTS coord_attention_open;
		CREATE INDEX IF NOT EXISTS coord_attention_open_room ON coord_attention(room_key,message_id) WHERE state='open';
		UPDATE coord_attention SET room_key=COALESCE((
			SELECT CASE m.destination_kind WHEN 'room' THEN m.destination_id
				ELSE (SELECT h.room_key FROM thread_homes h WHERE CAST(h.thread_id AS TEXT)=m.destination_id) END
			FROM coord_messages m WHERE m.id=coord_attention.message_id),'')
		WHERE state='open' AND room_key=''`); err != nil {
		return err
	}
	return nil
}

func ensureColumn(db *sql.DB, table, name, ddl string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var col, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notNull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || col == name
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + ddl)
	return err
}

// attentionRoomKeyTx ist der Raum, zu dem ein Attention-Eintrag zählt: der
// Raum der Nachricht, bei einem Thread sein Heimatraum, sonst "" (ein Thread
// ohne Heimat hat keinen Raum und zählt für keinen Wartekreis).
func attentionRoomKeyTx(tx *sql.Tx, kind, id string) string {
	if kind == DestinationRoom {
		return id
	}
	threadID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || threadID <= 0 {
		return ""
	}
	if home, found, err := threadHomeTx(tx, threadID); err == nil && found {
		return home.RoomKey
	}
	return ""
}

// roomWaitCycles sind die Wartekreise eines Raums, über ALLE aktiven
// Agenten-Mitglieder, nicht nur über die einer gefilterten Peer-Liste. Eine
// Quelle für die Anzeige (CoordPeers) und den Abgleich (reconcile).
func roomWaitCycles(db presenceDB, ref time.Time, roomKey string) ([]WaitCycle, error) {
	mr, err := db.Query(`SELECT a.external_id FROM coord_agents a
		JOIN coord_room_memberships m ON m.principal_id=a.external_id
		WHERE m.room_key=? AND m.left_at=''`, roomKey)
	if err != nil {
		return nil, err
	}
	var members []any
	for mr.Next() {
		var id string
		if err := mr.Scan(&id); err != nil {
			mr.Close()
			return nil, err
		}
		members = append(members, id)
	}
	if err := mr.Err(); err != nil {
		mr.Close()
		return nil, err
	}
	mr.Close()
	rows, err := loadWaitRows(db, ref, roomKey, members)
	if err != nil {
		return nil, err
	}
	var edges []WaitEdge
	for _, r := range rows {
		if e, ok := r.edge(ref); ok {
			edges = append(edges, e)
		}
	}
	return DetectWaitCycles(edges), nil
}

func cycleMembers(key string) map[string]bool {
	out := map[string]bool{}
	for _, m := range strings.Split(key, "\x1f") {
		out[m] = true
	}
	return out
}

func isSubset(sub, super map[string]bool) bool {
	for m := range sub {
		if !super[m] {
			return false
		}
	}
	return true
}

// execWaitSavepoint führt die Savepoint-Anweisungen aus; Variable, damit ein
// Test ein Scheitern des Rollbacks erzwingen kann.
var execWaitSavepoint = func(tx *sql.Tx, query string) error {
	_, err := tx.Exec(query)
	return err
}

// reconcileWaitCyclesSafeTx ist reconcileWaitCyclesTx in einem Savepoint: ein
// Fehler dort wird protokolliert und zurückgerollt, und der Vorgang, der den
// Graphen geändert hat (eine Nachricht, ein Schließen), gelingt trotzdem. Die
// Meldung ist ein Zusatz und darf nichts blockieren.
//
// Ausnahme: scheitert der Rollback selbst, ist der Zustand der Transaktion
// unbekannt (SQLite kann sie ganz zurückgerollt haben, dann liefe der Rest im
// Autocommit). Dann gibt die Funktion den Fehler zurück, und der Aufrufer
// bricht ab.
func reconcileWaitCyclesSafeTx(tx *sql.Tx, roomKey string, ref time.Time) error {
	if roomKey == "" {
		return nil
	}
	if err := execWaitSavepoint(tx, `SAVEPOINT wait_cycles`); err != nil {
		log.Printf("wait cycles: savepoint: %v", err)
		return nil
	}
	if err := reconcileWaitCyclesTx(tx, roomKey, ref); err != nil {
		log.Printf("wait cycles: room %s: %v", roomKey, err)
		if rerr := execWaitSavepoint(tx, `ROLLBACK TO wait_cycles`); rerr != nil {
			return fmt.Errorf("wait cycles: rollback failed after %v: %w", err, rerr)
		}
	}
	if err := execWaitSavepoint(tx, `RELEASE wait_cycles`); err != nil {
		log.Printf("wait cycles: release: %v", err)
	}
	return nil
}

// reconcileWaitCyclesTx gleicht die Kreise des Raums mit dem Merkbuch ab.
//
//   - Ein Kreis, den es nicht mehr gibt, gilt als aufgelöst.
//   - Ein Kreis, dessen Mitglieder alle schon in einem aktiven, gemeldeten
//     Kreis standen (er ist geschrumpft oder hat sich geteilt), wird still
//     übernommen: wer geht, löst keine neue Meldung aus.
//   - Ein Kreis mit mindestens einem Mitglied, das in keinem aktiven Kreis
//     stand, ist neu und bekommt EINE Nachricht an alle Beteiligten. Das
//     gilt auch, wenn ein Kreis wächst. Löst er sich auf und bildet sich
//     später neu, ist er wieder neu.
//   - Höchstens eine Meldung je Mitgliedermenge und waitNoteInterval. Eine
//     zurückgehaltene Meldung (held) steht im Merkbuch und wird beim nächsten
//     Abgleich nach Ablauf der Frist gesendet, wenn der Kreis dann noch
//     besteht. Die Frist gilt je Mitgliedermenge, nicht je Raum: ein Kreis aus
//     Gästen verbraucht kein Kontingent, auf das ein Kreis von Mitgliedern
//     angewiesen ist.
//
// Aufgerufen, wo sich der Graph ändert (Senden einer Frage, Freigabe oder
// eines Blockers; Schließen eines Eintrags). Die Meldung geht durch die
// gewöhnliche Weckregel (ShouldWake), es gibt keinen eigenen Weckpfad. Sie
// erzeugt keine Wartekante und beantwortet nichts, kann also keine Schleife
// anstoßen.
func reconcileWaitCyclesTx(tx *sql.Tx, roomKey string, ref time.Time) error {
	cycles, err := roomWaitCycles(tx, ref, roomKey)
	if err != nil {
		return err
	}
	current := map[string]WaitCycle{}
	for _, c := range cycles {
		current[c.Key()] = c
	}
	ar, err := tx.Query(`SELECT id,cycle_key,formed_at,held FROM coord_wait_cycles WHERE room_key=? AND dissolved_at=''`, roomKey)
	if err != nil {
		return err
	}
	type active struct {
		id      int64
		key, at string
		held    bool
		members map[string]bool
	}
	var rows []active
	byKey := map[string]active{}
	for ar.Next() {
		var r active
		var held int
		if err := ar.Scan(&r.id, &r.key, &r.at, &held); err != nil {
			ar.Close()
			return err
		}
		r.held, r.members = held != 0, cycleMembers(r.key)
		rows = append(rows, r)
		byKey[r.key] = r
	}
	if err := ar.Err(); err != nil {
		ar.Close()
		return err
	}
	ar.Close()
	refText := ref.Format(time.RFC3339)
	cutoff := ref.Add(-waitNoteInterval).Format(time.RFC3339)
	limited := func(key string) (bool, error) {
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM coord_wait_cycles
			WHERE room_key=? AND cycle_key=? AND notified_message_id<>0 AND notified_at>?`, roomKey, key, cutoff).Scan(&n)
		return n > 0, err
	}
	send := func(rowID int64, c WaitCycle) error {
		msgID, err := appendCoordMessageTx(tx, CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: roomKey,
			SenderExternalID: WaitCycleSender, AuthorPrincipalID: "system", AuthorKind: AuthorSystem,
			ClientID: randomNoteID(),
			Intent:   IntentAttention, Priority: "high", Mentions: normalizeMembers(c.Members), CreatedAt: refText,
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
		_, err = tx.Exec(`UPDATE coord_wait_cycles SET notified_message_id=?,notified_at=?,held=0 WHERE id=?`, msgID, refText, rowID)
		return err
	}
	for _, c := range cycles {
		key := c.Key()
		if r, ok := byKey[key]; ok {
			if r.held {
				if lim, err := limited(key); err != nil {
					return err
				} else if !lim {
					if err := send(r.id, c); err != nil {
						return err
					}
				}
			}
			continue
		}
		members := cycleMembers(key)
		formed, covered := refText, false
		for _, r := range rows {
			if !r.held && isSubset(members, r.members) {
				formed, covered = r.at, true
				break
			}
		}
		res, err := tx.Exec(`INSERT INTO coord_wait_cycles(room_key,cycle_key,formed_at) VALUES(?,?,?)`, roomKey, key, formed)
		if err != nil {
			return err
		}
		if covered {
			continue
		}
		rowID, _ := res.LastInsertId()
		if lim, err := limited(key); err != nil {
			return err
		} else if lim {
			if _, err := tx.Exec(`UPDATE coord_wait_cycles SET held=1 WHERE id=?`, rowID); err != nil {
				return err
			}
			continue
		}
		if err := send(rowID, c); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if _, ok := current[r.key]; !ok {
			if _, err := tx.Exec(`UPDATE coord_wait_cycles SET dissolved_at=? WHERE id=?`, refText, r.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// randomNoteID ist die ClientID einer Systemmeldung: unvorhersagbar. Die
// Meldung geht außerdem nie durch die Wiederholungserkennung (siehe
// appendCoordMessageTx), eine vorab gesendete ClientID unterdrückt sie also nicht.
func randomNoteID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("wait cycle note id: " + err.Error())
	}
	return "wait-cycle:" + hex.EncodeToString(b[:])
}
