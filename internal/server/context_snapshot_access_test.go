package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
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

func TestContextSnapshotCaptureOmitsOtherMachinesKnowledge(t *testing.T) {
	f := accessAPI(t, true)
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "mia box only", Body: "b", Scope: scope.Axes{Project: accProject, Machine: "mia-box"}, Person: "mia", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprint(id)
	f.expect(t, "lena", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "lena"))
	f.expect(t, "mia", 201, "POST", "/api/context-snapshots", snapshotBody(accProject, "mia"))
	f.expect(t, "lena", 404, "GET", snapURL("lena", accProject, "/entries")+"&domain=knowledge&key="+key, nil)
	f.expect(t, "mia", 200, "GET", snapURL("mia", accProject, "/entries")+"&domain=knowledge&key="+key, nil)
	// Everything else is still captured for the lead.
	f.expect(t, "lena", 200, "GET", snapURL("lena", accProject, "/entries")+"&domain=knowledge&key="+fmt.Sprint(f.id["k-mia"]), nil)
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
