package store

import (
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

// Die Idee betrifft ein Feature (REQ-611 Kriterium 13, REQ-612 Kriterium 7): sie
// zeigt in Konzept und Diskussion die aktuelle Fassung des Features (Ist) neben
// dem gewünschten Soll, und die Umsetzung schreibt diese Fassung fort, statt ein
// neues Feature anzulegen. Die Verknüpfung ist eine Zeile in feature_links
// (kind idea, rel affects).

// IdeaFeature ist ein Feature, das eine Idee betrifft, mit seinem Ist-Stand.
type IdeaFeature struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Revision int    `json:"revision"`
	// Advanced: eine Fassung des Features trägt diese Idee, die Beschreibung wurde
	// für sie fortgeschrieben.
	Advanced bool `json:"advanced"`
	// Current ist der Ist-Stand, die jüngste Fassung.
	Current feature.Content `json:"current"`
}

// IdeaAffects liefert die Features, die eine Idee betrifft und die der Betrachter
// lesen darf, mit ihrer aktuellen Fassung.
func (a *ProjectAccess) IdeaAffects(ideaID int64) ([]IdeaFeature, error) {
	i, err := a.ideaFor(ideaID, ActRead)
	if err != nil {
		return nil, err
	}
	ids, err := a.st.featuresAffectedBy(ideaID)
	if err != nil {
		return nil, err
	}
	out := []IdeaFeature{}
	for _, fid := range ids {
		f, err := a.st.featureByID(fid)
		if err != nil || f.Project != i.Project || !a.Allow(f.Project, ResFeature, ActRead, Object{}) {
			continue
		}
		advanced, _, _ := a.st.advancedRefs(fid)
		out = append(out, IdeaFeature{ID: f.ID, Name: f.Name, State: f.State, Revision: f.Revision, Advanced: advanced[ideaID], Current: f.Content})
	}
	return out, nil
}

// attachIdeaFeatures hängt die betroffenen Features in Kurzform an. Ein Fehler
// kostet nur die Auskunft.
func (a *ProjectAccess) attachIdeaFeatures(list []idea.Idea) {
	for n := range list {
		ids, err := a.st.featuresAffectedBy(list[n].ID)
		if err != nil {
			continue
		}
		advanced := map[int64]bool{}
		for _, fid := range ids {
			f, err := a.st.featureByID(fid)
			if err != nil || f.Project != list[n].Project || !a.Allow(f.Project, ResFeature, ActRead, Object{}) {
				continue
			}
			advIdeas, _, _ := a.st.advancedRefs(fid)
			advanced[fid] = advIdeas[list[n].ID]
			list[n].Features = append(list[n].Features, idea.FeatureRef{ID: f.ID, Name: f.Name, Revision: f.Revision, State: f.State, Advanced: advanced[fid]})
		}
	}
}

// LinkIdeaFeature lässt eine Idee ein Feature betreffen („betrifft FEAT-n“). Die
// Idee muss lesbar sein, am Feature gilt das Schreibrecht der Fassungen.
func (a *ProjectAccess) LinkIdeaFeature(ideaID, featureID int64, by string) (idea.Idea, error) {
	if _, err := a.ideaFor(ideaID, ActRead); err != nil {
		return idea.Idea{}, err
	}
	if _, err := a.LinkFeature(featureID, feature.KindIdea, ideaID, feature.RelAffects, by); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(ideaID)
}

// UnlinkIdeaFeature nimmt „betrifft FEAT-n“ zurück.
func (a *ProjectAccess) UnlinkIdeaFeature(ideaID, featureID int64) (idea.Idea, error) {
	if _, err := a.ideaFor(ideaID, ActRead); err != nil {
		return idea.Idea{}, err
	}
	if _, err := a.UnlinkFeature(featureID, feature.KindIdea, ideaID, feature.RelAffects); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(ideaID)
}
