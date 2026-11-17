package mcpserver

import (
	"errors"
	"net"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// criterionRetries: so oft wiederholt criterion_met oder criterion_waive nach
// einem Timeout, bevor der Fehler an den Agenten geht.
const criterionRetries = 2

// criterionRetryPause lässt einen Server Luft, der gerade hinter einem großen
// Upload steht; ein Test setzt ihn auf null.
var criterionRetryPause = 2 * time.Second

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// setCriterion schreibt den Zustand eines Kriteriums. Ein Timeout sagt nicht, ob
// der Server den Schreibvorgang angenommen hat; im Lauf 3 kam er bei einem Server
// unter Last, gespeichert war er trotzdem (REQ-690). Darum wiederholt es nach einem
// Timeout (der Server nimmt denselben Übergang mit demselben Nachweis zweimal an)
// und fragt, wenn auch das ein Timeout oder „schon erledigt“ ergibt, nach dem
// Zustand. Der Rückgabewert sagt, ob es dazu kam, damit die Antwort es nennt.
func (s *Server) setCriterion(requestID, criterionID int64, state string, evidence requestdomain.Evidence) (retried bool, err error) {
	err = s.client.SetRequestCriterion(criterionID, state, evidence)
	for attempt := 0; err != nil && isTimeout(err) && attempt < criterionRetries; attempt++ {
		retried = true
		time.Sleep(criterionRetryPause)
		if s.criterionHas(requestID, criterionID, state) {
			return true, nil
		}
		err = s.client.SetRequestCriterion(criterionID, state, evidence)
	}
	if err != nil && retried {
		var ae *client.APIError
		if (isTimeout(err) || (errors.As(err, &ae) && ae.Code == "criterion_terminal")) && s.criterionHas(requestID, criterionID, state) {
			return true, nil
		}
	}
	return retried, err
}

// criterionHas fragt den Server, ob das Kriterium schon in diesem Zustand ist.
func (s *Server) criterionHas(requestID, criterionID int64, state string) bool {
	detail, err := s.client.GetRequest(requestID)
	if err != nil {
		return false
	}
	for _, c := range detail.Criteria {
		if c.ID == criterionID {
			return c.State == state
		}
	}
	return false
}
