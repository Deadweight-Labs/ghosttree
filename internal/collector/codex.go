package collector

import "encoding/json"

type codexLine struct {
	Type    string `json:"type"`
	Payload *struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		SessionID string          `json:"session_id"`
		CWD       string          `json:"cwd"`
	} `json:"payload"`
}

func ParseCodexLine(line []byte) ParsedLine {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil || l.Payload == nil {
		return ParsedLine{}
	}
	if l.Type != "response_item" || l.Payload.Type != "message" {
		return ParsedLine{}
	}
	return ParsedLine{
		Role: l.Payload.Role,
		Text: contentText(l.Payload.Content, "input_text", "output_text", "text"),
	}
}

func CodexSessionMeta(firstLines [][]byte) (externalID, cwd string) {
	for _, line := range firstLines {
		var l codexLine
		if err := json.Unmarshal(line, &l); err != nil || l.Payload == nil {
			continue
		}
		if l.Type == "session_meta" {
			return l.Payload.SessionID, l.Payload.CWD
		}
	}
	return "", ""
}
