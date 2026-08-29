package snapshot

import "testing"

func TestSnapshotCursorRoundTripAndOrderingValue(t *testing.T) {
	cursor, err := EncodeSnapshotCursor(42)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "" || cursor == "42" {
		t.Fatalf("cursor is not opaque: %q", cursor)
	}
	got, err := DecodeSnapshotCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("decoded id = %d, want 42", got)
	}
}

func TestSnapshotCursorRejectsZeroNegativeMalformedAndWrongVersion(t *testing.T) {
	if _, err := EncodeSnapshotCursor(0); err == nil {
		t.Fatal("zero id accepted")
	}
	if _, err := EncodeSnapshotCursor(-1); err == nil {
		t.Fatal("negative id accepted")
	}
	for _, cursor := range []string{"", "%%%", "AQ", "AgAAAAAAAAAq", "Af__________"} {
		if _, err := DecodeSnapshotCursor(cursor); err == nil {
			t.Errorf("DecodeSnapshotCursor(%q) succeeded", cursor)
		}
	}
}

func TestEntryCursorRoundTrip(t *testing.T) {
	cursor, err := EncodeEntryCursor("ghost", "file/café.md")
	if err != nil {
		t.Fatal(err)
	}
	domain, key, err := DecodeEntryCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if domain != "ghost" || key != "file/café.md" {
		t.Fatalf("decoded (%q, %q)", domain, key)
	}
}

func TestEntryCursorRejectsInvalidOrNonCanonicalValues(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	for _, tuple := range [][2]string{{"", "key"}, {"ghost", ""}, {"gh ost", "key"}, {"ghost", invalidUTF8}} {
		if _, err := EncodeEntryCursor(tuple[0], tuple[1]); err == nil {
			t.Errorf("EncodeEntryCursor(%q, %q) succeeded", tuple[0], tuple[1])
		}
	}
	valid, err := EncodeEntryCursor("ghost", "key")
	if err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{"", "%%%", valid[:len(valid)-2], "AgVnaG9zdAADa2V5"} {
		if _, _, err := DecodeEntryCursor(cursor); err == nil {
			t.Errorf("DecodeEntryCursor(%q) succeeded", cursor)
		}
	}
}

func TestCursorCodecsRejectTheOtherCursorKind(t *testing.T) {
	entryCursor, err := EncodeEntryCursor("a", "bcde")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshotCursor(entryCursor); err == nil {
		t.Fatal("snapshot decoder accepted an entry cursor")
	}

	snapshotCursor, err := EncodeSnapshotCursor(0x0161010462636465)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeEntryCursor(snapshotCursor); err == nil {
		t.Fatal("entry decoder accepted a snapshot cursor")
	}
}
