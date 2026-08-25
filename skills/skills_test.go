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
