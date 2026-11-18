package server

import (
	"errors"
	"net/http"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func writeStoreError(w http.ResponseWriter, fallback int, err error) {
	if writeSecretRefusal(w, err) || writeWriterError(w, err, false) {
		return
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeBodyTooLarge(w, tooBig.Limit)
		return
	}
	if fallback >= http.StatusInternalServerError {
		writeInternalError(w, fallback, err)
		return
	}
	writeErr(w, fallback, err.Error())
}

// writeInternalError antwortet auf einen Fehler des Servers mit einer festen
// Meldung und der Nummer der Anfrage. Der Text des Fehlers (SQL, Pfade,
// Constraint-Namen) gehört ins Protokoll, das unter derselben Nummer steht,
// nicht zum Client.
func writeInternalError(w http.ResponseWriter, status int, err error) {
	id := w.Header().Get("X-Request-ID")
	recordResponseError(w, classifyRequestError(status, "", err.Error()), err.Error())
	msg := "internal error"
	if id != "" {
		msg += "; quote request id " + id + " to the operator"
	}
	writeJSON(w, status, map[string]string{"error": msg, "message": msg, "code": "internal_error", "request_id": id})
}

// writeSecretRefusal answers a write the store refused because the text carries
// a secret (PAIN-17) or is longer than a field may be (text_too_long): 422 with the code secret_detected and a message that says
// where and what to do, never the value. The message holds the instruction too,
// because the MCP tools show an agent the message and not the resolution.
func writeSecretRefusal(w http.ResponseWriter, err error) bool {
	var rule *requestdomain.RuleError
	if !errors.As(err, &rule) || (rule.ErrorCode != "secret_detected" && rule.ErrorCode != "text_too_long") {
		return false
	}
	writeCoded(w, http.StatusUnprocessableEntity, rule.ErrorCode, rule.Message)
	return true
}

func writeWriterError(w http.ResponseWriter, err error, snapshotOperation bool) bool {
	code, message, status, retryable := "", "", 0, false
	switch {
	case errors.Is(err, store.ErrWriterOperationsFull), errors.Is(err, store.ErrWriterBytesFull):
		code, message, status, retryable = "writer_busy", "store writer is busy; retry after 1 second", http.StatusServiceUnavailable, true
	case errors.Is(err, store.ErrWriterClosed):
		code, message, status, retryable = "writer_closed", "store writer is closed; retry after 1 second", http.StatusServiceUnavailable, true
	case errors.Is(err, store.ErrWriterOversized):
		code, message, status = "writer_payload_too_large", "operation exceeds the writer byte limit", http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrWriterInvalidPayload):
		code, message, status = "writer_invalid_payload", "operation payload cannot be admitted", http.StatusBadRequest
	default:
		return false
	}
	if retryable {
		w.Header().Set("Retry-After", "1")
		recordResponseError(w, "writer_busy", message)
	}
	if snapshotOperation {
		switch {
		case retryable:
			code = "snapshot_store_busy"
		case status == http.StatusRequestEntityTooLarge:
			code = "snapshot_limit_exceeded"
			status = http.StatusUnprocessableEntity
		default:
			code = "snapshot_invalid_input"
		}
		writeSnapshotRuleError(w, status, &snapshot.RuleError{Code: code, Message: message, Retryable: retryable})
	} else {
		recordResponseError(w, classifyRequestError(status, "", message), message)
		writeJSON(w, status, struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		}{code, message, retryable})
	}
	return true
}
