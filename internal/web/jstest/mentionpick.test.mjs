// REQ-684 AC-4: the names offered after @ in the message box of a room (static/mentions.js) are the
// ones whose name begins with what was typed, people before agents, an agent marked as the agent of
// a person, and the first line, the one Enter takes, a person when one fits.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

// The list the server writes next to the box (templates/coord.html, "coord-suggest"): the agents
// stand first, as the page used to give them, and an agent's label carries its machine and branch.
const LIST = `<form><div class="cx-w"><textarea name="body"></textarea><ul class="comp-suggest" role="listbox" hidden>
<li role="option" data-handle="claude-jonas" data-agent="1" data-owner="Jonas Brandt"><span class="comp-suggest-name">Claude · Jonas Brandt · jonas-laptop · main</span><small>Agent von Jonas Brandt</small></li>
<li role="option" data-handle="claude-mara" data-agent="1" data-owner="Mara Keller"><span class="comp-suggest-name">Claude · Mara Keller · mara-laptop · main</span><small>Agent von Mara Keller</small></li>
<li role="option" data-handle="claude-pia" data-agent="1" data-owner="Pia Novak"><span class="comp-suggest-name">Claude · Pia Novak · pia-laptop · mainex</span><small>Agent von Pia Novak</small></li>
<li role="option" data-handle="jonas"><span class="comp-suggest-name">Jonas Brandt</span></li>
<li role="option" data-handle="mara"><span class="comp-suggest-name">Mara Keller (Tech-Lead)</span></li>
</ul></div></form>`;

function type(text) {
  const p = loadPage(LIST, { hooks: {} });
  p.window.HTMLTextAreaElement = p.document.querySelector("textarea").constructor;
  p.run("mentions.js");
  const area = p.document.querySelector("textarea");
  area.value = text;
  area.selectionStart = text.length;
  area.dispatchEvent(new p.window.Event("input", { bubbles: true }));
  const shown = [...p.document.querySelectorAll("[role=option]")].filter((i) => !i.hidden);
  return { p, area, shown, handles: shown.map((i) => i.getAttribute("data-handle")) };
}

test("@Ma offers the person first and the agent of that person after, not the agents of others through a machine name", () => {
  const { handles, shown } = type("Hallo @Ma");
  assert.deepEqual(handles, ["mara", "claude-mara"]);
  assert.equal(shown[0].getAttribute("aria-selected"), "true", "the person is the one Enter takes");
  assert.equal(shown[1].querySelector("small").textContent, "Agent von Mara Keller");
});

test("@Jo offers Jonas before the agent of Jonas, and Enter writes the person", () => {
  const { p, area, handles } = type("@Jo");
  assert.deepEqual(handles, ["jonas", "claude-jonas"]);
  const enter = new p.window.Event("keydown", { bubbles: true, cancelable: true, key: "Enter" });
  area.dispatchEvent(enter);
  assert.equal(area.value, "@jonas ");
});

test("a word from the middle of a name is found, the machine of an agent is not", () => {
  assert.deepEqual(type("@Keller").handles, ["mara", "claude-mara"]);
  assert.deepEqual(type("@mainex").handles, [], "mainex is a machine, nobody's name");
  assert.deepEqual(type("@laptop").handles, []);
});

test("without a hit the list stays closed, and with only agents the first agent is taken", () => {
  assert.deepEqual(type("@zzz").handles, []);
  assert.deepEqual(type("@Claude").handles, ["claude-jonas", "claude-mara", "claude-pia"]);
});
