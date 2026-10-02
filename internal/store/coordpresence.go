package store

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// Presence trennt zwei Aussagen, die leicht verwechselt werden: ob ein Agent
// ERREICHBAR ist (kommt eine Nachricht an) und was er TUT (Spec §A9). Beides
// wird beim Lesen aus Belegen abgeleitet, nichts davon ist gespeichert außer
// dem Belegzeitpunkt selbst. Jedes Feld nennt Herkunft, Zeitpunkt und Alter,
// damit niemand mehr aus dem Wert herausliest, als dahintersteht.
//
// Regel dahinter: Schweigen ist "unknown". Aus ausbleibenden Signalen folgt
// weder "ended" noch "idle".

const (
	ReachConnected = "connected"
	ReachUnknown   = "unknown"
	ReachEnded     = "ended"

	WorkWorking     = "working"
	WorkWaitingUser = "waiting_user"
	WorkWaitingPeer = "waiting_peer"
	WorkBlocked     = "blocked"
	WorkPaused      = "paused"
	WorkUnknown     = "unknown"

	OriginObserved     = "observed"
	OriginSelfReported = "self_reported"
	OriginDerived      = "derived"

	// HeartbeatInterval: höchstens ein Schreibvorgang je Agent und Intervall.
	HeartbeatInterval = 30 * time.Second
	// PollFreshTTL ist ein Vielfaches des Heartbeats: ein einzelner verlorener
	// Schlag macht aus "verbunden" noch kein "unbekannt".
	PollFreshTTL = 3 * HeartbeatInterval
	// ActivityFreshTTL: so lange zählt ein beobachteter Werkzeugaufruf als
	// "arbeitet". Ein Wert zum Starten, nicht gemessen.
	ActivityFreshTTL = 2 * time.Minute
)

// PresenceGaps nennt, was ghosttree heute NICHT beobachten kann. Der Text geht
// in API und Werkzeugantwort, damit die Lücke da steht, wo der Wert steht.
var PresenceGaps = []string{
	"ended is never produced: no session-end hook or collector event exists, so a silent agent is unknown, not ended",
	"no self-report channel for waiting_user: it is only derived from an open question or approval addressed to a person",
	"working is seen only through tool calls that touch a path (collector transcript scan); Bash-only work and model streaming are invisible",
	"codex agents have no pause path and no matching hook signal; their work state is mostly unknown",
	"working needs a session id registered by the agent (ctx claude); agents without one stay unknown, and so does every direct or group room, where no project scopes the activity",
}

// PresenceField ist eine Angabe samt Beleg.
type PresenceField struct {
	Value  string `json:"value"`
	Origin string `json:"origin,omitempty"`
	// At ist der Zeitpunkt des Belegs, leer ohne Beleg. AgeSeconds ist das Alter
	// zum Zeitpunkt der Antwort.
	At         string `json:"at,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
}

// Presence ist Erreichbarkeit und Arbeitszustand eines Agenten.
type Presence struct {
	Reachability PresenceField `json:"reachability"`
	WorkState    PresenceField `json:"work_state"`
}

// PresenceWait ist ein offener Eintrag, auf den der Agent wartet.
type PresenceWait struct {
	Reason string // question, approval, blocker
	// Kind: "user" (Empfänger ist eine Person), "peer" (ein Agent), sonst "".
	Kind string
	At   string
}

// PresenceInput sind die Belege zu einem Agenten.
type PresenceInput struct {
	LastPollAt     string
	LastActivityAt string
	// PauseAt ist der Zeitpunkt, an dem eine Pause belegt wurde (Hook-Ack oder
	// Beleg im Transkript); leer, wenn keine aktive Pause belegt ist. Eine nur
	// angeforderte Pause ist kein Beleg.
	PauseAt string
	Waits   []PresenceWait
}

func parseAt(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

func field(value, origin, at string, ref time.Time) PresenceField {
	f := PresenceField{Value: value, Origin: origin, At: at}
	if t, ok := parseAt(at); ok {
		if age := ref.Sub(t); age > 0 {
			f.AgeSeconds = int64(age / time.Second)
		}
	}
	return f
}

// DerivePresence ist die reine Ableitung, mit Uhr als Parameter.
//
// Reihenfolge beim Arbeitszustand: eine belegte Pause, dann ein selbst
// gemeldeter Blocker, dann offene Wartepunkte, dann frische Aktivität. Ist die
// beobachtete Aktivität jünger als ein Wartepunkt, gewinnt sie: wer nach der
// Frage weiterarbeitet, wartet nicht.
func DerivePresence(ref time.Time, in PresenceInput) Presence {
	p := Presence{
		Reachability: PresenceField{Value: ReachUnknown},
		WorkState:    PresenceField{Value: WorkUnknown},
	}
	if t, ok := parseAt(in.LastPollAt); ok && ref.Sub(t) <= PollFreshTTL {
		p.Reachability = field(ReachConnected, OriginObserved, in.LastPollAt, ref)
	}
	activity, hasActivity := parseAt(in.LastActivityAt)
	if hasActivity && ref.Sub(activity) > ActivityFreshTTL {
		hasActivity = false
	}
	if in.PauseAt != "" {
		p.WorkState = field(WorkPaused, OriginObserved, in.PauseAt, ref)
		return p
	}
	var blocker, user, peer *PresenceWait
	for i := range in.Waits {
		w := &in.Waits[i]
		switch {
		case w.Reason == AttentionBlocker:
			if blocker == nil || laterThan(w.At, blocker.At) {
				blocker = w
			}
		case w.Kind == "user":
			if user == nil || laterThan(w.At, user.At) {
				user = w
			}
		case w.Kind == "peer":
			if peer == nil || laterThan(w.At, peer.At) {
				peer = w
			}
		}
	}
	later := func(w *PresenceWait) bool {
		if w == nil {
			return false
		}
		if !hasActivity {
			return true
		}
		t, ok := parseAt(w.At)
		return !ok || t.After(activity)
	}
	switch {
	case later(blocker):
		p.WorkState = field(WorkBlocked, OriginSelfReported, blocker.At, ref)
	case later(user):
		p.WorkState = field(WorkWaitingUser, OriginDerived, user.At, ref)
	case later(peer):
		p.WorkState = field(WorkWaitingPeer, OriginDerived, peer.At, ref)
	case hasActivity:
		p.WorkState = field(WorkWorking, OriginObserved, in.LastActivityAt, ref)
	}
	return p
}

// Describe beschreibt die Presence in einem Satz für Menschen und Agenten.
func (p Presence) Describe() string {
	one := func(f PresenceField) string {
		if f.Origin == "" {
			return f.Value + " (no observation)"
		}
		return f.Value + " (" + f.Origin + ", " + (time.Duration(f.AgeSeconds) * time.Second).String() + " ago)"
	}
	return "reachability " + one(p.Reachability) + ", work " + one(p.WorkState)
}

func ensureCoordAgentColumn(db *sql.DB, name, ddl string) error {
	rows, err := db.Query(`PRAGMA table_info(coord_agents)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var col, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if col == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE coord_agents ADD COLUMN ` + name + ` ` + ddl)
	return err
}

// ensureCoordAgentPresence ergänzt auf einer alten Datenbank die Spalten für
// den Abruf-Zeitpunkt (last_poll_at) und die vom Agenten gemeldete
// Session-Kennung (session_id), und den Index, der die Wartepunkt-Abfrage trägt.
func ensureCoordAgentPresence(db *sql.DB) error {
	if err := ensureCoordAgentColumn(db, "last_poll_at", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureCoordAgentColumn(db, "session_id", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS coord_messages_sender ON coord_messages(sender_external_id,id);
		CREATE INDEX IF NOT EXISTS coord_attention_message ON coord_attention(message_id,state)`)
	return err
}

// CoordAgentPollDue sagt, ob der letzte Abruf-Stempel älter als das
// HeartbeatInterval ist. Der Aufrufer fragt zuerst hier und schreibt nur bei
// true: so läuft ein Takt innerhalb des Intervalls gar nicht erst in die
// Schreibwarteschlange. Die Antwort geht nie an den Client zurück (kein
// Orakel, Pitfall #2447).
func (s *Store) CoordAgentPollDue(externalID string) bool {
	if s.reader != nil {
		return s.reader.CoordAgentPollDue(externalID)
	}
	var last string
	if err := s.db.QueryRow(`SELECT last_poll_at FROM coord_agents WHERE external_id=?`, externalID).Scan(&last); err != nil {
		return false
	}
	t, ok := parseAt(last)
	return !ok || time.Now().UTC().Sub(t) >= HeartbeatInterval
}

// TouchCoordAgentPoll stempelt den Abruf des Channels. Das Update prüft die
// Frist selbst: wer öfter klopft als alle HeartbeatInterval, ändert nichts, auch
// wenn er CoordAgentPollDue übergeht. Kein Ereignis, keine Sequenznummer: die
// Spalte hat keinen Trigger.
func (s *Store) TouchCoordAgentPoll(externalID string) error {
	if s.writer != nil {
		return queueWrite(s, []any{externalID}, func(d *Store, p []any) error {
			return d.TouchCoordAgentPoll(p[0].(string))
		})
	}
	ref := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE coord_agents SET last_poll_at=? WHERE external_id=? AND last_poll_at<=?`,
		ref.Format(time.RFC3339), externalID, ref.Add(-HeartbeatInterval).Format(time.RFC3339))
	return err
}

// SessionOwnedBy sagt, ob eine Transkript-Session zu diesem Konto gehört. Nur
// eine hochgeladene Session hat einen Eigentümer; Altbestand ohne Konto gehört
// dem Instanz-Owner.
func (s *Store) SessionOwnedBy(sessionExternalID, principalID string) bool {
	if s.reader != nil {
		return s.reader.SessionOwnedBy(sessionExternalID, principalID)
	}
	account, ok := accountNumericID(principalID)
	if !ok || sessionExternalID == "" {
		return false
	}
	owner := instanceOwnerID(s.db)
	// Alle Zeilen dieser Kennung müssen dem Konto gehören: dieselbe Kennung bei
	// einem anderen Harness unter fremdem Konto ist keine eigene Session.
	var total, mine int
	err := s.db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN (CASE WHEN account_id=0 THEN ? ELSE account_id END)=? THEN 1 ELSE 0 END),0)
		FROM sessions WHERE external_id=?`, owner, account, sessionExternalID).Scan(&total, &mine)
	return err == nil && total > 0 && mine == total
}

// presenceDB ist, was die Ableitung von der Datenbank braucht; ein Interface,
// damit ein Test die Zahl der Abfragen zählen kann.
type presenceDB interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// presenceAgent sind die Angaben, aus denen die Belege gesucht werden.
type presenceAgent struct {
	ExternalID, PrincipalID, SessionID, LastPoll string
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(agents []presenceAgent, pick func(presenceAgent) string) []any {
	args := make([]any, 0, len(agents))
	for _, a := range agents {
		args = append(args, pick(a))
	}
	return args
}

// presenceBatch leitet die Presence aller Agenten eines Raums mit einer festen
// Zahl von Abfragen ab (höchstens fünf), unabhängig von der Teilnehmerzahl.
//
// Zuordnung der Aktivität: nur über die vom Agenten gemeldete session_id, mit
// exakter Gleichheit, und nur wenn die Session demselben Konto gehört wie der
// Agent. Kein Suffix-Vergleich: eine Agenten-ID ist frei wählbar. Aktivität
// zählt nur im Projektraum und nur aus diesem Projekt, damit niemand über den
// Arbeitszustand erfährt, was in Projekten läuft, die er nicht sehen darf.
func presenceBatch(db presenceDB, ref time.Time, roomKey string, agents []presenceAgent) map[string]Presence {
	out := make(map[string]Presence, len(agents))
	if len(agents) == 0 {
		return out
	}
	inputs := make(map[string]*PresenceInput, len(agents))
	for _, a := range agents {
		inputs[a.ExternalID] = &PresenceInput{LastPollAt: a.LastPoll}
	}
	refText := ref.Format(time.RFC3339)
	ids := idArgs(agents, func(a presenceAgent) string { return a.ExternalID })

	// 1. Beobachtete Werkzeugaktivität (nur Projektraum, nur Agenten mit session_id).
	if remote, ok := strings.CutPrefix(roomKey, "project:"); ok {
		var withSession []presenceAgent
		for _, a := range agents {
			if a.SessionID != "" {
				withSession = append(withSession, a)
			}
		}
		if len(withSession) > 0 {
			owner := instanceOwnerID(db)
			args := append(idArgs(withSession, func(a presenceAgent) string { return a.SessionID }), remote, ref.Add(-ActivityFreshTTL).Format(time.RFC3339))
			rows, err := db.Query(`SELECT pa.session_external_id,
					CASE WHEN s.account_id=0 THEN `+strconv.FormatInt(owner, 10)+` ELSE s.account_id END, MAX(pa.at)
				FROM path_activity pa JOIN sessions s ON s.external_id=pa.session_external_id
				WHERE pa.session_external_id IN (`+placeholders(len(withSession))+`) AND s.project=? AND pa.at>=?
				GROUP BY pa.session_external_id, s.account_id`, args...)
			if err == nil {
				type seen struct {
					account int64
					at      string
				}
				bySession := map[string][]seen{}
				for rows.Next() {
					var sid, at string
					var account int64
					if rows.Scan(&sid, &account, &at) == nil {
						bySession[sid] = append(bySession[sid], seen{account, at})
					}
				}
				rows.Close()
				for _, a := range withSession {
					account, ok := accountNumericID(a.PrincipalID)
					if !ok {
						continue
					}
					for _, sn := range bySession[a.SessionID] {
						if sn.account == account && laterThan(sn.at, inputs[a.ExternalID].LastActivityAt) {
							inputs[a.ExternalID].LastActivityAt = sn.at
						}
					}
				}
			}
		}
	}

	// 2. Pausen: nur ein WIRKSAMER Vorgang (Hook-Ack und Transkript-Beleg zum
	// selben Aufruf) zählt. Angefordert oder nur quittiert ist keine Pause.
	rows, err := db.Query(`SELECT id,agent,action,resumed_at FROM agent_controls
		WHERE agent IN (`+placeholders(len(ids))+`) AND resumed_at='' AND action=? ORDER BY id`, append(append([]any{}, ids...), ControlPause)...)
	if err == nil {
		controls := map[int64]*AgentControl{}
		var controlIDs []any
		for rows.Next() {
			c := &AgentControl{}
			if rows.Scan(&c.ID, &c.Agent, &c.Action, &c.ResumedAt) == nil {
				controls[c.ID] = c
				controlIDs = append(controlIDs, c.ID)
			}
		}
		rows.Close()
		if len(controlIDs) > 0 {
			ev, err := db.Query(`SELECT control_id,kind,tool_use_id,recorded_at FROM agent_control_events
				WHERE control_id IN (`+placeholders(len(controlIDs))+`) ORDER BY id`, controlIDs...)
			if err == nil {
				for ev.Next() {
					var e ControlEvent
					if ev.Scan(&e.ControlID, &e.Kind, &e.ToolUseID, &e.RecordedAt) == nil {
						if c := controls[e.ControlID]; c != nil {
							c.Events = append(c.Events, e)
						}
					}
				}
				ev.Close()
			}
			for _, c := range controls {
				c.deriveState()
				// Mehrere offene Vorgänge desselben Agenten: der späteste Beleg gilt.
				if c.State == ControlEffective && laterThan(c.EffectiveAt, inputs[c.Agent].PauseAt) {
					inputs[c.Agent].PauseAt = c.EffectiveAt
				}
			}
		}
	}

	// 3. Offene Wartepunkte, nur aus diesem Raum: eine Frage im privaten
	// Gespräch wäre hier sonst für alle Mitglieder ablesbar.
	wr, err := db.Query(presenceWaitsSQL(len(ids)),
		append(append([]any{}, ids...), AttentionOpen, AttentionQuestion, AttentionApproval, AttentionBlocker,
			DestinationRoom, roomKey, DestinationDiscussion, roomKey)...)
	if err == nil {
		type rec struct{ sender, recipient, reason, at string }
		var recs []rec
		recipients := map[string]bool{}
		for wr.Next() {
			var r rec
			var exp string
			if wr.Scan(&r.sender, &r.recipient, &r.reason, &r.at, &exp) == nil && !expiredAt(exp, refText) {
				recs = append(recs, r)
				if _, perr := parsePersonPrincipalID(r.recipient); perr != nil {
					recipients[r.recipient] = true
				}
			}
		}
		wr.Close()
		// 4. Welche Empfänger sind Agenten? Eine Abfrage für alle.
		isAgent := map[string]bool{}
		if len(recipients) > 0 {
			list := make([]any, 0, len(recipients))
			for r := range recipients {
				list = append(list, r)
			}
			if ar, err := db.Query(`SELECT external_id FROM coord_agents WHERE external_id IN (`+placeholders(len(list))+`)`, list...); err == nil {
				for ar.Next() {
					var id string
					if ar.Scan(&id) == nil {
						isAgent[id] = true
					}
				}
				ar.Close()
			}
		}
		for _, r := range recs {
			w := PresenceWait{Reason: r.reason, At: r.at}
			if _, perr := parsePersonPrincipalID(r.recipient); perr == nil {
				w.Kind = "user"
			} else if isAgent[r.recipient] {
				w.Kind = "peer"
			}
			if in := inputs[r.sender]; in != nil {
				in.Waits = append(in.Waits, w)
			}
		}
	}

	for id, in := range inputs {
		out[id] = DerivePresence(ref, *in)
	}
	return out
}

// presenceWaitsSQL ist die Wartepunkt-Abfrage für n Absender. Eigene Funktion,
// damit ein Test dieselbe Abfrage mit EXPLAIN prüft, die sie auch ausführt.
func presenceWaitsSQL(n int) string {
	return `SELECT m.sender_external_id,a.recipient_principal_id,a.reason,a.created_at,COALESCE(m.expires_at,'')
		FROM coord_messages m JOIN coord_attention a ON a.message_id=m.id
		WHERE m.sender_external_id IN (` + placeholders(n) + `) AND a.state=? AND a.reason IN (?,?,?)
		  AND a.recipient_principal_id<>m.sender_external_id
		  AND ((m.destination_kind=? AND m.destination_id=?)
		    OR (m.destination_kind=? AND m.destination_id IN
		         (SELECT CAST(thread_id AS TEXT) FROM thread_homes WHERE room_key=?)))
		ORDER BY a.created_at DESC, a.id DESC LIMIT 2000`
}

// laterThan vergleicht Zeitpunkte als Zeiten, nicht als Text: Zeitzonen und
// Brüche würden sonst die Reihenfolge verfälschen. Ein nicht lesbarer Wert ist
// nie später.
func laterThan(a, b string) bool {
	ta, ok := parseAt(a)
	if !ok {
		return false
	}
	tb, ok := parseAt(b)
	return !ok || ta.After(tb)
}
