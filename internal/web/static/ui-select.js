// Styled menus for native selects. Every select.gfield gets a button with a menu
// of its options next to it; the select stays the form control (value, required,
// change event, auto-submit) and is only moved out of sight once the menu exists.
// Without this script the native select works as it is. A select can opt out with
// data-native. No inline script, no inline style.
(() => {
  const NS = "http://www.w3.org/2000/svg";
  const svg = (paths) => {
    const el = document.createElementNS(NS, "svg");
    el.setAttribute("class", "i");
    el.setAttribute("viewBox", "0 0 16 16");
    el.setAttribute("aria-hidden", "true");
    for (const d of paths) {
      const p = document.createElementNS(NS, "path");
      p.setAttribute("d", d);
      el.appendChild(p);
    }
    return el;
  };
  const node = (tag, cls, text) => {
    const el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  };

  const labelOf = (select) => {
    const o = select.selectedOptions[0];
    return o ? o.textContent.trim() : "";
  };

  const enhance = (select) => {
    const quiet = select.classList.contains("quiet");
    const dd = node("div", "dd selx" + (quiet ? " is-quiet" : ""));
    const button = node("button", "gbtn" + (quiet ? " sm quiet" : ""));
    button.type = "button";
    button.setAttribute("data-dd", "");
    button.setAttribute("aria-expanded", "false");
    button.setAttribute("aria-haspopup", "listbox");
    const name = select.getAttribute("aria-label") || (select.labels && select.labels[0] ? select.labels[0].textContent.trim() : "");
    if (name) button.setAttribute("aria-label", name);
    const shown = node("span", "ddv", labelOf(select));
    button.append(shown, svg(["m4.2 6.2 3.8 3.8 3.8-3.8"]));
    const pop = node("div", "pop");
    pop.setAttribute("role", "listbox");
    const items = [];
    for (const option of select.options) {
      if (option.hidden) continue;
      const item = node("button", "opt");
      item.type = "button";
      item.setAttribute("role", "option");
      item.dataset.val = option.value;
      item.disabled = option.disabled;
      const check = node("span", "chk");
      check.appendChild(svg(["m3.5 8.4 3 3 6-6.5"]));
      item.append(check, document.createTextNode(option.textContent.trim()));
      item.addEventListener("click", () => {
        select.value = option.value;
        select.dispatchEvent(new Event("change", { bubbles: true }));
        dd.classList.remove("open");
        button.setAttribute("aria-expanded", "false");
        button.focus();
      });
      items.push([option, item]);
      pop.appendChild(item);
    }
    const sync = () => {
      shown.textContent = labelOf(select);
      for (const [option, item] of items) {
        item.querySelector(".chk").classList.toggle("off", !option.selected);
        item.setAttribute("aria-selected", String(option.selected));
      }
    };
    select.addEventListener("change", sync);
    sync();
    dd.append(button, pop);
    select.classList.add("selx-native");
    select.tabIndex = -1;
    select.setAttribute("aria-hidden", "true");
    select.after(dd);
  };

  for (const select of document.querySelectorAll("select.gfield:not([multiple]):not([data-native])")) enhance(select);
})();
