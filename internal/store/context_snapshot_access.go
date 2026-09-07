package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type SnapshotAccess struct {
	Read        bool `json:"read"`
	Create      bool `json:"create"`
	ReleaseBind bool `json:"release_bind"`
}

func (s *Store) SetContextSnapshotAccess(person, project string, read, create, releaseBind bool) error {
	person = strings.TrimSpace(person)
	project = scope.NormalizeRemote(project)
	if person == "" {
		return fmt.Errorf("person is required")
	}
	if project == "" {
		return fmt.Errorf("project is required")
	}
	if releaseBind && (!read || !create) {
		return fmt.Errorf("release-bind requires both read and create access")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var personID int64
	if err := tx.QueryRow(`SELECT id FROM persons WHERE name = ?`, person).Scan(&personID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("person %q not found", person)
		}
		return err
	}
	rows, err := tx.Query(`SELECT project FROM context_snapshot_access WHERE person_id=?`, personID)
	if err != nil {
		return err
	}
	var aliases []string
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			rows.Close()
			return err
		}
		if stored != project && scope.NormalizeRemote(stored) == project {
			aliases = append(aliases, stored)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, alias := range aliases {
		if _, err := tx.Exec(`DELETE FROM context_snapshot_access WHERE person_id=? AND project=?`, personID, alias); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO context_snapshot_access(
		person_id, project, can_read, can_create, can_release_bind)
		VALUES(?,?,?,?,?)
		ON CONFLICT(person_id, project) DO UPDATE SET
			can_read=excluded.can_read,
			can_create=excluded.can_create,
			can_release_bind=excluded.can_release_bind
		WHERE can_read IS NOT excluded.can_read
			OR can_create IS NOT excluded.can_create
			OR can_release_bind IS NOT excluded.can_release_bind`,
		personID, project, read, create, releaseBind)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ContextSnapshotAccess(principalID, project string) (SnapshotAccess, error) {
	personID, err := parsePersonPrincipalID(principalID)
	if err != nil {
		return SnapshotAccess{}, err
	}
	project = scope.NormalizeRemote(project)
	if project == "" {
		return SnapshotAccess{}, fmt.Errorf("project is required")
	}
	var access SnapshotAccess
	err = s.db.QueryRow(`SELECT can_read, can_create, can_release_bind
		FROM context_snapshot_access WHERE person_id=? AND project=?`, personID, project).
		Scan(&access.Read, &access.Create, &access.ReleaseBind)
	if err == sql.ErrNoRows {
		return SnapshotAccess{}, nil
	}
	return access, err
}

func (s *Store) PrincipalByName(name string) (Principal, bool) {
	var id int64
	var label string
	err := s.db.QueryRow(`SELECT id, name FROM persons WHERE name=?`, strings.TrimSpace(name)).Scan(&id, &label)
	if err != nil {
		return Principal{}, false
	}
	return Principal{ID: "person:" + strconv.FormatInt(id, 10), Label: label}, true
}

func parsePersonPrincipalID(principalID string) (int64, error) {
	raw, ok := strings.CutPrefix(principalID, "person:")
	if !ok || raw == "" {
		return 0, fmt.Errorf("invalid principal ID %q", principalID)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, fmt.Errorf("invalid principal ID %q", principalID)
	}
	return id, nil
}
