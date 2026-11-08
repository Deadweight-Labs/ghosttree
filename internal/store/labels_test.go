package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestLabelNamesAreOneLineAndSafeInTheSearchLanguage(t *testing.T) {
	for _, bad := range []string{"", "  ", "a\nb", "a\x07b", "a,b", `a"b`, "a:b", "-x", "@me", strings.Repeat("x", MaxLabelName+1)} {
		if _, err := CleanLabelName(bad); err == nil {
			t.Errorf("label name %q accepted", bad)
		}
	}
	if n, err := CleanLabelName("  good first issue "); err != nil || n != "good first issue" {
		t.Fatalf("good name: %q %v", n, err)
	}
	for _, bad := range []string{"zzz", "12345", "#12345g", "1234567"} {
		if _, err := CleanLabelColor(bad); err == nil {
			t.Errorf("color %q accepted", bad)
		}
	}
	if c, _ := CleanLabelColor("#D73A4A"); c != "d73a4a" {
		t.Fatalf("color = %q", c)
	}
	if c, _ := CleanLabelColor(""); c != DefaultLabelColor {
		t.Fatalf("default color = %q", c)
	}
	if _, err := CleanLabelDescription("a\nb"); err == nil {
		t.Fatal("multi-line description accepted")
	}
}

func TestLabelsPerProjectAndOrgAndRights(t *testing.T) {
	w := newPainWorld(t)
	// Ein Mitglied legt ein Projekt-Label an, ein Gast nicht.
	bug, err := w.pa["mia"].CreateLabel(LabelInput{Project: roleProject, Name: "bug", Color: "d73a4a", Description: "kaputt"})
	if err != nil {
		t.Fatal(err)
	}
	if bug.Scope != "project" || bug.Project != roleProject || bug.Color != "d73a4a" {
		t.Fatalf("label: %+v", bug)
	}
	if _, err := w.pa["gus"].CreateLabel(LabelInput{Project: roleProject, Name: "x"}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest may not create: %v", err)
	}
	if _, err := w.pa["nora"].CreateLabel(LabelInput{Project: roleProject, Name: "x"}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger gets not found: %v", err)
	}
	// Doppelter Name, auch in anderer Schreibweise.
	if _, err := w.pa["mia"].CreateLabel(LabelInput{Project: roleProject, Name: "BUG"}); ruleCode(err) != "label_exists" {
		t.Fatalf("duplicate: %v", err)
	}
	// Org-Label: nur ein Owner der Org, ein Mitglied nicht, ein Fremder sieht nichts.
	proj, ok := w.st.ProjectByRemote(roleProject)
	if !ok {
		t.Fatal("no project")
	}
	org := proj.OrgID
	shared, err := w.pa["robin"].CreateLabel(LabelInput{OrgID: org, Name: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].CreateLabel(LabelInput{OrgID: org, Name: "x"}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member may not create an org label: %v", err)
	}
	if _, err := w.pa["nora"].CreateLabel(LabelInput{OrgID: org, Name: "x"}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("outsider may not create an org label: %v", err)
	}
	// Das Projekt sieht beide.
	list, err := w.pa["gus"].ListLabels(roleProject)
	if err != nil || len(list) != 2 {
		t.Fatalf("labels of the project: %v %v", list, err)
	}
	if _, err := w.pa["nora"].ListLabels(roleProject); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger lists labels: %v", err)
	}
	// Ändern ab lead, Org-Label nur der Owner; unbekannt und fremd gleich.
	if _, err := w.pa["mia"].UpdateLabel(bug.ID, LabelPatch{Color: ptr("00ff00")}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member edits label: %v", err)
	}
	if l, err := w.pa["lena"].UpdateLabel(bug.ID, LabelPatch{Color: ptr("00ff00")}); err != nil || l.Color != "00ff00" {
		t.Fatalf("lead edits label: %v %v", l, err)
	}
	if _, err := w.pa["lena"].UpdateLabel(shared.ID, LabelPatch{Color: ptr("00ff00")}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("lead is no org owner: %v", err)
	}
	if _, err := w.pa["nora"].UpdateLabel(bug.ID, LabelPatch{Color: ptr("00ff00")}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger edits label: %v", err)
	}
	if _, err := w.pa["nora"].UpdateLabel(99999, LabelPatch{Color: ptr("00ff00")}); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("unknown label: %v", err)
	}
}

func TestLabelsAreSetOnRequestsPainsAndIdeasWithTheRightsOfTheObject(t *testing.T) {
	w := newPainWorld(t)
	mustLabel := func(who, name string) {
		t.Helper()
		if _, err := w.pa[who].CreateLabel(LabelInput{Project: roleProject, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	mustLabel("lena", "bug")
	mustLabel("lena", "ux")
	req := w.request(t, "Etwas bauen")
	p := w.newPain(t, "mia")
	idea, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Dunkles Thema", Actor: ideaActor("mia")})
	if err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]int64{"request": req, "pain": p.Pain.ID, "idea": idea.ID} {
		got, err := w.pa["mia"].SetLabels(kind, id, []string{"Bug"}, nil)
		if err != nil || len(got) != 1 || got[0].Name != "bug" {
			t.Fatalf("%s: set label: %v %v", kind, got, err)
		}
		// Wiederholt schadet nicht, ein zweites kommt dazu.
		got, err = w.pa["mia"].SetLabels(kind, id, []string{"bug", "ux"}, nil)
		if err != nil || len(got) != 2 {
			t.Fatalf("%s: second label: %v %v", kind, got, err)
		}
		got, err = w.pa["mia"].SetLabels(kind, id, nil, []string{"ux"})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: remove label: %v %v", kind, got, err)
		}
		// Ein Gast liest, setzt aber nicht; ein Fremder erfährt nichts.
		if _, err := w.pa["gus"].SetLabels(kind, id, []string{"ux"}, nil); !errors.Is(err, ErrAccessForbidden) {
			t.Fatalf("%s: guest sets label: %v", kind, err)
		}
		if _, err := w.pa["nora"].SetLabels(kind, id, []string{"ux"}, nil); !errors.Is(err, ErrAccessNotFound) {
			t.Fatalf("%s: stranger sets label: %v", kind, err)
		}
		if _, err := w.pa["mia"].SetLabels(kind, id+1000, []string{"ux"}, nil); !errors.Is(err, ErrAccessNotFound) {
			t.Fatalf("%s: unknown object: %v", kind, err)
		}
		// Ein unbekannter Name ändert nichts.
		if _, err := w.pa["mia"].SetLabels(kind, id, []string{"ux", "nope"}, nil); ruleCode(err) != "label_not_found" {
			t.Fatalf("%s: unknown label: %v", kind, err)
		}
		got, _ = w.pa["gus"].LabelsOf(kind, id)
		if len(got) != 1 {
			t.Fatalf("%s: an unknown label name must not change anything: %v", kind, got)
		}
	}
	if _, err := w.pa["mia"].SetLabels("session", 1, []string{"bug"}, nil); ruleCode(err) != "label_kind_invalid" {
		t.Fatalf("labels do not apply to sessions: %v", err)
	}
}

func TestALabelOfAnotherProjectBehavesLikeAnUnknownName(t *testing.T) {
	w := newPainWorld(t)
	other := "github.com/acme/other"
	if _, err := w.st.EnsureProject("person:1", other); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["robin"].CreateLabel(LabelInput{Project: other, Name: "secret-plan"}); err != nil {
		t.Fatal(err)
	}
	req := w.request(t, "Etwas")
	if _, err := w.pa["mia"].SetLabels("request", req, []string{"secret-plan"}, nil); ruleCode(err) != "label_not_found" {
		t.Fatalf("foreign label: %v", err)
	}
	if _, err := w.pa["mia"].SetLabels("request", req, []string{"never-existed"}, nil); ruleCode(err) != "label_not_found" {
		t.Fatalf("unknown label: %v", err)
	}
	a, _ := w.pa["mia"].SetLabels("request", req, []string{"secret-plan"}, nil)
	_ = a
}

func TestDeletingALabelRemovesItFromTheObjects(t *testing.T) {
	w := newPainWorld(t)
	l, _ := w.pa["lena"].CreateLabel(LabelInput{Project: roleProject, Name: "tmp"})
	req := w.request(t, "Etwas")
	if _, err := w.pa["mia"].SetLabels("request", req, []string{"tmp"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.pa["mia"].DeleteLabel(l.ID); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member deletes: %v", err)
	}
	if err := w.pa["lena"].DeleteLabel(l.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := w.pa["mia"].LabelsOf("request", req)
	if len(got) != 0 {
		t.Fatalf("labels after delete: %v", got)
	}
	var n int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM label_assignments`).Scan(&n)
	if n != 0 {
		t.Fatalf("assignments left: %d", n)
	}
}

func TestTopicsResolveToPrefixesAndHaveRights(t *testing.T) {
	w := newPainWorld(t)
	if _, err := w.pa["mia"].SaveTopic(TopicInput{Project: roleProject, Name: "Suche", Prefixes: []string{"internal/embed", "./internal/store/embeddings/"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["gus"].SaveTopic(TopicInput{Project: roleProject, Name: "x", Prefixes: []string{"a"}}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest saves topic: %v", err)
	}
	// Ersetzen eines vorhandenen Themas braucht lead.
	if _, err := w.pa["mia"].SaveTopic(TopicInput{Project: roleProject, Name: "suche", Prefixes: []string{"a"}}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member replaces topic: %v", err)
	}
	topic, err := w.pa["lena"].SaveTopic(TopicInput{Project: roleProject, Name: "suche", Prefixes: []string{"internal/embed", "internal/store/embeddings"}})
	if err != nil || len(topic.Prefixes) != 2 {
		t.Fatalf("topic: %+v %v", topic, err)
	}
	prefixes, ok := w.pa["gus"].TopicPrefixes(roleProject)("SUCHE")
	if !ok || len(prefixes) != 2 || prefixes[0] != "internal/embed" {
		t.Fatalf("prefixes: %v %v", prefixes, ok)
	}
	if _, ok := w.pa["gus"].TopicPrefixes(roleProject)("nope"); ok {
		t.Fatal("unknown topic resolved")
	}
	for _, bad := range []string{"", "a\nb", "/abs", "../up"} {
		if _, err := w.pa["lena"].SaveTopic(TopicInput{Project: roleProject, Name: "t", Prefixes: []string{bad}}); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	if _, err := w.pa["lena"].SaveTopic(TopicInput{Project: roleProject, Name: "bad,name", Prefixes: []string{"a"}}); err == nil {
		t.Fatal("bad topic name accepted")
	}
	if _, err := w.pa["nora"].ListTopics(roleProject); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger lists topics: %v", err)
	}
	if ok, err := w.pa["lena"].DeleteTopic(roleProject, "Suche"); err != nil || !ok {
		t.Fatalf("delete topic: %v %v", ok, err)
	}
}

func TestSavedSearchesPerUserProjectAndOrg(t *testing.T) {
	w := newPainWorld(t)
	own, err := w.pa["mia"].SavePreset(roleProject, "user", "meine-bugs", "", []string{"label:bug is:open"})
	if err != nil {
		t.Fatal(err)
	}
	if own.Scope != "user" || len(own.Args) != 0 {
		t.Fatalf("preset: %+v", own)
	}
	proj, err := w.pa["mia"].SavePreset(roleProject, "project", "pr-bugs", "Bugs an einem PR", []string{"label:bug pr:{pr}"})
	if err != nil || len(proj.Args) != 1 || proj.Args[0] != "pr" {
		t.Fatalf("project preset: %+v %v", proj, err)
	}
	if _, err := w.pa["mia"].SavePreset(roleProject, "org", "x", "", []string{"is:open"}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member saves org search: %v", err)
	}
	org, err := w.pa["robin"].SavePreset(roleProject, "org", "alle-offen", "", []string{"is:open"})
	if err != nil {
		t.Fatal(err)
	}
	// Mia sieht ihre, die des Projekts und der Org; Lena nicht Mias persönliche.
	miaList, _ := w.pa["mia"].ListPresets(roleProject)
	lenaList, _ := w.pa["lena"].ListPresets(roleProject)
	if len(miaList) != 3 || len(lenaList) != 2 {
		t.Fatalf("lists: mia=%d lena=%d", len(miaList), len(lenaList))
	}
	// Löschen: persönlich nur der Besitzer, Projekt ab lead, Org nur Owner;
	// unbekannt und fremd gleich.
	if err := w.pa["lena"].DeletePreset(own.ID); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("foreign personal search: %v", err)
	}
	if err := w.pa["lena"].DeletePreset(99999); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("unknown search: %v", err)
	}
	if err := w.pa["mia"].DeletePreset(proj.ID); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member deletes project search: %v", err)
	}
	if err := w.pa["lena"].DeletePreset(proj.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.pa["lena"].DeletePreset(org.ID); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("lead deletes org search: %v", err)
	}
	if err := w.pa["nora"].DeletePreset(org.ID); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("stranger deletes org search: %v", err)
	}
	if err := w.pa["mia"].DeletePreset(own.ID); err != nil {
		t.Fatal(err)
	}
	// Speichern ersetzt gleichnamige.
	if _, err := w.pa["robin"].SavePreset(roleProject, "org", "alle-offen", "neu", []string{"is:done"}); err != nil {
		t.Fatal(err)
	}
	list, _ := w.pa["robin"].ListPresets(roleProject)
	if len(list) != 1 || list[0].Queries[0] != "is:done" {
		t.Fatalf("replace: %+v", list)
	}
	// Ungültige Suchzeilen stehen beim Speichern im Fehler.
	for _, bad := range [][]string{{"flavor:x"}, {"is:oen"}, {"label:{nope}"}, {""}, {"a\nb"}, {"a", "b", "c", "d", "e", "f"}} {
		if _, err := w.pa["mia"].SavePreset(roleProject, "user", "kaputt", "", bad); ruleCode(err) != "preset_invalid" {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if _, err := w.pa["mia"].SavePreset(roleProject, "user", "Bad Name", "", []string{"is:open"}); ruleCode(err) != "preset_invalid" {
		t.Fatalf("bad name: %v", err)
	}
}

func TestObjectFactsCoverAnchorsPackagesRelationsAndLabels(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	req := w.request(t, "Erster")
	parent := w.request(t, "Eltern")
	if _, err := st.AddRequestRelation(req, requestdomain.Relation{Kind: "parent", OtherRequestID: parent}, "mia"); err != nil {
		t.Fatal(err)
	}
	l, _ := w.pa["lena"].CreateLabel(LabelInput{Project: roleProject, Name: "bug"})
	_ = l
	if _, err := w.pa["mia"].SetLabels("request", req, []string{"bug"}, nil); err != nil {
		t.Fatal(err)
	}
	pkg, err := st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/x", Members: []requestdomain.PackageMember{{RequestID: req}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE work_packages SET pr=9 WHERE id=?`, pkg.ID); err != nil {
		t.Fatal(err)
	}
	// Ein Nachweis mit Anker und PR.
	if _, err := st.db.Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES('abcdef1234567','feat/x','',0,12,?)`, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE request_activity SET anchor_id=(SELECT MAX(id) FROM git_anchors) WHERE request_id=? AND kind='request.created'`, req); err != nil {
		t.Fatal(err)
	}
	facts, err := st.ObjectFacts("request", []int64{req})
	if err != nil {
		t.Fatal(err)
	}
	f := facts[req]
	if len(f.Parents) != 1 || f.Parents[0] != parent || len(f.Labels) != 1 || f.Labels[0] != "bug" ||
		!hasString(f.Branches, "feat/x") || !hasString(f.Commits, "abcdef1234567") || len(f.PRs) != 2 || len(f.Packages) != 1 {
		t.Fatalf("request facts: %+v", f)
	}
	// Kriterium und Aufgabe erben, das Kriterium nimmt PR und Commit nicht vom Request.
	var crit int64
	st.db.QueryRow(`SELECT id FROM request_criteria WHERE request_id=?`, req).Scan(&crit)
	cf, err := st.ObjectFacts("criterion", []int64{crit})
	if err != nil {
		t.Fatal(err)
	}
	if got := cf[crit]; got.RequestID != req || len(got.PRs) != 0 || !hasString(got.Branches, "feat/x") || len(got.Parents) != 0 || len(got.Labels) != 0 {
		t.Fatalf("criterion facts: %+v", got)
	}
	if _, err := st.ObjectFacts("session", []int64{1}); err == nil {
		t.Fatal("facts for an unknown kind")
	}
	// Pains.
	p := w.newPain(t, "mia")
	if _, err := w.pa["lena"].SetLabels("pain", p.Pain.ID, []string{"bug"}, nil); err != nil {
		t.Fatal(err)
	}
	pf, err := st.ObjectFacts("pain", []int64{p.Pain.ID})
	if err != nil || len(pf[p.Pain.ID].Labels) != 1 {
		t.Fatalf("pain facts: %+v %v", pf, err)
	}
	_ = pain.Active
}

func ideaActor(who string) idea.Actor { return idea.Actor{Person: who} }
