package server

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Sessions eines Requests über HTTP (IDEA-31 P4): dieselben drei Stufen wie am PR,
// und der Weg über das Paket und über Commit-Nachweise.

func requestSessionsURL(id int64) string {
	return idPath("/api/requests/%d/sessions", id)
}

// seedRequestSessions legt einen Request mit einem Kriterium an, dessen Nachweis
// den Commit A nennt, und ein Paket auf feat/x, das an PR 12 hängt und den Request
// trägt. mia hat leise committet (privat), rex den PR angelegt (freigegeben).
func (f *accessAPIFixture) seedRequestSessions(t *testing.T) int64 {
	t.Helper()
	f.seedSessionGit(t)
	d := decode[requestdomain.Detail](t, f.expect(t, "mia", 201, "POST", "/api/requests", map[string]any{
		"type": "feature", "title": "Etwas mit Sessions", "project": accProject, "criteria": []string{"Es gibt einen Nachweis"}}))
	id := d.Request.ID
	pkg := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/x", "name": "Mit Sessions", "requests": []map[string]any{{"request_id": id}}}))
	f.expect(t, "mia", 200, "PATCH", idPath("/api/packages/%d", pkg.ID), map[string]any{"pr": 12})
	return id
}

func TestRequestSessionsComeFromThePackagePullRequestAndCommitEvidence(t *testing.T) {
	f := accessAPI(t, true)
	id := f.seedRequestSessions(t)
	get := func(who string) store.GitSessionLinks {
		t.Helper()
		return decode[store.GitSessionLinks](t, f.expect(t, who, 200, "GET", requestSessionsURL(id), nil))
	}

	// Über das Paket: die Sessions von PR 12 (mia leise, rex als Ersteller).
	mia := get("mia")
	if len(mia.Items) != 2 || mia.Hidden != 0 {
		t.Fatalf("mia: %+v", mia)
	}
	for _, it := range mia.Items {
		if len(it.Origins) != 1 || it.Origins[0] != store.RequestOriginPR || it.PR != 12 {
			t.Errorf("the origin is the pull request of the package: %+v", it)
		}
		if it.Level != store.GitSessionLinkFull || it.SessionRef == "" {
			t.Errorf("mia may read both: %+v", it)
		}
	}

	// Ein Commit-Nachweis auf Commit B (ohne eigene Session, er liegt im PR) und
	// einer auf A mit freiem Text: dieselben Sessions, die Herkunft sagt beides.
	d := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", id), nil))
	f.expect(t, "mia", 200, "PATCH", idPath("/api/criteria/%d", d.Criteria[0].ID), map[string]any{
		"state": "met", "evidence_kind": "commit", "evidence_ref": "erster Commit (" + gitCommitA[:9] + ")"})
	mia = get("mia")
	var both int
	for _, it := range mia.Items {
		if strings.Join(it.Origins, ",") == store.RequestOriginEvidence+","+store.RequestOriginPR {
			both++
		}
	}
	if both != 1 {
		t.Fatalf("the session of mia is named by the package and by the evidence: %+v", mia)
	}

	// rex: seine eigene Session mit Titel, die private von mia als Zahl.
	rex := get("rex")
	if len(rex.Items) != 1 || rex.Items[0].Person != "rex" || rex.Hidden != 1 {
		t.Fatalf("rex: %+v", rex)
	}
	// Ein Gast sieht nur die Zahl, ohne Name, Datum und Titel.
	gus := get("gus")
	if len(gus.Items) != 0 || gus.Hidden != 2 {
		t.Fatalf("a guest sees a number only: %+v", gus)
	}
	// Ein Fremder bekommt dieselbe Antwort wie für einen Request, den es nicht gibt.
	code, out := f.call(t, "nora", "GET", requestSessionsURL(id), nil)
	_, missing := f.call(t, "nora", "GET", requestSessionsURL(id+9999), nil)
	if code != 404 || out != missing {
		t.Fatalf("an outsider: %d %s, want the answer for a request that does not exist: %s", code, out, missing)
	}
	// Anonym nicht.
	if code, _ := f.callNoToken(t, "GET", requestSessionsURL(id), nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// Was ein Betrachter nicht sehen darf, steht nicht im Rohtext.
	for _, who := range []string{"rex", "gus", "lena", "robin"} {
		body := f.expect(t, who, 200, "GET", requestSessionsURL(id), nil)
		if strings.Contains(body, "secretterm mia") || strings.Contains(body, "s-mia") {
			t.Errorf("%s: the private session leaks: %s", who, body)
		}
	}
}

// Wer start_work gerufen hat, steht schon unter "Work"; derselbe Request nennt ihn
// nicht ein zweites Mal als abgeleitet.
func TestRequestSessionsLeaveOutWhatStartWorkAlreadyHolds(t *testing.T) {
	f := accessAPI(t, true)
	id := f.seedRequestSessions(t)
	f.expect(t, "rex", 201, "POST", idPath("/api/requests/%d/work", id), map[string]any{"session_id": f.id["s-rex"], "role": "primary"})
	l := decode[store.GitSessionLinks](t, f.expect(t, "mia", 200, "GET", requestSessionsURL(id), nil))
	if len(l.Items) != 1 || l.Items[0].Person != "mia" {
		t.Fatalf("only the derived session of mia is left: %+v", l)
	}
}

// Ein Request ohne Paket und ohne Commit-Nachweis hat nichts Abgeleitetes.
func TestRequestSessionsAreEmptyWithoutPackageOrEvidence(t *testing.T) {
	f := accessAPI(t, true)
	f.seedSessionGit(t)
	l := decode[store.GitSessionLinks](t, f.expect(t, "mia", 200, "GET", requestSessionsURL(f.id["r-mia"]), nil))
	if len(l.Items) != 0 || l.Hidden != 0 {
		t.Fatalf("nothing derived: %+v", l)
	}
}
