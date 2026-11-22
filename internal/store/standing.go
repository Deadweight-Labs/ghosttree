package store

import "strings"

// Standing sagt einem Leser, von wem ein Text stammt, der in den Kontext seines
// Agenten geht: Person, Rolle im Projekt und ob der Text verbindlich gelten
// darf. Verbindlich ist er nur, wenn der Autor jetzt das Recht hat, jedes Wissen
// des Projekts zu ändern (der Katalog nennt es: knowledge.edit, ab Lead); alles
// andere ist der Hinweis eines Mitglieds. Der Leser sieht die Rolle von heute,
// nicht die zur Zeit des Schreibens.
type Standing struct {
	// Name ist der heutige Kontoname des Autors, leer ohne bekanntes Konto.
	Name string
	// Role ist die Projektrolle des Autors, leer ohne Rolle (oder ohne Projekt).
	Role string
	// Own: der Autor ist der Leser selbst.
	Own bool
	// Binding: der Text darf als Anweisung gelten.
	Binding bool
}

// Who ist die Kurzform für die Kopfzeile eines Eintrags, "Robin, lead".
func (s Standing) Who() string {
	name := PromptSafeName(s.Name)
	if name == "" {
		name = "unknown author"
	}
	switch {
	case s.Own:
		return name + " (you)"
	case s.Role != "":
		return name + ", " + s.Role
	}
	return name + ", no role in this project"
}

// standingOf bildet den Stand aus Konto und Rolle.
func standingOf(name string, role RoleInfo, own bool) Standing {
	return Standing{Name: name, Role: role.Role, Own: own, Binding: own || role.Has(RightKnowledgeEdit)}
}

// StandingReader löst den Stand von Autoren für einen Leser auf und hält die
// Abfragen, die dabei anfallen, für die Dauer einer Antwort. Er wird je Anfrage
// aus dem ProjectAccess des Lesers gebildet.
type StandingReader struct {
	a           *ProjectAccess
	names       map[int64]string
	roles       map[authorRoleKey]RoleInfo
	rolesByName map[string]map[string]RoleInfo
}

// Standings bildet den Leser für den Stand von Autoren.
func (a *ProjectAccess) Standings() *StandingReader { return &StandingReader{a: a} }

// KnowledgeStanding ist der Stand des Autors eines Wissenseintrags. Ein
// destillierter Eintrag und einer ohne Autorenkonto sind nie verbindlich: ihn
// hat niemand mit einer Rolle geschrieben. Ein Eintrag ohne Projekt (Maschine,
// global) ist verbindlich für seinen Besitzer, also wenn der Leser ihn selbst
// geschrieben hat; für jeden anderen ist er ein Hinweis.
func (r *StandingReader) KnowledgeStanding(k Knowledge) Standing {
	a := r.a
	if k.Origin == "distilled" {
		return Standing{Name: "ghosttree distiller"}
	}
	account := a.entryAuthor(k.ID)
	if account == 0 {
		return Standing{Name: k.Person}
	}
	name := r.accountName(account)
	if k.Scope.Project == "" {
		own := a.valid && account == a.account
		return Standing{Name: name, Own: own, Binding: own}
	}
	own := a.valid && account == a.account
	return standingOf(name, r.authorRole(k.Scope.Project, account), own)
}

// GhostStanding ist der Stand des Autors einer Dateibeschreibung. Eine
// Beschreibung ist nie verbindlich; die Rolle gehört trotzdem in die Zeile.
func (r *StandingReader) GhostStanding(g GhostFile) Standing {
	if g.Harness == GardenerHarness {
		return Standing{Name: "ghosttree gardener"}
	}
	st := r.StandingByName(g.Project, g.Person)
	st.Binding = false
	return st
}

// StandingByName ist der Stand eines Kontos nach seinem Namen im Projekt, für
// Runbooks und andere Texte, die den Autor als Namen führen.
func (r *StandingReader) StandingByName(project, name string) Standing {
	if name == "" {
		return Standing{}
	}
	if r.rolesByName == nil {
		r.rolesByName = map[string]map[string]RoleInfo{}
	}
	roles, ok := r.rolesByName[project]
	if !ok {
		roles = r.a.st.ProjectRolesByName(project)
		r.rolesByName[project] = roles
	}
	return standingOf(name, roles[name], r.a.IsAuthor(name))
}

func (r *StandingReader) accountName(account int64) string {
	if r.names == nil {
		r.names, _ = r.a.st.accountNames()
		if r.names == nil {
			r.names = map[int64]string{}
		}
	}
	return r.names[account]
}

func (r *StandingReader) authorRole(project string, account int64) RoleInfo {
	key := authorRoleKey{project, account}
	if info, ok := r.roles[key]; ok {
		return info
	}
	info := projectRoleTx(r.a.st.db, project, account)
	if r.roles == nil {
		r.roles = map[authorRoleKey]RoleInfo{}
	}
	r.roles[key] = info
	return info
}

type authorRoleKey struct {
	project string
	account int64
}

// ProjectRolesByName nennt zu jedem Konto der Organisation des Projekts die
// Rolle dort (Org-Owner implizit als owner). Dieselbe Ableitung wie
// ProjectRole; Konten ohne Rolle fehlen.
func (s *Store) ProjectRolesByName(remote string) map[string]RoleInfo {
	if s.reader != nil {
		return s.reader.ProjectRolesByName(remote)
	}
	out := map[string]RoleInfo{}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT p.name, om.role, COALESCE(pm.role,''), COALESCE(pm.can_review,0),
			EXISTS(SELECT 1 FROM shared_projects sp WHERE sp.project_id=pr.id)
			AND NOT EXISTS(SELECT 1 FROM org_guests og WHERE og.org_id=pr.org_id AND og.account_id=om.account_id)
			AND p.kind<>'service'
		FROM projects pr
		JOIN org_members om ON om.org_id=pr.org_id
		JOIN persons p ON p.id=om.account_id
		LEFT JOIN project_members pm ON pm.project_id=pr.id AND pm.account_id=om.account_id
		WHERE pr.remote=?`, strings.TrimSpace(remote))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, orgRole, role string
		var review int
		var share bool
		if rows.Scan(&name, &orgRole, &role, &review, &share) == nil {
			if info := deriveRole(orgRole, role, review == 1, share); info.Any() {
				out[name] = info
			}
		}
	}
	return out
}
