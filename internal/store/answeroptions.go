package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Antwortoptionen (IDEA-2, REQ-599): Ein Agent, der einem Menschen eine Frage
// stellt, kann zwei bis sechs kurze Antworten vorschlagen. Der Posteingang zeigt
// sie als „Vorschläge von <Agent>“ neben „Eigene Antwort“; ein Klick schreibt die
// Antwort so, als hätte der Mensch den Text getippt, und merkt sich nur, dass es
// eine Schnellantwort war.
//
// Wo die Optionen stehen:
//   - Frage im Raum (coord_send, Absicht question): Tabelle answer_options zur
//     Nachricht. Die Antwort ist eine Nachricht des Menschen mit reply_to.
//   - Aufgabe (human_task, Art input oder action): Tabelle answer_options zur
//     Aufgabe. Die Antwort ist die Notiz beim Erledigen.
//   - Entscheidung mit Varianten: die Varianten sind die Optionen (keine neue
//     Tabelle). Die Antwort ist das Entscheiden mit dieser Variante.
//
// Beantworten darf nur ein Mensch in einer Web-Sitzung (QuickAnswer); ein Token
// kann es nicht, auch das eines Agenten nicht, der die Frage selbst gestellt hat.
const answerOptionSchema = `
CREATE TABLE IF NOT EXISTS answer_options(
  subject_kind TEXT NOT NULL,
  subject_id INTEGER NOT NULL,
  position INTEGER NOT NULL,
  text TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id, position)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS quick_answers(
  subject_kind TEXT NOT NULL,
  subject_id INTEGER NOT NULL,
  option_key TEXT NOT NULL,
  option_text TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id)) WITHOUT ROWID;
`

func ensureAnswerOptions(db *sql.DB) error {
	_, err := db.Exec(answerOptionSchema)
	return err
}

// Gegenstände, an denen Optionen und Schnellantworten hängen.
const (
	AnswerOnMessage   = "message"
	AnswerOnTask      = "task"
	AnswerOnDecision  = "decision"
	AnswerOnAttention = "attention"
)

// Grenzen der Optionen.
const (
	MinAnswerOptions = 2
	MaxAnswerOptions = 6
	MaxOptionRunes   = 120
)

// AnswerOption ist ein Vorschlag. Key ist die Stelle (1 bis 6; bei einer
// Entscheidung der Buchstabe der Variante), Text steht so in der Antwort.
type AnswerOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// QuickAnswer sagt an einem Eintrag, dass er mit einem Vorschlag beantwortet wurde.
type QuickAnswer struct {
	Key    string `json:"key"`
	Text   string `json:"text"`
	Person string `json:"person,omitempty"`
	At     string `json:"at"`
}

// CleanAnswerOptions prüft und bereinigt die Optionen, die ein Agent mitgibt. Keine
// Angabe ist erlaubt (nil); sonst zwei bis sechs, je eine einzelne Zeile ohne
// Steuer- und Formatzeichen, nicht leer, höchstens 120 Zeichen und ohne Dopplung.
// Was nicht passt, wird abgelehnt statt gekürzt: der Mensch klickt auf genau den Text,
// den der Agent geschrieben hat.
func CleanAnswerOptions(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) < MinAnswerOptions || len(in) > MaxAnswerOptions {
		return nil, requestdomain.NewRuleError("options_count", fmt.Sprintf("give %d to %d answer options, not %d", MinAnswerOptions, MaxAnswerOptions, len(in)),
			"offer a few short answers, or none and let the person write their own", nil)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for i, raw := range in {
		text := cleanLine(raw, 4*MaxOptionRunes)
		switch {
		case text == "":
			return nil, requestdomain.NewRuleError("option_empty", fmt.Sprintf("answer option %d is empty", i+1), "write what the person would answer", nil)
		case utf8.RuneCountInString(text) > MaxOptionRunes:
			return nil, requestdomain.NewRuleError("option_too_long", fmt.Sprintf("answer option %d is longer than %d characters", i+1, MaxOptionRunes), "keep each option to a short line", nil)
		case seen[strings.ToLower(text)]:
			return nil, requestdomain.NewRuleError("option_duplicate", fmt.Sprintf("answer option %d repeats an earlier one", i+1), "offer different answers", nil)
		}
		seen[strings.ToLower(text)] = true
		out = append(out, text)
	}
	return out, nil
}

func insertAnswerOptionsTx(tx txExec, kind string, id int64, options []string) error {
	for i, text := range options {
		if _, err := tx.Exec(`INSERT INTO answer_options(subject_kind,subject_id,position,text) VALUES(?,?,?,?)`, kind, id, i+1, text); err != nil {
			return err
		}
	}
	return nil
}

// SaveAnswerOptions hängt Optionen an eine Nachricht (kind message), die der
// Aufrufer gerade selbst gesendet hat. Hat die Nachricht schon welche (ein
// wiederholter Aufruf mit derselben Client-Id), bleiben sie, wie sie sind: nur wer
// eine Nachricht neu anlegt, bestimmt ihre Optionen. Die Optionen müssen schon durch
// CleanAnswerOptions gegangen sein.
func (s *Store) SaveAnswerOptions(kind string, id int64, options []string) error {
	if s.writer != nil {
		return queueWrite(s, []any{kind, id, options}, func(d *Store, p []any) error {
			return d.SaveAnswerOptions(p[0].(string), p[1].(int64), p[2].([]string))
		})
	}
	if kind != AnswerOnMessage || id <= 0 || len(options) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var have int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM answer_options WHERE subject_kind=? AND subject_id=?`, kind, id).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	if err := insertAnswerOptionsTx(tx, kind, id, options); err != nil {
		return err
	}
	return tx.Commit()
}

// answerOptionsFor liest die Optionen von Nachrichten oder Aufgaben.
func (s *Store) answerOptionsFor(kind string, ids []int64) (map[int64][]AnswerOption, error) {
	if s.reader != nil {
		return s.reader.answerOptionsFor(kind, ids)
	}
	out := map[int64][]AnswerOption{}
	err := chunked(ids, func(part []int64) error {
		args := append([]any{kind}, factIDArgs(part)...)
		rows, err := s.db.Query(`SELECT subject_id, position, text FROM answer_options WHERE subject_kind=? AND subject_id IN (`+placeholders(len(part))+`)
			ORDER BY subject_id, position`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var pos int
			var o AnswerOption
			if err := rows.Scan(&id, &pos, &o.Text); err != nil {
				return err
			}
			o.Key = strconv.Itoa(pos)
			out[id] = append(out[id], o)
		}
		return rows.Err()
	})
	return out, err
}

// quickAnswersFor liest, was als Schnellantwort beantwortet wurde.
func (s *Store) quickAnswersFor(kind string, ids []int64) (map[int64]QuickAnswer, error) {
	if s.reader != nil {
		return s.reader.quickAnswersFor(kind, ids)
	}
	out := map[int64]QuickAnswer{}
	err := chunked(ids, func(part []int64) error {
		args := append([]any{kind}, factIDArgs(part)...)
		rows, err := s.db.Query(`SELECT subject_id, option_key, option_text, person, created_at FROM quick_answers WHERE subject_kind=? AND subject_id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var q QuickAnswer
			if err := rows.Scan(&id, &q.Key, &q.Text, &q.Person, &q.At); err != nil {
				return err
			}
			out[id] = q
		}
		return rows.Err()
	})
	return out, err
}

// QuickAnswers sagt für Nachrichten, Aufgaben, Entscheidungen oder Posteingangs-Einträge
// (kind message, task, decision, attention), welche per Schnellantwort beantwortet wurden.
// Das Ergebnis enthält nur Nummern mit Schnellantwort, die der Betrachter lesen
// darf: eine Nachricht (und der Eintrag zu ihr) nur, wenn er ihren Raum oder Thread
// lesen darf (ca ist sein Raumzugang), eine Aufgabe nach GetHumanTask, eine
// Entscheidung nach DecisionView. Was es nicht gibt und was er nicht lesen darf,
// fehlt gleich.
func (a *ProjectAccess) QuickAnswers(ca CoordAccess, kind string, ids []int64) (map[int64]QuickAnswer, error) {
	switch kind {
	case AnswerOnMessage, AnswerOnTask, AnswerOnDecision, AnswerOnAttention:
	default:
		return nil, requestdomain.NewRuleError("invalid_answer_subject", "quick answers hang on a message, task, decision or attention item", "use kind message, task, decision or attention", nil)
	}
	if len(ids) > 500 {
		return nil, requestdomain.NewRuleError("too_many_objects", "ask for at most 500 at a time", "page the list", nil)
	}
	marks, err := a.st.quickAnswersFor(kind, ids)
	if err != nil || len(marks) == 0 {
		return marks, err
	}
	for id := range marks {
		if !a.canSeeQuickSubject(ca, kind, id) {
			delete(marks, id)
		}
	}
	return marks, nil
}

// canSeeQuickSubject ist die Sichtprüfung je Art für QuickAnswers.
func (a *ProjectAccess) canSeeQuickSubject(ca CoordAccess, kind string, id int64) bool {
	switch kind {
	case AnswerOnTask:
		_, err := a.GetHumanTask(id)
		return err == nil
	case AnswerOnDecision:
		_, err := a.DecisionView(id)
		return err == nil
	case AnswerOnMessage:
		return a.canReadMessage(ca, id)
	case AnswerOnAttention:
		reader := a.st
		if reader.reader != nil {
			reader = reader.reader
		}
		var messageID int64
		if err := reader.db.QueryRow(`SELECT message_id FROM coord_attention WHERE id=?`, id).Scan(&messageID); err != nil {
			return false
		}
		return a.canReadMessage(ca, messageID)
	}
	return false
}

func (a *ProjectAccess) canReadMessage(ca CoordAccess, messageID int64) bool {
	if ca.isZero() {
		return false
	}
	_, _, err := ca.messageTarget(messageID)
	return err == nil
}

func (s *Store) commitQuickMark(kind string, id int64, key, text string, account int64, person string) error {
	if s.writer != nil {
		return queueWrite(s, []any{kind, id, key, text, account, person}, func(d *Store, p []any) error {
			return d.commitQuickMark(p[0].(string), p[1].(int64), p[2].(string), p[3].(string), p[4].(int64), p[5].(string))
		})
	}
	// Upsert: eine neue Antwort auf denselben Gegenstand (eine wieder geöffnete
	// Aufgabe) ersetzt den alten Vermerk, statt hinter ihm zu verschwinden.
	_, err := s.db.Exec(`INSERT INTO quick_answers(subject_kind,subject_id,option_key,option_text,account_id,person,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(subject_kind,subject_id) DO UPDATE SET option_key=excluded.option_key, option_text=excluded.option_text,
			account_id=excluded.account_id, person=excluded.person, created_at=excluded.created_at`,
		kind, id, key, text, account, person, now())
	return err
}

// proposers liest, wer Fragen gestellt hat, als kurzen Namen des Werkzeugs
// (claude, codex, sonst der Name der Session-Art). Leer, wenn es sich nicht
// ermitteln lässt.
func harnessLabel(h string) string {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "claude-code", "claude":
		return "claude"
	case "":
		return ""
	}
	return cleanLine(h, 30)
}

func (s *Store) taskProposers(ids []int64) (map[int64]string, error) {
	if s.reader != nil {
		return s.reader.taskProposers(ids)
	}
	out := map[int64]string{}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT t.id, COALESCE(s.harness,'') FROM human_tasks t LEFT JOIN sessions s ON s.id=t.created_session_id AND t.created_session_id>0
			WHERE t.id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var h string
			if err := rows.Scan(&id, &h); err != nil {
				return err
			}
			if l := harnessLabel(h); l != "" {
				out[id] = l
			}
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) decisionProposers(ids []int64) (map[int64]string, error) {
	if s.reader != nil {
		return s.reader.decisionProposers(ids)
	}
	out := map[int64]string{}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT d.id, COALESCE((SELECT s.harness FROM sessions s WHERE s.external_id=d.session_ref LIMIT 1),'') FROM open_decisions d
			WHERE d.session_ref!='' AND d.id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var h string
			if err := rows.Scan(&id, &h); err != nil {
				return err
			}
			l := harnessLabel(h)
			if l == "" {
				l = "agent"
			}
			out[id] = l
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) agentProviders(externalIDs []string) (map[string]string, error) {
	if s.reader != nil {
		return s.reader.agentProviders(externalIDs)
	}
	out := map[string]string{}
	for _, id := range externalIDs {
		if id == "" {
			continue
		}
		if _, done := out[id]; done {
			continue
		}
		// Nur ein angemeldeter Agent hat einen Namen; ein Mensch als Absender hat
		// keinen, und "agent" an seinem Namen wäre falsch.
		var provider string
		err := s.db.QueryRow(`SELECT provider FROM coord_agents WHERE external_id=? LIMIT 1`, id).Scan(&provider)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out[id] = ""
		case err != nil:
			return nil, err
		default:
			l := harnessLabel(provider)
			if l == "" {
				l = "agent"
			}
			out[id] = l
		}
	}
	return out, nil
}

// optionsOfDecision macht aus den Varianten einer Entscheidung ihre Optionen:
// Schlüssel ist der Buchstabe, Text der Titel. Ohne mindestens zwei Varianten gibt
// es keine Vorschläge.
func optionsOfDecision(d idea.Decision) []AnswerOption {
	if len(d.Variants) < MinAnswerOptions {
		return nil
	}
	out := make([]AnswerOption, 0, len(d.Variants))
	for _, v := range d.Variants {
		out = append(out, AnswerOption{Key: v.Label, Text: cleanLine(v.Title, MaxOptionRunes)})
	}
	return out
}

// Schnellantwort-Text im Wissen, das beim Entscheiden entsteht, und im Verlauf.
const quickAnswerReason = "per Schnellantwort im Posteingang"

// QuickAnswerInput wählt einen Vorschlag.
type QuickAnswerInput struct {
	// Kind ist attention (Frage im Raum, ID die des Posteingangs-Eintrags
	// attention:n), task oder decision.
	Kind string
	ID   int64
	// Option ist der Schlüssel des Vorschlags (siehe AnswerOption.Key).
	Option string
	// Reason ist bei einer Entscheidung die Begründung des Menschen (eine Zeile,
	// höchstens idea.MaxReasonLine Zeichen); leer heißt „per Schnellantwort“.
	// Bei einer Frage oder Aufgabe zählt sie nicht.
	Reason string
}

// QuickAnswerResult sagt, was geschrieben wurde.
type QuickAnswerResult struct {
	Kind   string       `json:"kind"`
	ID     int64        `json:"id"`
	Option AnswerOption `json:"option"`
	// MessageID ist bei einer Frage im Raum die Antwort-Nachricht.
	MessageID int64 `json:"message_id,omitempty"`
}

// QuickAnswerWebOnly ist der Regelfehler, den jeder Weg außer der Web-Sitzung bekommt.
func quickAnswerWebOnly() error {
	return requestdomain.NewRuleError("web_session_required", "only a person in an interactive web session answers a question to people",
		"open the inbox in the web UI and pick an answer there", nil)
}

func optionNotFound(key string) error {
	return requestdomain.NewRuleError("option_not_found", fmt.Sprintf("there is no answer option %q on this entry", cleanLine(key, 20)), "pick one of the offered answers", nil)
}

// QuickAnswer beantwortet einen Eintrag des Posteingangs mit einem der
// vorgeschlagenen Texte, genau so, als hätte der Mensch ihn getippt:
//
//   - Frage im Raum: eine Nachricht des Menschen mit reply_to an die Frage (im
//     selben Raum oder Thread, an den Fragenden gerichtet), danach ist der Eintrag
//     beantwortet;
//   - Aufgabe: erledigt, der Text steht als Notiz;
//   - Entscheidung: entschieden mit der Variante, Begründung „per Schnellantwort“.
//
// Die Antwort ist als Schnellantwort vermerkt (QuickAnswers). Das darf nur ein
// Mensch in einer Web-Sitzung: ein Token, auch das eines Agenten, bekommt
// web_session_required, und zwar bevor irgendetwas gelesen wird. Was es nicht gibt,
// was nicht an den Betrachter gerichtet ist und was er nicht lesen darf, antwortet
// gleich. ca ist der Raumzugang des Betrachters (Posteingang): ohne ihn gibt es keine Fragen.
func (a *ProjectAccess) QuickAnswer(ca CoordAccess, in QuickAnswerInput) (QuickAnswerResult, error) {
	if !a.valid || a.account == 0 || a.principal.TokenKind != WebSessionKind {
		return QuickAnswerResult{}, quickAnswerWebOnly()
	}
	key := strings.TrimSpace(in.Option)
	switch in.Kind {
	case "attention":
		return a.quickAnswerAttention(ca, in.ID, key)
	case AnswerOnTask:
		return a.quickAnswerTask(in.ID, key)
	case AnswerOnDecision:
		return a.quickAnswerDecision(in.ID, key, in.Reason)
	}
	return QuickAnswerResult{}, requestdomain.NewRuleError("invalid_answer_subject", "answer a question in a room, a task or a decision", "use kind attention, task or decision", nil)
}

func pickOption(options []AnswerOption, key string) (AnswerOption, bool) {
	for _, o := range options {
		if o.Key == key {
			return o, true
		}
	}
	return AnswerOption{}, false
}

func (a *ProjectAccess) quickAnswerAttention(ca CoordAccess, id int64, key string) (QuickAnswerResult, error) {
	if ca.isZero() {
		return QuickAnswerResult{}, ErrCoordNotFound
	}
	items, err := ca.Attention()
	if err != nil {
		return QuickAnswerResult{}, err
	}
	var item *AttentionItem
	for i := range items {
		if items[i].ID == id && items[i].IsRecipient && items[i].State == AttentionOpen {
			item = &items[i]
		}
	}
	if item == nil || item.Reason != AttentionQuestion {
		return QuickAnswerResult{}, ErrCoordNotFound
	}
	opts, err := a.st.answerOptionsFor(AnswerOnMessage, []int64{item.MessageID})
	if err != nil {
		return QuickAnswerResult{}, err
	}
	opt, ok := pickOption(opts[item.MessageID], key)
	if !ok {
		return QuickAnswerResult{}, optionNotFound(key)
	}
	msg := CoordMessage{ClientID: fmt.Sprintf("quick-answer-%d", item.ID), Body: opt.Text, ReplyTo: item.MessageID}
	if item.SenderID != "" {
		msg.Mentions = []string{item.SenderID}
	}
	var replyID int64
	if item.DestinationKind == DestinationDiscussion {
		threadID, perr := parseThreadDestination(item.DestinationID)
		if perr != nil {
			return QuickAnswerResult{}, perr
		}
		replyID, err = ca.ThreadPost(threadID, msg)
	} else {
		msg.DestinationKind, msg.DestinationID = item.DestinationKind, item.DestinationID
		replyID, err = ca.Send(msg)
	}
	if err != nil {
		return QuickAnswerResult{}, err
	}
	if err := a.st.commitQuickMark(AnswerOnMessage, replyID, opt.Key, opt.Text, a.account, cleanLine(a.principal.Label, 100)); err != nil {
		return QuickAnswerResult{}, err
	}
	if err := a.st.commitQuickMark(AnswerOnAttention, item.ID, opt.Key, opt.Text, a.account, cleanLine(a.principal.Label, 100)); err != nil {
		return QuickAnswerResult{}, err
	}
	if err := ca.ResolveAttention(item.ID, AttentionActionAnswer); err != nil {
		return QuickAnswerResult{}, err
	}
	return QuickAnswerResult{Kind: "attention", ID: item.ID, Option: opt, MessageID: replyID}, nil
}

func (a *ProjectAccess) webTaskActor() humantask.Actor {
	return humantask.Actor{AccountID: a.account, Person: cleanLine(a.principal.Label, 100), Web: true}
}

func (a *ProjectAccess) quickAnswerTask(id int64, key string) (QuickAnswerResult, error) {
	t, err := a.GetHumanTask(id)
	if err != nil {
		return QuickAnswerResult{}, err
	}
	if t.State != humantask.Open {
		return QuickAnswerResult{}, requestdomain.NewRuleError("task_not_open", fmt.Sprintf("%s is not open", t.HumanID()), "only an open task takes an answer", nil)
	}
	opts, err := a.st.answerOptionsFor(AnswerOnTask, []int64{id})
	if err != nil {
		return QuickAnswerResult{}, err
	}
	opt, ok := pickOption(opts[id], key)
	if !ok {
		return QuickAnswerResult{}, optionNotFound(key)
	}
	if _, err := a.MarkTasksDone([]int64{id}, opt.Text, a.webTaskActor()); err != nil {
		return QuickAnswerResult{}, err
	}
	if err := a.st.commitQuickMark(AnswerOnTask, id, opt.Key, opt.Text, a.account, cleanLine(a.principal.Label, 100)); err != nil {
		return QuickAnswerResult{}, err
	}
	return QuickAnswerResult{Kind: AnswerOnTask, ID: id, Option: opt}, nil
}

func (a *ProjectAccess) quickAnswerDecision(id int64, key, reason string) (QuickAnswerResult, error) {
	d, err := a.DecisionView(id)
	if err != nil {
		return QuickAnswerResult{}, err
	}
	if d.State != idea.DecisionOpen {
		return QuickAnswerResult{}, requestdomain.NewRuleError("decision_not_open", fmt.Sprintf("%s is already closed", d.HumanID()), "only an open decision takes an answer", nil)
	}
	opt, ok := pickOption(optionsOfDecision(d), key)
	if !ok {
		return QuickAnswerResult{}, optionNotFound(key)
	}
	// Die Begründung steht im Wissen, wie der Mensch sie schrieb (Leerraum zu einem
	// Leerzeichen); zu lang wird abgewiesen, nicht gekürzt.
	why := cleanLine(reason, 10*idea.MaxReasonLine)
	if utf8.RuneCountInString(why) > idea.MaxReasonLine {
		return QuickAnswerResult{}, requestdomain.NewRuleError("reason_too_long", fmt.Sprintf("the reason is longer than %d characters", idea.MaxReasonLine), "shorten the reason", nil)
	}
	if why == "" {
		why = quickAnswerReason
	}
	actor := idea.Actor{AccountID: a.account, Person: cleanLine(a.principal.Label, 100), Agent: false, GitAnchor: gitanchor.Web()}
	if _, err := a.CloseDecision(CloseDecisionInput{ID: id, Outcome: idea.DecisionDecided, Variant: opt.Key, Reason: why, Actor: actor}); err != nil {
		return QuickAnswerResult{}, err
	}
	if err := a.st.commitQuickMark(AnswerOnDecision, id, opt.Key, opt.Text, a.account, cleanLine(a.principal.Label, 100)); err != nil {
		return QuickAnswerResult{}, err
	}
	return QuickAnswerResult{Kind: AnswerOnDecision, ID: id, Option: opt}, nil
}
