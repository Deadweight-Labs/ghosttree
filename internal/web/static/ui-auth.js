// A button that names the fields it waits for (data-needs: selectors) stays
// disabled until each of them is filled in or ticked. Without script the form
// works as before. No inline script, no inline style.
(() => {
  for (const button of document.querySelectorAll("button[data-needs]")) {
    const form = button.closest("form");
    if (!form) continue;
    const fields = [...form.querySelectorAll(button.dataset.needs)];
    const ready = () => fields.every((f) => (f.type === "checkbox" ? f.checked : f.value.trim() !== ""));
    const sync = () => { button.disabled = !ready(); };
    for (const f of fields) { f.addEventListener("input", sync); f.addEventListener("change", sync); }
    sync();
  }
})();
