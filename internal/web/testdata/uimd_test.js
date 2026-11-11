// Checks of static/ui-md.js that need a script runtime, run by uimd_perf_test.go:
//   node uimd_test.js <ui-md.js> <uimd_sample.md> <uimd_sample.html>
// Exit code 0 when everything holds, 1 with the failures on stderr.
"use strict";
const fs = require("fs");
const { load, html } = require("./uimd_harness.js");

const [, , scriptPath, samplePath, goldenPath] = process.argv;
const md = load(scriptPath);
const failures = [];
const fail = (m) => failures.push(m);

// Every input below made the first version of the renderer run for seconds to
// minutes (the sizes are the ones measured); each must now be done in well under
// the limit. The limit is far above the measured time so a slow machine does not fail it.
const LIMIT_MS = 1000;
const N = 60000;
const slow = {
  "300 KB of ' *a' (over the size limit)": " *a".repeat(100000),
  "60 KB of ' *a'": " *a".repeat(20000),
  "60 KB of ' _a'": " _a".repeat(20000),
  "60 KB of ' **a'": " **a".repeat(20000),
  "60 KB of '*a '": "*a ".repeat(20000),
  "bare address, then 60 KB of ')'": "https://x.org/" + ")".repeat(N),
  "bare address, then 30 KB of ').'": "https://x.org/" + ").".repeat(N / 2),
  "bare address, then ']' and ')' mixed": "https://x.org/" + "])".repeat(N / 2),
  "60 KB bare address": "https://x.org/" + "h/".repeat(N / 2),
  "'[' x 60 KB then ']'": "[".repeat(N) + "]",
  "'[a](' x 12 KB": "[a](".repeat(N / 5),
  "'[a](' then 60 KB of '('": "[a]" + "(".repeat(N),
  "a word, 60 KB of spaces, a word": "a" + " ".repeat(N) + "b",
  "heading, 60 KB of spaces, text": "# a" + " ".repeat(N) + "b",
  "heading, 60 KB of tabs, #": "# a" + "\t".repeat(N) + "#b",
  "table separator with 60 KB of spaces": "|a\n|-" + " ".repeat(N) + "x",
  "fence with 60 KB of spaces and a backtick": "```" + " ".repeat(N) + "`",
  "list, 60 KB of blank lines, an indented line": "- a" + "\n".repeat(N) + "  x",
  "table of 5000 columns and 5000 rows": "|a".repeat(5000) + "\n" + "|-".repeat(5000) + "\n" + "|\n".repeat(5000),
  "backtick runs of every length": Array.from({ length: 340 }, (_, i) => "`".repeat(i + 1) + "x").join(" "),
  "60 KB of '- '": "- ".repeat(N / 2),
  "60 KB of '> '": "> ".repeat(N / 2),
};
for (const [name, text] of Object.entries(slow)) {
  const t0 = process.hrtime.bigint();
  md.render(text, {});
  const ms = Number(process.hrtime.bigint() - t0) / 1e6;
  if (ms > LIMIT_MS) fail(`${name}: ${ms.toFixed(0)} ms, limit ${LIMIT_MS} ms`);
}

// A text over the size limit is not read as Markdown: one block of plain text.
{
  const text = "**not bold** [x](https://a.example) ".repeat(Math.ceil(md.maxLength / 30));
  const out = md.render(text, {});
  if (text.length <= md.maxLength) fail("the test text is not over the limit");
  if (out.children.length !== 1 || out.children[0].tag !== "pre" || out.children[0].textContent !== text) fail("a text over the limit is not shown as plain text");
  const edge = md.render("a".repeat(md.maxLength), {});
  if (edge.children[0].tag !== "p") fail("a text of exactly the limit is not rendered as Markdown");
}

// Bare addresses: over 2 KB none is made, at 2 KB one is; a direction mark stops the link.
{
  const render = (t) => html(md.render(t, {}));
  if (!render("see https://x.org/" + "a".repeat(2000)).includes("<a ")) fail("an address of 2 KB is not linked");
  if (render("see https://x.org/" + "a".repeat(2100)).includes("<a ")) fail("an address over 2 KB is linked");
  if (!render("<https://x.org/" + "a".repeat(2000) + ">").includes("<a ")) fail("an autolink of 2 KB is not linked");
  if (render("<https://x.org/" + "a".repeat(2100) + ">").includes("<a ")) fail("an autolink over 2 KB is linked");
  for (const mark of ["‪", "‫", "‬", "‭", "‮", "⁦", "⁧", "⁨", "⁩", "‎", "‏"]) {
    const name = "U+" + mark.charCodeAt(0).toString(16);
    if (md.safeUrl("https://good.example/" + mark + "evil.example") !== null) fail("safeUrl lets " + name + " pass");
    if (md.safeUrl("/ui/x" + mark) !== null) fail("safeUrl lets " + name + " pass in a relative address");
    for (const text of ["https://good.example/" + mark + "moc.live", "<https://good.example/" + mark + "x>", "[a](https://good.example/" + mark + "x)", "[a](/p" + mark + ")"]) {
      if (render(text).includes("<a ")) fail(name + " in an address still makes a link: " + JSON.stringify(text));
    }
  }
  if (md.safeUrl("https://good.example/ü") === null) fail("safeUrl refuses a plain letter");
}

// What the first version drew for a text with every element stays what it draws.
{
  const got = html(md.render(fs.readFileSync(samplePath, "utf8"), {})) + "\n";
  const want = fs.readFileSync(goldenPath, "utf8");
  if (got !== want) {
    let k = 0;
    while (k < got.length && got[k] === want[k]) k++;
    fail("the sample renders differently from the stored output, first at " + k + ":\n  got  ..." + got.slice(Math.max(0, k - 40), k + 80) + "\n  want ..." + want.slice(Math.max(0, k - 40), k + 80));
  }
}

// Closing marks are found as before: the cases where the old search had its quirks.
{
  const r = (t) => html(md.render(t, {}));
  const eq = (t, want) => { const got = r(t); if (got !== want) fail(`${JSON.stringify(t)}: ${got} != ${want}`); };
  eq("**a***", "<p><strong>a*</strong></p>");
  eq("***a**", "<p><strong>*a</strong></p>");
  eq("****a**", "<p><strong>**a</strong></p>");
  eq("*a **b* c**", "<p><em>a **b</em> c**</p>");
  eq("_a_b _c_", "<p><em>a_b _c</em></p>");
  eq("a *b *c", "<p>a *b *c</p>");
  eq("[a](b\nc)", "<p>[a](b<br></br>c)</p>");
  eq("[a [b] c](/p)", '<p><a href="/p">a [b] c</a></p>');
}

// The anchors of equal headings count up without starting at 2 again.
{
  const src = fs.readFileSync(scriptPath, "utf8");
  const a = src.indexOf("  function anchorOf(");
  const b = src.indexOf("  // The headings of a rendered text get their address");
  if (a < 0 || b <= a) fail("anchorOf is not independently testable");
  else {
    const program = src.slice(a, b) + "\nreturn anchorOf;";
    const anchorOf = new Function(program)();
    const used = {};
    const ids = ["A", "a", "a-2", "a", "a", "b"].map((t) => anchorOf(t, used)).join(" ");
    if (ids !== "h-a h-a-2 h-a-2-2 h-a-3 h-a-4 h-b") fail("anchors of equal headings: " + ids);
    const t0 = process.hrtime.bigint(), many = {};
    for (let k = 0; k < 20000; k++) anchorOf("same", many);
    const ms = Number(process.hrtime.bigint() - t0) / 1e6;
    if (ms > LIMIT_MS) fail("20000 equal headings: " + ms.toFixed(0) + " ms");
  }
}

// An address in the URL that is not valid percent-encoding must not throw.
{
  const src = fs.readFileSync(scriptPath, "utf8");
  const a = src.indexOf("  function hashId(");
  const b = src.indexOf("  // A text the server wrote as plain blocks");
  if (a < 0 || b <= a) fail("hashId is not independently testable");
  else {
    const program = src.slice(a, b) + "\nreturn [hashId()];";
    for (const [hash, want] of [["#%E0", "%E0"], ["#%", "%"], ["#h-a%20b", "h-a b"], ["#h-plain", "h-plain"], ["", ""], ["#", ""]]) {
      globalThis.location = { hash };
      let got;
      try { got = new Function(program)()[0]; } catch (e) { fail("hashId(" + JSON.stringify(hash) + ") throws " + e.message); continue; }
      if (got !== want) fail("hashId(" + JSON.stringify(hash) + ") = " + JSON.stringify(got) + ", want " + JSON.stringify(want));
    }
  }
}

if (failures.length) {
  console.error(failures.join("\n"));
  process.exit(1);
}
