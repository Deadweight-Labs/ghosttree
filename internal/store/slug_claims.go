package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// A GitHub owner verified by GitHub (a person's linked login with github_id >
// 0, an organization's connected GitHub source) has precedence over an
// unverified namespace of the same name (Robin's decision E10). When the
// login is linked or the source connected, a namespace that holds that slug
// without the verification yields: it gets "<slug>-<n>", its log records
// it, and its owners find a notice. Its old address does not redirect, since
// the slug now belongs to the verified owner; former slugs (slug history)
// reserve nothing against a verified owner. The other way round, no
// namespace takes a slug a verified login owns.

const slugNoticeSchema = `
CREATE TABLE IF NOT EXISTS namespace_notices(
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL,
  org_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  old_slug TEXT NOT NULL,
  new_slug TEXT NOT NULL,
  created_at TEXT NOT NULL,
  seen_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS namespace_notices_account ON namespace_notices(account_id, seen_at);
`

// slugReservedTx: a verified GitHub owner other than the namespace org holds
// the slug: a person's verified login (unless org is that person's own
// namespace) or the login of another organization's connected GitHub source.
func slugReservedTx(q rowQuerier, slug string, org int64) bool {
	var n int
	_ = q.QueryRow(`SELECT
		(SELECT COUNT(*) FROM account_github ag WHERE ag.github_id>0 AND lower(ag.login)=?
			AND NOT EXISTS(SELECT 1 FROM orgs o WHERE o.kind='user' AND o.account_id=ag.account_id AND o.id=?))
		+ (SELECT COUNT(*) FROM git_sources gs WHERE gs.kind=? AND lower(gs.login)=? AND gs.org_id<>?)`,
		slug, org, SourceGitHub, slug, org).Scan(&n)
	return n > 0
}

// slugHeldTx: a namespace has the slug, or had it (slug history).
func slugHeldTx(q rowQuerier, slug string) bool {
	var n int
	_ = q.QueryRow(`SELECT (SELECT COUNT(*) FROM orgs WHERE slug=?) + (SELECT COUNT(*) FROM org_slug_history WHERE old_slug=?)`, slug, slug).Scan(&n)
	return n > 0
}

// slugReservedForOthersTx: a verified owner other than the account holds the
// slug.
func slugReservedForOthersTx(q rowQuerier, slug string, account int64) bool {
	var n int
	_ = q.QueryRow(`SELECT (SELECT COUNT(*) FROM account_github WHERE github_id>0 AND lower(login)=? AND account_id<>?)
		+ (SELECT COUNT(*) FROM git_sources WHERE kind=? AND lower(login)=?)`, slug, account, SourceGitHub, slug).Scan(&n)
	return n > 0
}

// verifiedForSlugTx: the namespace is the verified owner of its slug.
func verifiedForSlugTx(q rowQuerier, o Org) bool {
	var n int
	if o.IsUser() {
		_ = q.QueryRow(`SELECT COUNT(*) FROM account_github WHERE account_id=? AND github_id>0 AND lower(login)=?`, o.AccountID, o.Slug).Scan(&n)
	} else {
		_ = q.QueryRow(`SELECT COUNT(*) FROM git_sources WHERE org_id=? AND kind=? AND lower(login)=?`, o.ID, SourceGitHub, o.Slug).Scan(&n)
	}
	return n > 0
}

// yieldSlugTx frees slug for a verified owner (keep: its namespace, 0 when it
// has none yet): an unverified namespace holding it moves to "<slug>-<n>"
// without a redirect, and no former slug reserves it any more. A verified
// holder keeps it; ok is false then.
func yieldSlugTx(tx *sql.Tx, slug string, keep int64, by string) (ok bool, err error) {
	if !slugPattern.MatchString(slug) {
		return false, nil
	}
	var holder Org
	switch err := scanOrg(tx.QueryRow(`SELECT `+orgCols+` FROM orgs WHERE slug=?`, slug), &holder); {
	case err == sql.ErrNoRows:
	case err != nil:
		return false, err
	case holder.ID == keep:
	case verifiedForSlugTx(tx, holder):
		return false, nil
	default:
		next := ""
		for i := 2; i < 1000; i++ {
			c := fmt.Sprintf("%s-%d", slug, i)
			if slugPattern.MatchString(c) && !slugTakenTx(tx, c, holder.ID) {
				next = c
				break
			}
		}
		if next == "" {
			return false, fmt.Errorf("%w: no free address for %s", ErrOrgSlugTaken, slug)
		}
		if _, err := tx.Exec(`UPDATE orgs SET slug=? WHERE id=?`, next, holder.ID); err != nil {
			return false, err
		}
		// The named projects carry the address in their id.
		rows, err := tx.Query(`SELECT p.remote FROM projects p JOIN named_projects n ON n.project_id=p.id
			WHERE p.org_id=? AND substr(p.remote, 1, ?) = ?`, holder.ID, len(slug)+1, slug+"/")
		if err != nil {
			return false, err
		}
		var named []string
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil {
				named = append(named, r)
			}
		}
		rows.Close()
		for _, r := range named {
			if err := renameProjectTx(tx, r, next+r[len(slug):]); err != nil {
				return false, fmt.Errorf("named project %s: %w", r, err)
			}
		}
		if err := orgEvent(tx, holder.ID, "slug_yielded", by, next, slug+" -> "+next+": the address belongs to its verified GitHub owner"); err != nil {
			return false, err
		}
		owners := []int64{holder.AccountID}
		if !holder.IsUser() {
			owners = nil
			r2, err := tx.Query(`SELECT account_id FROM org_members WHERE org_id=? AND role='owner'`, holder.ID)
			if err != nil {
				return false, err
			}
			for r2.Next() {
				var id int64
				if r2.Scan(&id) == nil {
					owners = append(owners, id)
				}
			}
			r2.Close()
		}
		for _, a := range owners {
			if _, err := tx.Exec(`INSERT INTO namespace_notices(account_id, org_id, kind, old_slug, new_slug, created_at) VALUES(?,?,'slug_yielded',?,?,?)`,
				a, holder.ID, slug, next, now()); err != nil {
				return false, err
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM org_slug_history WHERE old_slug=? AND org_id<>?`, slug, keep); err != nil {
		return false, err
	}
	return true, nil
}

// takeVerifiedLoginTx runs when a person's GitHub login is verified: their
// namespace takes the login as its address, a namespace that held it without
// verification yields.
func takeVerifiedLoginTx(tx *sql.Tx, account int64, login string) error {
	slug := strings.ToLower(strings.TrimSpace(login))
	ns, has := userNamespaceTx(tx, account)
	keep := int64(0)
	if has {
		keep = ns.ID
	}
	ok, err := yieldSlugTx(tx, slug, keep, principalOfID(account))
	if err != nil || !ok || !has || ns.Slug == slug {
		return err
	}
	if _, err := tx.Exec(`UPDATE orgs SET slug=? WHERE id=?`, slug, ns.ID); err != nil {
		return err
	}
	return moveSlugTx(tx, ns.ID, ns.Slug, slug)
}

// NamespaceNotice tells an owner that their namespace moved.
type NamespaceNotice struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	OldSlug string `json:"old_slug"`
	NewSlug string `json:"new_slug"`
	At      string `json:"created_at"`
}

// NamespaceNotices are the owner's notices not seen yet.
func (s *Store) NamespaceNotices(accountPrincipal string) ([]NamespaceNotice, error) {
	if s.reader != nil {
		return s.reader.NamespaceNotices(accountPrincipal)
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, kind, old_slug, new_slug, created_at FROM namespace_notices WHERE account_id=? AND seen_at='' ORDER BY id LIMIT ?`, id, maxListRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NamespaceNotice
	for rows.Next() {
		var n NamespaceNotice
		if err := rows.Scan(&n.ID, &n.Kind, &n.OldSlug, &n.NewSlug, &n.At); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNamespaceNoticesSeen marks the owner's notices as seen.
func (s *Store) MarkNamespaceNoticesSeen(accountPrincipal string) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountPrincipal}, func(d *Store, p []any) error {
			return d.MarkNamespaceNoticesSeen(p[0].(string))
		})
	}
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE namespace_notices SET seen_at=? WHERE account_id=? AND seen_at=''`, now(), id)
	return err
}
