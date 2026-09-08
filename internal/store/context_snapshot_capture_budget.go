package store

import (
	"errors"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type snapshotCaptureBudget struct{ remaining int64 }

func (b *snapshotCaptureBudget) Write(p []byte) (int, error) {
	if b.remaining >= 0 {
		if int64(len(p)) > b.remaining {
			return 0, &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
		}
		b.remaining -= int64(len(p))
	}
	return len(p), nil
}

func (b *snapshotCaptureBudget) add(value any, comma bool) error {
	if comma {
		if _, err := b.Write([]byte{','}); err != nil {
			return err
		}
	}
	return snapshotCaptureError(snapshot.WriteCanonical(b, value))
}

func (c *snapshotCollector) entryBudget(domain, key string) (*snapshotCaptureBudget, error) {
	capacity := c.limits.MaxEntryPayloadBytes
	for _, bound := range []struct{ limit, used int64 }{
		{c.limits.MaxSnapshotPayloadBytes, c.payloadBytes},
		{c.limits.MaxSnapshotLogicalBytes, c.logicalBytes + int64(len(domain)) + int64(len(key)) + 32},
	} {
		if bound.limit < 0 {
			continue
		}
		if bound.used > bound.limit {
			return nil, &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
		}
		remaining := bound.limit - bound.used
		if capacity < 0 || remaining < capacity {
			capacity = remaining
		}
	}
	return &snapshotCaptureBudget{remaining: capacity}, nil
}

func snapshotCaptureError(err error) error {
	if err == nil {
		return nil
	}
	var ruleErr *snapshot.RuleError
	if errors.As(err, &ruleErr) {
		return ruleErr
	}
	code := "snapshot_invalid_payload"
	if strings.Contains(err.Error(), "UTF-8") {
		code = "snapshot_invalid_utf8"
	}
	return &snapshot.RuleError{Code: code}
}
