package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// "Global" heißt "überall für mich": Wissen und Aufträge ohne Projekt und ohne
// Maschine sehen und bekommen nur ihr Autor und dessen Sessions. Ein frisches
// Konto ohne Org, ein Gast, ein Mitglied und auch der Instanz-Admin sehen sie
// nicht (Abnahme 2026-10-11, Befund 1).
type globalFixture struct {
	st                      *Store
	robinK, miaK, legacyK   Knowledge
	robinR, miaR, legacyR   int64
	machineK, projectGlobal Knowledge
}

func newGlobalFixture(t *testing.T) globalFixture {
	t.Helper()
	st := accessFixture(t)
	f := globalFixture{st: st}
	ins := func(person string, author int64, ax scope.Axes, title string) Knowledge {
		k := Knowledge{Type: "note", Title: title, Body: "body " + title, Scope: ax, Person: person, AuthorID: author}
		id, err := st.InsertKnowledge(k)
		if err != nil {
			t.Fatal(err)
		}
		k.ID, k.Confidence = id, "trusted"
		return k
	}
	f.robinK = ins("robin", 1, scope.Axes{}, "robin global")
	f.miaK = ins("mia", 3, scope.Axes{}, "mia global")
	// Altbestand vor Konten: kein Autor-Konto, ein Label, das auf kein Konto passt.
	f.legacyK = ins("claude", 0, scope.Axes{}, "legacy global")
	if err := st.ClaimMachine("mia-box", "person:3"); err != nil {
		t.Fatal(err)
	}
	f.machineK = ins("mia", 3, scope.Axes{Machine: "mia-box"}, "mia machine")
	f.projectGlobal = ins("mia", 3, scope.Axes{Project: roleProject}, "project entry")
	req := func(person, title string) int64 {
		d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Person: person}})
		if err != nil {
			t.Fatal(err)
		}
		return d.Request.ID
	}
	f.robinR, f.miaR, f.legacyR = req("robin", "robin req"), req("mia", "mia req"), req("claude", "legacy req")
	return f
}

var globalViewers = []struct{ id, label string }{
	{"person:1", "robin"}, {"person:2", "lena"}, {"person:3", "mia"},
	{"person:4", "rex"}, {"person:5", "gus"}, {"person:6", "nora"},
}

func TestGlobalKnowledgeIsOnlyForItsAuthor(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	want := map[string]map[int64]bool{
		"person:1": {f.robinK.ID: true, f.legacyK.ID: true}, // legacy "claude" belongs to the instance owner
		"person:3": {f.miaK.ID: true},
	}
	for _, v := range globalViewers {
		pa := viewer(f.st, v.id, v.label)
		for _, k := range []Knowledge{f.robinK, f.miaK, f.legacyK} {
			got := pa.CanSeeKnowledge(k)
			if got != want[v.id][k.ID] {
				t.Errorf("%s sees %q: %v, want %v", v.label, k.Title, got, want[v.id][k.ID])
			}
			if err := pa.CheckKnowledge(k, ActRead); (err == nil) != want[v.id][k.ID] {
				t.Errorf("%s CheckKnowledge read %q: %v", v.label, k.Title, err)
			}
			if want[v.id][k.ID] {
				continue
			}
			for _, act := range []Action{ActEdit, ActVerify} {
				if err := pa.CheckKnowledge(k, act); err != ErrAccessNotFound {
					t.Errorf("%s %s on %q: %v, want not found", v.label, act, k.Title, err)
				}
			}
			if pa.CanDeliverKnowledge(k) && pa.CanSeeKnowledge(k) {
				t.Errorf("%s would be handed %q", v.label, k.Title)
			}
		}
	}
	// The author may keep working on his own entry.
	if err := viewer(f.st, "person:3", "mia").CheckKnowledge(f.miaK, ActEdit); err != nil {
		t.Errorf("author edit: %v", err)
	}
	// Machine-axis knowledge and project knowledge stay as they were.
	for _, v := range globalViewers {
		pa := viewer(f.st, v.id, v.label)
		if got, want := pa.CanSeeKnowledge(f.machineK), v.id == "person:3"; got != want {
			t.Errorf("%s sees machine entry of mia: %v", v.label, got)
		}
		if got, want := pa.CanSeeKnowledge(f.projectGlobal), v.id != "person:6"; got != want {
			t.Errorf("%s sees project entry: %v, want %v", v.label, got, want)
		}
	}
}

func TestGlobalKnowledgeStaysOpenWithoutEnforcement(t *testing.T) {
	f := newGlobalFixture(t)
	if !viewer(f.st, "person:6", "nora").CanSeeKnowledge(f.robinK) {
		t.Error("log mode must not hide")
	}
}

func TestKnowledgeWindowShowsOnlyOwnGlobalEntries(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	titles := func(who, label string) map[string]bool {
		pa := viewer(f.st, who, label)
		w := pa.RestrictKnowledgeWindow(KnowledgeWindow{})
		ks, err := f.st.KnowledgeWindow(w)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, k := range ks {
			out[k.Title] = true
		}
		return out
	}
	if got := titles("person:6", "nora"); len(got) != 0 {
		t.Errorf("fresh account: %v", got)
	}
	if got := titles("person:5", "gus"); got["robin global"] || got["mia global"] || got["legacy global"] {
		t.Errorf("guest: %v", got)
	}
	robin := titles("person:1", "robin")
	if !robin["robin global"] || !robin["legacy global"] || robin["mia global"] {
		t.Errorf("robin: %v", robin)
	}
	mia := titles("person:3", "mia")
	if !mia["mia global"] || mia["robin global"] || mia["legacy global"] {
		t.Errorf("mia: %v", mia)
	}
}

func TestGlobalRequestsAreOnlyForTheirAuthor(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	want := map[string]map[int64]bool{
		"person:1": {f.robinR: true, f.legacyR: true},
		"person:3": {f.miaR: true},
	}
	for _, v := range globalViewers {
		pa := viewer(f.st, v.id, v.label)
		page, err := f.st.SearchRequests(pa.RequestFilter(requestdomain.SearchFilter{Limit: 25}))
		if err != nil {
			t.Fatal(err)
		}
		got := map[int64]bool{}
		for _, h := range page.Results {
			got[h.Request.ID] = true
		}
		for _, id := range []int64{f.robinR, f.miaR, f.legacyR} {
			if got[id] != want[v.id][id] {
				t.Errorf("%s lists request %d: %v, want %v", v.label, id, got[id], want[v.id][id])
			}
			ref, err := f.st.RequestRef(RefRequest, id)
			if err != nil {
				t.Fatal(err)
			}
			obj, visible := pa.RequestObject(ref)
			if !visible {
				t.Fatalf("%s: request %d held", v.label, id)
			}
			for _, act := range []Action{ActRead, ActEdit, ActWork} {
				allowed := pa.Check(ref.Project, ResRequest, act, obj) == nil
				if allowed != want[v.id][id] {
					t.Errorf("%s %s request %d: %v, want %v", v.label, act, id, allowed, want[v.id][id])
				}
			}
		}
	}
	// Anyone may still file a global request of their own.
	if err := viewer(f.st, "person:6", "nora").Check("", ResRequest, ActCreate, Object{Own: true}); err != nil {
		t.Errorf("create: %v", err)
	}
}
