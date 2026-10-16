package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

func taskPath(id int64) string { return "/api/tasks/" + strconv.FormatInt(id, 10) }

// CreateHumanTask asks the human for something. Only where a value lives goes in
// the location, never the value.
func (c *Client) CreateHumanTask(in humantask.CreateInput) (humantask.Task, error) {
	var out humantask.Task
	err := c.do("POST", "/api/tasks", nil, in, &out)
	return out, err
}

func (c *Client) GetHumanTask(id int64) (humantask.Task, error) {
	var out humantask.Task
	err := c.do("GET", taskPath(id), nil, nil, &out)
	return out, err
}

func (c *Client) ListHumanTasks(project, state string, mine bool, requestID int64) ([]humantask.Task, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	if mine {
		q.Set("mine", "1")
	}
	if requestID > 0 {
		q.Set("request_id", strconv.FormatInt(requestID, 10))
	}
	var out []humantask.Task
	err := c.do("GET", "/api/tasks", q, nil, &out)
	return out, err
}

func (c *Client) MarkTasksDone(ids []int64, note string, sessionID int64) ([]humantask.Task, error) {
	var out []humantask.Task
	err := c.do("POST", "/api/tasks/done", nil, map[string]any{"ids": ids, "note": note, "session_id": sessionID}, &out)
	return out, err
}

func (c *Client) ConfirmHumanTask(id, sessionID int64) (humantask.Task, error) {
	var out humantask.Task
	err := c.do("POST", taskPath(id)+"/confirm", nil, map[string]any{"session_id": sessionID}, &out)
	return out, err
}

func (c *Client) ReopenHumanTask(id int64, reason string, sessionID int64) (humantask.Task, error) {
	var out humantask.Task
	err := c.do("POST", taskPath(id)+"/reopen", nil, map[string]any{"reason": reason, "session_id": sessionID}, &out)
	return out, err
}

func (c *Client) DropHumanTask(id int64, reason string, sessionID int64) (humantask.Task, error) {
	var out humantask.Task
	err := c.do("POST", taskPath(id)+"/drop", nil, map[string]any{"reason": reason, "session_id": sessionID}, &out)
	return out, err
}
