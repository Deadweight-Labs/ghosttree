package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// standingTaskRequestKey is the idempotency key of the request that carries the
// tasks an agent asks for without working on a request of its own: one per
// project and person, so nobody's tasks land in somebody else's issue.
func standingTaskRequestKey(project string, account int64, generation int) string {
	key := fmt.Sprintf("agent-tasks:%s:%d", project, account)
	if generation > 1 {
		key += fmt.Sprintf(":%d", generation)
	}
	return key
}

// standingTaskRequest returns the open request an agent's task hangs on when
// the session works on none (REQ-688): the agent no longer has to invent an
// issue just to ask a human for something. The request is made once per
// project and person and stays open; when somebody closed it, the next
// generation takes over. ok is false when the viewer may not create requests
// there or the project is unknown, and the caller reports request_required.
func (a *ProjectAccess) standingTaskRequest(project string, actor humantask.Actor) (int64, bool) {
	if project == "" || !a.valid || a.account == 0 {
		return 0, false
	}
	if err := a.Check(project, ResRequest, ActCreate, Object{Own: true}); err != nil {
		return 0, false
	}
	for generation := 1; generation <= 20; generation++ {
		key := standingTaskRequestKey(project, a.account, generation)
		if id, state, found := a.st.requestByKey(key, project); found {
			if state == "open" {
				return id, true
			}
			continue
		}
		detail, err := a.st.CreateRequest(requestdomain.CreateInput{
			Request: requestdomain.Request{
				Type: "change", Title: "Agent tasks",
				Description: "The tasks agents asked a person for without working on an issue of their own.",
				Scope:       scope.Axes{Project: project}, Origin: "agent", Person: actor.Person,
			},
			IdempotencyKey: key, AuthorID: a.account,
		})
		if err != nil {
			return 0, false
		}
		return detail.Request.ID, true
	}
	return 0, false
}

func (s *Store) requestByKey(key, project string) (id int64, state string, found bool) {
	if s.reader != nil {
		return s.reader.requestByKey(key, project)
	}
	err := s.db.QueryRowContext(s.ctx(), `SELECT id,state FROM requests WHERE idempotency_key=? AND project=?`, key, project).Scan(&id, &state)
	return id, state, err == nil
}

// mentionedAssignee is the person a task is for when it names no assignee but
// its text addresses a member of the project with @name: the first one who is
// not the writer (an agent reaches the person it works for, so this is never
// the agent's own human by accident of the text). Zero when nobody qualifies.
func (a *ProjectAccess) mentionedAssignee(project string, in humantask.CreateInput, actor humantask.Actor) int64 {
	text := strings.TrimSpace(in.Title + "\n" + in.Description)
	if !strings.Contains(text, "@") || !a.valid || a.account == 0 {
		return 0
	}
	reached, _, err := a.st.resolveTextMentions(project, a.account, actor.Person, text)
	if err != nil {
		return 0
	}
	for _, account := range reached {
		if account == a.account {
			continue
		}
		if a.st.ProjectRole(project, principalOfID(account)).Any() {
			return account
		}
	}
	return 0
}
