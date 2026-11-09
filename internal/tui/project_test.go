package tui

import (
	"strings"
	"testing"
)

// noRawHTTP fails when a status code or HTTP wording reaches the screen.
func noRawHTTP(t *testing.T, where, view string) {
	t.Helper()
	for _, bad := range []string{"HTTP", "400", "404", "project_required", "GET /api"} {
		if strings.Contains(view, bad) {
			t.Fatalf("%s shows %q:\n%s", where, bad, view)
		}
	}
}

func TestFolderWithUnknownRemoteOffersToCreateTheProject(t *testing.T) {
	f := newFake().inFolder("unknown")
	m := ready(f, Options{}, 110, 44, darkBG)
	v := plainView(m)
	for _, want := range []string{"Kein Projekt in diesem Ordner", "/home/robin/side", "someone/side ist ghosttree nicht bekannt", "Ordner als Projekt anlegen", "Anderen Ordner wählen…", "Projekte anzeigen", "ctx project claim github.com/someone/side"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	if m.zone != zoneButtons || m.button != btnClaim {
		t.Fatalf("focus zone %d button %d", m.zone, m.button)
	}
	noRawHTTP(t, "unknown remote", v)
}

func TestFolderWithoutRemoteSaysWhatIsMissing(t *testing.T) {
	f := newFake().inFolder("noremote")
	m := ready(f, Options{}, 110, 44, darkBG)
	v := plainView(m)
	if !strings.Contains(v, "kein Git-Remote") || strings.Contains(v, "Ordner als Projekt anlegen") {
		t.Fatalf("view:\n%s", v)
	}
	if m.button != btnDir {
		t.Fatalf("focus on button %d, want the folder chooser", m.button)
	}
	m.moveButton(1)
	m.moveButton(1)
	if m.button == btnClaim {
		t.Fatal("focus landed on the missing claim button")
	}
	if cmd := m.activateButton(btnClaim); cmd != nil || m.screen != ScreenHome {
		t.Fatal("claim without remote did something")
	}
	// The request list says why it is empty instead of listing every project.
	m.zone = zoneList
	m = drive(m, key("tab"))
	if v := plainView(m); !strings.Contains(v, "kein Projekt in diesem Ordner") {
		t.Fatalf("requests tab:\n%s", v)
	}
	noRawHTTP(t, "no remote", plainView(m))
}

func TestFolderWithoutGit(t *testing.T) {
	f := newFake().inFolder("nogit")
	m := ready(f, Options{}, 110, 44, darkBG)
	v := plainView(m)
	if !strings.Contains(v, "kein Git-Repository") || !strings.Contains(v, "/home/robin/Downloads") {
		t.Fatalf("view:\n%s", v)
	}
	if !strings.Contains(v, "TASK-133") {
		t.Fatalf("own tasks should still show:\n%s", v)
	}
	noRawHTTP(t, "no git", v)
}

func TestProjectWithoutRightsSaysSo(t *testing.T) {
	f := newFake().inFolder("forbidden")
	m := ready(f, Options{}, 110, 44, darkBG)
	v := plainView(m)
	if strings.Contains(v, "Kein Projekt in diesem Ordner") {
		t.Fatalf("a known project is not 'no project':\n%s", v)
	}
	if !strings.Contains(v, "keine Rechte in diesem Projekt") {
		t.Fatalf("view:\n%s", v)
	}
	noRawHTTP(t, "forbidden", v)
	// The rest keeps working: search and settings.
	m = typeText(m, "REQ-619")
	m = drive(m, key("enter"))
	if m.screen != ScreenCard || m.cardErr != nil {
		t.Fatalf("search: screen %v err %v", m.screen, m.cardErr)
	}
}

func TestEveryProblemHasWords(t *testing.T) {
	m := ready(newFake(), Options{}, 110, 44, darkBG)
	for kind, want := range map[Problem]string{
		ProblemNoProject: "kein Projekt in diesem Ordner",
		ProblemForbidden: "keine Rechte in diesem Projekt",
		ProblemOffline:   "Server nicht erreichbar",
		ProblemSignedOut: "nicht angemeldet",
		ProblemOther:     "konnte nicht geladen werden",
	} {
		m = drive(m, listMsg{which: listTasks, err: &ProblemError{Kind: kind}})
		m.zone, m.section = zoneList, listTasks
		if v := plainView(m); !strings.Contains(v, want) {
			t.Errorf("kind %d: missing %q", kind, want)
		}
	}
}

func TestOfflineProjectCheckIsNotNoProject(t *testing.T) {
	f := newFake()
	f.checkErr = &ProblemError{Kind: ProblemOffline}
	m := ready(f, Options{}, 110, 44, darkBG)
	if strings.Contains(plainView(m), "Kein Projekt in diesem Ordner") {
		t.Fatal("an unreachable server made the folder look projectless")
	}
}

func TestCreateProjectAsksWithNameAndOrganization(t *testing.T) {
	f := newFake().inFolder("unknown")
	m := ready(f, Options{}, 110, 44, darkBG)
	m = drive(m, key("enter"))
	if m.screen != ScreenClaim {
		t.Fatalf("screen %v", m.screen)
	}
	v := plainView(m)
	for _, want := range []string{"github.com/someone/side", "Deadweight Labs", "Side Org", "github.com/someone/side in deadweight-labs anlegen?", "ctx project claim github.com/someone/side --org deadweight-labs"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	if len(f.claimed) != 0 {
		t.Fatal("created before the confirmation")
	}
	m = drive(m, key("left"))
	if m.claimOrg() != "side-org" || !strings.Contains(plainView(m), "Anlegen braucht Owner") {
		t.Fatalf("org %q view %s", m.claimOrg(), plainView(m))
	}
	m = drive(m, key("right"))
	m = drive(m, key("enter"))
	if len(f.claimed) != 1 || f.claimed[0] != "github.com/someone/side@deadweight-labs" {
		t.Fatalf("claimed %v", f.claimed)
	}
	if m.screen != ScreenHome || !strings.Contains(m.flash, "gehört jetzt zu deadweight-labs") {
		t.Fatalf("screen %v flash %q", m.screen, m.flash)
	}
	if strings.Contains(plainView(m), "Kein Projekt in diesem Ordner") {
		t.Fatalf("still no project after creating it:\n%s", plainView(m))
	}
}

func TestCreateProjectRefusalIsExplained(t *testing.T) {
	f := newFake().inFolder("unknown")
	f.claimErr = &ClaimError{Code: "not_org_owner"}
	m := ready(f, Options{}, 110, 44, darkBG)
	m = drive(m, key("enter"))
	m = drive(m, key("enter"))
	if m.screen != ScreenClaim || !strings.Contains(plainView(m), "Nur Owner der Organisation können Projekte anlegen") {
		t.Fatalf("screen %v view %s", m.screen, plainView(m))
	}
	m = drive(m, key("esc"))
	if m.screen != ScreenHome {
		t.Fatalf("esc: screen %v", m.screen)
	}
}

func TestChooseAnotherFolder(t *testing.T) {
	f := newFake()
	home := f.local // the repository the fake starts in
	f.inFolder("nogit")
	f.recent = []string{"/home/robin/ghosttree", "/home/robin/side"}
	f.dirs = map[string]Local{"/home/robin/ghosttree": home}
	m := ready(f, Options{}, 110, 44, darkBG)
	m = drive(m, key("enter")) // focus is on "choose folder"
	if m.screen != ScreenDirs {
		t.Fatalf("screen %v", m.screen)
	}
	v := plainView(m)
	if !strings.Contains(v, "/home/robin/ghosttree") || !strings.Contains(v, "cd /home/robin/ghosttree && ctx") {
		t.Fatalf("dirs view:\n%s", v)
	}
	m = typeText(m, "/home/robin/gh")
	m = drive(m, key("tab"))
	if got := m.dirs.input.Value(); got != "/home/robin/ghosttree/" {
		t.Fatalf("completion %q", got)
	}
	m = drive(m, key("enter"))
	if len(f.changedTo) != 1 || f.changedTo[0] != "/home/robin/ghosttree" {
		t.Fatalf("changed to %v", f.changedTo)
	}
	if m.screen != ScreenHome || strings.Contains(plainView(m), "Kein Projekt in diesem Ordner") {
		t.Fatalf("after change: screen %v view %s", m.screen, plainView(m))
	}
	if !strings.Contains(plainView(m), "deadweight-labs/ghosttree") {
		t.Fatalf("project of the new folder not shown:\n%s", plainView(m))
	}
}

func TestChooseFolderFromRecentList(t *testing.T) {
	f := newFake()
	home := f.local
	f.inFolder("nogit")
	f.recent = []string{"/home/robin/elsewhere", "/home/robin/ghosttree"}
	f.dirs = map[string]Local{"/home/robin/ghosttree": home}
	m := ready(f, Options{}, 110, 44, darkBG)
	m = drive(m, key("enter"))
	m = drive(m, key("down"))
	m = drive(m, key("enter"))
	if len(f.changedTo) != 1 || f.changedTo[0] != "/home/robin/ghosttree" {
		t.Fatalf("changed to %v", f.changedTo)
	}
	// A folder that is gone stays on the chooser with a reason.
	f2 := newFake().inFolder("nogit")
	f2.recent = []string{"/gone"}
	m = ready(f2, Options{}, 110, 44, darkBG)
	m = drive(m, key("enter"))
	m = drive(m, key("enter"))
	if m.screen != ScreenDirs || m.dirs.err == nil {
		t.Fatalf("screen %v err %v", m.screen, m.dirs.err)
	}
}

func TestShowProjectsWithPreviewAndBrowser(t *testing.T) {
	f := newFake().inFolder("nogit")
	m := ready(f, Options{}, 110, 44, darkBG)
	m.moveButton(1) // from "choose folder" to "show projects"
	m = drive(m, key("enter"))
	if m.screen != ScreenProjects {
		t.Fatalf("screen %v", m.screen)
	}
	v := plainView(m)
	for _, want := range []string{"deadweight-labs/design-system", "deadweight-labs/ghosttree", "REQ-7", "Look ghosttree freigeben", "ctx project list"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	m = drive(m, key("down"))
	if v := plainView(m); !strings.Contains(v, "REQ-619") || !strings.Contains(v, "TASK-133") {
		t.Fatalf("preview of the second project:\n%s", v)
	}
	m = drive(m, key("enter"))
	if got := f.opened[len(f.opened)-1]; got != "https://ghosttree.example.com/ui/deadweight-labs/ghosttree" {
		t.Fatalf("opened %q", got)
	}
}

func TestProjectsFromSettingsFreeHome(t *testing.T) {
	// With a project the panel stays away and tab cycles only the lists.
	m := ready(newFake(), Options{}, 110, 44, darkBG)
	if m.noProject() || strings.Contains(plainView(m), "Kein Projekt") {
		t.Fatal("panel shown for a known project")
	}
	for i := 0; i < 4; i++ {
		m = drive(m, key("tab"))
		if m.zone != zoneList {
			t.Fatal("tab reached buttons that are not shown")
		}
	}
}
