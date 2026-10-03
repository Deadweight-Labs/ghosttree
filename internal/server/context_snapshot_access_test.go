package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func snapshotBody(project, name string) snapshot.CreateInput {
	return snapshot.CreateInput{Project: project, Name: name, Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("a", 40)}}
}

func snapURL(name, project, tail string) string {
	return "/api/context-snapshots/" + url.PathEscape(name) + tail + "?project=" + url.QueryEscape(project)
}

func TestContextSnapshotsFollowProjectRoles(t *testing.T) {
	f := accessAPI(t, true)
	if _, err := f.st.AddAccount("root", "", true); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.tok["root"], _, err = f.st.CreateToken("root", store.TokenSpec{Label: "t"}); err != nil {
		t.Fatal(err)
	}
	// Missing snapshot, as an allowed member sees it: the reference answer.
	_, notFound := f.call(t, "mia", "GET", snapURL("nope", accProject, ""), nil)
	if !strings.Contains(notFound, "snapshot_not_found") {
		t.Fatalf("reference answer: %s", notFound)
	}

	for _, who := range []string{"mia", "rex", "lena", "robin", "root"} {
		f.expect(t, who, 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "by-"+who))
		f.expect(t, who, 200, "GET", snapURL("by-"+who, accProject, ""), nil)
		f.expect(t, who, 200, "GET", "/api/context-snapshots?project="+accProject, nil)
		f.expect(t, who, 200, "GET", snapURL("by-"+who, accProject, "/entries"), nil)
	}

	// Guest and stranger: every shape answers exactly like a missing snapshot.
	for _, who := range []string{"gus", "nora"} {
		code, out := f.call(t, who, "POST", "/api/context-snapshots", snapshotBody(accProject, "by-"+who))
		if code != 404 || out != notFound {
			t.Errorf("%s create = %d %q, want 404 %q", who, code, out, notFound)
		}
		for _, p := range []string{
			snapURL("by-mia", accProject, ""),
			snapURL("by-mia", accProject, "/entries") + "&domain=knowledge&key=" + fmt.Sprint(f.id["k-mia"]),
			snapURL("by-mia", accProject, "/entries"),
		} {
			if code, out := f.call(t, who, "GET", p, nil); code != 404 || out != notFound {
				t.Errorf("%s GET %s = %d %q, want 404 %q", who, p, code, out, notFound)
			}
		}
		if code, out := f.call(t, who, "GET", "/api/context-snapshots?project="+accProject, nil); code != 404 || out != notFound {
			t.Errorf("%s list = %d %q", who, code, out)
		}
		// Same answer for a project that does not exist at all.
		if code, out := f.call(t, who, "GET", "/api/context-snapshots?project=github.com/dw/none", nil); code != 404 || out != notFound {
			t.Errorf("%s list of unknown project = %d %q", who, code, out)
		}
		code, out = f.call(t, who, "POST", "/api/context-snapshots", snapshotBody("github.com/dw/none", "x"))
		if code != 404 || out != notFound {
			t.Errorf("%s create in unknown project = %d %q", who, code, out)
		}
	}
	// Nothing was created by the denied calls.
	if _, out := f.call(t, "robin", "GET", "/api/context-snapshots?project="+accProject, nil); strings.Contains(out, "by-gus") || strings.Contains(out, "by-nora") {
		t.Errorf("denied create left a snapshot: %s", out)
	}
	// A member of one project gets nothing from another.
	f.expect(t, "mia", 404, "GET", "/api/context-snapshots?project="+accOther, nil)
	f.expect(t, "mia", 404, "POST", "/api/context-snapshots", snapshotBody(accOther, "x"))
}

func TestContextSnapshotNeverHoldsMachineKnowledge(t *testing.T) {
	f := accessAPI(t, true)
	key := fmt.Sprint(f.id["k-machine"])
	other := fmt.Sprint(f.id["k-mia"])
	// Ersteller ist die Besitzerin der Maschine: trotzdem kein Maschinen-Wissen.
	f.expect(t, "mia", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "by-owner"))
	f.expect(t, "lena", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "by-lead"))
	for _, name := range []string{"by-owner", "by-lead"} {
		for _, who := range []string{"mia", "lena", "rex", "robin"} {
			f.expect(t, who, 404, "GET", snapURL(name, accProject, "/entries")+"&domain=knowledge&key="+key, nil)
			f.expect(t, who, 200, "GET", snapURL(name, accProject, "/entries")+"&domain=knowledge&key="+other, nil)
		}
	}
	// Gleicher Inhalt, egal wer ihn anlegt (Kopf und Digest tragen Namen,
	// Ersteller und Zeit, die Einträge nicht).
	_, a := f.call(t, "mia", "GET", snapURL("by-owner", accProject, "/entries"), nil)
	_, b := f.call(t, "mia", "GET", snapURL("by-lead", accProject, "/entries"), nil)
	if a != b {
		t.Fatalf("entries depend on the creator:\n%s\n%s", a, b)
	}
}

// Altbestand: ein im Log-Modus vor der Regel angelegter Snapshot enthält
// Maschinen-Wissen. Wird die Durchsetzung später eingeschaltet, darf kein
// Mitglied es über irgendeinen Lesepfad bekommen.
func TestContextSnapshotLegacyMachineEntriesAreHiddenOnRead(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "mia", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "old"))
	db := f.st.DB()
	for _, stmt := range []string{`DROP TRIGGER IF EXISTS context_snapshot_entry_insert`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	payload := `{"id":9999,"machine":"mia-box","title":"legacy secret","body":"x"}`
	if _, err := db.Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) SELECT id,'knowledge','9999',?,zeroblob(32),? FROM context_snapshots WHERE name='old'`, []byte(payload), len(payload)); err != nil {
		t.Fatal(err)
	}
	// Der Kopf des Altbestands zählt den Eintrag mit.
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS context_snapshot_head_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE context_snapshots SET entry_count=entry_count+1, payload_bytes_total=payload_bytes_total+?, counts_json=CAST(json_set(CAST(counts_json AS TEXT),'$.knowledge',COALESCE(json_extract(CAST(counts_json AS TEXT),'$.knowledge'),0)+1) AS BLOB) WHERE name='old'`, len(payload)); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"mia", "lena", "rex", "robin"} {
		f.expect(t, who, 404, "GET", snapURL("old", accProject, "/entries")+"&domain=knowledge&key=9999", nil)
		if _, out := f.call(t, who, "GET", snapURL("old", accProject, "/entries")+"&domain=knowledge", nil); strings.Contains(out, "9999") || strings.Contains(out, "legacy secret") {
			t.Errorf("%s list shows legacy machine entry: %s", who, out)
		}
		if _, out := f.call(t, who, "GET", snapURL("old", accProject, "/entries"), nil); strings.Contains(out, "9999") {
			t.Errorf("%s unfiltered list shows legacy machine entry: %s", who, out)
		}
	}
	// Export und Mirror laufen über denselben Lesepfad. Verify prüft gegen den
	// versiegelten Kopf: ohne die gefilterten Einträge schlägt es fehl
	// (Integritätsfehler), statt den Inhalt preiszugeben.
	c := client.New(config.Config{ServerURL: f.srv.URL, Token: f.tok["rex"]})
	var out bytes.Buffer
	if err := c.ExportContextSnapshot(context.Background(), accProject, "old", nil, &out); err == nil && strings.Contains(out.String(), "legacy secret") {
		t.Errorf("export carries legacy machine entry")
	}
	if _, err := c.VerifyContextSnapshot(context.Background(), accProject, "old"); err == nil {
		t.Errorf("verify passed although filtered entries no longer match the sealed head")
	}
}

func TestContextSnapshotExplicitRevocationStillBitesForMembers(t *testing.T) {
	f := accessAPI(t, true)
	if err := f.st.SetContextSnapshotAccess("mia", accProject, false, false, false); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 403, "POST", "/api/context-snapshots", snapshotBody(accProject, "x"))
	f.expect(t, "mia", 403, "GET", "/api/context-snapshots?project="+accProject, nil)
}

func TestContextSnapshotsLogModeKeepsBehaviourAndLogsWouldDeny(t *testing.T) {
	f := accessAPI(t, false)
	var buf bytes.Buffer
	f.st.SetAccessMode(store.AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	f.expect(t, "nora", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "log"))
	f.expect(t, "gus", 200, "GET", snapURL("log", accProject, ""), nil)
	out := buf.String()
	if !strings.Contains(out, "would deny") || !strings.Contains(out, "resource=snapshot") {
		t.Fatalf("no would-deny line: %s", out)
	}
}
