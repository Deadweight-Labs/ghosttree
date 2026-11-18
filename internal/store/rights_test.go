package store

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func legacyRank(role string) int {
	switch role {
	case RoleOwner:
		return 4
	case RoleLead:
		return 3
	case RoleMember:
		return 2
	case RoleGuest:
		return 1
	}
	return 0
}

// legacyMatrixAllows is the matrix as it stood before the rights catalog
// (dev 0b41747c), kept verbatim to prove the presets give the same answers.
func legacyMatrixAllows(role RoleInfo, res Resource, act Action, obj Object) bool {
	rank := legacyRank(role.Role)
	switch res {
	case ResKnowledge:
		switch act {
		case ActRead:
			if rank >= 2 {
				return true
			}
			return rank == 1 && (obj.Confidence == "trusted" || obj.Confidence == "verified")
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && (obj.Own || obj.OriginOwner))
		case ActVerify:
			return rank >= 3 || (rank >= 2 && role.CanReview)
		case ActRelease:
			return rank >= 2 && obj.OriginOwner
		}
	case ResRequest:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && (obj.Own || obj.OriginOwner))
		}
	case ResPain:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResIdea:
		// Wie ein Pain: lesen ab Gast, anlegen und bearbeiten wie dort.
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResFeature:
		// Lesen ab Gast. Anlegen und eine neue Fassung schreiben (ActWork) ab
		// member: die Beschreibung pflegt, wer den PR schreibt. Umbenennen,
		// einstellen und wieder aufnehmen (ActEdit) ab lead. Die Zuordnung steht
		// hier an einer Stelle: wer sie enger will, ändert nur diese Zeilen.
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3
		}
	case ResComment:
		// Lesen ab Gast, schreiben ab member. Ändern darf nur der Autor, löschen
		// der Autor und jeder Lead oder Owner (Moderation).
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 2 && obj.Own
		case ActDelete:
			return rank >= 3 || (rank >= 2 && obj.Own)
		}
	case ResLabel:
		// Lesen ab Gast, anlegen ab member, ändern und löschen ab lead. Ein Label
		// zu setzen prüft das Objekt selbst (ActWork auf dem Objekt).
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3
		}
	case ResTask:
		switch act {
		case ActRead:
			return rank >= 2 || (rank == 1 && obj.Assigned)
		case ActCreate:
			return rank >= 2
		case ActWork:
			// Ein Gast liest nur, auch eine ihm zugewiesene Aufgabe meldet er
			// nicht als erledigt (Robin, 2026-11-09).
			return rank >= 3 || (rank == 2 && (obj.Assigned || obj.Unassigned))
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResSessionMeta:
		// Die eigene Session anlegen und fortschreiben darf jedes Konto; wem sie
		// gehört, regelt der Upload (gateMachine, mayWriteSession).
		return act == ActCreate || (act == ActRead && (rank >= 2 || (rank == 1 && obj.Guests)))
	case ResTranscript:
		switch act {
		case ActRead:
			// Privat heißt privat: eine nicht freigegebene Session liest nur ihr
			// Besitzer, auch kein Lead, kein Projekt-Owner (REQ-438 D).
			return obj.Own || (rank >= 2 && obj.Shared) || (rank >= 1 && obj.Guests)
		case ActShare:
			// Der Besitzer der Session; der Projekt-Owner darf eine freigegebene
			// Session zurücknehmen, eine private nie erreichen.
			return obj.Own || (rank >= 4 && obj.Shared)
		}
	case ResRunbook:
		switch act {
		case ActRead, ActCreate, ActWork:
			// ActWork: einen Lauf melden.
			return rank >= 2
		case ActEdit:
			// Wie bei Wissen (#1444): wer es besser weiß, schreibt eine neue
			// Fassung. Ausführbar wird sie dadurch nicht, das tut erst die
			// Freigabe.
			return rank >= 2
		case ActApprove:
			return rank >= 3
		}
	case ResWorkflow:
		switch act {
		case ActRead:
			return rank >= 2
		case ActEdit:
			return rank >= 3
		}
	case ResSnapshot:
		return (act == ActRead || act == ActCreate) && rank >= 2
	case ResRoom:
		return ((act == ActRead || act == ActCreate) && rank >= 1) || (act == ActPost && rank >= 2)
	case ResProject:
		return act == ActRead && rank >= 1
	case ResAgents:
		return act == ActRead && rank >= 2
	case ResMembers:
		// Der Gast sieht nur den eigenen Eintrag; das filtert der Aufrufer.
		return act == ActRead && rank >= 1
	case ResDocument:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResGhost:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActEdit:
			return rank >= 2
		}
	}
	return false
}

var allResources = []Resource{ResKnowledge, ResRequest, ResSessionMeta, ResTranscript, ResRoom, ResAgents,
	ResMembers, ResDocument, ResGhost, ResProject, ResSnapshot, ResPain, ResTask, ResIdea, ResLabel,
	ResRunbook, ResFeature, ResComment, ResWorkflow, ResDelivery, ResPackage}

var allActions = []Action{ActRead, ActCreate, ActEdit, ActVerify, ActRelease, ActWork, ActShare, ActPost, ActApprove, ActDelete}

// TestPresetsMatchLegacyMatrix: for every role, review flag, resource, action
// and every combination of object properties the presets answer exactly as
// the old matrix did (AC-722.1).
func TestPresetsMatchLegacyMatrix(t *testing.T) {
	roles := []string{"", RoleGuest, RoleMember, RoleLead, RoleOwner, "maintainer"}
	confidences := []string{"", "staged", "trusted", "verified"}
	checked := 0
	for _, role := range roles {
		for _, review := range []bool{false, true} {
			ri := RoleInfo{Role: role, CanReview: review}
			for _, res := range allResources {
				for _, act := range allActions {
					for bits := 0; bits < 64; bits++ {
						for _, conf := range confidences {
							obj := Object{Own: bits&1 != 0, Shared: bits&2 != 0, Guests: bits&4 != 0,
								OriginOwner: bits&8 != 0, Assigned: bits&16 != 0, Unassigned: bits&32 != 0, Confidence: conf}
							// Packages had no row of their own: the API asked the
							// issue rights, read, create and work (DEC-51 A).
							legacyRes, legacyAct := res, act
							if res == ResPackage {
								legacyRes = ResRequest
								if act == ActEdit {
									legacyAct = ActWork
								}
							}
							got, want := matrixAllows(ri, res, act, obj), legacyMatrixAllows(ri, legacyRes, legacyAct, obj)
							checked++
							if got != want {
								t.Fatalf("role %q review %v %s %s %+v: rights say %v, old matrix %v", role, review, res, act, obj, got, want)
							}
						}
					}
				}
			}
		}
	}
	if checked < 100000 {
		t.Fatalf("only %d combinations checked", checked)
	}
}

func TestRightCatalog(t *testing.T) {
	seen := map[Right]bool{}
	for _, r := range Rights() {
		if seen[r.Right] {
			t.Errorf("%s twice", r.Right)
		}
		seen[r.Right] = true
		if r.DE == "" || r.EN == "" {
			t.Errorf("%s: description missing", r.Right)
		}
		if !strings.HasPrefix(string(r.Right), strings.SplitN(string(r.Right), ".", 2)[0]+".") {
			t.Errorf("%s: not scope.verb", r.Right)
		}
		switch r.Scope {
		case ScopeProject:
			if !ValidProjectRole(r.Recommend) {
				t.Errorf("%s: recommended for %q, not a project preset", r.Right, r.Recommend)
			}
		case ScopeOrg:
			if !ValidOrgRole(r.Recommend) {
				t.Errorf("%s: recommended for %q, not an organization preset", r.Right, r.Recommend)
			}
		case ScopeInstance, ScopeAccount:
		default:
			t.Errorf("%s: unknown scope %q", r.Right, r.Scope)
		}
	}
	// Presets are nested: every role has what the role below it has.
	order := ProjectRoles()
	for i := 0; i+1 < len(order); i++ {
		for r := range PresetRights(order[i+1]) {
			if !PresetRights(order[i]).Has(r) {
				t.Errorf("%s has %s, %s does not", order[i+1], r, order[i])
			}
		}
	}
	// DEC-49: roles and members, AI keys and budget are human rights.
	for _, r := range []Right{RightRolesGrant, RightRolesGrantAny, RightOrgMembers, RightOrgAI, RightInstanceAI, RightAccountSecurity} {
		if !slices.Contains(HumanRights(), r) {
			t.Errorf("%s is not on the human list", r)
		}
	}
	if (RoleInfo{Role: RoleGuest, CanReview: true}).Has(RightKnowledgeVerify) {
		t.Error("the review flag gives a guest the verify right")
	}
	if !(RoleInfo{Role: RoleMember, CanReview: true}).Has(RightKnowledgeVerify) {
		t.Error("the review flag does not give a member the verify right")
	}
	if (RoleInfo{Role: RoleOwner}).Has("no.such") {
		t.Error("an unknown right is granted")
	}
}

func TestHumanRightsNeedSignIn(t *testing.T) {
	web := Principal{ID: "person:1", TokenKind: WebSessionKind}
	token := Principal{ID: "person:1", TokenKind: "machine"}
	for _, r := range HumanRights() {
		if humanOK(token, r) {
			t.Errorf("%s usable with a token", r)
		}
		if !humanOK(web, r) {
			t.Errorf("%s refused from a sign-in", r)
		}
	}
	if !humanOK(token, RightPackageManage) {
		t.Error("package.manage refused for a token")
	}
}

// guardPatterns are the forms of a rights decision that may only appear in
// rights.go: ranks, role names compared, the admin flag and the review flag
// read as a right.
var guardPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b[Rr]oleRank\(|\borgRoleRank\(`),
	regexp.MustCompile(`(==|!=)\s*(store\.)?(Role(Owner|Lead|Member|Guest)|Org(Owner|Member))\b`),
	regexp.MustCompile(`\b(store\.)?(Role(Owner|Lead|Member|Guest)|Org(Owner|Member))\s*(==|!=)`),
	regexp.MustCompile(`(\.Role|[rR]ole)\s*(==|!=)\s*"(owner|lead|member|guest)"`),
	regexp.MustCompile(`\bisServerAdminTx\(`),
	regexp.MustCompile(`CanReview\s*(\|\||&&)|(\|\||&&)\s*[\w.()]*\bCanReview\b`),
}

// TestNoRoleComparisonsOutsideCatalog fails when code in internal/store,
// internal/web or internal/server decides by a role rank or a role name
// instead of a named right (AC-722.2).
func TestNoRoleComparisonsOutsideCatalog(t *testing.T) {
	root := filepath.Join("..", "..")
	var bad []string
	for _, dir := range []string{"internal/store", "internal/web", "internal/server"} {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel := filepath.ToSlash(strings.TrimPrefix(f, root+string(filepath.Separator)))
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for n, line := range strings.Split(stripComments(t, f, b), "\n") {
				for _, re := range guardPatterns {
					if !re.MatchString(line) {
						continue
					}
					// rights.go is the catalog; ai_connections.go defines the admin
					// lookup instanceCanTx asks.
					if rel == "internal/store/rights.go" || (rel == "internal/store/ai_connections.go" && strings.Contains(line, "func isServerAdminTx(")) {
						continue
					}
					bad = append(bad, rel+":"+strconv.Itoa(n+1)+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("decide through a named right (rights.go), not by role or rank:\n%s", strings.Join(bad, "\n"))
	}
}

// stripComments blanks comments so that prose about roles does not count;
// line numbers stay.
func stripComments(t *testing.T, name string, src []byte) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	out := []byte(string(src))
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			s, e := fset.Position(c.Pos()).Offset, fset.Position(c.End()).Offset
			for i := s; i < e; i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	return string(out)
}
