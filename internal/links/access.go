package links

import (
	"database/sql"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// AccessReader liest für Peek serverseitig, im Namen eines Betrachters. Jede
// Methode geht durch die Sichtprüfung, die auch die API und die Seiten für
// dieses Objekt anwenden; was der Betrachter nicht lesen darf, ist ein Fehler
// wie ein Objekt, das es nicht gibt. Die Oberfläche braucht dafür keinen
// HTTP-Umweg: links.Lookup(links.New(baseURL, project), links.AccessReader{PA: pa}, refs, project).
type AccessReader struct {
	PA *store.ProjectAccess
}

var (
	_ Reader     = AccessReader{}
	_ PullReader = AccessReader{}
)

func (r AccessReader) GetRequest(id int64) (requestdomain.Detail, error) { return r.PA.RequestRead(id) }

func (r AccessReader) GetHumanTask(id int64) (humantask.Task, error) { return r.PA.GetHumanTask(id) }

func (r AccessReader) GetPain(id int64) (pain.Detail, error) { return r.PA.PainDetailView(id) }

func (r AccessReader) GetIdea(id int64) (idea.Idea, error) { return r.PA.IdeaView(id) }

func (r AccessReader) GetFeature(id int64) (feature.Feature, error) { return r.PA.FeatureView(id) }

func (r AccessReader) GetDecision(id int64) (idea.Decision, error) { return r.PA.DecisionView(id) }

func (r AccessReader) KnowledgeByID(id int64) (store.Knowledge, error) { return r.PA.KnowledgeRead(id) }

func (r AccessReader) PullRequest(project string, n int64) (store.PullRequestInfo, error) {
	p, err := r.PA.PullRequestView(project, n)
	if err != nil {
		return p, sql.ErrNoRows
	}
	return p, nil
}
