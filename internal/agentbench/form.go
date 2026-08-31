package agentbench

import (
	"encoding/json"
	"errors"
	"strings"
)

const formFence = "```agentbench-form"

type SlotValue struct {
	Path    *string `json:"path,omitempty"`
	String  *string `json:"string,omitempty"`
	Integer *int    `json:"integer,omitempty"`
	Boolean *bool   `json:"boolean,omitempty"`
}

func (v SlotValue) IsAbstention() bool {
	return v.Path == nil && v.String == nil && v.Integer == nil && v.Boolean == nil
}

type Evidence struct {
	Path  string `json:"path"`
	Lines string `json:"lines"`
}

type ResponseForm struct {
	Slots       map[string]SlotValue `json:"slots"`
	Evidence    []Evidence           `json:"evidence,omitempty"`
	Explanation string               `json:"explanation,omitempty"`
}

var ErrNoForm = errors.New("agent output contains no complete agentbench-form block")

func ParseForm(agentOutput string) (ResponseForm, error) {
	start := strings.LastIndex(agentOutput, formFence)
	if start < 0 {
		return ResponseForm{}, ErrNoForm
	}
	rest := agentOutput[start+len(formFence):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return ResponseForm{}, ErrNoForm
	}
	var form ResponseForm
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[:end])), &form); err != nil {
		return ResponseForm{}, err
	}
	if form.Slots == nil {
		form.Slots = map[string]SlotValue{}
	}
	return form, nil
}
