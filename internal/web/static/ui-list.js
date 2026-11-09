// Filter of a list: the text of the field and the choices of the menus narrow
// the rows named in data-list-filter (a selector); a group (data-list-group) with
// no row left is hidden. A menu is a details.dd[data-pick="<key>"] whose buttons
// carry data-value; a row matches when its data-<key> equals the chosen value.
// No inline script, no inline style.
(() => {
  const input = document.querySelector("input[data-list-filter]");
  const menus = [...document.querySelectorAll("details[data-pick]")];
  const selector = (input || menus[0])?.dataset.listFilter || menus[0]?.dataset.rows;
  if (!selector) return;
  const chosen = {};
  const refresh = () => {
    const q = input ? input.value.trim().toLocaleLowerCase() : "";
    for (const row of document.querySelectorAll(selector)) {
      let show = q === "" || row.textContent.toLocaleLowerCase().includes(q);
      for (const [key, value] of Object.entries(chosen)) {
        if (value !== "" && row.dataset[key] !== value) show = false;
      }
      row.hidden = !show;
    }
    for (const group of document.querySelectorAll("[data-list-group]")) {
      group.hidden = !group.querySelector(`${selector}:not([hidden])`);
    }
  };
  if (input) input.addEventListener("input", refresh);
  for (const menu of menus) {
    const key = menu.dataset.pick;
    const label = menu.querySelector("[data-pick-label]");
    const all = label ? label.textContent : "";
    menu.addEventListener("click", (event) => {
      const button = event.target.closest("button[data-value]");
      if (!button) return;
      chosen[key] = button.dataset.value;
      for (const other of menu.querySelectorAll("button[data-value]")) other.setAttribute("aria-pressed", String(other === button));
      if (label) label.textContent = button.dataset.value === "" ? all : button.textContent.trim();
      menu.classList.toggle("on", button.dataset.value !== "");
      menu.open = false;
      refresh();
    });
  }
})();
