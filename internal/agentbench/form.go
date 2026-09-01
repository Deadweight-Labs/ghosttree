package agentbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
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

// plain renders the value for a human reading the report. A bare string fills
// both String and Path, so String is read first and the two never disagree.
func (v SlotValue) plain() string {
	switch {
	case v.String != nil:
		return *v.String
	case v.Path != nil:
		return *v.Path
	case v.Integer != nil:
		return strconv.Itoa(*v.Integer)
	case v.Boolean != nil:
		return strconv.FormatBool(*v.Boolean)
	}
	return ""
}

// UnmarshalJSON accepts the bare value as well as the typed wrapper. The
// wrapper `{"still_open": {"boolean": true}}` is the documented form; agents
// routinely answer `{"still_open": true}`, which is the more natural JSON and
// says exactly the same thing.
//
// The first pilot rejected 8 of 48 runs over this — and all six runs of the
// ledger task, the category the whole comparison is most interested in. Every
// arm was hit equally, so the contrast was not skewed; the evidence was simply
// thrown away. A response form is a means of reading the answer, not a test of
// whether the agent guessed the harness's nesting.
//
// A bare value carries no type label, so it is placed by its JSON type: a
// string fills both String and Path and the slot's own type decides which one
// is read.
func (v *SlotValue) UnmarshalJSON(data []byte) error {
	type wrapper SlotValue
	var typed wrapper
	if err := json.Unmarshal(data, &typed); err == nil {
		*v = SlotValue(typed)
		return nil
	}

	var bare any
	if err := json.Unmarshal(data, &bare); err != nil {
		return err
	}
	switch value := bare.(type) {
	case string:
		v.String, v.Path = &value, &value
	case bool:
		v.Boolean = &value
	case float64:
		// JSON kennt nur eine Zahl. Ein Slot vom Typ integer erwartet eine
		// ganze; alles andere bleibt unbesetzt und zaehlt als Enthaltung.
		if value == math.Trunc(value) {
			n := int(value)
			v.Integer = &n
		}
	case nil:
		// Ausdrueckliches null ist eine Enthaltung, kein Fehler.
	default:
		return fmt.Errorf("slot value %s is neither the typed form nor a plain value", data)
	}
	return nil
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
