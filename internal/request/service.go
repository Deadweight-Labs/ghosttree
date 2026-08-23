package request

import (
	"fmt"
	"strings"
)

func ValidateType(typ string) error {
	switch typ {
	case "feature", "change", "bug", "investigation":
		return nil
	default:
		return NewRuleError("invalid_type", "request type must be feature, change, bug, or investigation", "choose one of the supported request types", nil)
	}
}

func ValidateEvidence(e Evidence) error {
	if strings.TrimSpace(e.Kind) == "" || strings.TrimSpace(e.Ref) == "" {
		return NewRuleError("evidence_required", "evidence kind and reference are required", "provide a commit, test, file, decision, session, or URL", nil)
	}
	switch e.Kind {
	case "commit", "test", "file", "decision", "session", "url":
		return nil
	default:
		return NewRuleError("invalid_evidence_kind", fmt.Sprintf("unsupported evidence kind %q", e.Kind), "use commit, test, file, decision, session, or URL", nil)
	}
}
