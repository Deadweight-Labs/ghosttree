package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The operator's services (explanations, the distillation of sessions, the
// vectors of the search) send the texts of an organization to the language
// model the operator configured. orgs.external_models says whether they may do
// that for this organization. It is 1 for every organization that existed
// before the switch and for the organizations of the instance owner, and 0 for
// every organization someone else makes: the owner of such an organization
// decides.

// ErrNotOrgOwnerModels: the setting belongs to the owner of the organization.
var ErrNotOrgOwnerModels = ErrNotOrgOwner

// setExternalModelsDefaultTx sets the value of a new organization: on for what
// the instance owner makes, off for everyone else.
func setExternalModelsDefaultTx(tx execQueryer, orgID, creator int64) error {
	on := 0
	if owner := instanceOwnerID(tx); owner != 0 && owner == creator {
		on = 1
	}
	_, err := tx.Exec(`UPDATE orgs SET external_models=? WHERE id=?`, on, orgID)
	return err
}

// externalModelsOKSQL is a condition for a query on a table that has a project
// column (projectCol, with its alias): the project does not belong to an
// organization that keeps its texts from the operator's models. An object
// without a project, or of a project nobody claimed, is not in any organization
// and passes.
func externalModelsOKSQL(projectCol string) string {
	return `NOT EXISTS (SELECT 1 FROM projects xp JOIN orgs xo ON xo.id = xp.org_id WHERE xp.remote = ` + projectCol + ` AND xo.external_models = 0)`
}

// ExternalModelsFor says whether the operator's models may read the texts of
// the organization.
func (s *Store) ExternalModelsFor(orgID int64) bool {
	if s.reader != nil {
		return s.reader.ExternalModelsFor(orgID)
	}
	var on int
	if err := s.db.QueryRow(`SELECT external_models FROM orgs WHERE id=?`, orgID).Scan(&on); err != nil {
		return false
	}
	return on == 1
}

// ProjectAllowsExternalModels says whether the operator's models may read the
// texts of a project, by the organization it belongs to.
func (s *Store) ProjectAllowsExternalModels(project string) bool {
	if s.reader != nil {
		return s.reader.ProjectAllowsExternalModels(project)
	}
	project = scope.NormalizeRemote(project)
	if project == "" {
		return true
	}
	var off int
	// A failed read says no: a text is not sent when it is not known whether
	// the organization agreed.
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects xp JOIN orgs xo ON xo.id = xp.org_id WHERE xp.remote = ? AND xo.external_models = 0`, project).Scan(&off); err != nil {
		return false
	}
	return off == 0
}

// SetExternalModels changes the setting; only an owner of the organization may.
// Switching it off stops what is sent from now on; what a provider received
// before stays with the provider.
func (s *Store) SetExternalModels(actorPrincipal string, orgID int64, on bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, on}, func(d *Store, p []any) error {
			return d.SetExternalModels(p[0].(string), p[1].(int64), p[2].(bool))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !orgCanTx(tx, orgID, actor, RightOrgAI) {
		return ErrNotOrgOwnerModels
	}
	var was int
	if err := tx.QueryRow(`SELECT external_models FROM orgs WHERE id=?`, orgID).Scan(&was); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrgNotFound
		}
		return err
	}
	value := 0
	if on {
		value = 1
	}
	if was == value {
		return nil
	}
	if _, err := tx.Exec(`UPDATE orgs SET external_models=? WHERE id=?`, value, orgID); err != nil {
		return err
	}
	if err := orgEvent(tx, orgID, "external_models", principalOfID(actor), strings.ToLower(boolWord(on)), ""); err != nil {
		return err
	}
	return tx.Commit()
}

func boolWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
