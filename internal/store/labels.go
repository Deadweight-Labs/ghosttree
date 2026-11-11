package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// labelsSchema (REQ-600): echte Labels wie bei GitHub, dazu Themen, gespeicherte
// Suchen und Ideen (REQ-606). Alles neue Tabellen, additiv.
//
// Ein Label gehört einem Projekt (scope_ref = Remote) oder einer Org (scope_ref
// = Org-Id): ein Org-Label gilt in allen Projekten der Org. Die Zuordnung zu
// Objekten steht in label_assignments mit (Art, Id) statt einer Tabelle je Art,
// weil Labels an Requests, Pains, Ideen und Wissen hängen und die Art selten
// dazukommt. Ein Objekt ist über sein Projekt einer Label-Menge zugeordnet; die
// Zuordnung prüft der Zugriffs-Teil, die Tabelle selbst kennt keine Projekte.
//
// Namen gelten ohne Rücksicht auf Groß- und Kleinschreibung einmalig je Bereich.
//
// Themen: benannte Bereiche eines Projekts als Liste von Pfad-Präfixen.
// Gespeicherte Suchen: Name, Beschreibung und eine bis mehrere Suchzeilen mit
// Platzhaltern, persönlich (scope_ref = Konto), je Projekt oder je Org.
// Ideen: wie Pains ein Eingang, mit Autor, Anker und Kontext wie dort.
const labelsSchema = `
CREATE TABLE IF NOT EXISTS labels(
  id INTEGER PRIMARY KEY,
  scope_kind TEXT NOT NULL CHECK(scope_kind IN ('project','org')),
  scope_ref TEXT NOT NULL CHECK(scope_ref != ''),
  name TEXT NOT NULL CHECK(trim(name) != ''),
  color TEXT NOT NULL DEFAULT 'ededed',
  description TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS labels_name ON labels(scope_kind, scope_ref, lower(name));
CREATE TABLE IF NOT EXISTS label_assignments(
  label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  obj_kind TEXT NOT NULL CHECK(obj_kind IN ('request','pain','idea','knowledge')),
  obj_id INTEGER NOT NULL,
  assigned_by TEXT NOT NULL DEFAULT '', assigned_at TEXT NOT NULL,
  PRIMARY KEY(label_id, obj_kind, obj_id));
CREATE INDEX IF NOT EXISTS label_assignments_obj ON label_assignments(obj_kind, obj_id);
CREATE TABLE IF NOT EXISTS topics(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  name TEXT NOT NULL CHECK(trim(name) != ''),
  description TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS topics_name ON topics(project, lower(name));
CREATE TABLE IF NOT EXISTS topic_prefixes(
  topic_id INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  prefix TEXT NOT NULL CHECK(prefix != ''),
  PRIMARY KEY(topic_id, prefix)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS search_presets(
  id INTEGER PRIMARY KEY,
  scope_kind TEXT NOT NULL CHECK(scope_kind IN ('user','project','org')),
  scope_ref TEXT NOT NULL CHECK(scope_ref != ''),
  name TEXT NOT NULL CHECK(trim(name) != ''),
  description TEXT NOT NULL DEFAULT '',
  queries TEXT NOT NULL CHECK(trim(queries) != ''),
  created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS search_presets_name ON search_presets(scope_kind, scope_ref, lower(name));
CREATE TABLE IF NOT EXISTS ideas(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  title TEXT NOT NULL CHECK(trim(title) != ''),
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','accepted','dismissed')),
  reason TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK(state != 'dismissed' OR trim(reason) != ''));
CREATE INDEX IF NOT EXISTS ideas_project_state ON ideas(project, state);
CREATE TABLE IF NOT EXISTS idea_authors(
  idea_id INTEGER PRIMARY KEY REFERENCES ideas(id) ON DELETE RESTRICT, account_id INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS idea_requests(
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  linked_by TEXT NOT NULL DEFAULT '', linked_at TEXT NOT NULL,
  PRIMARY KEY(idea_id, request_id));
CREATE INDEX IF NOT EXISTS idea_requests_request ON idea_requests(request_id);
`

// ensureLabelsAndIdeas legt die Tabellen an, beliebig oft aufrufbar. Er läuft vor
// ensureGitAnchors: ideas trägt wie die anderen Schreib-Tabellen anchor_id und
// origin_id (siehe anchoredTables).
func ensureLabelsAndIdeas(db *sql.DB) error {
	if _, err := db.Exec(labelsSchema); err != nil {
		return err
	}
	// updated_by: wer eine gespeicherte Suche zuletzt geändert hat, additiv.
	return addColumnIfMissing(db, "search_presets", "updated_by", "TEXT NOT NULL DEFAULT ''")
}

// Maße der Label-Texte.
const (
	MaxLabelName = 50
	MaxLabelDesc = 200
	// DefaultLabelColor ist das helle Grau, wenn niemand eine Farbe nennt.
	DefaultLabelColor = "ededed"
)

// Label ist ein Label eines Projekts oder einer Org.
type Label struct {
	ID int64 `json:"id"`
	// Scope ist "project" oder "org".
	Scope string `json:"scope"`
	// Project ist die Remote bei einem Projekt-Label, sonst leer; OrgID die der
	// Org bei einem Org-Label.
	Project     string `json:"project,omitempty"`
	OrgID       int64  `json:"org_id,omitempty"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// LabelInput legt ein Label an. Genau eines von Project und OrgID.
type LabelInput struct {
	Project     string
	OrgID       int64
	Name        string
	Color       string
	Description string
	By          string
}

// LabelPatch ändert ein Label; nil lässt ein Feld stehen.
type LabelPatch struct {
	Name        *string
	Color       *string
	Description *string
}

func labelRule(code, msg, res string) error { return requestdomain.NewRuleError(code, msg, res, nil) }

// oneLineText prüft einen Text, der in Briefings und Listen landet: eine Zeile,
// ohne Steuer- und unsichtbare Formatzeichen (Bidi, Nullbreite), höchstens
// max Zeichen.
func oneLineText(s string, max int) error {
	if len([]rune(s)) > max {
		return fmt.Errorf("longer than %d characters", max)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("must be one line without control characters")
		}
	}
	return nil
}

// CleanLabelName prüft einen Label-Namen. Er steht in der Suchsprache als
// Wert (label:name), darum sind Komma, Anführungszeichen und Doppelpunkt
// ausgeschlossen, und er landet in Briefings, darum nur eine Zeile.
func CleanLabelName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", labelRule("label_name_required", "a label needs a name", "give the label a name")
	}
	if err := oneLineText(s, MaxLabelName); err != nil {
		return "", labelRule("label_name_invalid", "label name: "+err.Error(), "use a short one-line name")
	}
	if strings.ContainsAny(s, `,":`) || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "@") {
		return "", labelRule("label_name_invalid", "a label name cannot contain a comma, a quotation mark or a colon, or start with - or @", "these characters have a meaning in the search language")
	}
	return s, nil
}

// CleanLabelColor liefert die Farbe als sechs kleine Hexziffern ohne "#".
func CleanLabelColor(s string) (string, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if s == "" {
		return DefaultLabelColor, nil
	}
	if len(s) != 6 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", labelRule("label_color_invalid", "a label color is six hexadecimal digits such as d73a4a", "use a value like d73a4a or #d73a4a")
	}
	return s, nil
}

// CleanLabelDescription prüft die Beschreibung: eine Zeile.
func CleanLabelDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if err := oneLineText(s, MaxLabelDesc); err != nil {
		return "", labelRule("label_description_invalid", "label description: "+err.Error(), "use one short line")
	}
	return s, nil
}

func labelNotFound() error {
	return labelRule("label_not_found", "no such label", "list the labels of the project to find its name")
}

const labelCols = `id,scope_kind,scope_ref,name,color,description,created_by,created_at,updated_at`

func scanLabel(row interface{ Scan(...any) error }) (Label, error) {
	var l Label
	var ref string
	if err := row.Scan(&l.ID, &l.Scope, &ref, &l.Name, &l.Color, &l.Description, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return l, err
	}
	if l.Scope == "org" {
		fmt.Sscanf(ref, "%d", &l.OrgID)
	} else {
		l.Project = ref
	}
	return l, nil
}

// CreateLabel legt ein Label an. Ein Name gilt je Bereich einmal, ohne
// Rücksicht auf Groß- und Kleinschreibung.
func (s *Store) CreateLabel(in LabelInput) (Label, error) {
	name, err := CleanLabelName(in.Name)
	if err != nil {
		return Label{}, err
	}
	color, err := CleanLabelColor(in.Color)
	if err != nil {
		return Label{}, err
	}
	desc, err := CleanLabelDescription(in.Description)
	if err != nil {
		return Label{}, err
	}
	in.Name, in.Color, in.Description = name, color, desc
	in.Project = scope.NormalizeRemote(in.Project)
	if (in.Project == "") == (in.OrgID == 0) {
		return Label{}, labelRule("label_scope_invalid", "a label belongs to a project or to an org, not both and not neither", "name the project or the org")
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (Label, error) { return d.CreateLabel(p[0].(LabelInput)) })
	}
	kind, ref := "project", in.Project
	if in.OrgID != 0 {
		kind, ref = "org", fmt.Sprint(in.OrgID)
	}
	ts := now()
	res, err := s.db.Exec(`INSERT INTO labels(scope_kind,scope_ref,name,color,description,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		kind, ref, in.Name, in.Color, in.Description, in.By, ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Label{}, labelRule("label_exists", fmt.Sprintf("there is already a label %q here", in.Name), "use it, or pick another name")
		}
		return Label{}, err
	}
	id, _ := res.LastInsertId()
	return scanLabel(s.db.QueryRow(`SELECT `+labelCols+` FROM labels WHERE id=?`, id))
}

// LabelByID liefert ein Label ohne Zugriffsprüfung; der Aufrufer prüft.
func (s *Store) LabelByID(id int64) (Label, error) {
	if s.reader != nil {
		return s.reader.LabelByID(id)
	}
	l, err := scanLabel(s.db.QueryRow(`SELECT `+labelCols+` FROM labels WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return Label{}, labelNotFound()
	}
	return l, err
}

// UpdateLabel ändert Name, Farbe oder Beschreibung.
func (s *Store) UpdateLabel(id int64, p LabelPatch) (Label, error) {
	if p.Name != nil {
		n, err := CleanLabelName(*p.Name)
		if err != nil {
			return Label{}, err
		}
		p.Name = &n
	}
	if p.Color != nil {
		c, err := CleanLabelColor(*p.Color)
		if err != nil {
			return Label{}, err
		}
		p.Color = &c
	}
	if p.Description != nil {
		d, err := CleanLabelDescription(*p.Description)
		if err != nil {
			return Label{}, err
		}
		p.Description = &d
	}
	if s.writer != nil {
		return queueValue(s, []any{id, p}, func(d *Store, v []any) (Label, error) {
			return d.UpdateLabel(v[0].(int64), v[1].(LabelPatch))
		})
	}
	cur, err := s.LabelByID(id)
	if err != nil {
		return Label{}, err
	}
	if p.Name != nil {
		cur.Name = *p.Name
	}
	if p.Color != nil {
		cur.Color = *p.Color
	}
	if p.Description != nil {
		cur.Description = *p.Description
	}
	if _, err := s.db.Exec(`UPDATE labels SET name=?,color=?,description=?,updated_at=? WHERE id=?`, cur.Name, cur.Color, cur.Description, now(), id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Label{}, labelRule("label_exists", fmt.Sprintf("there is already a label %q here", cur.Name), "pick another name")
		}
		return Label{}, err
	}
	return s.LabelByID(id)
}

// DeleteLabel löscht ein Label samt seinen Zuordnungen.
func (s *Store) DeleteLabel(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.DeleteLabel(p[0].(int64)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM label_assignments WHERE label_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM labels WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// LabelsAvailable liefert die Labels, die in einem Projekt gelten: die des
// Projekts und die seiner Org, nach Name.
func (s *Store) LabelsAvailable(project string) ([]Label, error) {
	if s.reader != nil {
		return s.reader.LabelsAvailable(project)
	}
	project = scope.NormalizeRemote(project)
	rows, err := s.db.Query(`SELECT `+labelCols+` FROM labels
		WHERE (scope_kind='project' AND scope_ref=?)
		   OR (scope_kind='org' AND scope_ref=(SELECT CAST(org_id AS TEXT) FROM projects WHERE remote=?))
		ORDER BY lower(name), scope_kind`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelsOfOrg liefert die Labels einer Org.
func (s *Store) LabelsOfOrg(orgID int64) ([]Label, error) {
	if s.reader != nil {
		return s.reader.LabelsOfOrg(orgID)
	}
	rows, err := s.db.Query(`SELECT `+labelCols+` FROM labels WHERE scope_kind='org' AND scope_ref=? ORDER BY lower(name)`, fmt.Sprint(orgID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelNamed findet ein Label im Projekt nach Name; das des Projekts geht dem
// der Org vor. labelNotFound, wenn es keines gibt.
func (s *Store) LabelNamed(project, name string) (Label, error) {
	if s.reader != nil {
		return s.reader.LabelNamed(project, name)
	}
	name = strings.TrimSpace(name)
	all, err := s.LabelsAvailable(project)
	if err != nil {
		return Label{}, err
	}
	var found *Label
	for i, l := range all {
		if strings.EqualFold(l.Name, name) && (found == nil || l.Scope == "project") {
			found = &all[i]
		}
	}
	if found == nil {
		return Label{}, labelNotFound()
	}
	return *found, nil
}

// LabelObjectKinds sind die Arten, die ein Label tragen können.
var LabelObjectKinds = []string{"request", "pain", "idea", "knowledge"}

func validLabelKind(kind string) bool {
	for _, k := range LabelObjectKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// AssignLabel setzt ein Label an ein Objekt; wiederholt schadet nicht. Ob das
// Label zum Projekt des Objekts gehört, prüft der Aufrufer (ProjectAccess).
func (s *Store) AssignLabel(labelID int64, kind string, objID int64, by string) error {
	if !validLabelKind(kind) {
		return labelRule("label_kind_invalid", fmt.Sprintf("labels do not apply to %q", kind), "use request, pain, idea or knowledge")
	}
	if s.writer != nil {
		return queueWrite(s, []any{labelID, kind, objID, by}, func(d *Store, p []any) error {
			return d.AssignLabel(p[0].(int64), p[1].(string), p[2].(int64), p[3].(string))
		})
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO label_assignments(label_id,obj_kind,obj_id,assigned_by,assigned_at) VALUES(?,?,?,?,?)`,
		labelID, kind, objID, by, now())
	return err
}

// UnassignLabel nimmt ein Label von einem Objekt.
func (s *Store) UnassignLabel(labelID int64, kind string, objID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{labelID, kind, objID}, func(d *Store, p []any) error {
			return d.UnassignLabel(p[0].(int64), p[1].(string), p[2].(int64))
		})
	}
	_, err := s.db.Exec(`DELETE FROM label_assignments WHERE label_id=? AND obj_kind=? AND obj_id=?`, labelID, kind, objID)
	return err
}

// LabelsFor liefert die Labels der Objekte einer Art, nach Objekt-Id.
func (s *Store) LabelsFor(kind string, ids []int64) (map[int64][]Label, error) {
	if s.reader != nil {
		return s.reader.LabelsFor(kind, ids)
	}
	out := map[int64][]Label{}
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		args := []any{kind}
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT a.obj_id, l.id,l.scope_kind,l.scope_ref,l.name,l.color,l.description,l.created_by,l.created_at,l.updated_at
			FROM label_assignments a JOIN labels l ON l.id=a.label_id
			WHERE a.obj_kind=? AND a.obj_id IN (`+placeholders(len(part))+`) ORDER BY lower(l.name)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var obj int64
			var l Label
			var ref string
			if err := rows.Scan(&obj, &l.ID, &l.Scope, &ref, &l.Name, &l.Color, &l.Description, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			if l.Scope == "org" {
				fmt.Sscanf(ref, "%d", &l.OrgID)
			} else {
				l.Project = ref
			}
			out[obj] = append(out[obj], l)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
