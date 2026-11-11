package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Zugriff auf Labels, Themen und gespeicherte Suchen (REQ-600). Die Regeln:
//
//   - Labels, Themen und Suchen eines Projekts lesen ab Gast, anlegen ab member,
//     ändern und löschen ab lead (ResLabel).
//   - Ein Org-Label lesen alle Mitglieder der Org, anlegen, ändern und löschen
//     nur ein Owner der Org. Wer nicht Mitglied ist, bekommt "nicht gefunden".
//   - Ein Label an ein Objekt setzen oder nehmen darf, wer am Objekt arbeiten
//     darf (ActWork, bei Wissen ActCreate): dieselbe Prüfung wie für das Objekt.
//     Ein Objekt, das es nicht gibt, und eines, das der Aufrufer nicht sehen
//     darf, antworten gleich.
//   - Das Label muss im Projekt des Objekts gelten (Projekt oder Org des
//     Projekts); ein Label eines anderen Projekts verhält sich wie ein
//     unbekannter Name.

// orgGate prüft den Zugriff auf eine Org: lesen als Mitglied, schreiben als Owner.
func (a *ProjectAccess) orgGate(orgID int64, write bool) error {
	a.Filtered()
	if !a.valid {
		return ErrAccessNotFound
	}
	switch a.st.OrgRole(orgID, a.principal.ID) {
	case "owner":
		return nil
	case "member":
		if write {
			return ErrAccessForbidden
		}
		return nil
	}
	return ErrAccessNotFound
}

// ListLabels liefert die Labels, die im Projekt gelten.
func (a *ProjectAccess) ListLabels(project string) ([]Label, error) {
	project = scope.NormalizeRemote(project)
	if err := a.Check(project, ResLabel, ActRead, Object{}); err != nil {
		return nil, err
	}
	return a.st.LabelsAvailable(project)
}

// ListOrgLabels liefert die Labels einer Org für ihre Mitglieder.
func (a *ProjectAccess) ListOrgLabels(orgID int64) ([]Label, error) {
	if err := a.orgGate(orgID, false); err != nil {
		return nil, err
	}
	return a.st.LabelsOfOrg(orgID)
}

// CreateLabel legt ein Label im Projekt (Project gesetzt) oder in der Org
// (OrgID gesetzt) an.
func (a *ProjectAccess) CreateLabel(in LabelInput) (Label, error) {
	if in.OrgID != 0 {
		if err := a.orgGate(in.OrgID, true); err != nil {
			return Label{}, err
		}
	} else if err := a.Check(scope.NormalizeRemote(in.Project), ResLabel, ActCreate, Object{Own: true}); err != nil {
		return Label{}, err
	}
	in.By = a.principal.Label
	return a.st.CreateLabel(in)
}

// labelGate prüft eine Änderung an einem vorhandenen Label.
func (a *ProjectAccess) labelGate(l Label, act Action) error {
	if l.Scope == "org" {
		return a.orgGate(l.OrgID, act != ActRead)
	}
	return a.Check(l.Project, ResLabel, act, Object{})
}

// LabelForEdit lädt ein Label und prüft, dass der Aufrufer es ändern darf.
// Unbekannt und fremd antworten gleich (nicht gefunden).
func (a *ProjectAccess) LabelForEdit(id int64) (Label, error) {
	l, err := a.st.LabelByID(id)
	if err != nil {
		a.Filtered()
		return Label{}, ErrAccessNotFound
	}
	if err := a.labelGate(l, ActRead); err != nil {
		return Label{}, ErrAccessNotFound
	}
	if err := a.labelGate(l, ActEdit); err != nil {
		return Label{}, err
	}
	return l, nil
}

// UpdateLabel ändert Name, Farbe oder Beschreibung eines Labels.
func (a *ProjectAccess) UpdateLabel(id int64, p LabelPatch) (Label, error) {
	if _, err := a.LabelForEdit(id); err != nil {
		return Label{}, err
	}
	return a.st.UpdateLabel(id, p)
}

// DeleteLabel löscht ein Label samt Zuordnungen.
func (a *ProjectAccess) DeleteLabel(id int64) error {
	if _, err := a.LabelForEdit(id); err != nil {
		return err
	}
	return a.st.DeleteLabel(id)
}

// LabelTarget prüft, dass der Aufrufer am Objekt arbeiten darf, und liefert das
// Projekt des Objekts. Alles, was nicht geht und nicht ausdrücklich fehlt der
// Rolle ("forbidden"), ist ErrAccessNotFound.
func (a *ProjectAccess) LabelTarget(kind string, id int64, act Action) (string, error) {
	if !validLabelKind(kind) {
		return "", labelRule("label_kind_invalid", fmt.Sprintf("labels do not apply to %q", kind), "use request, pain, idea or knowledge")
	}
	hidden := func(err error) error {
		if err == nil || err == ErrAccessNotFound {
			return ErrAccessNotFound
		}
		return err
	}
	switch kind {
	case "request":
		ref, err := a.st.RequestRef(RefRequest, id)
		if err != nil {
			a.Filtered()
			return "", ErrAccessNotFound
		}
		obj, visible := a.RequestObject(ref)
		if !visible || ref.Project == "" {
			a.Filtered()
			return "", ErrAccessNotFound
		}
		if err := a.Check(ref.Project, ResRequest, ActRead, obj); err != nil {
			return "", hidden(err)
		}
		if err := a.Check(ref.Project, ResRequest, act, obj); err != nil {
			return "", err
		}
		return ref.Project, nil
	case "pain":
		p, err := a.st.PainByID(id)
		if err != nil {
			a.Filtered()
			return "", ErrAccessNotFound
		}
		if err := a.CheckPain(p, ActRead); err != nil {
			return "", hidden(err)
		}
		if err := a.CheckPain(p, act); err != nil {
			return "", err
		}
		return p.Project, nil
	case "idea":
		i, err := a.st.IdeaByID(id)
		if err != nil {
			a.Filtered()
			return "", ErrAccessNotFound
		}
		if err := a.CheckIdea(i, ActRead); err != nil {
			return "", hidden(err)
		}
		if err := a.CheckIdea(i, act); err != nil {
			return "", err
		}
		return i.Project, nil
	default:
		k, err := a.st.KnowledgeByID(id)
		if err != nil || k.Scope.Project == "" {
			a.Filtered()
			return "", ErrAccessNotFound
		}
		if err := a.CheckKnowledge(k, ActRead); err != nil {
			return "", hidden(err)
		}
		if err := a.CheckKnowledge(k, ActCreate); err != nil {
			return "", err
		}
		return k.Scope.Project, nil
	}
}

// SetLabels setzt oder nimmt Labels (nach Name) an einem Objekt. Alle Namen
// werden zuerst aufgelöst: ein unbekannter ändert nichts.
func (a *ProjectAccess) SetLabels(kind string, id int64, add, remove []string) ([]Label, error) {
	project, err := a.LabelTarget(kind, id, ActWork)
	if err != nil {
		return nil, err
	}
	resolve := func(names []string) ([]Label, error) {
		var out []Label
		for _, n := range names {
			l, err := a.st.LabelNamed(project, n)
			if err != nil {
				return nil, labelRule("label_not_found", fmt.Sprintf("no label %q in this project", strings.TrimSpace(n)), "list the labels of the project to find its name")
			}
			out = append(out, l)
		}
		return out, nil
	}
	adds, err := resolve(add)
	if err != nil {
		return nil, err
	}
	removes, err := resolve(remove)
	if err != nil {
		return nil, err
	}
	for _, l := range adds {
		if err := a.st.AssignLabel(l.ID, kind, id, a.principal.Label); err != nil {
			return nil, err
		}
	}
	for _, l := range removes {
		if err := a.st.UnassignLabel(l.ID, kind, id); err != nil {
			return nil, err
		}
	}
	m, err := a.st.LabelsFor(kind, []int64{id})
	return m[id], err
}

// LabelsOf liefert die Labels eines Objekts für den Betrachter; ohne Recht am
// Objekt (oder wenn es fehlt) ErrAccessNotFound.
func (a *ProjectAccess) LabelsOf(kind string, id int64) ([]Label, error) {
	if _, err := a.LabelTarget(kind, id, ActRead); err != nil {
		return nil, err
	}
	m, err := a.st.LabelsFor(kind, []int64{id})
	return m[id], err
}

// ---- Themen

// ListTopics liefert die Themen eines Projekts.
func (a *ProjectAccess) ListTopics(project string) ([]Topic, error) {
	project = scope.NormalizeRemote(project)
	if err := a.Check(project, ResLabel, ActRead, Object{}); err != nil {
		return nil, err
	}
	return a.st.TopicsOf(project)
}

// SaveTopic legt ein Thema an oder ersetzt das gleichnamige (ab member, ein
// vorhandenes ersetzen ab lead).
func (a *ProjectAccess) SaveTopic(in TopicInput) (Topic, error) {
	in.Project = scope.NormalizeRemote(in.Project)
	if err := a.Check(in.Project, ResLabel, ActCreate, Object{Own: true}); err != nil {
		return Topic{}, err
	}
	if existing, _ := a.st.TopicsOf(in.Project); len(existing) > 0 {
		for _, t := range existing {
			if strings.EqualFold(t.Name, strings.TrimSpace(in.Name)) {
				if err := a.Check(in.Project, ResLabel, ActEdit, Object{}); err != nil {
					return Topic{}, err
				}
			}
		}
	}
	in.By = a.principal.Label
	return a.st.SaveTopic(in)
}

// DeleteTopic löscht ein Thema (ab lead).
func (a *ProjectAccess) DeleteTopic(project, name string) (bool, error) {
	project = scope.NormalizeRemote(project)
	if err := a.Check(project, ResLabel, ActEdit, Object{}); err != nil {
		return false, err
	}
	return a.st.DeleteTopic(project, name)
}

// TopicPrefixes löst ein Thema in seine Präfixe auf. Gebraucht von der Suche,
// die das Lesen des Projekts schon geprüft hat.
func (a *ProjectAccess) TopicPrefixes(project string) func(string) ([]string, bool) {
	list, _ := a.st.TopicsOf(project)
	return func(name string) ([]string, bool) {
		for _, t := range list {
			if strings.EqualFold(t.Name, name) {
				return t.Prefixes, true
			}
		}
		return nil, false
	}
}

// ---- Gespeicherte Suchen

// ListPresets liefert die gespeicherten Suchen für den Aufrufer im Projekt:
// persönliche, die des Projekts und die der Org. Der Zugriff aufs Projekt gilt
// wie beim Lesen von Labels.
func (a *ProjectAccess) ListPresets(project string) ([]SearchPreset, error) {
	project = scope.NormalizeRemote(project)
	if err := a.Check(project, ResLabel, ActRead, Object{}); err != nil {
		return nil, err
	}
	list, err := a.st.PresetsFor(a.account, project)
	if err != nil {
		return nil, err
	}
	// Eine ältere gespeicherte Suche mit dem Namen einer mitgelieferten verdeckt
	// diese nicht (CheckPreset sperrt den Namen seither).
	out := list[:0]
	for _, p := range list {
		if _, taken := query.BuiltinByName(strings.ToLower(p.Name)); !taken {
			out = append(out, p)
		}
	}
	return out, nil
}

// SavePreset speichert eine Suche. Persönliche darf jeder speichern, der das
// Projekt lesen darf; die des Projekts ab member; die der Org ein Owner der Org.
// project ist das Projekt, in dem gespeichert wird (bei Org: eines der Org).
func (a *ProjectAccess) SavePreset(project, scopeKind, name, description string, queries []string) (SearchPreset, error) {
	project = scope.NormalizeRemote(project)
	in := PresetInput{Scope: scopeKind, Name: name, Description: description, Queries: queries, By: a.principal.Label}
	switch scopeKind {
	case "user":
		if err := a.Check(project, ResLabel, ActRead, Object{}); err != nil {
			return SearchPreset{}, err
		}
		if !a.valid {
			return SearchPreset{}, ErrAccessNotFound
		}
		in.Ref = fmt.Sprint(a.account)
	case "project":
		if err := a.Check(project, ResLabel, ActCreate, Object{Own: true}); err != nil {
			return SearchPreset{}, err
		}
		in.Ref = project
		in.MayReplace = a.Check(project, ResLabel, ActEdit, Object{}) == nil
	case "org":
		p, ok := a.st.ProjectByRemote(project)
		if !ok {
			return SearchPreset{}, ErrAccessNotFound
		}
		if err := a.orgGate(p.OrgID, true); err != nil {
			return SearchPreset{}, err
		}
		in.Ref = fmt.Sprint(p.OrgID)
		in.MayReplace = true
	default:
		return SearchPreset{}, labelRule("preset_scope_invalid", fmt.Sprintf("unknown scope %q", scopeKind), "use user, project or org")
	}
	return a.st.SavePreset(in)
}

// DeletePreset löscht eine gespeicherte Suche. Eine persönliche löscht nur ihr
// Besitzer, eine des Projekts ein lead, eine der Org ein Org-Owner. Unbekannt und
// fremd antworten gleich.
func (a *ProjectAccess) DeletePreset(id int64) error {
	p, err := a.st.PresetByID(id)
	if err != nil {
		a.Filtered()
		return ErrAccessNotFound
	}
	switch p.Scope {
	case "user":
		a.Filtered()
		if !a.valid || p.Ref != fmt.Sprint(a.account) {
			return ErrAccessNotFound
		}
	case "project":
		if err := a.Check(p.Ref, ResLabel, ActRead, Object{}); err != nil {
			return ErrAccessNotFound
		}
		if err := a.Check(p.Ref, ResLabel, ActEdit, Object{}); err != nil {
			return err
		}
	case "org":
		var org int64
		fmt.Sscanf(p.Ref, "%d", &org)
		if err := a.orgGate(org, false); err != nil {
			return ErrAccessNotFound
		}
		if err := a.orgGate(org, true); err != nil {
			return err
		}
	}
	return a.st.DeletePreset(id)
}
