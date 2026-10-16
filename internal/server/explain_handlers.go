package server

import (
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// checkExplanation validates the optional "Kurz erklärt" text an agent sends
// with a new object, before anything is written: an object whose explanation
// would be refused is not created half-way. Empty text is fine and means the
// explanation is generated later.
func checkExplanation(w http.ResponseWriter, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", true
	}
	if rule := humantask.CheckText("explanation", text); rule != nil {
		writeCoded(w, http.StatusUnprocessableEntity, rule.ErrorCode, rule.Message+"; "+rule.Resolution)
		return "", false
	}
	clean, err := explain.Clean(text)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "explanation_invalid", "explanation: "+err.Error()+"; write two to four plain sentences or leave it out")
		return "", false
	}
	return clean, true
}

// saveExplanation stores the checked text beside the object that was just
// created. The object exists either way, so a failure here is logged, not
// returned: the explanation is then generated like any missing one.
func (a *api) saveExplanation(kind string, id int64, text string) {
	if text == "" || id <= 0 {
		return
	}
	if err := a.st.SaveAgentExplanation(kind, id, text); err != nil {
		a.logger.Warn("explanation_not_stored", "kind", kind, "id", id, "error", err.Error())
	}
}

// attentionIntent: the intents that ask a person for something.
func attentionIntent(intent string) bool {
	switch intent {
	case store.IntentQuestion, store.IntentApproval, store.IntentBlocker, store.IntentHandoff:
		return true
	}
	return false
}
