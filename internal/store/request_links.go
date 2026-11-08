package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// allowDerivedRelation erweitert die CHECK-Bedingung von request_relations um die
// Art derived („entstanden aus“, ein Auftrag aus einem anderen). SQLite ändert
// eine CHECK-Bedingung nicht; die Tabelle wird einmal neu gebaut, Zeilen und
// Nummern bleiben. Eine Datenbank, die die Art schon kennt, bleibt unberührt,
// der Aufruf ist wiederholbar. Er läuft nach ensureWorkPackages, weil die Spalte
// package_id dort entsteht und mit hinüber muss.
//
// Auf request_relations zeigt kein Fremdschlüssel einer anderen Tabelle, und es
// gibt weder Index noch Trigger darauf; der Umbau braucht also nur die Prüfung
// der eigenen Verweise (auf requests, knowledge und work_packages), und die
// geschieht für diese eine Tabelle, nicht für die ganze Datenbank.
func allowDerivedRelation(db *sql.DB) error {
	known := func(q interface {
		QueryRow(string, ...any) *sql.Row
	}) (bool, error) {
		var ddl string
		if err := q.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='request_relations'`).Scan(&ddl); err != nil {
			return false, err
		}
		return strings.Contains(ddl, "'derived'"), nil
	}
	if ok, err := known(db); err != nil || ok {
		return err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if ok, err := known(tx); err != nil || ok {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE request_relations_new(
  id INTEGER PRIMARY KEY,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE RESTRICT,
  other_request_id INTEGER REFERENCES requests(id) ON DELETE RESTRICT,
  knowledge_id INTEGER REFERENCES knowledge(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('parent','related','blocks','duplicates','supersedes','knowledge','external','derived')),
  external_ref TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
  package_id INTEGER REFERENCES work_packages(id))`,
		`INSERT INTO request_relations_new(id,request_id,other_request_id,knowledge_id,kind,external_ref,created_at,package_id)
  SELECT id,request_id,other_request_id,knowledge_id,kind,external_ref,created_at,package_id FROM request_relations`,
		`DROP TABLE request_relations`,
		`ALTER TABLE request_relations_new RENAME TO request_relations`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(request_relations)`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		return errors.New("request_relations rebuild: a row refers to something that is gone")
	}
	return tx.Commit()
}

func linkTargetNotFound(ref string) error {
	return requestdomain.NewRuleError("origin_not_found", ref+" not found, or you may not read it",
		"name an object of the same project that you can read", nil)
}

// ResolveRequestLinks prüft Eltern-Issue und Quellen eines neuen Auftrags im
// Projekt project für den Betrachter und liefert die Verknüpfungen, die der
// Store beim Anlegen setzt. Jedes Ziel muss im selben Projekt liegen und für den
// Betrachter lesbar sein; was es nicht gibt, was in einem anderen Projekt liegt
// und was er nicht lesen darf, antwortet gleich (origin_not_found). Eine
// Schreibweise, die kein Verweis ist, ist ein Eingabefehler (invalid_link).
// Nichts wird geschrieben.
func (a *ProjectAccess) ResolveRequestLinks(project, parent string, derivedFrom []string) (requestdomain.Links, error) {
	project = scope.NormalizeRemote(project)
	out := requestdomain.Links{Principal: a.principal.ID, WebSession: a.principal.TokenKind == WebSessionKind}
	bad := func(s string, err error) error {
		return requestdomain.NewRuleError("invalid_link", fmt.Sprintf("%q: %v", s, err), "write REQ-12, IDEA-3, PAIN-7 or thread:5", nil)
	}
	if strings.TrimSpace(parent) != "" {
		ref, err := requestdomain.ParseOriginRef(parent)
		if err != nil || ref.Kind != requestdomain.OriginRequest {
			if err == nil {
				err = errors.New("a parent is an issue, REQ-n")
			}
			return out, bad(parent, err)
		}
		if !a.requestInProject(project, ref.ID) {
			a.Filtered()
			return out, linkTargetNotFound(fmt.Sprintf("REQ-%d", ref.ID))
		}
		out.Parent = ref.ID
	}
	if len(derivedFrom) > requestdomain.MaxDerivedFrom {
		return out, requestdomain.NewRuleError("too_many_links", fmt.Sprintf("at most %d sources in derived_from", requestdomain.MaxDerivedFrom), "name fewer sources", nil)
	}
	seen := map[requestdomain.OriginRef]bool{}
	for _, raw := range derivedFrom {
		ref, err := requestdomain.ParseOriginRef(raw)
		if err != nil {
			return out, bad(raw, err)
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		switch ref.Kind {
		case requestdomain.OriginRequest:
			if !a.requestInProject(project, ref.ID) {
				a.Filtered()
				return out, linkTargetNotFound(fmt.Sprintf("REQ-%d", ref.ID))
			}
			out.Requests = append(out.Requests, ref.ID)
		case requestdomain.OriginIdea:
			i, err := a.st.IdeaByID(ref.ID)
			if err != nil || scope.NormalizeRemote(i.Project) != project || !a.CanSeeIdea(i) {
				a.Filtered()
				return out, linkTargetNotFound(fmt.Sprintf("IDEA-%d", ref.ID))
			}
			if err := a.checkIdeaLinkable(i); err != nil {
				if err == ErrAccessNotFound {
					a.Filtered()
					return out, linkTargetNotFound(fmt.Sprintf("IDEA-%d", ref.ID))
				}
				return out, err
			}
			out.Ideas = append(out.Ideas, requestdomain.IdeaLink{ID: ref.ID, Promote: true})
		case requestdomain.OriginPain:
			p, err := a.st.PainByID(ref.ID)
			if err != nil || scope.NormalizeRemote(p.Project) != project || a.CheckPain(p, ActRead) != nil {
				a.Filtered()
				return out, linkTargetNotFound(fmt.Sprintf("PAIN-%d", ref.ID))
			}
			// A link is an edge the pain gets: it needs the right to work on it.
			if err := a.CheckPain(p, ActWork); err != nil {
				return out, err
			}
			out.Pains = append(out.Pains, ref.ID)
		case requestdomain.OriginThread:
			t, err := a.st.CoordinationPublicFor(a.principal).Thread(ref.ID)
			if err != nil || scope.NormalizeRemote(t.Project) != project {
				a.Filtered()
				return out, linkTargetNotFound(fmt.Sprintf("thread %d", ref.ID))
			}
			out.Threads = append(out.Threads, ref.ID)
		}
	}
	return out, nil
}

// requestInProject: der Auftrag liegt im Projekt und der Betrachter darf ihn lesen.
func (a *ProjectAccess) requestInProject(project string, id int64) bool {
	ref, err := a.st.RequestRef(RefRequest, id)
	return err == nil && scope.NormalizeRemote(ref.Project) == project && requestReadable(a, ref)
}

// insertRequestLinksTx legt die geprüften Verknüpfungen eines neuen Auftrags in
// der Transaktion des Anlegens an: schlägt eine fehl, entsteht auch der Auftrag
// nicht. Jede Verknüpfung nimmt den Weg der bestehenden Funktion (Relation
// parent oder derived, idea_requests, entity_links motivates, thread_links).
func (s *Store) insertRequestLinksTx(tx *sql.Tx, id int64, project, person string, author int64, l requestdomain.Links, ts string,
	anchor *gitanchor.Anchor, anchorID, originID any, origin *originctx.Context) error {
	relation := func(kind string, other int64) error {
		if _, err := tx.Exec(`INSERT INTO request_relations(request_id,other_request_id,kind,created_at) VALUES(?,?,?,?)`, id, other, kind, ts); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,anchor_id,origin_id) VALUES(?,'relation.added',?,?,?,?,?)`,
			id, person, fmt.Sprintf("%s REQ-%d", kind, other), ts, anchorID, originID)
		return err
	}
	if l.Parent != 0 {
		if err := relation("parent", l.Parent); err != nil {
			return err
		}
	}
	for _, other := range l.Requests {
		if other == l.Parent {
			continue
		}
		if err := relation("derived", other); err != nil {
			return err
		}
	}
	for _, link := range l.Ideas {
		if err := linkIdeaRequestTx(tx, link.ID, id, person, ts, link.Promote); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,anchor_id,origin_id) VALUES(?,'relation.added',?,?,?,?,?)`,
			id, person, fmt.Sprintf("derived IDEA-%d", link.ID), ts, anchorID, originID); err != nil {
			return err
		}
	}
	actor := pain.Actor{AccountID: author, Person: person, GitAnchor: anchor, Origin: origin}
	for _, painID := range l.Pains {
		if _, _, err := insertLinkTx(tx, painID, project, resolvedTarget{Kind: "request", ID: id}, actor, ts); err != nil {
			return err
		}
	}
	for _, threadID := range l.Threads {
		if err := s.linkThreadToRequestTx(tx, l, threadID, id, person, anchor); err != nil {
			return err
		}
	}
	return nil
}

// linkThreadToRequestTx hängt einen Thread als Quelle an den neuen Auftrag und
// prüft dafür dasselbe wie CoordAccess.LinkThread: das Konto darf den Thread
// ändern, der Auftrag gehört zum Raum des Threads und ist für das Konto nicht
// zurückgehalten. Ein Task-Thread (er hat ein Zuhause im Raum) trägt höchstens
// einen Auftrag; hängt er schon an einem anderen, scheitert das Anlegen. Jede
// Ablehnung antwortet wie ein Thread, den es nicht gibt.
func (s *Store) linkThreadToRequestTx(tx *sql.Tx, l requestdomain.Links, threadID, requestID int64, person string, anchor *gitanchor.Anchor) error {
	denied := linkTargetNotFound(fmt.Sprintf("thread %d", threadID))
	if l.Principal == "" {
		return denied
	}
	who := Principal{ID: l.Principal, Label: person}
	if l.WebSession {
		who.TokenKind = WebSessionKind
	}
	acc := CoordAccess{Store: s, Principal: who}
	actor, err := acc.actorTx(tx)
	if err != nil {
		return denied
	}
	if _, err := acc.requireThreadMutationTx(tx, actor, threadID); err != nil {
		if errors.Is(err, ErrCoordForbidden) || errors.Is(err, ErrCoordNotFound) {
			return denied
		}
		return err
	}
	objectID := fmt.Sprintf("REQ-%d", requestID)
	if home, found, err := threadHomeTx(tx, threadID); err != nil {
		return err
	} else if found {
		if err := validateRequestLinkTx(tx, home.RoomKey, objectID); err != nil {
			return denied
		}
		if acc.requestHeldTx(tx, objectID) {
			return denied
		}
		var existing string
		err := tx.QueryRow(`SELECT object_id FROM thread_links WHERE thread_id=? AND object_kind='request' ORDER BY rowid LIMIT 1`, threadID).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && existing != objectID {
			return requestdomain.NewRuleError("thread_linked", "that thread already belongs to another issue", "a task thread links only one issue", nil)
		}
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at) VALUES(?,'request',?,'',?)`,
		threadID, objectID, now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return insertThreadEventTx(tx, threadID, ThreadEventLinked, person, outcomeEventData("request", objectID, "linked"), anchor)
	}
	return nil
}
