package skills

import (
	"strings"
	"testing"
)

func TestBothSkillsAreEmbeddedWithFrontmatter(t *testing.T) {
	names := Names()
	if len(names) != 2 || names[0] != "ghosttree-onboard-repo" || names[1] != "ghosttree-setup" {
		t.Fatalf("want both skills sorted, got %v", names)
	}
	for _, n := range names {
		files, err := Files(n)
		if err != nil {
			t.Fatal(err)
		}
		md, ok := files["SKILL.md"]
		if !ok {
			t.Fatalf("%s has no SKILL.md", n)
		}
		body := string(md)
		if !strings.HasPrefix(body, "---\n") {
			t.Errorf("%s: SKILL.md must open with YAML frontmatter", n)
		}
		if !strings.Contains(body, "name: "+n) {
			t.Errorf("%s: frontmatter name must match the directory", n)
		}
		// The description is the trigger. Without it no harness finds the
		// skill, and a skill nobody finds does not exist.
		if !strings.Contains(body, "description:") {
			t.Errorf("%s: frontmatter needs a description", n)
		}
	}
}

func TestReferencesAreShippedAlongsideTheSkill(t *testing.T) {
	// Progressive disclosure only works if the details are actually there to
	// disclose. A SKILL.md that points at references/ which was never embedded
	// sends the reader to a file that does not exist on their machine.
	want := map[string][]string{
		"ghosttree-setup": {
			"references/local-server.md", "references/verification.md",
		},
		"ghosttree-onboard-repo": {
			"references/migration.md", "references/interview.md",
			"references/inventory-run.md", "references/acceptance.md",
		},
	}
	for skill, refs := range want {
		files, err := Files(skill)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if _, ok := files[ref]; !ok {
				t.Errorf("%s is missing %s", skill, ref)
			}
		}
		// Every reference the SKILL.md points at must exist.
		body := string(files["SKILL.md"])
		for ref := range files {
			if ref == "SKILL.md" {
				continue
			}
			if !strings.Contains(body, ref) {
				t.Errorf("%s ships %s but never points at it", skill, ref)
			}
		}
	}
}

func TestSkillsAreEnglish(t *testing.T) {
	// Shipped product is English. The umlauts are the cheapest check that
	// nobody pasted a German paragraph over from the spec.
	for _, n := range Names() {
		files, _ := Files(n)
		for path, content := range files {
			if strings.ContainsAny(string(content), "äöüßÄÖÜ") {
				t.Errorf("%s/%s contains German characters", n, path)
			}
		}
	}
}

func TestInventoryRunCarriesTheThreeHardRules(t *testing.T) {
	files, err := Files("ghosttree-onboard-repo")
	if err != nil {
		t.Fatal(err)
	}
	ref := string(files["references/inventory-run.md"])
	// These three sentences are the reason a bulk run is permitted at all. If
	// one of them disappears in a rewrite, the condition under which the ghost
	// files design's non-goal was revised has quietly gone with it.
	for _, must := range []string{
		"MAY produce a Synopsis",
		"MAY only carry over Context from knowledge it was given",
		"MUST NOT infer Context from the code",
	} {
		if !strings.Contains(ref, must) {
			t.Errorf("inventory-run.md lost the rule %q", must)
		}
	}
}

func TestTheFullRunIsGatedOnTheDeliveryBudget(t *testing.T) {
	files, _ := Files("ghosttree-onboard-repo")
	joined := string(files["SKILL.md"]) + string(files["references/inventory-run.md"])
	if !strings.Contains(joined, "REQ-198") {
		t.Error("the full inventory run is gated on the delivery budget; the skill must name it")
	}
}

func TestTheIssueTrackerStepIsMarkedOptional(t *testing.T) {
	files, _ := Files("ghosttree-onboard-repo")
	ref := string(files["references/migration.md"])
	// A skill that quietly does one step less without GitHub is how a
	// harness-neutral tool becomes a GitHub tool without anyone deciding to.
	if !strings.Contains(ref, "gh auth status") || !strings.Contains(ref, "skip") {
		t.Error("migration.md must gate the tracker step and say when it is skipped")
	}
}

func TestFilesRejectsPathEscapes(t *testing.T) {
	for _, bad := range []string{"", "../secrets", "ghosttree-setup/references"} {
		if _, err := Files(bad); err == nil {
			t.Errorf("Files(%q) must be rejected", bad)
		}
	}
}

// Die vierte Regel ist die, die im ersten echten Pilotlauf nicht angekommen
// ist: 29 Dateien gelesen, 29 beschrieben, kein einziges "nichts zu sagen".
// Sie stand damals als Fussnote zwischen drei nummerierten Regeln.
func TestWritingNothingIsAFullRuleNotAFootnote(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	if !strings.Contains(ref, "The run MAY write nothing at all for a file") {
		t.Error("writing nothing must stand among the numbered rules, not beside them")
	}
	if !strings.Contains(ref, "nothing_to_say") {
		t.Error("the rule needs the tool that carries it out")
	}
	// Null Verwerfungen und null Schweigen sind Warnsignale, keine Ergebnisse.
	if !strings.Contains(ref, "Zero `nothing_to_say`") {
		t.Error("the halt must read a count of zero as a warning")
	}
}

// Die Schwelle nennt jetzt ihren Zweck. Ohne ihn liest sie sich mechanisch, und
// im Pilotlauf war das mechanisch Richtige nachweislich das Falsche: ein nicht
// geteiltes Paket aus sechs Backends lieferte Beschreibungen, die aufeinander
// Bezug nahmen.
func TestTheUnitOfWorkExplainsItselfBeforeItLimits(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	for _, must := range []string{"so that siblings are read together", "where you start weighing"} {
		if !strings.Contains(ref, must) {
			t.Errorf("inventory-run.md lost %q — the threshold is a judgement, not a rule", must)
		}
	}
}

// Eine Kompaktierung behaelt die Anweisung und verliert die Ausnahmen. Die
// Datei liegt auf der Platte; sie erneut zu lesen kostet einen Werkzeugaufruf.
func TestTheRunSurvivesACompaction(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	if !strings.Contains(ref, "interrupted or compacted") {
		t.Error("the run must tell a resumed session to re-read its own rules")
	}
}

// Ohne origin gibt es keine Projektachse — und der Abbruch kam bisher erst
// mitten in Phase 1 aus ctx migrate.
func TestPreconditionsAreCheckedBeforePhaseOne(t *testing.T) {
	md := string(mustFiles(t, "ghosttree-onboard-repo")["SKILL.md"])
	for _, must := range []string{"git remote get-url origin", "collector"} {
		if !strings.Contains(md, must) {
			t.Errorf("SKILL.md must check %q up front", must)
		}
	}
}

// "Weiss niemand" ist ein Fakt ueber den Zustand und gehoert in den Baum. Eine
// Liste offener Fragen in einer Chatnachricht ist morgen weg.
func TestUnansweredQuestionsGetAHome(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/acceptance.md"])
	if !strings.Contains(ref, "cannot answer either") || !strings.Contains(ref, "`note`") {
		t.Error("acceptance.md must say WHERE unanswered questions go, not just that they are kept")
	}
	// Was man selbst lesen kann, fragt man nicht.
	if !strings.Contains(ref, "Does the code do X?") {
		t.Error("acceptance.md must separate what the reader can answer from what only the operator can")
	}
}

// Ein kaltes Repo hat kein Gedaechtnis, das man stuetzen koennte.
func TestTheInterviewAsksHowColdTheRepositoryIs(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/interview.md"])
	for _, must := range []string{"git log -1 --format=%cr", "PRIMARY", "already answered"} {
		if !strings.Contains(ref, must) {
			t.Errorf("interview.md lost %q", must)
		}
	}
}

func mustFiles(t *testing.T, name string) map[string][]byte {
	t.Helper()
	f, err := Files(name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Die drei Verbote muessen in SKILL.md selbst stehen, nicht nur in den
// Referenzdateien. Am 2026-08-26 uebernahm ein Lauf nach einer Kompaktierung
// alle 18 offenen Issues in den Ledger — das Verbot stand in migration.md, die
// er in dieser Phase nicht mehr gelesen hatte. Ein Verbot ist genau das, was
// eine Zusammenfassung zuerst verliert.
func TestTheThreeProhibitionsAreInTheFileEverySessionReads(t *testing.T) {
	md := string(mustFiles(t, "ghosttree-onboard-repo")["SKILL.md"])
	for _, must := range []string{
		"Never import an issue tracker wholesale",
		"Never invent Context",
		"Never delete without a separate consent",
	} {
		if !strings.Contains(md, must) {
			t.Errorf("SKILL.md lost the prohibition %q", must)
		}
	}
	// Ueberstimmen ist erlaubt, stilles Ueberstimmen nicht.
	if !strings.Contains(md, "SAY SO first") {
		t.Error("an override must be spoken, otherwise nobody can tell it from a mistake")
	}
	if !strings.Contains(md, "After an interruption or a compaction") {
		t.Error("SKILL.md must tell a resumed session to re-read its phase reference")
	}
}

// Ein Gate, das der Betreiber nicht ueberstimmen kann, waere falsch gebaut —
// eines, das unbemerkt faellt, auch.
func TestTheGateCanBeOverruledButNotSilently(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	for _, must := range []string{"can overrule this", "WITH THE MEASURED NUMBERS", "belongs to ANOTHER project"} {
		if !strings.Contains(ref, must) {
			t.Errorf("inventory-run.md lost %q", must)
		}
	}
}

// "Komplett autonom durchlaufen" entfernt den Halt — und der Halt war die
// einzige Stelle, an der die Verwerfungsquoten ueberhaupt angesehen wurden.
// Autonomie nimmt das Gespraech weg, nicht die Pruefung.
func TestAutonomousRunsStillCheckAndReportTheNumbers(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	if !strings.Contains(ref, "told you to run autonomously") {
		t.Fatal("the run has no story for autonomous operation")
	}
	for _, must := range []string{
		"Autonomy removes the\nCONVERSATION, not the CHECK",
		"compute the three numbers anyway",
		"report all of it at the end",
	} {
		if !strings.Contains(ref, must) {
			t.Errorf("the autonomous path lost %q", must)
		}
	}
}

// Am 2026-08-26 lieferte ein Leser vier Context-Zeilen mit einer echten
// Eintragsnummer fuer Fakten, die dort nicht stehen. Eine Anwesenheitspruefung
// haette alle vier durchgelassen — und zitierte Erfindung ist glaubwuerdiger
// als eine ehrliche Beschreibung ohne Context.
func TestTheCriticVerifiesTheCitationNotItsPresence(t *testing.T) {
	ref := string(mustFiles(t, "ghosttree-onboard-repo")["references/inventory-run.md"])
	if !strings.Contains(ref, "Does the source actually say this?") {
		t.Fatal("the critic must open the entry, not count the number")
	}
	if !strings.Contains(ref, "cheap to catch") {
		t.Error("the reason the citation rule works belongs next to the check it enables")
	}
}

// Die Anweisung "nach einer Unterbrechung erneut lesen" stand zuerst nur in
// der Datei, die man dafuer erneut lesen muesste — eine Sitzung, die sie
// verloren hat, erfaehrt nie, dass es sie gibt. Was ueberlebt, ist der
// Request: unterbrochene Arbeit wird bei Sitzungsbeginn ungefragt geliefert.
func TestTheResumeHintLivesWhereItComesBackOnItsOwn(t *testing.T) {
	md := string(mustFiles(t, "ghosttree-onboard-repo")["SKILL.md"])
	if !strings.Contains(md, "Put a resume line in its description") {
		t.Fatal("the resume hint must be parked on the ledger entry, not only in this file")
	}
	// Ein Zeiger, keine Kopie: zwei Fassungen driften, und die Kopie waere die
	// komprimierte.
	if !strings.Contains(md, "A pointer and not a copy") {
		t.Error("the reason it is a pointer belongs next to it")
	}
}

func TestOnboardingPublishesLongFormDocumentsOutsideGit(t *testing.T) {
	files := mustFiles(t, "ghosttree-onboard-repo")
	joined := string(files["SKILL.md"]) + string(files["references/migration.md"])
	for _, want := range []string{"ctx doc import", ".ghosttree/edit/", "must not be committed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("document lifecycle guidance is missing %q", want)
		}
	}
}
