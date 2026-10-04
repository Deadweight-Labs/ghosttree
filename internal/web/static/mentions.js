// Completes @names in the message boxes of a room. The list of names is in the
// page next to each box (.comp-suggest); the server decides what is in it.
(() => {
  const trigger = /(^|[^\p{L}\p{N}_.@:-])@([\p{L}\p{N}._:-]*)$/u;
  const limit = 6;
  let current = null;

  const fold = (value) => value.toLowerCase().replace(/\s+/g, "-");
  const listOf = (area) => area.closest("form")?.querySelector(".comp-suggest");
  const areaOf = (target) =>
    target instanceof HTMLTextAreaElement && target.name === "body" && listOf(target) ? target : null;

  const state = (area) => {
    const before = area.value.slice(0, area.selectionStart);
    const match = trigger.exec(before);
    return match ? { query: fold(match[2]), start: before.length - match[2].length - 1 } : null;
  };

  const close = () => {
    if (!current) return;
    current.list.hidden = true;
    current.area.removeAttribute("aria-activedescendant");
    current.area.removeAttribute("aria-expanded");
    current = null;
  };

  const visible = (list) => [...list.querySelectorAll("[role=option]")].filter((item) => !item.hidden);

  const mark = (index) => {
    const items = visible(current.list);
    if (!items.length) return;
    current.index = (index + items.length) % items.length;
    items.forEach((item, at) => {
      const on = at === current.index;
      item.setAttribute("aria-selected", on ? "true" : "false");
      if (on) {
        current.area.setAttribute("aria-activedescendant", item.id);
        item.scrollIntoView({ block: "nearest" });
      }
    });
  };

  const open = (area) => {
    const found = state(area);
    const list = listOf(area);
    if (!found || !list) return close();
    let shown = 0;
    list.querySelectorAll("[role=option]").forEach((item, at) => {
      if (!item.id) item.id = `mention-option-${at}-${Math.random().toString(36).slice(2, 7)}`;
      const name = fold(item.querySelector(".comp-suggest-name")?.textContent || "");
      const hit = !found.query || item.dataset.handle.includes(found.query) || name.includes(found.query);
      item.hidden = !hit || shown >= limit;
      if (hit && !item.hidden) shown += 1;
    });
    if (!shown) return close();
    current = { area, list, start: found.start, index: 0 };
    list.hidden = false;
    area.setAttribute("aria-expanded", "true");
    mark(0);
  };

  const choose = (item) => {
    if (!current || !item) return;
    const { area, start } = current;
    const handle = item.dataset.handle;
    const after = area.value.slice(area.selectionStart);
    const insert = `@${handle} `;
    area.value = area.value.slice(0, start) + insert + after.replace(/^[^\s]*/, "");
    const caret = start + insert.length;
    area.setSelectionRange(caret, caret);
    close();
    area.focus();
    area.dispatchEvent(new Event("input", { bubbles: true }));
  };

  // Capture phase: with the list open, Enter picks a name instead of sending.
  document.addEventListener("keydown", (event) => {
    if (!current || event.isComposing || event.target !== current.area) return;
    const items = visible(current.list);
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      event.stopPropagation();
      mark(current.index + (event.key === "ArrowDown" ? 1 : -1));
    } else if (event.key === "Enter" || event.key === "Tab") {
      if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return;
      event.preventDefault();
      event.stopPropagation();
      choose(items[current.index]);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close();
    }
  }, true);

  document.addEventListener("input", (event) => {
    const area = areaOf(event.target);
    if (area) open(area); else close();
  });
  document.addEventListener("click", (event) => {
    const area = areaOf(event.target);
    if (area) open(area);
  });
  document.addEventListener("mousedown", (event) => {
    const item = event.target instanceof Element ? event.target.closest(".comp-suggest [role=option]") : null;
    if (item) {
      event.preventDefault();
      choose(item);
    } else if (current && event.target !== current.area) {
      close();
    }
  });
  document.addEventListener("focusout", (event) => {
    if (current && event.target === current.area) close();
  });
})();
