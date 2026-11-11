// Suggestions under the search fields (input[data-suggest]) and the key that
// jumps into the field. What is typed goes to the address in data-suggest; the
// answer is the list of rows (templates/search.html, "searchsuggest"): the object
// a number names, the qualifier being typed, completed, and the line that
// searches for all of it. A qualifier row writes its text over the word being
// typed; the others are links. Ctrl+K (Cmd+K) and "/" (shell.js) focus the field
// of the header.
(() => {
  const fields = Array.from(document.querySelectorAll("input[data-suggest]"));
  if (!fields.length) return;

  // Ctrl+K belongs to the field of the header, unless the page's own editor took it
  // (the link of a composer): that one has already prevented the default.
  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.altKey || event.shiftKey || !(event.ctrlKey || event.metaKey)) return;
    if ((event.key || "").toLowerCase() !== "k") return;
    const field = document.getElementById("shell-search") || fields[0];
    event.preventDefault();
    field.focus();
    if (field.select) field.select();
  });
  if (!window.fetch) return;

  const fragment = (html) => {
    const t = document.createElement("template");
    t.innerHTML = html;
    return t.content;
  };
  let uid = 0;

  const attach = (input) => {
    const form = input.closest("form");
    if (!form) return;
    const list = document.createElement("ul");
    list.className = "sg-list";
    list.id = "sg-" + ++uid;
    list.setAttribute("role", "listbox");
    list.hidden = true;
    if (input.getAttribute("aria-label")) list.setAttribute("aria-label", input.getAttribute("aria-label"));
    form.classList.add("sg-host");
    form.append(list);
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-controls", list.id);

    let seq = 0;
    let timer = 0;
    let active = -1;
    const rows = () => Array.from(list.querySelectorAll("li.sg"));
    const close = () => {
      list.hidden = true;
      active = -1;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
    };
    const mark = (at) => {
      const all = rows();
      active = all.length ? (at + all.length) % all.length : -1;
      all.forEach((li, i) => li.setAttribute("aria-selected", i === active ? "true" : "false"));
      if (active >= 0) input.setAttribute("aria-activedescendant", all[active].id); else input.removeAttribute("aria-activedescendant");
    };
    const show = (html) => {
      list.textContent = "";
      list.append(fragment(html));
      rows().forEach((li, i) => { li.id = list.id + "-" + i; });
      active = -1;
      list.hidden = rows().length === 0;
      input.setAttribute("aria-expanded", list.hidden ? "false" : "true");
      input.removeAttribute("aria-activedescendant");
    };
    const ask = () => {
      const mine = ++seq;
      if (!input.value.trim()) {
        close();
        return;
      }
      const u = new URL(input.dataset.suggest, location.href);
      u.searchParams.set("q", input.value);
      if (input.dataset.suggestProject) u.searchParams.set("project", input.dataset.suggestProject);
      fetch(u.pathname + u.search, { credentials: "same-origin", headers: { Accept: "text/html" } })
        .then((res) => (res.ok ? res.text() : ""))
        .then((html) => { if (mine === seq && document.activeElement === input) show(html); })
        .catch(() => { if (mine === seq) close(); });
    };
    // A qualifier row replaces the word at the end of the line; a key leaves the
    // colon open for its value, a value ends the word.
    const take = (li) => {
      if (!li.classList.contains("sg-q")) {
        const link = li.querySelector("a");
        if (link) link.click();
        return;
      }
      const text = input.value;
      const from = Math.max(text.lastIndexOf(" "), text.lastIndexOf("\t")) + 1;
      const word = li.dataset.insert || "";
      input.value = text.slice(0, from) + word + (word.endsWith(":") ? "" : " ");
      input.focus();
      const end = input.value.length;
      if (input.setSelectionRange) input.setSelectionRange(end, end);
      ask();
    };

    input.addEventListener("input", () => {
      clearTimeout(timer);
      timer = setTimeout(ask, 120);
    });
    input.addEventListener("blur", () => { seq++; close(); });
    input.addEventListener("keydown", (event) => {
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (list.hidden) {
          ask();
        } else {
          event.preventDefault();
          mark(active < 0 ? (event.key === "ArrowDown" ? 0 : -1) : active + (event.key === "ArrowDown" ? 1 : -1));
        }
      } else if ((event.key === "Enter" || event.key === "Tab") && active >= 0 && !list.hidden) {
        const li = rows()[active];
        if (event.key === "Tab" && !li.classList.contains("sg-q")) return;
        event.preventDefault();
        take(li);
      } else if (event.key === "Escape" && !list.hidden) {
        event.preventDefault();
        close();
      }
    });
    // The field keeps the focus while a row is pressed.
    list.addEventListener("mousedown", (event) => event.preventDefault());
    list.addEventListener("click", (event) => {
      const li = event.target instanceof Element ? event.target.closest("li.sg-q") : null;
      if (!li) return;
      event.preventDefault();
      take(li);
    });
  };

  for (const input of fields) attach(input);
})();
