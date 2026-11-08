package store

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Reaktionen an Kommentaren, Ideen und Issues (REQ-599): eine kleine, feste Liste
// wie bei GitHub, keine freien Emojis. Eine Person setzt jede Reaktion höchstens
// einmal an einem Objekt; setzen und entfernen sind idempotent. Lesen darf, wer das
// Objekt lesen darf, reagieren ab member (wie Kommentare schreiben).
//
// Die Reaktion gehört dem Konto (account_id), der Name steht daneben, wie er beim
// Reagieren lautete. Es gibt keine Tabelle der erlaubten Werte und keinen CHECK auf
// der Spalte content: die Liste ist Code, und eine Erweiterung braucht so keinen
// Tabellenumbau.
const reactionSchema = `
CREATE TABLE IF NOT EXISTS reactions(
  id INTEGER PRIMARY KEY,
  object_kind TEXT NOT NULL,
  object_id INTEGER NOT NULL,
  content TEXT NOT NULL,
  account_id INTEGER NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(object_kind, object_id, content, account_id));
CREATE INDEX IF NOT EXISTS reactions_account ON reactions(account_id);
`

func ensureReactions(db *sql.DB) error {
	_, err := db.Exec(reactionSchema)
	return err
}

// Arten von Objekten, an denen man reagieren kann.
const (
	ReactOnComment = "comment"
	ReactOnIdea    = "idea"
	ReactOnRequest = "request"
)

// ReactionKind ist eine erlaubte Reaktion: der Schlüssel (so steht er in der
// Datenbank und in der API, die Namen sind die von GitHub), das Zeichen und ein
// deutscher Name für die Beschriftung.
type ReactionKind struct {
	Key   string `json:"key"`
	Emoji string `json:"emoji"`
	Label string `json:"label"`
}

// ReactionSet ist die feste Liste, in der Reihenfolge der Auswahl.
var ReactionSet = []ReactionKind{
	{"+1", "👍", "Daumen hoch"},
	{"-1", "👎", "Daumen runter"},
	{"laugh", "😄", "Lachen"},
	{"hooray", "🎉", "Hurra"},
	{"confused", "😕", "Verwirrt"},
	{"heart", "❤️", "Herz"},
	{"rocket", "🚀", "Rakete"},
	{"eyes", "👀", "Augen"},
}

// MaxReactionPeople begrenzt, wie viele Namen eine Reaktion in der Antwort nennt;
// die Zahl zählt trotzdem alle.
const MaxReactionPeople = 50

// ParseReaction löst eine Angabe auf einen Schlüssel der festen Liste auf. Sie darf
// der Schlüssel ("+1", "heart") oder das Zeichen sein (mit oder ohne
// Variantenselektor). Alles andere ist kein Treffer: es gibt keine freien Emojis.
func ParseReaction(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "️", ""))
	if s == "" {
		return "", false
	}
	for _, k := range ReactionSet {
		if strings.EqualFold(s, k.Key) || s == strings.ReplaceAll(k.Emoji, "️", "") {
			return k.Key, true
		}
	}
	return "", false
}

func reactionKindOf(key string) ReactionKind {
	for _, k := range ReactionSet {
		if k.Key == key {
			return k
		}
	}
	return ReactionKind{Key: key}
}

// ReactionCount ist eine Reaktion an einem Objekt: wie oft, ob der Betrachter sie
// gesetzt hat und wer (die ersten MaxReactionPeople, älteste zuerst).
type ReactionCount struct {
	Content string   `json:"content"`
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	Mine    bool     `json:"mine"`
	People  []string `json:"people"`
}

// ReactionSummary sind die Reaktionen eines Objekts in der Reihenfolge der festen
// Liste; nur Reaktionen mit mindestens einer Person stehen darin. CanReact sagt, ob
// der Betrachter reagieren darf (die Oberfläche zeigt die Auswahl danach).
type ReactionSummary struct {
	Kind     string          `json:"kind"`
	ID       int64           `json:"id"`
	Items    []ReactionCount `json:"items"`
	CanReact bool            `json:"can_react"`
}

func reactionTargetNotFound(kind string, id int64) error {
	return requestdomain.NewRuleError("reaction_target_not_found", fmt.Sprintf("%s %d not found, or you may not read it", kind, id), "name a comment, idea or issue you can read", nil)
}

func invalidReaction(content string) error {
	keys := make([]string, 0, len(ReactionSet))
	for _, k := range ReactionSet {
		keys = append(keys, k.Key)
	}
	return requestdomain.NewRuleError("invalid_reaction", fmt.Sprintf("%q is not one of the reactions", cleanLine(content, 40)), "use one of: "+strings.Join(keys, ", "), nil)
}

// reactionTarget prüft, dass der Betrachter das Objekt lesen darf, und sagt, ob er
// reagieren darf. Was es nicht gibt, was er nicht lesen darf (auch ein gelöschter
// Kommentar) antwortet gleich.
func (a *ProjectAccess) reactionTarget(kind string, id int64) (canReact bool, err error) {
	notFound := reactionTargetNotFound(kind, id)
	switch kind {
	case ReactOnComment:
		c, err := a.commentFor(id)
		if err != nil || c.Deleted {
			return false, notFound
		}
		return a.Allow(c.Project, ResComment, ActCreate, Object{Own: true}), nil
	case ReactOnIdea:
		i, err := a.st.IdeaByID(id)
		if err != nil || !a.CanSeeIdea(i) {
			a.Filtered()
			return false, notFound
		}
		return a.Allow(i.Project, ResIdea, ActWork, a.ideaObject(i)), nil
	case ReactOnRequest:
		ref, err := a.st.RequestRef(RefRequest, id)
		if err != nil || strings.TrimSpace(ref.Project) == "" || !requestReadable(a, ref) {
			a.Filtered()
			return false, notFound
		}
		return a.Allow(ref.Project, ResRequest, ActWork, Object{Own: true}), nil
	}
	return false, requestdomain.NewRuleError("invalid_reaction_target", "reactions hang on a comment, idea or issue", "use kind comment, idea or request", nil)
}

// React setzt eine Reaktion des Betrachters. Hat er sie schon gesetzt, ändert sich
// nichts (kein Fehler). Ein Gast liest nur (403).
func (a *ProjectAccess) React(kind string, id int64, content string) (ReactionSummary, error) {
	return a.setReaction(kind, id, content, true)
}

// Unreact nimmt die Reaktion des Betrachters zurück; hat er sie nicht gesetzt,
// ändert sich nichts.
func (a *ProjectAccess) Unreact(kind string, id int64, content string) (ReactionSummary, error) {
	return a.setReaction(kind, id, content, false)
}

func (a *ProjectAccess) setReaction(kind string, id int64, content string, set bool) (ReactionSummary, error) {
	can, err := a.reactionTarget(kind, id)
	if err != nil {
		return ReactionSummary{}, err
	}
	if !can {
		return ReactionSummary{}, ErrAccessForbidden
	}
	key, ok := ParseReaction(content)
	if !ok {
		return ReactionSummary{}, invalidReaction(content)
	}
	if !a.valid || a.account == 0 {
		return ReactionSummary{}, ErrAccessForbidden
	}
	if err := a.st.commitReaction(kind, id, key, a.account, cleanLine(a.principal.Label, 100), set); err != nil {
		return ReactionSummary{}, err
	}
	return a.Reactions(kind, id)
}

// Reactions liefert die Reaktionen eines Objekts. Wer das Objekt nicht lesen darf,
// bekommt dieselbe Antwort wie für eines, das es nicht gibt.
func (a *ProjectAccess) Reactions(kind string, id int64) (ReactionSummary, error) {
	can, err := a.reactionTarget(kind, id)
	if err != nil {
		return ReactionSummary{}, err
	}
	rows, err := a.st.reactionRows(kind, []int64{id})
	if err != nil {
		return ReactionSummary{}, err
	}
	return a.summarize(kind, id, rows[id], can), nil
}

// CommentReactions liefert die Reaktionen aller Kommentare eines Objekts (request,
// idea oder pain) mit einem Aufruf, nach Kommentar-Nummer. Kommentare ohne Reaktion
// fehlen nicht: sie haben eine leere Liste. Ein gelöschter Kommentar trägt nichts.
func (a *ProjectAccess) CommentReactions(kind string, objectID int64) (map[int64]ReactionSummary, error) {
	comments, err := a.Comments(kind, objectID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(comments))
	for _, c := range comments {
		if !c.Deleted {
			ids = append(ids, c.ID)
		}
	}
	rows, err := a.st.reactionRows(ReactOnComment, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ReactionSummary, len(ids))
	for _, c := range comments {
		if c.Deleted {
			continue
		}
		out[c.ID] = a.summarize(ReactOnComment, c.ID, rows[c.ID], a.Allow(c.Project, ResComment, ActCreate, Object{Own: true}))
	}
	return out, nil
}

// ReactionsFor liefert die Reaktionen mehrerer Ideen oder Issues (kind idea oder
// request) mit einem Aufruf, für Listen. Was der Betrachter nicht lesen darf, fehlt
// im Ergebnis, ohne Fehler und ohne Unterschied zu einem, das es nicht gibt.
func (a *ProjectAccess) ReactionsFor(kind string, ids []int64) (map[int64]ReactionSummary, error) {
	if kind != ReactOnIdea && kind != ReactOnRequest {
		return nil, requestdomain.NewRuleError("invalid_reaction_target", "list reactions of ideas or issues", "use kind idea or request", nil)
	}
	if len(ids) > 500 {
		return nil, requestdomain.NewRuleError("too_many_objects", "ask for at most 500 objects at a time", "page the list", nil)
	}
	readable := make([]int64, 0, len(ids))
	can := map[int64]bool{}
	for _, id := range ids {
		c, err := a.reactionTarget(kind, id)
		if err != nil {
			continue
		}
		readable = append(readable, id)
		can[id] = c
	}
	rows, err := a.st.reactionRows(kind, readable)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ReactionSummary, len(readable))
	for _, id := range readable {
		out[id] = a.summarize(kind, id, rows[id], can[id])
	}
	return out, nil
}

type reactionRow struct {
	Content string
	Account int64
	Person  string
}

func (a *ProjectAccess) summarize(kind string, id int64, rows []reactionRow, can bool) ReactionSummary {
	s := ReactionSummary{Kind: kind, ID: id, Items: []ReactionCount{}, CanReact: can}
	for _, k := range ReactionSet {
		var rc ReactionCount
		for _, r := range rows {
			if r.Content != k.Key {
				continue
			}
			rc.Count++
			if a.valid && a.account != 0 && r.Account == a.account {
				rc.Mine = true
			}
			if len(rc.People) < MaxReactionPeople && !slices.Contains(rc.People, r.Person) {
				rc.People = append(rc.People, r.Person)
			}
		}
		if rc.Count == 0 {
			continue
		}
		rc.Content, rc.Emoji = k.Key, k.Emoji
		if rc.People == nil {
			rc.People = []string{}
		}
		s.Items = append(s.Items, rc)
	}
	return s
}

func (s *Store) commitReaction(kind string, id int64, content string, account int64, person string, set bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{kind, id, content, account, person, set}, func(d *Store, p []any) error {
			return d.commitReaction(p[0].(string), p[1].(int64), p[2].(string), p[3].(int64), p[4].(string), p[5].(bool))
		})
	}
	if set {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO reactions(object_kind,object_id,content,account_id,person,created_at) VALUES(?,?,?,?,?,?)`,
			kind, id, content, account, person, now())
		return err
	}
	_, err := s.db.Exec(`DELETE FROM reactions WHERE object_kind=? AND object_id=? AND content=? AND account_id=?`, kind, id, content, account)
	return err
}

// reactionRows liest die Reaktionen von Objekten einer Art, die ältesten zuerst.
func (s *Store) reactionRows(kind string, ids []int64) (map[int64][]reactionRow, error) {
	if s.reader != nil {
		return s.reader.reactionRows(kind, ids)
	}
	out := map[int64][]reactionRow{}
	err := chunked(ids, func(part []int64) error {
		args := append([]any{kind}, factIDArgs(part)...)
		rows, err := s.db.Query(`SELECT object_id, content, account_id, person FROM reactions WHERE object_kind=? AND object_id IN (`+placeholders(len(part))+`)
			ORDER BY id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var r reactionRow
			if err := rows.Scan(&id, &r.Content, &r.Account, &r.Person); err != nil {
				return err
			}
			out[id] = append(out[id], r)
		}
		return rows.Err()
	})
	return out, err
}
