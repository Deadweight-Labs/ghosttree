package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

type failAfterWriter struct {
	remaining       int
	err             error
	failed          bool
	writesAfterFail int
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.failed {
		w.writesAfterFail++
		return 0, w.err
	}
	if w.remaining == 0 {
		w.failed = true
		return 0, w.err
	}
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		w.failed = true
		return n, w.err
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestCanonicalJSONV1Golden(t *testing.T) {
	got, err := MarshalCanonical(map[string]any{"z": int64(2), "a": "<\n", "n": nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":"<\n","n":null,"z":2}` {
		t.Fatalf("%q", got)
	}
}

func TestWriteCanonicalMatchesMarshalAndStopsAfterWriterError(t *testing.T) {
	value := map[string]any{"z": int64(2), "a": "payload", "n": nil}
	want, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteCanonical(&out, value); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("WriteCanonical=%q, MarshalCanonical=%q", out.Bytes(), want)
	}

	writeErr := errors.New("writer full")
	failing := &failAfterWriter{remaining: 5, err: writeErr}
	if err := WriteCanonical(failing, value); !errors.Is(err, writeErr) {
		t.Fatalf("WriteCanonical error=%v, want %v", err, writeErr)
	}
	if failing.writesAfterFail != 0 {
		t.Fatalf("encoder attempted %d writes after the first writer error", failing.writesAfterFail)
	}
	if _, err := io.WriteString(failing, "x"); !errors.Is(err, writeErr) {
		t.Fatalf("test writer did not preserve failure: %v", err)
	}
	if err := WriteCanonical(shortWriter{}, value); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error=%v, want io.ErrShortWrite", err)
	}
}

func TestCanonicalJSONV1EscapesAndTime(t *testing.T) {
	timestamp := time.Date(2026, time.August, 29, 21, 4, 5, 123_000_000, time.FixedZone("offset", 2*60*60))
	got, err := MarshalCanonical(map[string]any{
		"controls": "\b\t\n\f\r\x00\x1f",
		"literal":  "/<>&\u2028\u2029",
		"time":     timestamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"controls":"\b\t\n\f\r\u0000\u001f","literal":"/<>&  ","time":"2026-08-29T19:04:05.123Z"}`
	if string(got) != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestCanonicalJSONV1SortsUTF8KeysByBytes(t *testing.T) {
	got, err := MarshalCanonical(map[string]any{"é": 3, "z": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1,"z":2,"é":3}` {
		t.Fatalf("%q", got)
	}
}

func TestCanonicalRejectsUnsupportedValuesAndInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, value := range map[string]any{
		"float":          1.5,
		"invalid-key":    map[string]any{invalid: 1},
		"invalid-val":    invalid,
		"non-string-map": map[int]string{1: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MarshalCanonical(value); err == nil {
				t.Fatalf("MarshalCanonical(%#v) succeeded", value)
			}
		})
	}
}

func TestValidateCanonicalAcceptsOnlyExactCanonicalBytes(t *testing.T) {
	valid := [][]byte{
		[]byte(`null`),
		[]byte("{\"a\":[true,false,-2,0,2],\"é\":\"/<>&  \"}"),
	}
	for _, raw := range valid {
		if err := ValidateCanonical(raw); err != nil {
			t.Errorf("ValidateCanonical(%q): %v", raw, err)
		}

	}

	invalid := [][]byte{
		[]byte(`{"n":1.5}`),
		{0xff},
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"b":1,"a":2}`),
		[]byte(`{"a": 1}`),
		[]byte(`{"a":"\u0061"}`),
		[]byte(`{"a":"\/"}`),
		[]byte(`{"a":"\u001B"}`),
		[]byte(`{"a":"\u2028"}`),
		[]byte(`{"n":01}`),
		[]byte(`{"n":-0}`),
		[]byte(`null null`),
		append([]byte{0xef, 0xbb, 0xbf}, []byte(`null`)...),
	}
	for _, raw := range invalid {
		if err := ValidateCanonical(raw); err == nil {
			t.Errorf("ValidateCanonical(%q) succeeded", raw)
		}
	}
}

func TestExportHeadV1HasClosedFieldSet(t *testing.T) {
	typ := reflect.TypeOf(ExportHeadV1{})
	if typ.NumField() != 20 {
		t.Fatalf("ExportHeadV1 has %d fields, want 20", typ.NumField())
	}
	want := []string{
		"project", "name", "schema_version", "content_digest", "git_object_format",
		"git_commit", "git_ref", "git_branch", "git_dirty", "git_worktree_fingerprint_version",
		"git_worktree_fingerprint", "allow_dirty_used", "git_metadata_source", "message", "actor_id",
		"actor_label", "session_ref", "created_at", "entry_count", "payload_bytes_total",
	}
	for i, field := range want {
		if got := typ.Field(i).Tag.Get("json"); got != field {
			t.Errorf("field %d json tag = %q, want %q", i, got, field)
		}
	}
}

func TestEntryPageSeparatesSummariesFromExactPayload(t *testing.T) {
	summaryPage := EntryPage{Entries: []EntrySummary{{Domain: "ghost", Key: "file/a"}}}
	summaryJSON, err := json.Marshal(summaryPage)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(summaryJSON, []byte(`"payload":`)) {
		t.Fatalf("summary page leaked a payload field: %s", summaryJSON)
	}

	exactPage := EntryPage{Exact: &Entry{Domain: "ghost", Key: "file/a", Payload: json.RawMessage(`{"path":"a"}`)}}
	exactJSON, err := json.Marshal(exactPage)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(exactJSON, []byte(`"payload":{"path":"a"}`)) {
		t.Fatalf("exact page omitted raw payload: %s", exactJSON)
	}
}
