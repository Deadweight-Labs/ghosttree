package server

import (
	"errors"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func writeStoreError(w http.ResponseWriter, fallback int, err error) {
	if writeWriterError(w, err, false) {
		return
	}
	writeErr(w, fallback, err.Error())
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
