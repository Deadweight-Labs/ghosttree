package server

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// maxToolUseBody bounds a report of ghosttree tool calls; one is a few
// hundred bytes.
const maxToolUseBody = 64 << 10

// recordToolUses takes what a hook or ctx mcp counted for one session
// (REQ-436). Only who may upload the project's sessions may report for it.
func (a *api) recordToolUses(w http.ResponseWriter, r *http.Request) {
	var b store.ToolUseBatch
	if !readLimitedJSON(w, r, &b, maxToolUseBody) {
		return
	}
	b.Project = scope.NormalizeRemote(b.Project)
	if err := b.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.access(r).RecordToolUses(b)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
