package snapshot

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const (
	cursorVersion      byte = 1
	snapshotCursorKind byte = 1
	entryCursorKind    byte = 2
)

func EncodeSnapshotCursor(id int64) (string, error) {
	if id <= 0 {
		return "", fmt.Errorf("snapshot cursor id must be positive")
	}
	var raw [10]byte
	raw[0] = cursorVersion
	raw[1] = snapshotCursorKind
	binary.BigEndian.PutUint64(raw[2:], uint64(id))
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func DecodeSnapshotCursor(cursor string) (int64, error) {
	raw, err := decodeCursor(cursor)
	if err != nil {
		return 0, fmt.Errorf("decode snapshot cursor: %w", err)
	}
	if len(raw) != 10 || raw[0] != cursorVersion || raw[1] != snapshotCursorKind {
		return 0, fmt.Errorf("invalid snapshot cursor")
	}
	id := int64(binary.BigEndian.Uint64(raw[2:]))
	if id <= 0 {
		return 0, fmt.Errorf("snapshot cursor id must be positive")
	}
	return id, nil
}

func EncodeEntryCursor(domain, key string) (string, error) {
	if err := validateEntryCursorTuple(domain, key); err != nil {
		return "", err
	}
	raw := make([]byte, 2+1+len(domain)+2+len(key))
	raw[0] = cursorVersion
	raw[1] = entryCursorKind
	raw[2] = byte(len(domain))
	copy(raw[3:], domain)
	keyLengthOffset := 3 + len(domain)
	binary.BigEndian.PutUint16(raw[keyLengthOffset:], uint16(len(key)))
	copy(raw[keyLengthOffset+2:], key)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeEntryCursor(cursor string) (string, string, error) {
	raw, err := decodeCursor(cursor)
	if err != nil {
		return "", "", fmt.Errorf("decode entry cursor: %w", err)
	}
	if len(raw) < 6 || raw[0] != cursorVersion || raw[1] != entryCursorKind {
		return "", "", fmt.Errorf("invalid entry cursor")
	}
	domainLength := int(raw[2])
	keyLengthOffset := 3 + domainLength
	if domainLength == 0 || keyLengthOffset+2 > len(raw) {
		return "", "", fmt.Errorf("invalid entry cursor length")
	}
	keyLength := int(binary.BigEndian.Uint16(raw[keyLengthOffset:]))
	if keyLengthOffset+2+keyLength != len(raw) {
		return "", "", fmt.Errorf("invalid entry cursor length")
	}
	domain := string(raw[3:keyLengthOffset])
	key := string(raw[keyLengthOffset+2:])
	if err := validateEntryCursorTuple(domain, key); err != nil {
		return "", "", err
	}
	return domain, key, nil
}

func decodeCursor(cursor string) ([]byte, error) {
	if cursor == "" {
		return nil, fmt.Errorf("cursor is empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	if base64.RawURLEncoding.EncodeToString(raw) != cursor {
		return nil, fmt.Errorf("cursor is not canonically encoded")
	}
	return raw, nil
}

func validateEntryCursorTuple(domain, key string) error {
	if len(domain) == 0 || len(domain) > 32 {
		return fmt.Errorf("entry cursor domain must contain 1 to 32 bytes")
	}
	for i, b := range []byte(domain) {
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || (i > 0 && (b == '-' || b == '_' || b == '.'))) {
			return fmt.Errorf("entry cursor domain is not an ASCII identifier")
		}
	}
	if len(key) == 0 || len(key) > 4096 || !utf8.ValidString(key) {
		return fmt.Errorf("entry cursor key must contain 1 to 4096 valid UTF-8 bytes")
	}
	return nil
}
