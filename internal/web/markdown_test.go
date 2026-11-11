package web

import (
	"os/exec"
	"strings"
	"testing"
)

func kinds(blocks []bodyBlock) string {
	var out []string
	for _, b := range blocks {
		k := b.Kind
		if k == "" {
			k = "p"
		}
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

func TestBodyBlocksReadEveryKindOfBlock(t *testing.T) {
	src := "# Ziel\n\nEin Absatz\nzweite Zeile\n\n- eins\n- zwei\n  - innen\n\n1. a\n2. b\n\n> Zitat\n> weiter\n\n| A | B |\n|---|:-:|\n| 1 | 2 |\n\n---\n\n```sh\nls\n\nls -l\n```\nNach dem Code"
	got := bodyBlocks(src, 0)
	if want := "h,p,list,quote,table,hr,code,p"; kinds(got) != want {
		t.Fatalf("kinds = %s, want %s\n%+v", kinds(got), want, got)
	}
	if got[0].Level != 1 || got[0].Text != "Ziel" {
		t.Errorf("heading = %+v", got[0])
	}
	if got[1].Text != "Ein Absatz\nzweite Zeile" {
		t.Errorf("paragraph = %q", got[1].Text)
	}
	if !got[6].Code || got[6].Text != "ls\n\nls -l" {
		t.Errorf("a fence keeps its blank lines in one block: %+v", got[6])
	}
	// a list goes on over a blank line to the next item, even of another kind;
	// the script splits it when it draws it. The blocks joined by a blank line
	// are the source the script renders.
	if mdSource(got) != strings.Replace(src, "```\nNach", "```\n\nNach", 1) {
		t.Errorf("source = %q", mdSource(got))
	}
}

func TestBodyBlocksLeaveTableAndRuleLookAlikesAlone(t *testing.T) {
	// a bar in a line is no table without the separator line under it
	if got := bodyBlocks("a | b\nc | d", 0); kinds(got) != "p" {
		t.Errorf("kinds = %s", kinds(got))
	}
	// the number of cells must match
	if got := bodyBlocks("| a | b |\n|---|", 0); kinds(got) == "table" {
		t.Error("a separator with another number of cells made a table")
	}
	// #1970 at the start of a line is no heading
	if got := bodyBlocks("#1970 hängt hier", 0); kinds(got) != "p" {
		t.Errorf("kinds = %s", kinds(got))
	}
	// an unclosed fence runs to the end of the text
	if got := bodyBlocks("```\nopen", 0); len(got) != 1 || !got[0].Code || got[0].Text != "open" {
		t.Errorf("unclosed fence = %+v", got)
	}
}

func TestMdBlocksCarryTheSourceAndEscapeEverything(t *testing.T) {
	src := "## <img src=x onerror=alert(1)>\n\n<script>alert(1)</script> [x](javascript:alert(1)) \"q\" & `<b>`\n\n- <i>a</i>\n\n| <u>h</u> |\n|---|\n| <s>c</s> |"
	got := string(mdText(src))
	for _, bad := range []string{"<script>", "<img", "<i>", "<u>", "<s>", "<b>", `href="javascript`} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains %q: %s", bad, got)
		}
	}
	for _, want := range []string{`data-md="## &lt;img src=x onerror=alert(1)&gt;`, "<h2>&lt;img src=x onerror=alert(1)&gt;</h2>", "&lt;script&gt;alert(1)&lt;/script&gt;", "<code>&lt;b&gt;</code>", "<li>&lt;i&gt;a&lt;/i&gt;</li>", "<th>&lt;u&gt;h&lt;/u&gt;</th>", "<td>&lt;s&gt;c&lt;/s&gt;</td>"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestMdBlocksWriteListsAndTablesForAPageWithoutScript(t *testing.T) {
	got := string(mdText("- eins\n  - innen\n- zwei\n\n1. a\n2. b\n\n- [x] fertig\n- [ ] offen\n\n| A | B |\n|---|---|\n| 1 | 2 | 3 |\n| 4 |"))
	for _, want := range []string{
		"<ul><li>eins<ul><li>innen</li></ul></li><li>zwei</li></ul>",
		"<ol><li>a</li><li>b</li></ol>",
		"<li>☑ fertig</li><li>☐ offen</li>",
		"<tr><td>1</td><td>2</td></tr>", // a row longer than the head loses its extra cell
		"<tr><td>4</td><td></td></tr>",  // a shorter one is filled up
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestMdBlocksMarkMentionsLikeTheScript(t *testing.T) {
	got := string(mdText("@claude:mainex schau (@Robin) a@b.de"))
	for _, want := range []string{`<span class="mention">@claude:mainex</span>`, `<span class="mention">@Robin</span>`, "a@b.de"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, `<span class="mention">@b.de`) {
		t.Errorf("an address was taken for a mention: %s", got)
	}
}

func TestAnchorsCountUpUntilFreeAndMatchTheScript(t *testing.T) {
	got := string(mdBlocksOpt(bodyBlocks("# Ziel\n\n## Ziel\n\n### Ziel!\n\n## Über **Größe** & `Code`\n\n## !!!", 0), mdOptions{Anchors: true}))
	for _, want := range []string{`id="h-ziel"`, `id="h-ziel-2"`, `id="h-ziel-3"`, `id="h-über-größe-code"`, `id="h-section"`, ` data-md-anchors`} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(mdText("# Ziel")), ` id="`) {
		t.Error("a text without anchors gives its headings an address")
	}
}

func TestWithoutTitleHeadingDropsOnlyTheHeadingThatSaysTheTitle(t *testing.T) {
	for _, c := range []struct{ src, title, want string }{
		{"# Plan\n\nText", "Plan", "Text"},
		{"\n# plan **eins**\n\nText", "Plan eins", "Text"},
		{"# Anders\n\nText", "Plan", "# Anders\n\nText"},
		{"## Plan\n\nText", "Plan", "## Plan\n\nText"},
		{"Text\n\n# Plan", "Plan", "Text\n\n# Plan"},
	} {
		if got := withoutTitleHeading(c.src, c.title); got != c.want {
			t.Errorf("withoutTitleHeading(%q, %q) = %q, want %q", c.src, c.title, got, c.want)
		}
	}
}

func TestExcerptReadsMarkdownAsWords(t *testing.T) {
	for src, want := range map[string]string{
		"## Ziel\n\nText":                   "Ziel",
		"- **Fett** und `code`\n- zwei":     "Fett und code zwei",
		"> Zitat mit __Gewicht__":           "Zitat mit Gewicht",
		"```\ncode only\n```":               "code only",
		"1. erster\n2. zweiter":             "erster zweiter",
		"- [x] erledigt":                    "erledigt",
		"Absatz mit ~~gestrichen~~ Wort.":   "Absatz mit gestrichen Wort.",
		"| a | b |\n|---|---|\n| 1 | 2 |\n": "| a | b |",
	} {
		if got := excerpt(src); got != want {
			t.Errorf("excerpt(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestSnippetBackticksBecomeCode(t *testing.T) {
	join := func(parts []partView) string {
		var b strings.Builder
		for _, p := range parts {
			if p.CodeStart {
				b.WriteString("<c>")
			}
			if p.Hit {
				b.WriteString("[" + p.Text + "]")
			} else {
				b.WriteString(p.Text)
			}
			if p.CodeEnd {
				b.WriteString("</c>")
			}
		}
		return b.String()
	}
	for _, c := range []struct {
		parts []partView
		want  string
	}{
		{[]partView{{Text: "run `ls"}, {Text: " -l", Hit: true}, {Text: "` now"}}, "run <c>ls[ -l]</c> now"},
		{[]partView{{Text: "a `b` c `d` e"}}, "a <c>b</c> c <c>d</c> e"},
		{[]partView{{Text: "ein ` ohne Partner"}}, "ein ` ohne Partner"},
		{[]partView{{Text: "`x` und ` offen"}}, "<c>x</c> und ` offen"},
	} {
		if got := join(codePieces(c.parts)); got != c.want {
			t.Errorf("codePieces = %q, want %q", got, c.want)
		}
	}
}

// The helpers of the script that need no browser: the end of a bare address and
// the cells of a table row.
func TestUiMdAddressAndTableHelpers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := string(mustReadEmbedded(t, "static/ui-md.js"))
	cut := func(from, to string) string {
		i, j := strings.Index(src, from), strings.Index(src, to)
		if i < 0 || j <= i {
			t.Fatalf("cannot cut %q", from)
		}
		return src[i:j]
	}
	program := cut("  function trimUrl(", "  function link(") + cut("  function cellsOf(", "  // A table starts at line i") + `
const eq = (a, b) => { if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(JSON.stringify(a) + " != " + JSON.stringify(b)); };
eq(trimUrl("https://x.org/a."), "https://x.org/a");
eq(trimUrl("https://x.org/a),"), "https://x.org/a");
eq(trimUrl("https://x.org/a_(b)"), "https://x.org/a_(b)");
eq(trimUrl("https://x.org/a?x=1&y=2!"), "https://x.org/a?x=1&y=2");
eq(trimUrl("https://x.org/**"), "https://x.org/");
eq(cellsOf("| a | b |"), ["a", "b"]);
eq(cellsOf("a | b | c"), ["a", "b", "c"]);
eq(cellsOf("| a \\| b | c |"), ["a | b", "c"]);
`
	if out, err := exec.Command("node", "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
