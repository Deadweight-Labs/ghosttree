package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Bild-Anhänge (REQ-611) liegen als Blob in der Datenbank, nicht in Transkripten
// und nicht als Datei neben ihr: so reist ein Backup vollständig, und die Rechte
// sind genau die des Gegenstands (Idee oder Issue), an dem das Bild hängt. Was
// ankommt, hat idea.CheckImage bestanden: PNG, JPEG oder WebP nach den ersten
// Bytes, höchstens 5 MB, begrenzte Maße. Der Dateiname des Absenders wird nie
// gespeichert, die Auslieferung nennt das Bild nach seiner Nummer.

func attachmentNotFound(id int64) error {
	return requestdomain.NewRuleError("attachment_not_found", fmt.Sprintf("no attachment ATT-%d", id), "list the attachments of the idea to find its number", nil)
}

// AttachmentURL ist der relative Pfad der Auslieferung.
func AttachmentURL(id int64) string { return "/api/attachments/" + strconv.FormatInt(id, 10) }

const attachmentMetaCols = `id,subject_kind,subject_id,owner_kind,owner_ref,mime,bytes,width,height,sha256,alt,person,created_at`

func scanAttachmentMetas(rows *sql.Rows) ([]idea.AttachmentMeta, error) {
	defer rows.Close()
	out := []idea.AttachmentMeta{}
	for rows.Next() {
		var m idea.AttachmentMeta
		if err := rows.Scan(&m.ID, &m.Subject, &m.SubjectID, &m.Owner, &m.OwnerRef, &m.Mime, &m.Bytes, &m.Width, &m.Height, &m.SHA256, &m.Alt, &m.Person, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.URL = AttachmentURL(m.ID)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) attachmentMetas(kind string, id int64) ([]idea.AttachmentMeta, error) {
	if s.reader != nil {
		return s.reader.attachmentMetas(kind, id)
	}
	rows, err := s.db.Query(`SELECT `+attachmentMetaCols+` FROM attachments WHERE subject_kind=? AND subject_id=? ORDER BY id`, kind, id)
	if err != nil {
		return nil, err
	}
	return scanAttachmentMetas(rows)
}

// AttachmentsOf listet die Anhänge einer Idee oder eines Issues ohne Bytes.
func (a *ProjectAccess) AttachmentsOf(kind string, id int64) ([]idea.AttachmentMeta, error) {
	if _, err := a.decisionSubject(kind, id, ActRead); err != nil {
		return nil, err
	}
	return a.st.attachmentMetas(kind, id)
}

// AttachInput hängt ein Bild an.
type AttachInput struct {
	Subject   string
	SubjectID int64
	// Owner ist idea, reply oder variant. Bei variant ist VariantID die Variante,
	// bei reply ThreadID die Diskussion, deren Antwort das Bild begleitet.
	Owner     string
	VariantID int64
	ThreadID  int64
	Data      []byte
	// DeclaredType ist der Typ, den der Absender angibt; er muss zum Inhalt passen.
	DeclaredType string
	Alt          string
	Actor        idea.Actor
}

// Attach prüft das Bild und legt es an. Wer an der Idee oder dem Issue
// mitarbeiten darf, darf Bilder anhängen; dasselbe Bild am selben Platz noch
// einmal ist ein No-op und liefert den vorhandenen Anhang.
func (a *ProjectAccess) Attach(in AttachInput) (idea.AttachmentMeta, error) {
	project, err := a.decisionSubject(in.Subject, in.SubjectID, ActWork)
	if err != nil {
		return idea.AttachmentMeta{}, err
	}
	img, err := idea.CheckImage(in.Data, in.DeclaredType)
	if err != nil {
		return idea.AttachmentMeta{}, err
	}
	alt, err := idea.CleanOneLine(in.Alt, "alt", "the description", idea.MaxAlt, false)
	if err != nil {
		return idea.AttachmentMeta{}, err
	}
	ownerRef := ""
	switch in.Owner {
	case idea.OwnerIdea:
		if in.Subject != idea.SubjectIdea {
			return idea.AttachmentMeta{}, requestdomain.NewRuleError("owner_invalid", "an image on an issue belongs to a variant of one of its decisions", "attach it to a variant", nil)
		}
		i, err := a.ideaFor(in.SubjectID, ActWork)
		if err != nil {
			return idea.AttachmentMeta{}, err
		}
		if i.Phase == idea.PhaseDismissed || i.Phase == idea.PhaseDone {
			return idea.AttachmentMeta{}, ideaClosed(i)
		}
	case idea.OwnerVariant:
		ok, err := a.st.variantBelongsTo(in.VariantID, in.Subject, in.SubjectID)
		if err != nil {
			return idea.AttachmentMeta{}, err
		}
		if !ok {
			// Eine Variante, die es nicht gibt oder die woanders hängt.
			a.Filtered()
			return idea.AttachmentMeta{}, requestdomain.NewRuleError("variant_not_found", "no such variant here", "attach to a variant of a decision of this idea or issue", nil)
		}
		ownerRef = strconv.FormatInt(in.VariantID, 10)
	case idea.OwnerReply:
		if in.Subject != idea.SubjectIdea {
			return idea.AttachmentMeta{}, requestdomain.NewRuleError("owner_invalid", "a reply image belongs to the discussion of an idea", "attach it to the idea or to a variant", nil)
		}
		if !a.st.threadLinkedToIdea(in.ThreadID, in.SubjectID) {
			a.Filtered()
			return idea.AttachmentMeta{}, requestdomain.NewRuleError("thread_not_found", "no such discussion on this idea", "open the discussion with thread_open and link_kind idea", nil)
		}
		ownerRef = strconv.FormatInt(in.ThreadID, 10)
	default:
		return idea.AttachmentMeta{}, requestdomain.NewRuleError("owner_invalid", fmt.Sprintf("unknown owner %q", in.Owner), "use idea, reply or variant", nil)
	}
	sum := sha256.Sum256(in.Data)
	c := attachCommit{Project: project, Subject: in.Subject, SubjectID: in.SubjectID, Owner: in.Owner, OwnerRef: ownerRef, Mime: img.Mime,
		Width: img.Width, Height: img.Height, SHA256: hex.EncodeToString(sum[:]), Alt: alt, Person: in.Actor.Person, Data: in.Data}
	id, err := a.st.commitAttachment(c)
	if err != nil {
		return idea.AttachmentMeta{}, err
	}
	metas, err := a.st.attachmentMetas(in.Subject, in.SubjectID)
	if err != nil {
		return idea.AttachmentMeta{}, err
	}
	for _, m := range metas {
		if m.ID == id {
			return m, nil
		}
	}
	return idea.AttachmentMeta{}, attachmentNotFound(id)
}

type attachCommit struct {
	Project, Subject string
	SubjectID        int64
	Owner, OwnerRef  string
	Mime             string
	Width, Height    int
	SHA256, Alt      string
	Person           string
	Data             []byte
}

func (s *Store) commitAttachment(c attachCommit) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{c}, func(d *Store, p []any) (int64, error) { return d.commitAttachment(p[0].(attachCommit)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRow(`SELECT id FROM attachments WHERE subject_kind=? AND subject_id=? AND owner_kind=? AND owner_ref=? AND sha256=?`,
		c.Subject, c.SubjectID, c.Owner, c.OwnerRef, c.SHA256).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM attachments WHERE subject_kind=? AND subject_id=?`, c.Subject, c.SubjectID).Scan(&n); err != nil {
		return 0, err
	}
	if n >= idea.MaxAttachmentPerSubject {
		return 0, requestdomain.NewRuleError("attachment_limit", fmt.Sprintf("at most %d images per idea or issue", idea.MaxAttachmentPerSubject), "remove one first", nil)
	}
	res, err := tx.Exec(`INSERT INTO attachments(project,subject_kind,subject_id,owner_kind,owner_ref,mime,bytes,width,height,sha256,alt,person,created_at,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Project, c.Subject, c.SubjectID, c.Owner, c.OwnerRef, c.Mime, len(c.Data), c.Width, c.Height, c.SHA256, c.Alt, c.Person, now(), c.Data)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, tx.Commit()
}

// variantBelongsTo sagt, ob die Variante zu einer offenen Entscheidung dieses
// Gegenstands gehört.
func (s *Store) variantBelongsTo(variantID int64, kind string, id int64) (bool, error) {
	if s.reader != nil {
		return s.reader.variantBelongsTo(variantID, kind, id)
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM decision_variants v JOIN open_decisions d ON d.id=v.decision_id
		WHERE v.id=? AND d.subject_kind=? AND d.subject_id=? AND d.state='open'`, variantID, kind, id).Scan(&n)
	return n > 0, err
}

// threadLinkedToIdea sagt, ob die Diskussion mit der Idee verknüpft ist (in
// beiden Schreibweisen der Verknüpfung: IDEA-n und die bloße Zahl).
func (s *Store) threadLinkedToIdea(threadID, ideaID int64) bool {
	if s.reader != nil {
		return s.reader.threadLinkedToIdea(threadID, ideaID)
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM thread_links WHERE thread_id=? AND object_kind='idea' AND object_id IN (?,?)`,
		threadID, fmt.Sprintf("IDEA-%d", ideaID), strconv.FormatInt(ideaID, 10)).Scan(&n)
	return err == nil && n > 0
}

// AttachmentData liefert die Metadaten und die Bytes eines Anhangs für den
// Betrachter. Wer den Gegenstand nicht lesen darf, bekommt dieselbe Antwort wie
// bei einer Nummer, die es nicht gibt.
func (a *ProjectAccess) AttachmentData(id int64) (idea.AttachmentMeta, []byte, error) {
	m, data, err := a.st.attachmentByID(id)
	if err != nil {
		a.Filtered()
		return idea.AttachmentMeta{}, nil, attachmentNotFound(id)
	}
	if _, err := a.decisionSubject(m.Subject, m.SubjectID, ActRead); err != nil {
		return idea.AttachmentMeta{}, nil, attachmentNotFound(id)
	}
	return m, data, nil
}

func (s *Store) attachmentByID(id int64) (idea.AttachmentMeta, []byte, error) {
	if s.reader != nil {
		return s.reader.attachmentByID(id)
	}
	var m idea.AttachmentMeta
	var data []byte
	err := s.db.QueryRow(`SELECT `+attachmentMetaCols+`,data FROM attachments WHERE id=?`, id).Scan(&m.ID, &m.Subject, &m.SubjectID, &m.Owner, &m.OwnerRef,
		&m.Mime, &m.Bytes, &m.Width, &m.Height, &m.SHA256, &m.Alt, &m.Person, &m.CreatedAt, &data)
	m.URL = AttachmentURL(m.ID)
	return m, data, err
}

// RemoveAttachment löscht einen Anhang. Das darf, wer am Gegenstand
// Bearbeitungsrecht hat (Owner der Idee, Maintainer); ein Bild mit etwas, das
// nicht hineingehört, muss wieder verschwinden können.
func (a *ProjectAccess) RemoveAttachment(id int64) error {
	m, _, err := a.st.attachmentByID(id)
	if err != nil {
		a.Filtered()
		return attachmentNotFound(id)
	}
	if _, err := a.decisionSubject(m.Subject, m.SubjectID, ActRead); err != nil {
		return attachmentNotFound(id)
	}
	if _, err := a.decisionSubject(m.Subject, m.SubjectID, ActEdit); err != nil {
		return err
	}
	return a.st.commitAttachmentRemoval(id)
}

func (s *Store) commitAttachmentRemoval(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.commitAttachmentRemoval(p[0].(int64)) })
	}
	_, err := s.db.Exec(`DELETE FROM attachments WHERE id=?`, id)
	return err
}
