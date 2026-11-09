// A button that names the fields it waits for (data-needs: selectors) reads as
// blocked until each of them is filled in or ticked. A blocked button stays
// clickable: the click says what is missing (the hint of the form, the empty field)
// instead of doing nothing. Without script the form works as before, the browser
// checks "required". No inline script, no inline style.
(() => {
  for (const button of document.querySelectorAll("button[data-needs]")) {
    const form = button.closest("form");
    if (!form) continue;
    const fields = [...form.querySelectorAll(button.dataset.needs)];
    const hint = form.querySelector(".chkhint");
    const filled = (f) => (f.type === "checkbox" ? f.checked : f.value.trim() !== "");
    const ready = () => fields.every(filled);
    const sync = () => {
      button.setAttribute("aria-disabled", ready() ? "false" : "true");
      if (hint && fields.every((f) => f.type !== "checkbox" || f.checked)) hint.hidden = true;
    };
    button.addEventListener("click", (event) => {
      if (ready()) return;
      event.preventDefault();
      const missing = fields.find((f) => !filled(f));
      if (hint && missing && missing.type === "checkbox") hint.hidden = false;
      if (missing) missing.focus();
    });
    for (const f of fields) { f.addEventListener("input", sync); f.addEventListener("change", sync); }
    sync();
  }
})();
