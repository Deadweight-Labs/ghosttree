// Filter of a list: the text of the field and the choices of the menus narrow
// the rows named in data-list-filter (a selector); a group (data-list-group) with
// no row left is hidden, its [data-list-count] shows how many rows are left, and a
// folded details group opens while a hit is in it. A menu is a details.dd[data-pick="<key>"] whose buttons
// carry data-value; a row matches when its data-<key> equals the chosen value.
// A list that is narrowed to nothing shows [data-list-none] (its data-fmt with {q} in
// place of the text of the field, and a button [data-list-clear] that empties the
// field and the menus); [data-list-n] counts the rows left (data-one is the text
// for one row; the other text carries a number, which is replaced).
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
    const left = document.querySelectorAll(`${selector}:not([hidden])`).length;
    const narrowedNow = q !== "" || Object.values(chosen).some((v) => v !== "");
    for (const el of document.querySelectorAll("[data-list-n]")) {
      if (el.dataset.many === undefined) el.dataset.many = el.textContent;
      el.textContent = left === 1 && el.dataset.one ? el.dataset.one : el.dataset.many.replace(/\d+/, String(left));
    }
    for (const none of document.querySelectorAll("[data-list-none]")) {
      none.hidden = !(narrowedNow && left === 0);
      const text = none.querySelector("[data-list-none-text]");
      if (text && !none.hidden) text.textContent = (none.dataset.fmt || "").replace("{q}", input ? input.value.trim() : "");
    }
    for (const group of document.querySelectorAll("[data-list-group]")) {
      const shown = group.querySelectorAll(`${selector}:not([hidden])`).length;
      group.hidden = shown === 0;
      const count = group.querySelector("[data-list-count]");
      if (count) count.textContent = String(shown);
      // A folded group (details) with a hit opens while the list is narrowed and
      // goes back to what it was when the narrowing ends.
      if (group.tagName === "DETAILS") {
        const narrowed = q !== "" || Object.values(chosen).some((v) => v !== "");
        if (group.dataset.wasOpen === undefined) group.dataset.wasOpen = String(group.open);
        group.open = narrowed ? shown > 0 : group.dataset.wasOpen === "true";
      }
    }
  };
  if (input) input.addEventListener("input", refresh);
  for (const clear of document.querySelectorAll("[data-list-clear]")) {
    clear.addEventListener("click", () => {
      if (input) {
        input.value = "";
        input.focus();
      }
      for (const key of Object.keys(chosen)) chosen[key] = "";
      for (const menu of menus) {
        menu.classList.remove("on");
        const label = menu.querySelector("[data-pick-label]");
        if (label && menu.dataset.allLabel !== undefined) label.textContent = menu.dataset.allLabel;
        for (const b of menu.querySelectorAll("button[data-value]")) b.setAttribute("aria-pressed", String(b.dataset.value === ""));
      }
      refresh();
    });
  }
  for (const menu of menus) {
    const key = menu.dataset.pick;
    const label = menu.querySelector("[data-pick-label]");
    const all = label ? label.textContent : "";
    menu.dataset.allLabel = all;
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
