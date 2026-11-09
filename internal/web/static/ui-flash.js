// A form the server did not accept comes back with a red note and with what was typed.
// Two jobs, both small:
//  1. every form that posts carries the address of the page it is on (gt_back), so the
//     server can send the person back here instead of leaving them on the POST address;
//  2. after a rejection the server writes the fields into #gt-flash; the form the
//     note belongs to gets its text back, the note moves over it, the field in question
//     is marked and has the focus.
(() => {
  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || (form.method || "").toLowerCase() !== "post") return;
    let back = form.querySelector('input[name="gt_back"]');
    if (!back) {
      back = document.createElement("input");
      back.type = "hidden";
      back.name = "gt_back";
      form.appendChild(back);
    }
    back.value = location.pathname + location.search;
  }, true);

  const block = document.getElementById("gt-flash");
  const note = document.getElementById("gt-flashnote");
  if (!block) return;
  let data;
  try { data = JSON.parse(block.textContent); } catch (_) { return; }
  const fields = data.fields || {};

  const sameForm = (form) => {
    let path = "";
    try { path = new URL(form.getAttribute("action") || "", location.href).pathname; } catch (_) { return false; }
    if (path !== data.action) return false;
    // Forms with the same action (a row each): the fixed fields tell them apart.
    for (const input of form.querySelectorAll('input[type="hidden"]')) {
      if (input.name === "csrf_token" || input.name === "gt_back" || !(input.name in fields)) continue;
      if (!fields[input.name].includes(input.value)) return false;
    }
    return true;
  };
  const form = [...document.forms].find(sameForm);

  if (form) {
    // A composer's mode and signal are menus over a hidden field: pick the same entry again,
    // so the fields that belong to that mode are there before the text goes in.
    for (const hidden of form.querySelectorAll("input[data-mode-input], input[data-pick-input]")) {
      const want = (fields[hidden.name] || [])[0];
      if (!want || hidden.value === want) continue;
      const scope = hidden.hasAttribute("data-pick-input") ? hidden.closest(".dd") : hidden.closest(".cx");
      const entry = scope && [...scope.querySelectorAll(".opt[data-val]")].find((el) => el.dataset.val === want);
      if (entry) entry.click();
    }
    const written = [];
    for (const name of Object.keys(fields)) {
      const values = fields[name];
      const controls = [...form.elements].filter((el) => el.name === name && el.type !== "hidden" && el.type !== "file" && el.type !== "submit" && el.type !== "button");
      controls.forEach((el, i) => {
        if (el.type === "checkbox") el.checked = values.includes(el.value);
        else if (el.type === "radio") el.checked = values[0] === el.value;
        else if (i < values.length) el.value = values[i];
        else return;
        written.push(el);
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    }
    const named = data.field ? form.elements[data.field] : null;
    const target = (named && named.type !== "hidden" ? named : null) || written.find((el) => el.tagName === "TEXTAREA") || written[0] || form.querySelector("textarea, input:not([type=hidden])");
    if (named && named.type !== "hidden") named.setAttribute("aria-invalid", "true");
    // The form may sit in a closed <details> (an edit form): open it so the text is seen.
    for (let el = form.parentElement; el; el = el.parentElement) if (el.tagName === "DETAILS") el.open = true;
    // A composer sits in a row with its avatar: the note goes over the row, not into it.
    if (note) (form.closest(".cx-cmt") || form).before(note);
    if (target) target.focus({ preventScroll: true });
    (note || form).scrollIntoView({ block: "center" });
    if (target) target.addEventListener("input", () => target.removeAttribute("aria-invalid"), { once: true });
  } else if (note) {
    note.scrollIntoView({ block: "nearest" });
  }
  block.remove();
})();
