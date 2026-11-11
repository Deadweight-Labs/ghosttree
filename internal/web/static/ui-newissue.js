// New issue: add and remove requirement rows, show the fields of a new work
// package only when "new work package" is picked. Without this script the form
// still works: three requirement fields and the package fields are all there.
(() => {
  const $ = (s, c) => (c || document).querySelector(s);
  const $$ = (s, c) => [...(c || document).querySelectorAll(s)];

  const box = $("[data-reqs]");
  if (box) {
    const number = () => {
      let n = 0;
      for (const row of $$(".rq-row", box)) {
        const no = $(".no", row);
        if (no && $("input", row)) no.textContent = String(++n);
      }
    };
    const tpl = $("template[data-rq-tpl]", box);
    box.addEventListener("click", (event) => {
      const del = event.target.closest("[data-rq-del]");
      if (del) {
        del.closest(".rq-row").remove();
        number();
        return;
      }
      if (event.target.closest("[data-rq-add]") && tpl) {
        const row = tpl.content.firstElementChild.cloneNode(true);
        $(".rq-add", box).before(row);
        number();
        $("input", row).focus();
      }
    });
    number();
  }

  // Labels: the chosen ones show as pills above the menu that picks them.
  const menu = $(".lbmenu");
  if (menu) {
    menu.addEventListener("change", (event) => {
      const box = event.target;
      if (!(box instanceof HTMLInputElement) || box.name !== "label") return;
      for (const pill of $$("[data-lb]")) if (pill.dataset.lb === box.value) pill.hidden = !box.checked;
    });
    document.addEventListener("click", (event) => {
      if (menu.open && !menu.contains(event.target)) menu.open = false;
    });
  }

  const select = $("[data-pkg-select]");
  const fresh = $("[data-pkg-new]");
  if (select && fresh) {
    const sync = () => { fresh.hidden = select.value !== "new"; };
    select.addEventListener("change", sync);
    sync();
  }
})();
