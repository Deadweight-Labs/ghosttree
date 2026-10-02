package store

import (
	"database/sql"
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
			if blocker == nil || w.At > blocker.At {
				blocker = w
			}
		case w.Kind == "user":
			if user == nil || w.At > user.At {
				user = w
			}
		case w.Kind == "peer":
			if peer == nil || w.At > peer.At {
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

func ensureCoordAgentLastPoll(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(coord_agents)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "last_poll_at" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE coord_agents ADD COLUMN last_poll_at TEXT NOT NULL DEFAULT ''`)
	return err
}

// TouchCoordAgentPoll stempelt den Abruf des Channels. Der Schreibvorgang
// geschieht höchstens einmal je HeartbeatInterval und Agent: das Update trägt
// die Drosselung selbst, ein Aufrufer, der öfter klopft, ändert nichts. Kein
// Ereignis, keine Sequenznummer: die Spalte hat keinen Trigger, und der Aufrufer
// erfährt nicht, ob geschrieben wurde (kein Orakel, Pitfall #2447).
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

// presenceFor sammelt die Belege eines Agenten im Raum und leitet ab.
func presenceFor(db *sql.DB, ref time.Time, roomKey, externalID, lastPoll string) Presence {
	in := PresenceInput{LastPollAt: lastPoll}
	// Beobachtete Werkzeugaktivität: der Collector schreibt die Session-ID des
	// Transkripts, die Agenten-ID endet auf dieselbe Kennung (claude:<host>:<uuid>).
	session := externalID
	if i := strings.LastIndex(externalID, ":"); i >= 0 {
		session = externalID[i+1:]
	}
	cutoff := ref.Add(-ActivityFreshTTL).Format(time.RFC3339)
	var at sql.NullString
	if db.QueryRow(`SELECT MAX(at) FROM path_activity WHERE session_external_id IN (?,?) AND at>=?`,
		externalID, session, cutoff).Scan(&at) == nil && at.Valid {
		in.LastActivityAt = at.String
	}
	// Pause: nur ein belegter Vorgang zählt, kein angeforderter.
	if c, ok, err := activeControlTx(db, externalID); err == nil && ok && c.Action == ControlPause {
		switch c.State {
		case ControlEffective:
			in.PauseAt = c.EffectiveAt
		case ControlAcknowledged:
			in.PauseAt = c.AckedAt
		}
	}
	// Offene Wartepunkte, nur aus diesem Raum: eine Frage im privaten Gespräch
	// wäre hier sonst für alle Mitglieder ablesbar.
	rows, err := db.Query(`SELECT a.recipient_principal_id,a.reason,a.created_at,COALESCE(m.expires_at,'')
		FROM coord_attention a JOIN coord_messages m ON m.id=a.message_id
		WHERE m.sender_external_id=? AND a.state=? AND a.reason IN (?,?,?)
		  AND a.recipient_principal_id<>?
		  AND ((m.destination_kind=? AND m.destination_id=?)
		    OR (m.destination_kind=? AND m.destination_id IN
		         (SELECT CAST(thread_id AS TEXT) FROM thread_homes WHERE room_key=?)))
		ORDER BY a.created_at DESC, a.id DESC LIMIT 50`,
		externalID, AttentionOpen, AttentionQuestion, AttentionApproval, AttentionBlocker, externalID,
		DestinationRoom, roomKey, DestinationDiscussion, roomKey)
	if err == nil {
		type rec struct{ recipient, reason, at, exp string }
		var recs []rec
		for rows.Next() {
			var r rec
			if rows.Scan(&r.recipient, &r.reason, &r.at, &r.exp) == nil {
				recs = append(recs, r)
			}
		}
		rows.Close()
		for _, r := range recs {
			if expiredAt(r.exp, ref.Format(time.RFC3339)) {
				continue
			}
			w := PresenceWait{Reason: r.reason, At: r.at}
			if _, perr := parsePersonPrincipalID(r.recipient); perr == nil {
				w.Kind = "user"
			} else {
				var one int
				if db.QueryRow(`SELECT 1 FROM coord_agents WHERE external_id=?`, r.recipient).Scan(&one) == nil {
					w.Kind = "peer"
				}
			}
			in.Waits = append(in.Waits, w)
		}
	}
	return DerivePresence(ref, in)
}
