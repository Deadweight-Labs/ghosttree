package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"
)

// TestWriteSnapshots writes the screens as ANSI files for design review when
// GHOSTTREE_TUI_SNAPSHOTS names a directory; otherwise it is skipped.
func TestWriteSnapshots(t *testing.T) {
	dir := os.Getenv("GHOSTTREE_TUI_SNAPSHOTS")
	if dir == "" {
		t.Skip("set GHOSTTREE_TUI_SNAPSHOTS to write the screens")
	}
	write := func(name, content string) {
		// Pad every line to the full width so the picture keeps the
		// terminal's proportions.
		lines := strings.Split(content, "\n")
		for i, l := range lines {
			width := 104
			if strings.HasPrefix(name, "frame-") {
				width = 24
			}
			if w := lipgloss.Width(l); w < width {
				lines[i] = l + strings.Repeat(" ", width-w-1) + "\u2800"
			}
		}
		content = strings.Join(lines, "\n")
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backgrounds := []struct {
		name string
		msg  tea.BackgroundColorMsg
	}{{"dark", darkBG}, {"light", lightBG}}
	for _, look := range []LogoStyle{LogoMark, LogoTree, LogoLines} {
		for _, bg := range backgrounds {
			m := ready(newFake(), Options{Look: look}, 104, 36, bg.msg)
			write("home-"+look.String()+"-"+bg.name, m.View().Content)
			for f := 0; f <= logoFrames; f++ {
				l := logo{style: look, theme: m.th, frame: f}
				write(fmt.Sprintf("frame-%s-%s-%02d", look, bg.name, f), l.View())
			}
		}
	}
	for _, bg := range backgrounds {
		f := newFake().inFolder("unknown")
		m := ready(f, Options{}, 104, 36, bg.msg)
		write("noproject-"+bg.name, m.View().Content)
		m = drive(m, key("enter"))
		write("claim-"+bg.name, m.View().Content)
		f = newFake().inFolder("nogit")
		m = ready(f, Options{}, 104, 36, bg.msg)
		write("nogit-"+bg.name, m.View().Content)
		m.moveButton(1)
		m = drive(m, key("enter"))
		write("projects-"+bg.name, m.View().Content)
	}
	for _, bg := range backgrounds {
		f := newFake()
		m := ready(f, Options{}, 104, 36, bg.msg)
		m = drive(m, key("ctrl+s"))
		for i := 0; i < 3; i++ {
			m = drive(m, key("down"))
		}
		write("settings-"+bg.name, m.View().Content)
		f.waitBlock = true
		m = ready(f, Options{Start: ScreenLogin, LoginServer: "https://ghosttree.example.com", LoginMachine: "workstation", LoginDirect: true}, 104, 36, bg.msg)
		m = runCmd(m, m.startLogin(), 0)
		write("login-"+bg.name, m.View().Content)
		f.waitBlock = false
		m = ready(f, Options{Start: ScreenLogin, LoginServer: "https://ghosttree.example.com", LoginMachine: "workstation"}, 104, 36, bg.msg)
		write("loginform-"+bg.name, m.View().Content)
		m = ready(f, Options{}, 104, 36, bg.msg)
		m = typeText(m, "REQ-619")
		m = drive(m, key("enter"))
		write("card-"+bg.name, m.View().Content)
	}
}
