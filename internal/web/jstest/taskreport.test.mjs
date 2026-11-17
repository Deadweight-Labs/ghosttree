// REQ-685 AC-2: the note typed into the field of a task page goes along with "Report anyway" and with the plain
// report (static/ui-parts.js). The page is what the template renders (fixtures/task-report.html, written by
// TestReportCarriesTheNoteThatWasTyped in task_report_note_test.go).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { loadPage, Event } from "./dom.mjs";

const html = readFileSync(new URL("./fixtures/task-report.html", import.meta.url), "utf8");

function page() {
  const p = loadPage(html, { scripts: ["ui-parts.js"] });
  return { ...p, area: p.document.querySelector('form[data-draft] textarea[name="note"]'), report: p.document.querySelector(".treport form") };
}
const submit = (form) => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
const sent = (form) => [...form.querySelectorAll("input")].filter((i) => i.getAttribute("name") === "note");

test("Report anyway sends the note that was typed", () => {
  const p = page();
  assert.ok(p.report, "the form of the report is on the page");
  assert.equal(p.report.querySelector('input[name="confirm_open"]').value, "1", "it is the form that answers the question about the open steps");
  p.area.value = "  The key is in place, rights 0600.  ";
  submit(p.report);
  const notes = sent(p.report);
  assert.equal(notes.length, 1, "one field with the note");
  assert.equal(notes[0].value, "The key is in place, rights 0600.");
  assert.equal(notes[0].getAttribute("type"), "hidden");
  assert.equal(p.area.value, "  The key is in place, rights 0600.  ", "the field of the page keeps what was typed");
});

test("the note of a second try replaces the first and an empty field sends none", () => {
  const p = page();
  p.area.value = "first";
  submit(p.report);
  p.area.value = "second";
  submit(p.report);
  assert.deepEqual(sent(p.report).map((i) => i.value), ["second"]);
  p.area.value = "   ";
  submit(p.report);
  assert.equal(sent(p.report).length, 0, "nothing typed: nothing sent");
});
