package snapshot

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestLegacyExportPreservesHistoricalDigestAndIntegrityScope(t *testing.T) {
	goldens := map[uint32]string{
		1: "bc6bc7355dec14d08ae42a79730e1482ba3fca668aa9d085731e9545742e3bbf",
		2: "9dc56efbb6df363e56844fbc72090cf61b16850417bbe261d302728d2dc287ee",
	}
	for schema, golden := range goldens {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			entry := Entry{Domain: "ghost", Key: "file/a", Payload: []byte(`{}`), PayloadSize: 2, PayloadDigest: EntryDigest([]byte(`{}`))}
			head := Head{Project: "p", Name: "legacy", SchemaVersion: schema, State: "sealed", GitObjectFormat: "sha1", GitCommit: strings.Repeat("a", 40), GitMetadataSource: "client-reported", ActorID: "actor", CreatedAt: "2026-08-29T00:00:00Z", EntryCount: 1, PayloadBytesTotal: 2}
			raw, err := hex.DecodeString(golden)
			if err != nil {
				t.Fatal(err)
			}
			copy(head.ContentDigest[:], raw)
			counts := map[string]int64{"document": 0, "ghost": 1, "ghost-review": 0, "knowledge": 0, "request": 0}
			var out bytes.Buffer
			if err := WriteExport(&out, head, counts, []Entry{entry}, nil); err != nil {
				t.Fatal(err)
			}
			for _, version := range []uint32{1, ExportVersion} {
				raw := bytes.Replace(out.Bytes(), []byte(fmt.Sprintf(`"export_version":%d`, ExportVersion)), []byte(fmt.Sprintf(`"export_version":%d`, version)), 1)
				verification, err := VerifyExport(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				if !verification.Full || verification.HeadBound || verification.Digest == nil || verification.Digest.String() != golden {
					t.Fatalf("verification=%+v", verification)
				}
				corrupt := bytes.Replace(raw, []byte(`"payload":{}`), []byte(`"payload":{"x":1}`), 1)
				if _, err := VerifyExport(bytes.NewReader(corrupt)); err == nil {
					t.Fatal("legacy payload corruption accepted")
				}
			}
		})
	}
}

func TestCurrentExportReportsHeadBoundOnlyForFullVerification(t *testing.T) {
	entries := exportFixtureEntries(t)
	head := exportFixtureHead(entries)
	counts := exportFixtureCounts()
	for _, filter := range []*ExportFilter{nil, {Domain: "knowledge"}} {
		var out bytes.Buffer
		if err := WriteExport(&out, head, counts, entries, filter); err != nil {
			t.Fatal(err)
		}
		got, err := VerifyExport(bytes.NewReader(out.Bytes()))
		if err != nil || got.HeadBound != (filter == nil) {
			t.Fatalf("verification=%+v err=%v", got, err)
		}
		oldEnvelope := bytes.Replace(out.Bytes(), []byte(`"export_version":2`), []byte(`"export_version":1`), 1)
		if _, err := VerifyExport(bytes.NewReader(oldEnvelope)); err == nil {
			t.Fatal("current schema accepted in historical export format")
		}
	}
}
